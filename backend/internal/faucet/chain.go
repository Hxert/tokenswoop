package faucet

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
)

type RPCChain struct{ RPC *payments.RPCChain }

func word(value string) string {
	return fmt.Sprintf("%064s", strings.TrimPrefix(strings.ToLower(value), "0x"))
}
func (c *RPCChain) Sign(ctx context.Context, o Claim) (payments.Signed, error) {
	p := c.RPC.Config
	if p.NetworkID() != 10143 || o.ChainID != 10143 || o.Amount != Amount || o.Token != p.Token || o.Sponsor != p.Provider || !security.WalletPattern.MatchString(o.Wallet) {
		return payments.Signed{}, ErrDisabled
	}
	var balance string
	if err := c.RPC.RPC(ctx, "eth_call", []any{map[string]string{"to": p.Token, "data": "0x70a08231" + word(p.Provider)}, "latest"}, &balance); err != nil {
		return payments.Signed{}, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(balance, "0x"), 16)
	if !ok || len(balance) != 66 {
		return payments.Signed{}, fmt.Errorf("invalid faucet balance")
	}
	if n.Cmp(big.NewInt(10000000)) < 0 {
		return payments.Signed{}, ErrEmpty
	}
	return c.RPC.SignContract(ctx, p.Token, "0xa9059cbb"+word(o.Wallet)+word("989680"))
}
func (c *RPCChain) Broadcast(ctx context.Context, raw string) error {
	if c.RPC.Config.NetworkID() != 10143 {
		return ErrDisabled
	}
	return c.RPC.Broadcast(ctx, raw)
}
func (c *RPCChain) Confirm(ctx context.Context, o Claim) (string, error) {
	p := c.RPC.Config
	if p.NetworkID() != 10143 || o.ChainID != 10143 || o.Amount != Amount || o.Token != p.Token || o.Sponsor != p.Provider {
		return "", ErrDisabled
	}
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
		}
	}
	if err := c.RPC.RPC(ctx, "eth_getTransactionReceipt", []any{o.Hash}, &receipt); err != nil {
		return "", err
	}
	if receipt == nil {
		return "", nil
	}
	if !strings.EqualFold(receipt.Hash, o.Hash) || !strings.EqualFold(receipt.From, o.Sponsor) || !strings.EqualFold(receipt.To, o.Token) {
		return "review", nil
	}
	var finalized, canonical *struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	if err := c.RPC.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); err != nil {
		return "", err
	}
	if finalized == nil {
		return "", fmt.Errorf("faucet finality unavailable")
	}
	f, ok := new(big.Int).SetString(strings.TrimPrefix(finalized.Number, "0x"), 16)
	n, valid := new(big.Int).SetString(strings.TrimPrefix(receipt.Block, "0x"), 16)
	if !ok || !valid {
		return "", fmt.Errorf("invalid faucet receipt block")
	}
	if f.Cmp(n) < 0 {
		return "", nil
	}
	if err := c.RPC.RPC(ctx, "eth_getBlockByNumber", []any{receipt.Block, false}, &canonical); err != nil {
		return "", err
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return "", nil
	}
	if receipt.Status == "0x0" {
		return "failed", nil
	}
	if receipt.Status != "0x1" {
		return "review", nil
	}
	transfers := 0
	for _, log := range receipt.Logs {
		if strings.EqualFold(log.Address, o.Token) && len(log.Topics) == 3 && strings.EqualFold(log.Topics[0], payments.Keccak([]byte("Transfer(address,address,uint256)"))) && strings.EqualFold(log.Topics[1], "0x"+word(o.Sponsor)) && strings.EqualFold(log.Topics[2], "0x"+word(o.Wallet)) && strings.EqualFold(log.Data, "0x"+word("989680")) {
			transfers++
		}
	}
	if transfers != 1 {
		return "review", nil
	}
	return "confirmed", nil
}
