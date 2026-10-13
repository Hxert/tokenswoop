// Package reserve integrates confirmed principal with offchain tiers and fee clearance.
package reserve

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

type Withdrawal struct {
	ID              string `json:"id"`
	Amount          string `json:"amount_units"`
	OperatorVersion string `json:"operator_version"`
	RequestedAt     int64  `json:"requested_at"`
	ApprovedUntil   int64  `json:"approved_until"`
	Active          bool   `json:"active"`
}
type Snapshot struct {
	Debt            string     `json:"debt_units,omitempty"`
	Frozen          string     `json:"frozen_units,omitempty"`
	Withdrawable    string     `json:"withdrawable_units,omitempty"`
	DebtEpoch       string     `json:"debt_epoch,omitempty"`
	Principal       string     `json:"principal_units"`
	Balance         string     `json:"balance_units"`
	Allowance       string     `json:"allowance_units"`
	OperatorVersion string     `json:"operator_version"`
	Paused          bool       `json:"paused"`
	Withdrawal      Withdrawal `json:"withdrawal"`
	Block           string     `json:"block_number"`
	BlockHash       string     `json:"block_hash"`
}
type Approval struct {
	ID, UserID, Wallet, Contract, Operator, RequestID, Amount, OperatorVersion string
	Until                                                                      int64
	Hash, Cipher                                                               string
}
type Chain interface {
	Validate(context.Context) error
	Snapshot(context.Context, string) (Snapshot, error)
	SignApproval(context.Context, Approval) (payments.Signed, error)
	Broadcast(context.Context, string) error
	Confirm(context.Context, Approval) (payments.Confirmation, error)
}
type RPCChain struct {
	RPC    *payments.RPCChain
	Config config.Reserve
	Token  string
}

func NewChain(p config.Payments, c config.Reserve, wait ...func(context.Context) error) *RPCChain {
	p.Provider, p.Keystore, p.PasswordFile = c.Operator, c.Keystore, c.PasswordFile
	return &RPCChain{RPC: payments.NewChain(p, wait...), Config: c, Token: p.Token}
}
func word(s string) string {
	return strings.Repeat("0", 64-len(strings.TrimPrefix(s, "0x"))) + strings.TrimPrefix(strings.ToLower(s), "0x")
}
func data(sig string, args ...string) string {
	out := payments.Keccak([]byte(sig))[:10]
	for _, a := range args {
		out += word(a)
	}
	return out
}
func number(s string) (*big.Int, error) {
	if !strings.HasPrefix(s, "0x") {
		return nil, fmt.Errorf("invalid reserve integer")
	}
	n, ok := new(big.Int).SetString(s[2:], 16)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return nil, fmt.Errorf("invalid reserve integer")
	}
	return n, nil
}
func (r *RPCChain) read(ctx context.Context, to, call, block string, words int) ([]*big.Int, error) {
	var out string
	if e := r.RPC.RPC(ctx, "eth_call", []any{map[string]string{"to": to, "data": call}, block}, &out); e != nil {
		return nil, e
	}
	if len(out) != 2+64*words || !strings.HasPrefix(out, "0x") {
		return nil, fmt.Errorf("invalid reserve contract result")
	}
	result := make([]*big.Int, words)
	for i := range result {
		n, e := number("0x" + out[2+64*i:2+64*(i+1)])
		if e != nil {
			return nil, e
		}
		result[i] = n
	}
	return result, nil
}
func (r *RPCChain) Validate(ctx context.Context) error {
	var chain, code string
	if e := r.RPC.RPC(ctx, "eth_chainId", []any{}, &chain); e != nil {
		return e
	}
	n, e := number(chain)
	if e != nil || n.Cmp(big.NewInt(r.Config.NetworkID())) != 0 {
		return fmt.Errorf("reserve RPC chain does not match configuration")
	}
	if e = r.RPC.RPC(ctx, "eth_getCode", []any{r.Config.Contract, "latest"}, &code); e != nil {
		return e
	}
	b, e := hex.DecodeString(strings.TrimPrefix(code, "0x"))
	if e != nil || len(b) == 0 || payments.Keccak(b) != r.Config.CodeHash {
		return fmt.Errorf("reserve runtime code mismatch")
	}
	for _, v := range [][2]string{{"reserveToken()", r.Token}, {"settlementOperator()", r.Config.Operator}} {
		n, e := r.read(ctx, r.Config.Contract, data(v[0]), "latest", 1)
		if e != nil {
			return e
		}
		if word(n[0].Text(16)) != word(v[1]) {
			return fmt.Errorf("reserve token or operator mismatch")
		}
	}
	if r.Config.DebtMode() {
		manager, err := r.read(ctx, r.Config.Contract, data("manager()"), "latest", 1)
		if err != nil {
			return err
		}
		if word(manager[0].Text(16)) != word(r.Config.Manager) {
			return fmt.Errorf("reserve manager mismatch")
		}
	}
	if r.Config.Custody == "vault" {
		mode, err := r.read(ctx, r.Config.Contract, data("custodyMode()"), "latest", 1)
		if err != nil {
			return err
		}
		if "0x"+word(mode[0].Text(16)) != payments.Keccak([]byte("vault")) {
			return fmt.Errorf("reserve custody mismatch")
		}
	}
	for _, v := range [][2]string{{"aavePool()", r.Config.Pool}, {"aToken()", r.Config.AToken}, {"treasury()", r.Config.Treasury}} {
		if v[1] == "" {
			continue
		}
		value, err := r.read(ctx, r.Config.Contract, data(v[0]), "latest", 1)
		if err != nil {
			return err
		}
		if word(value[0].Text(16)) != word(v[1]) {
			return fmt.Errorf("reserve pool, aToken or treasury mismatch")
		}
	}
	return nil
}
func (r *RPCChain) ValidateSigner(ctx context.Context) error { return r.RPC.ValidateSigner(ctx) }

type block struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

func (r *RPCChain) finalized(ctx context.Context) (*block, error) {
	var b *block
	if e := r.RPC.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &b); e != nil {
		return nil, e
	}
	if b == nil || len(b.Hash) != 66 {
		return nil, fmt.Errorf("reserve finality unavailable")
	}
	if _, e := number(b.Number); e != nil {
		return nil, e
	}
	n, e := number(b.Timestamp)
	if e != nil || !n.IsInt64() || time.Now().Unix()-n.Int64() > 120 || n.Int64() > time.Now().Unix()+30 {
		return nil, fmt.Errorf("reserve finalized block is stale")
	}
	return b, nil
}
func (r *RPCChain) Snapshot(ctx context.Context, wallet string) (Snapshot, error) {
	if r.Config.DebtMode() {
		return r.debtSnapshot(ctx, wallet)
	}
	var s Snapshot
	b, e := r.finalized(ctx)
	if e != nil {
		return s, e
	}
	queries := [][3]string{
		{r.Config.Contract, data("principalOf(address)", wallet), "1"},
		{r.Config.Contract, data("getWithdrawal(address)", wallet), "6"},
		{r.Config.Contract, data("operatorVersion()"), "1"},
		{r.Config.Contract, data("paused()"), "1"},
		{r.Token, data("balanceOf(address)", wallet), "1"},
		{r.Token, data("allowance(address,address)", wallet, r.Config.Contract), "1"},
		{r.Config.Contract, data("settlementOperator()"), "1"},
	}
	values := make([][]*big.Int, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := 1
			if q[2] == "6" {
				count = 6
			}
			values[i], errs[i] = r.read(ctx, q[0], q[1], b.Number, count)
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return s, e
		}
	}
	if word(values[6][0].Text(16)) != word(r.Config.Operator) {
		return s, fmt.Errorf("reserve operator changed")
	}
	w := values[1]
	if !w[3].IsInt64() || !w[4].IsInt64() || w[5].Cmp(big.NewInt(1)) > 0 || values[3][0].Cmp(big.NewInt(1)) > 0 {
		return s, fmt.Errorf("invalid reserve state")
	}
	s = Snapshot{Principal: values[0][0].String(), Balance: values[4][0].String(), Allowance: values[5][0].String(), OperatorVersion: values[2][0].String(), Paused: values[3][0].Sign() != 0, Block: b.Number, BlockHash: b.Hash, Withdrawal: Withdrawal{ID: w[0].String(), Amount: w[1].String(), OperatorVersion: w[2].String(), RequestedAt: w[3].Int64(), ApprovedUntil: w[4].Int64(), Active: w[5].Sign() != 0}}
	return s, nil
}
func decimalHex(v string) (string, error) {
	n, ok := new(big.Int).SetString(v, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return "", fmt.Errorf("invalid approval integer")
	}
	return n.Text(16), nil
}
func (r *RPCChain) SignApproval(ctx context.Context, a Approval) (payments.Signed, error) {
	if a.Contract != r.Config.Contract || a.Operator != r.Config.Operator || a.Until <= time.Now().Unix() || a.Until > time.Now().Unix()+900 {
		return payments.Signed{}, fmt.Errorf("invalid approval scope")
	}
	// Recheck latest state too: a cancellation, replacement or operator change
	// between finalized sampling and signing must not authorize a different exit.
	w, e := r.read(ctx, a.Contract, data("getWithdrawal(address)", a.Wallet), "latest", 6)
	if e != nil {
		return payments.Signed{}, e
	}
	op, e := r.read(ctx, a.Contract, data("operatorVersion()"), "latest", 1)
	if e != nil {
		return payments.Signed{}, e
	}
	if w[0].String() != a.RequestID || w[1].String() != a.Amount || w[5].Sign() == 0 || op[0].String() != a.OperatorVersion {
		return payments.Signed{}, fmt.Errorf("withdrawal changed before approval")
	}
	id, e := decimalHex(a.RequestID)
	if e != nil {
		return payments.Signed{}, e
	}
	return r.RPC.SignContract(ctx, a.Contract, data("approveWithdrawal(address,uint256,uint64)", a.Wallet, id, big.NewInt(a.Until).Text(16)))
}
func (r *RPCChain) Broadcast(ctx context.Context, raw string) error { return r.RPC.Broadcast(ctx, raw) }
func (r *RPCChain) Confirm(ctx context.Context, a Approval) (payments.Confirmation, error) {
	var c payments.Confirmation
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
	if e := r.RPC.RPC(ctx, "eth_getTransactionReceipt", []any{a.Hash}, &receipt); e != nil {
		return c, e
	}
	if receipt == nil {
		return c, nil
	}
	if !strings.EqualFold(receipt.Hash, a.Hash) || !strings.EqualFold(receipt.From, a.Operator) || !strings.EqualFold(receipt.To, a.Contract) {
		return c, fmt.Errorf("reserve receipt identity mismatch")
	}
	b, e := r.finalized(ctx)
	if e != nil {
		return c, e
	}
	fn, _ := number(b.Number)
	rn, e := number(receipt.Block)
	if e != nil {
		return c, e
	}
	if fn.Cmp(rn) < 0 {
		return c, nil
	}
	var canonical *block
	if e = r.RPC.RPC(ctx, "eth_getBlockByNumber", []any{receipt.Block, false}, &canonical); e != nil {
		return c, e
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return c, fmt.Errorf("reserve receipt is not canonical")
	}
	if receipt.Status != "0x1" && receipt.Status != "0x0" {
		return c, fmt.Errorf("invalid reserve receipt status")
	}
	c = payments.Confirmation{Finalized: true, Success: receipt.Status == "0x1", Block: receipt.Block, BlockHash: receipt.BlockHash}
	if !c.Success {
		return c, nil
	}
	id, e := decimalHex(a.RequestID)
	if e != nil {
		return payments.Confirmation{}, e
	}
	ver, e := decimalHex(a.OperatorVersion)
	if e != nil {
		return payments.Confirmation{}, e
	}
	matches := 0
	for _, l := range receipt.Logs {
		if strings.EqualFold(l.Address, a.Contract) && len(l.Topics) == 4 && strings.EqualFold(l.Topics[0], payments.Keccak([]byte("WithdrawalApproved(address,uint256,address,uint256,uint64)"))) && strings.EqualFold(l.Topics[1], "0x"+word(a.Wallet)) && strings.EqualFold(l.Topics[2], "0x"+word(id)) && strings.EqualFold(l.Topics[3], "0x"+word(a.Operator)) && strings.EqualFold(l.Data, "0x"+word(ver)+word(big.NewInt(a.Until).Text(16))) {
			matches++
		}
	}
	if matches != 1 {
		return payments.Confirmation{}, fmt.Errorf("receipt does not prove the saved approval")
	}
	return c, nil
}
