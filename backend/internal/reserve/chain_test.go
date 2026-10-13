package reserve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

func TestApprovalReceiptRequiresExactCanonicalFinalizedEvent(t *testing.T) {
	a := Approval{Wallet: "0x" + strings.Repeat("1", 40), Operator: "0x" + strings.Repeat("2", 40), Contract: "0x" + strings.Repeat("3", 40), RequestID: "7", OperatorVersion: "2", Until: time.Now().Unix() + 600, Hash: "0x" + strings.Repeat("4", 64)}
	for _, scenario := range []string{"valid", "unfinalized", "stale_finality", "wrong_block", "wrong_request", "wrong_version", "wrong_expiry", "missing_event", "wrong_operator", "reverted"} {
		t.Run(scenario, func(t *testing.T) {
			canonical := "0x" + strings.Repeat("5", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Method string
					Params []json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					http.Error(w, "invalid", 400)
					return
				}
				var result any
				if in.Method == "eth_getTransactionReceipt" {
					id, ver, expiry := "7", "2", fmt.Sprintf("%x", a.Until)
					from, status := a.Operator, "0x1"
					if scenario == "wrong_request" {
						id = "8"
					}
					if scenario == "wrong_version" {
						ver = "3"
					}
					if scenario == "wrong_expiry" {
						expiry = "1"
					}
					if scenario == "wrong_operator" {
						from = a.Wallet
					}
					if scenario == "reverted" {
						status = "0x0"
					}
					logs := []any{map[string]any{"address": a.Contract, "topics": []string{payments.Keccak([]byte("WithdrawalApproved(address,uint256,address,uint256,uint64)")), "0x" + word(a.Wallet), "0x" + word(id), "0x" + word(a.Operator)}, "data": "0x" + word(ver) + word(expiry)}}
					if scenario == "missing_event" {
						logs = nil
					}
					result = map[string]any{"transactionHash": a.Hash, "from": from, "to": a.Contract, "status": status, "blockNumber": "0x10", "blockHash": canonical, "logs": logs}
				} else if in.Method == "eth_getBlockByNumber" {
					var tag string
					json.Unmarshal(in.Params[0], &tag)
					n, h, timestamp := "0x11", canonical, time.Now().Unix()
					if scenario == "unfinalized" && tag == "finalized" {
						n = "0xf"
					}
					if scenario == "wrong_block" && tag != "finalized" {
						h = "0x" + strings.Repeat("6", 64)
					}
					if scenario == "stale_finality" {
						timestamp -= 300
					}
					result = map[string]string{"number": n, "hash": h, "timestamp": fmt.Sprintf("0x%x", timestamp)}
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			chain := NewChain(config.Payments{RPC: server.URL}, config.Reserve{})
			got, e := chain.Confirm(context.Background(), a)
			switch scenario {
			case "valid":
				if e != nil || !got.Finalized || !got.Success {
					t.Fatal(got, e)
				}
			case "unfinalized":
				if e != nil || got.Finalized {
					t.Fatal(got, e)
				}
			case "reverted":
				if e != nil || !got.Finalized || got.Success {
					t.Fatal(got, e)
				}
			default:
				if e == nil {
					t.Fatal("accepted unproven approval")
				}
			}
		})
	}
}
