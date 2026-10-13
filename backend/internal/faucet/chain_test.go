package faucet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

func TestFaucetConfirmationProvesExactFinalizedTransfer(t *testing.T) {
	o := Claim{ChainID: 10143, Amount: Amount, Wallet: "0x" + strings.Repeat("1", 40), Sponsor: "0x" + strings.Repeat("2", 40), Token: "0x" + strings.Repeat("3", 40), Hash: "0x" + strings.Repeat("4", 64)}
	for _, mode := range []string{"success", "pending", "unfinalized", "reorg", "from", "to", "amount", "duplicate", "missing", "reverted"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Method string
					Params []json.RawMessage
				}
				json.NewDecoder(r.Body).Decode(&in)
				var result any
				switch in.Method {
				case "eth_getTransactionReceipt":
					if mode == "pending" {
						break
					}
					from, to, status := o.Sponsor, o.Token, "0x1"
					if mode == "from" {
						from = o.Wallet
					}
					if mode == "to" {
						to = o.Wallet
					}
					if mode == "reverted" {
						status = "0x0"
					}
					log := map[string]any{"address": o.Token, "topics": []string{payments.Keccak([]byte("Transfer(address,address,uint256)")), "0x" + word(o.Sponsor), "0x" + word(o.Wallet)}, "data": "0x" + word("989680")}
					logs := []any{log}
					if mode == "amount" {
						log["data"] = "0x" + word("1")
					}
					if mode == "duplicate" {
						logs = append(logs, log)
					}
					if mode == "missing" {
						logs = nil
					}
					result = map[string]any{"transactionHash": o.Hash, "from": from, "to": to, "status": status, "blockNumber": "0x10", "blockHash": "0xabc", "logs": logs}
				case "eth_getBlockByNumber":
					number, hash := "0x10", "0xabc"
					if mode == "unfinalized" {
						number = "0xf"
					}
					if mode == "reorg" {
						hash = "0xdef"
					}
					result = map[string]string{"number": number, "hash": hash}
				default:
					t.Error("unexpected RPC", in.Method)
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			chain := &RPCChain{payments.NewChain(config.Payments{RPC: server.URL, ChainID: 10143, Token: o.Token, Provider: o.Sponsor})}
			state, err := chain.Confirm(context.Background(), o)
			want := map[string]string{"success": "confirmed", "pending": "", "unfinalized": "", "reorg": "", "from": "review", "to": "review", "amount": "review", "duplicate": "review", "missing": "review", "reverted": "failed"}[mode]
			if err != nil || state != want {
				t.Fatal(mode, state, err)
			}
		})
	}
}
