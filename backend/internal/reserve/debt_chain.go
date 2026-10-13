package reserve

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"tokenswoop/backend/internal/payments"
)

type DebtItem struct {
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	Amount    string `json:"amount_units"`
}
type DebtReport struct {
	ID, UserID, Wallet, Contract, Operator, Epoch, Data, Hash, Cipher string
	ChainID                                                           int64
	Items                                                             []DebtItem
}
type DebtChain interface {
	SignDebt(context.Context, DebtReport) (payments.Signed, error)
	ConfirmDebt(context.Context, DebtReport) (payments.Confirmation, error)
}

func (r *RPCChain) Replace(ctx context.Context, raw string) (payments.Signed, error) {
	return r.RPC.Replace(ctx, raw)
}

func debtData(user, epoch string, items []DebtItem) (string, error) {
	if len(items) == 0 || len(items) > 64 {
		return "", fmt.Errorf("invalid debt report size")
	}
	e, err := decimalHex(epoch)
	if err != nil {
		return "", err
	}
	if !sort.SliceIsSorted(items, func(i, j int) bool { return items[i].ID < items[j].ID }) {
		return "", fmt.Errorf("unsorted debt IDs")
	}
	ids, amounts := word(big.NewInt(int64(len(items))).Text(16)), word(big.NewInt(int64(len(items))).Text(16))
	for i, it := range items {
		id, err := hex.DecodeString(strings.TrimPrefix(it.ID, "0x"))
		if err != nil || len(id) != 32 || new(big.Int).SetBytes(id).Sign() == 0 || (i > 0 && items[i-1].ID == it.ID) {
			return "", fmt.Errorf("invalid debt ID")
		}
		a, err := decimalHex(it.Amount)
		if err != nil {
			return "", err
		}
		ids += hex.EncodeToString(id)
		amounts += word(a)
	}
	return data("reportDebts(address,bytes32[],uint256[],uint256)", user, "80", big.NewInt(int64(128+32*(1+len(items)))).Text(16), e) + ids + amounts, nil
}

func (r *RPCChain) debtSnapshot(ctx context.Context, wallet string) (Snapshot, error) {
	var s Snapshot
	b, err := r.finalized(ctx)
	if err != nil {
		return s, err
	}
	queries := [][2]string{{r.Config.Contract, data("principalOf(address)", wallet)}, {r.Config.Contract, data("debtOf(address)", wallet)}, {r.Config.Contract, data("debtEpoch(address)", wallet)}, {r.Config.Contract, data("paused()")}, {r.Token, data("balanceOf(address)", wallet)}, {r.Token, data("allowance(address,address)", wallet, r.Config.Contract)}, {r.Config.Contract, data("manager()")}, {r.Config.Contract, data("settlementOperator()")}}
	v := make([]*big.Int, len(queries))
	for i, q := range queries {
		out, e := r.read(ctx, q[0], q[1], b.Number, 1)
		if e != nil {
			return s, e
		}
		v[i] = out[0]
	}
	if word(v[6].Text(16)) != word(r.Config.Manager) || word(v[7].Text(16)) != word(r.Config.Operator) {
		return s, fmt.Errorf("reserve authority mismatch")
	}
	if v[3].Cmp(big.NewInt(1)) > 0 {
		return s, fmt.Errorf("invalid deposit pause state")
	}
	frozen := new(big.Int).Set(v[1])
	if frozen.Cmp(v[0]) > 0 {
		frozen.Set(v[0])
	}
	s = Snapshot{Principal: v[0].String(), Debt: v[1].String(), DebtEpoch: v[2].String(), Frozen: frozen.String(), Withdrawable: new(big.Int).Sub(v[0], frozen).String(), Paused: v[3].Sign() != 0, Balance: v[4].String(), Allowance: v[5].String(), Block: b.Number, BlockHash: b.Hash}
	return s, nil
}

func (r *RPCChain) SignDebt(ctx context.Context, a DebtReport) (payments.Signed, error) {
	expected, err := debtData(a.Wallet, a.Epoch, a.Items)
	if err != nil || a.ChainID != r.Config.NetworkID() || a.Contract != r.Config.Contract || a.Operator != r.Config.Operator || a.Data != expected {
		return payments.Signed{}, fmt.Errorf("invalid debt report scope")
	}
	epoch, err := r.read(ctx, a.Contract, data("debtEpoch(address)", a.Wallet), "latest", 1)
	if err != nil {
		return payments.Signed{}, err
	}
	if epoch[0].String() != a.Epoch {
		return payments.Signed{}, fmt.Errorf("manager cleared debt before signing")
	}
	return r.RPC.SignContract(ctx, a.Contract, a.Data)
}

func (r *RPCChain) ConfirmDebt(ctx context.Context, a DebtReport) (payments.Confirmation, error) {
	var result payments.Confirmation
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
	if err := r.RPC.RPC(ctx, "eth_getTransactionReceipt", []any{a.Hash}, &receipt); err != nil {
		return result, err
	}
	if receipt == nil {
		return result, nil
	}
	if !strings.EqualFold(receipt.Hash, a.Hash) || !strings.EqualFold(receipt.From, a.Operator) || !strings.EqualFold(receipt.To, a.Contract) {
		return result, fmt.Errorf("debt receipt identity mismatch")
	}
	b, err := r.finalized(ctx)
	if err != nil {
		return result, err
	}
	final, _ := number(b.Number)
	height, err := number(receipt.Block)
	if err != nil {
		return result, err
	}
	if final.Cmp(height) < 0 {
		return result, nil
	}
	var canonical *block
	if err = r.RPC.RPC(ctx, "eth_getBlockByNumber", []any{receipt.Block, false}, &canonical); err != nil {
		return result, err
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return result, fmt.Errorf("debt receipt is not canonical")
	}
	if receipt.Status != "0x1" && receipt.Status != "0x0" {
		return result, fmt.Errorf("invalid debt receipt status")
	}
	result = payments.Confirmation{Finalized: true, Success: receipt.Status == "0x1", Block: receipt.Block, BlockHash: receipt.BlockHash}
	if !result.Success {
		return result, nil
	}
	epoch, err := decimalHex(a.Epoch)
	if err != nil {
		return payments.Confirmation{}, err
	}
	calldata, err := hex.DecodeString(strings.TrimPrefix(a.Data, "0x"))
	if err != nil {
		return payments.Confirmation{}, err
	}
	matches := 0
	for _, l := range receipt.Logs {
		if strings.EqualFold(l.Address, a.Contract) && len(l.Topics) == 3 && strings.EqualFold(l.Topics[0], payments.Keccak([]byte("DebtReported(address,uint256,bytes32)"))) && strings.EqualFold(l.Topics[1], "0x"+word(a.Wallet)) && strings.EqualFold(l.Topics[2], "0x"+word(epoch)) && strings.EqualFold(l.Data, payments.Keccak(calldata)) {
			matches++
		}
	}
	if matches != 1 {
		return payments.Confirmation{}, fmt.Errorf("receipt does not prove saved debt report")
	}
	return result, nil
}
