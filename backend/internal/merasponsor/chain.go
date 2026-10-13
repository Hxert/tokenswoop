package merasponsor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os/exec"
	"strings"

	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

type Signed struct{ Raw, Hash, Nonce, Budget string }
type Chain interface {
	CheckApproval(context.Context, string, string, string) error
	Prepare(context.Context, string) (*int64, error)
	Sign(context.Context, Operation, string, *Authorization) (Signed, error)
	Broadcast(context.Context, string) error
	Confirm(context.Context, Operation) (string, error)
}

func (c *RPCChain) CheckApproval(ctx context.Context, wallet, action, amount string) error {
	if (action != "payment_approve" && action != "reserve_approve") || amount == "0" {
		return nil
	}
	if _, err := Action(action, amount); err != nil {
		return ErrInvalid
	}
	var result string
	data := "0x70a08231" + hex.EncodeToString(word(raw(wallet)))
	if err := c.RPC.RPC(ctx, "eth_call", []any{map[string]string{"to": c.Token, "data": data}, "latest"}, &result); err != nil {
		return err
	}
	if len(result) != 66 || !strings.HasPrefix(result, "0x") {
		return fmt.Errorf("invalid approval balance response")
	}
	balance, ok := new(big.Int).SetString(result[2:], 16)
	if !ok || balance.Sign() < 0 {
		return fmt.Errorf("invalid approval balance response")
	}
	requested, _ := new(big.Int).SetString(amount, 10)
	if balance.Cmp(requested) < 0 {
		return fmt.Errorf("%w: you have %s %s; the approval amount is %s %s", ErrInsufficientBalance,
			decimal.NewFromBigInt(balance, -6).String(), config.NetworkFor(c.RPC.Config.NetworkID()).Symbol,
			decimal.NewFromBigInt(requested, -6).String(), config.NetworkFor(c.RPC.Config.NetworkID()).Symbol)
	}
	return nil
}

type RPCChain struct {
	RPC                     *payments.RPCChain
	Config                  config.Mera
	Token, Payment, Reserve string
}

func NewChain(p config.Payments, m config.Mera, reserve string, wait func(context.Context) error) *RPCChain {
	p.Provider, p.Keystore, p.PasswordFile = m.Sponsor, m.Keystore, m.PasswordFile
	return &RPCChain{payments.NewChain(p, wait), m, p.Token, p.Contract, reserve}
}
func (c *RPCChain) Validate(ctx context.Context, signer bool) error {
	var chain, code string
	if err := c.RPC.RPC(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return err
	}
	if chain != fmt.Sprintf("0x%x", c.RPC.Config.NetworkID()) {
		return fmt.Errorf("Mera RPC network mismatch")
	}
	if err := c.RPC.RPC(ctx, "eth_getCode", []any{c.Config.Implementation, "latest"}, &code); err != nil {
		return err
	}
	if payments.Keccak(raw(code)) != c.Config.CodeHash {
		return fmt.Errorf("Mera runtime code mismatch")
	}
	for method, expected := range map[string]string{"implementation()": c.Config.Implementation, "paymentToken()": c.Token, "paymentContract()": c.Payment, "reserveContract()": c.Reserve} {
		var out string
		data := "0x" + hex.EncodeToString(hash([]byte(method))[:4])
		if err := c.RPC.RPC(ctx, "eth_call", []any{map[string]string{"to": c.Config.Implementation, "data": data}, "latest"}, &out); err != nil {
			return err
		}
		if !strings.EqualFold(out, "0x"+hex.EncodeToString(word(raw(expected)))) {
			return fmt.Errorf("Mera contract targets mismatch")
		}
	}
	if signer {
		return c.RPC.ValidateSigner(ctx)
	}
	return nil
}
func (c *RPCChain) Prepare(ctx context.Context, wallet string) (*int64, error) {
	var code string
	if err := c.RPC.RPC(ctx, "eth_getCode", []any{wallet, "latest"}, &code); err != nil {
		return nil, err
	}
	if strings.EqualFold(code, "0xef0100"+strings.TrimPrefix(c.Config.Implementation, "0x")) {
		return nil, nil
	}
	if code != "0x" {
		return nil, fmt.Errorf("%w: wallet has a different account implementation", ErrInvalid)
	}
	n, err := c.nonce(ctx, wallet)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
func (c *RPCChain) nonce(ctx context.Context, wallet string) (int64, error) {
	var pending, latest string
	if err := c.RPC.RPC(ctx, "eth_getTransactionCount", []any{wallet, "pending"}, &pending); err != nil {
		return 0, err
	}
	if err := c.RPC.RPC(ctx, "eth_getTransactionCount", []any{wallet, "latest"}, &latest); err != nil {
		return 0, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(pending, "0x"), 16)
	if !ok || !n.IsInt64() || n.Sign() < 0 || n.BitLen() > 53 || pending != latest {
		return 0, fmt.Errorf("wallet transaction pending")
	}
	return n.Int64(), nil
}
func (c *RPCChain) Sign(ctx context.Context, o Operation, signature string, auth *Authorization) (Signed, error) {
	var result Signed
	if err := c.Validate(ctx, false); err != nil {
		return result, err
	}
	if err := c.CheckApproval(ctx, o.Wallet, o.Action, o.Amount); err != nil {
		return result, err
	}
	current, err := c.Prepare(ctx, o.Wallet)
	if err != nil {
		return result, err
	}
	if current != nil && (auth == nil || *current != int64(auth.Nonce)) {
		return result, fmt.Errorf("%w: wallet authorization nonce changed", ErrInvalid)
	}
	nonce, err := c.nonce(ctx, c.Config.Sponsor)
	if err != nil {
		return result, err
	}
	tx := map[string]any{"from": c.Config.Sponsor, "to": o.Wallet, "data": o.Calldata(signature), "value": "0x0"}
	if current != nil {
		tx["authorizationList"] = []any{auth.RPC()}
		tx["type"] = "0x4"
	}
	var gasHex, priceHex string
	gasResult, rpcError, err := c.RPC.Request(ctx, "eth_estimateGas", []any{tx})
	if err != nil {
		return result, err
	}
	if rpcError != nil {
		if rpcError.Message == "execution reverted" {
			return result, ErrSimulation
		}
		return result, fmt.Errorf("Mera simulation temporarily unavailable")
	}
	if json.Unmarshal(gasResult, &gasHex) != nil {
		return result, fmt.Errorf("invalid Mera gas estimate")
	}
	if err = c.RPC.RPC(ctx, "eth_gasPrice", []any{}, &priceHex); err != nil {
		return result, err
	}
	gas, ok := new(big.Int).SetString(strings.TrimPrefix(gasHex, "0x"), 16)
	if !ok || gas.Sign() <= 0 || gas.Cmp(big.NewInt(600000)) > 0 {
		return result, fmt.Errorf("Mera gas estimate outside limit")
	}
	gas.Mul(gas, big.NewInt(125))
	gas.Div(gas, big.NewInt(100))
	price, ok := new(big.Int).SetString(strings.TrimPrefix(priceHex, "0x"), 16)
	if !ok || price.Sign() <= 0 {
		return result, fmt.Errorf("invalid gas price")
	}
	price.Mul(price, big.NewInt(2))
	budget := new(big.Int).Mul(gas, price)
	cap, _ := new(big.Int).SetString(c.Config.MaxGasWei, 10)
	if budget.Cmp(cap) > 0 {
		return result, fmt.Errorf("Mera transaction exceeds gas budget")
	}
	args := []string{"mktx", o.Wallet, o.Calldata(signature), "--chain", fmt.Sprint(o.ChainID), "--nonce", fmt.Sprint(nonce), "--gas-limit", gas.String(), "--gas-price", price.String(), "--priority-gas-price", "0", "--rpc-url", c.RPC.Config.RPC, "--keystore", c.Config.Keystore, "--password-file", c.Config.PasswordFile, "--from", c.Config.Sponsor}
	if current != nil {
		args = append(args, "--auth", auth.Encoded(), "--force")
	}
	out, err := exec.CommandContext(ctx, c.RPC.Config.Cast, args...).Output()
	if err != nil {
		return result, fmt.Errorf("Mera transaction signer failed")
	}
	encoded := strings.TrimSpace(string(out))
	b, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil || len(b) < 100 {
		return result, fmt.Errorf("invalid signed Mera transaction")
	}
	return Signed{encoded, payments.Keccak(b), fmt.Sprint(nonce), budget.String()}, nil
}
func (c *RPCChain) Broadcast(ctx context.Context, raw string) error { return c.RPC.Broadcast(ctx, raw) }
func (c *RPCChain) Confirm(ctx context.Context, o Operation) (string, error) {
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
	if err := c.RPC.RPC(ctx, "eth_getTransactionReceipt", []any{o.Hash}, &receipt); err != nil {
		return "", err
	}
	if receipt == nil {
		return "", nil
	}
	if !strings.EqualFold(receipt.Hash, o.Hash) || !strings.EqualFold(receipt.From, c.Config.Sponsor) || !strings.EqualFold(receipt.To, o.Wallet) {
		return "review", fmt.Errorf("Mera receipt identity mismatch")
	}
	var finalized, canonical *struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	if err := c.RPC.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); err != nil {
		return "", err
	}
	a, ok := new(big.Int).SetString(strings.TrimPrefix(receipt.Block, "0x"), 16)
	if !ok || finalized == nil {
		return "", fmt.Errorf("Mera finality unavailable")
	}
	b, ok := new(big.Int).SetString(strings.TrimPrefix(finalized.Number, "0x"), 16)
	if !ok || b.Cmp(a) < 0 {
		return "", nil
	}
	if err := c.RPC.RPC(ctx, "eth_getBlockByNumber", []any{receipt.Block, false}, &canonical); err != nil {
		return "", err
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return "", fmt.Errorf("Mera receipt is not canonical")
	}
	if receipt.Status == "0x0" {
		// A third party can relay the public owner-signed operation first. A reverted
		// sponsor transaction must not be presented as a failed deposit if the ID
		// was already executed elsewhere; preserve it for reconciliation instead.
		var used string
		data := "0x" + hex.EncodeToString(join(hash([]byte("operationUsed(bytes32)"))[:4], raw(o.OperationID)))
		if err := c.RPC.RPC(ctx, "eth_call", []any{map[string]string{"to": o.Wallet, "data": data}, receipt.Block}, &used); err != nil {
			return "", err
		}
		if used == "0x"+hex.EncodeToString(number(1)) {
			return "review", fmt.Errorf("Mera operation executed by another transaction")
		}
		if used != "0x"+hex.EncodeToString(number(0)) {
			return "review", fmt.Errorf("Mera operation state unavailable")
		}
		return "reverted", nil
	}
	if receipt.Status != "0x1" {
		return "review", fmt.Errorf("invalid Mera receipt")
	}
	action, _ := Action(o.Action, o.Amount)
	n, _ := new(big.Int).SetString(o.Amount, 10)
	expected := "0x" + hex.EncodeToString(join(number(int64(action)), word(n.Bytes())))
	count := 0
	for _, log := range receipt.Logs {
		if strings.EqualFold(log.Address, o.Wallet) && len(log.Topics) == 2 && strings.EqualFold(log.Topics[0], payments.Keccak([]byte("OperationExecuted(bytes32,uint8,uint256)"))) && strings.EqualFold(log.Topics[1], o.OperationID) && strings.EqualFold(log.Data, expected) {
			count++
		}
	}
	if count != 1 {
		return "review", fmt.Errorf("Mera receipt does not prove operation")
	}
	return "confirmed", nil
}
