// Package payments settles only completed usage. It never reserves wallet funds.
package payments

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/sha3"
	"tokenswoop/backend/internal/config"
)

type Snapshot struct {
	Balance   string `json:"balance_units"`
	Allowance string `json:"allowance_units"`
	Block     string `json:"block_number"`
}
type Signed struct{ Raw, Hash, Nonce string }
type Confirmation struct {
	Finalized, Success bool
	Block, BlockHash   string
}
type Chain interface {
	Validate(context.Context) error
	Snapshot(context.Context, string) (Snapshot, error)
	IsCharged(context.Context, Bill) (bool, error)
	Sign(context.Context, Bill) (Signed, error)
	Broadcast(context.Context, string) error
	Confirm(context.Context, Bill, string) (Confirmation, error)
}

type RPCChain struct {
	Config   config.Payments
	HTTP     *http.Client
	rpcSlots <-chan time.Time
	waitRPC  func(context.Context) error
}

var rpcLimiters sync.Map

func NewChain(c config.Payments, wait ...func(context.Context) error) *RPCChain {
	r := &RPCChain{Config: c, HTTP: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if len(wait) > 0 && wait[0] != nil {
		r.waitRPC = wait[0]
	} else if c.RPCRequestsPerSecond > 0 {
		// Standalone callers retain local pacing. API and Worker inject the same
		// Redis-backed endpoint limiter, including browser RPC requests.
		key := fmt.Sprintf("%s:%d", c.RPC, c.RPCRequestsPerSecond)
		ticker := time.NewTicker(time.Second / time.Duration(c.RPCRequestsPerSecond))
		value, loaded := rpcLimiters.LoadOrStore(key, ticker)
		if loaded {
			ticker.Stop()
		}
		r.rpcSlots = value.(*time.Ticker).C
	}
	return r
}
func Keccak(b []byte) string {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return "0x" + hex.EncodeToString(h.Sum(nil))
}
func word(v string) string {
	return strings.Repeat("0", 64-len(strings.TrimPrefix(v, "0x"))) + strings.TrimPrefix(strings.ToLower(v), "0x")
}
func calldata(signature string, args ...string) string {
	out := Keccak([]byte(signature))[:10]
	for _, v := range args {
		out += word(v)
	}
	return out
}

var hexData = regexp.MustCompile(`^0x[0-9a-fA-F]*$`)

func integer(v string) (*big.Int, error) {
	if !hexData.MatchString(v) || len(v) < 3 {
		return nil, fmt.Errorf("invalid RPC integer")
	}
	n, ok := new(big.Int).SetString(v[2:], 16)
	if !ok {
		return nil, fmt.Errorf("invalid RPC integer")
	}
	return n, nil
}
func (r *RPCChain) RPC(ctx context.Context, method string, params any, out any) error {
	result, rpcError, err := r.Request(ctx, method, params)
	if err != nil {
		return err
	}
	if rpcError != nil {
		return fmt.Errorf("payment RPC %s failed", method)
	}
	if json.Unmarshal(result, out) != nil {
		return fmt.Errorf("invalid payment RPC response")
	}
	return nil
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Request preserves JSON-RPC error codes and revert bytes for wallet clients,
// while keeping endpoint URLs, credentials and arbitrary upstream text private.
func (r *RPCChain) Request(ctx context.Context, method string, params any) (json.RawMessage, *RPCError, error) {
	if r.waitRPC != nil {
		if err := r.waitRPC(ctx); err != nil {
			return nil, nil, fmt.Errorf("payment RPC capacity unavailable")
		}
	}
	if r.rpcSlots != nil {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-r.rpcSlots:
		}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, e := http.NewRequestWithContext(ctx, "POST", r.Config.RPC, bytes.NewReader(b))
	if e != nil {
		return nil, nil, fmt.Errorf("payment RPC request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := r.HTTP.Do(req)
	if e != nil {
		return nil, nil, fmt.Errorf("payment RPC unavailable")
	}
	defer res.Body.Close()
	var result struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&result) != nil || result.Error == nil && len(result.Result) == 0 {
		return nil, nil, fmt.Errorf("payment RPC %s failed", method)
	}
	if result.Error != nil {
		safe := &RPCError{Code: result.Error.Code, Message: "Chain RPC rejected this request."}
		for _, message := range []string{"execution reverted", "insufficient funds", "nonce too low", "already known", "replacement transaction underpriced", "transaction underpriced"} {
			if strings.Contains(strings.ToLower(result.Error.Message), message) {
				safe.Message = message
				break
			}
		}
		var data string
		if json.Unmarshal(result.Error.Data, &data) == nil && len(data) <= 16386 && hexData.MatchString(data) {
			safe.Data, _ = json.Marshal(data)
		}
		return nil, safe, nil
	}
	return result.Result, nil, nil
}
func (r *RPCChain) call(ctx context.Context, to, data, block string) (string, error) {
	var out string
	e := r.RPC(ctx, "eth_call", []any{map[string]string{"to": to, "data": data}, block}, &out)
	if e == nil && (!hexData.MatchString(out) || len(out) != 66) {
		e = fmt.Errorf("invalid contract result")
	}
	return strings.ToLower(out), e
}
func (r *RPCChain) chain(ctx context.Context) error {
	var v string
	if e := r.RPC(ctx, "eth_chainId", []any{}, &v); e != nil {
		return e
	}
	n, e := integer(v)
	if e != nil || n.Cmp(big.NewInt(r.Config.NetworkID())) != 0 {
		return fmt.Errorf("payment RPC chain does not match configuration")
	}
	return nil
}
func (r *RPCChain) Validate(ctx context.Context) error {
	if e := r.chain(ctx); e != nil {
		return e
	}
	p := r.Config
	for _, v := range [][2]string{{p.Contract, p.ContractCodeHash}, {p.Token, p.TokenCodeHash}} {
		var code string
		if e := r.RPC(ctx, "eth_getCode", []any{v[0], "latest"}, &code); e != nil {
			return e
		}
		b, e := hex.DecodeString(strings.TrimPrefix(code, "0x"))
		if e != nil || len(b) == 0 || Keccak(b) != v[1] {
			return fmt.Errorf("payment runtime code mismatch")
		}
	}
	if p.StopAuthority != "" {
		out, err := r.call(ctx, p.Contract, calldata("owner()"), "latest")
		if err != nil {
			return err
		}
		if out != "0x"+word(p.StopAuthority) {
			return fmt.Errorf("payment stop authority mismatch")
		}
	}
	// Pausing new charges must not prevent confirming transfers already mined.
	if p.Recipient != "" {
		out, err := r.call(ctx, p.Contract, calldata("recipient()"), "latest")
		if err != nil {
			return err
		}
		if out != "0x"+word(p.Recipient) {
			return fmt.Errorf("payment recipient mismatch")
		}
	}
	for _, v := range [][3]string{{p.Contract, "provider()", "0x" + word(p.Provider)}, {p.Contract, "paymentToken()", "0x" + word(p.Token)}, {p.Token, "decimals()", "0x" + word("6")}} {
		out, e := r.call(ctx, v[0], calldata(v[1]), "latest")
		if e != nil {
			return e
		}
		if out != v[2] {
			return fmt.Errorf("payment contract configuration mismatch")
		}
	}
	return nil
}

type block struct {
	Number string `json:"number"`
	Hash   string `json:"hash"`
}

func (r *RPCChain) Snapshot(ctx context.Context, wallet string) (Snapshot, error) {
	var s Snapshot
	if e := r.chain(ctx); e != nil {
		return s, e
	}
	var b *block
	if e := r.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &b); e != nil {
		return s, e
	}
	if b == nil {
		return s, fmt.Errorf("finalized block unavailable")
	}
	balance, e := r.call(ctx, r.Config.Token, calldata("balanceOf(address)", wallet), b.Number)
	if e != nil {
		return s, e
	}
	allowance, e := r.call(ctx, r.Config.Token, calldata("allowance(address,address)", wallet, r.Config.Contract), b.Number)
	if e != nil {
		return s, e
	}
	bn, e := integer(balance)
	if e != nil {
		return s, e
	}
	an, e := integer(allowance)
	if e != nil {
		return s, e
	}
	return Snapshot{bn.String(), an.String(), b.Number}, nil
}
func (r *RPCChain) IsCharged(ctx context.Context, b Bill) (bool, error) {
	v, e := r.call(ctx, b.Contract, calldata("isCharged(address,bytes32)", b.Wallet, b.ID), "latest")
	return v == "0x"+word("1"), e
}
func (r *RPCChain) cast(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Config.Cast, args...)
	out, e := cmd.Output()
	if e != nil {
		return "", fmt.Errorf("payment signer failed")
	}
	return strings.TrimSpace(string(out)), nil
}
func (r *RPCChain) ValidateSigner(ctx context.Context) error {
	p := r.Config
	for _, path := range []string{p.Keystore, p.PasswordFile} {
		st, e := os.Stat(path)
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("worker requires private keystore and password files (mode 0600)")
		}
	}
	address, e := r.cast(ctx, "wallet", "address", "--keystore", p.Keystore, "--password-file", p.PasswordFile)
	if e != nil {
		return e
	}
	if !strings.EqualFold(address, p.Provider) {
		return fmt.Errorf("payment signer differs from fixed provider")
	}
	return nil
}
func (r *RPCChain) Sign(ctx context.Context, b Bill) (Signed, error) {
	var s Signed
	if e := r.chain(ctx); e != nil {
		return s, e
	}
	if !b.matches(r.Config) {
		return s, fmt.Errorf("bill configuration mismatch")
	}
	units, ok := new(big.Int).SetString(b.Units, 10)
	if !ok || units.Sign() <= 0 || units.BitLen() > 256 {
		return s, fmt.Errorf("invalid bill units")
	}
	return r.SignContract(ctx, b.Contract, calldata("chargeBatch(address,bytes32,uint256)", b.Wallet, b.ID, units.Text(16)))
}

// SignContract is used only by the durable payment and reserve journals. Callers
// pin the destination and construct allowed calldata; no HTTP input reaches it.
func (r *RPCChain) SignContract(ctx context.Context, to, data string) (Signed, error) {
	var s Signed
	if e := r.chain(ctx); e != nil {
		return s, e
	}
	if !regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`).MatchString(to) || !hexData.MatchString(data) || len(data) < 10 {
		return s, fmt.Errorf("invalid signing target")
	}
	var pending, latest, gasHex, priceHex string
	if e := r.RPC(ctx, "eth_getTransactionCount", []any{r.Config.Provider, "pending"}, &pending); e != nil {
		return s, e
	}
	if e := r.RPC(ctx, "eth_getTransactionCount", []any{r.Config.Provider, "latest"}, &latest); e != nil {
		return s, e
	}
	n, e := integer(pending)
	if e != nil {
		return s, e
	}
	l, e := integer(latest)
	if e != nil || n.Cmp(l) != 0 {
		return s, fmt.Errorf("provider has a pending transaction")
	}
	if e = r.RPC(ctx, "eth_estimateGas", []any{map[string]string{"from": r.Config.Provider, "to": to, "data": data}}, &gasHex); e != nil {
		return s, e
	}
	if e = r.RPC(ctx, "eth_gasPrice", []any{}, &priceHex); e != nil {
		return s, e
	}
	gas, e := integer(gasHex)
	if e != nil {
		return s, e
	}
	gas.Mul(gas, big.NewInt(120))
	gas.Div(gas, big.NewInt(100))
	price, e := integer(priceHex)
	if e != nil {
		return s, e
	}
	if new(big.Int).Mul(gas, price).Cmp(big.NewInt(50_000_000_000_000_000)) >= 0 {
		return s, fmt.Errorf("testnet transaction gas cap exceeded")
	}
	raw, e := r.cast(ctx, "mktx", to, data, "--chain", fmt.Sprint(r.Config.NetworkID()), "--legacy", "--nonce", n.String(), "--gas-limit", gas.String(), "--gas-price", price.String(), "--rpc-url", r.Config.RPC, "--keystore", r.Config.Keystore, "--password-file", r.Config.PasswordFile, "--from", r.Config.Provider)
	if e != nil {
		return s, e
	}
	bytes, e := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if e != nil || len(bytes) < 100 {
		return s, fmt.Errorf("invalid signed payment")
	}
	return Signed{raw, Keccak(bytes), n.String()}, nil
}
func (r *RPCChain) Broadcast(ctx context.Context, raw string) error {
	if e := r.chain(ctx); e != nil {
		return e
	}
	var hash string
	if e := r.RPC(ctx, "eth_sendRawTransaction", []any{raw}, &hash); e != nil {
		return e
	}
	b, e := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if e != nil || !strings.EqualFold(hash, Keccak(b)) {
		return fmt.Errorf("broadcast hash mismatch")
	}
	return nil
}
func (r *RPCChain) Confirm(ctx context.Context, b Bill, hash string) (Confirmation, error) {
	var c Confirmation
	var receipt *struct {
		Hash      string `json:"transactionHash"`
		From      string `json:"from"`
		To        string `json:"to"`
		Status    string `json:"status"`
		Block     string `json:"blockNumber"`
		BlockHash string `json:"blockHash"`
		Logs      []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
			Data    string   `json:"data"`
		} `json:"logs"`
	}
	if e := r.RPC(ctx, "eth_getTransactionReceipt", []any{hash}, &receipt); e != nil {
		return c, e
	}
	if receipt == nil {
		return c, nil
	}
	if !strings.EqualFold(receipt.Hash, hash) || !strings.EqualFold(receipt.From, b.Provider) || !strings.EqualFold(receipt.To, b.Contract) {
		return c, fmt.Errorf("payment receipt identity mismatch")
	}
	var finalized, canonical *block
	if e := r.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); e != nil {
		return c, e
	}
	if finalized == nil {
		return c, fmt.Errorf("finality unavailable")
	}
	fn, e := integer(finalized.Number)
	if e != nil {
		return c, e
	}
	rn, e := integer(receipt.Block)
	if e != nil {
		return c, e
	}
	if fn.Cmp(rn) < 0 {
		return c, nil
	}
	if e := r.RPC(ctx, "eth_getBlockByNumber", []any{receipt.Block, false}, &canonical); e != nil {
		return c, e
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return c, fmt.Errorf("payment receipt is not canonical")
	}
	if receipt.Status != "0x1" && receipt.Status != "0x0" {
		return c, fmt.Errorf("invalid receipt status")
	}
	c = Confirmation{true, receipt.Status == "0x1", receipt.Block, receipt.BlockHash}
	if !c.Success {
		return c, nil
	}
	units, ok := new(big.Int).SetString(b.Units, 10)
	if !ok {
		return Confirmation{}, fmt.Errorf("invalid bill units")
	}
	data := "0x" + word(units.Text(16))
	charge, transfer := 0, 0
	for _, log := range receipt.Logs {
		topics := log.Topics
		for i := range topics {
			topics[i] = strings.ToLower(topics[i])
		}
		if strings.EqualFold(log.Address, b.Contract) && len(topics) == 4 && (topics[0] == Keccak([]byte("BatchCharged(address,address,bytes32,uint256)")) || topics[0] == Keccak([]byte("Charged(address,address,bytes32,uint256)"))) && topics[1] == "0x"+word(b.Wallet) && topics[2] == "0x"+word(b.Provider) && topics[3] == b.ID && strings.EqualFold(log.Data, data) {
			charge++
		}
		if strings.EqualFold(log.Address, b.Token) && len(topics) == 3 && topics[0] == Keccak([]byte("Transfer(address,address,uint256)")) && topics[1] == "0x"+word(b.Wallet) && topics[2] == "0x"+word(b.recipientAddress()) && strings.EqualFold(log.Data, data) {
			transfer++
		}
	}
	if charge != 1 || transfer != 1 {
		return Confirmation{}, fmt.Errorf("payment receipt does not prove the exact bill transfer")
	}
	return c, nil
}
