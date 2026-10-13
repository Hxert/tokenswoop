package merasponsor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
)

func TestApprovalRequiresCurrentTokenBalance(t *testing.T) {
	wallet, token := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40)
	for _, action := range []string{"payment_approve", "reserve_approve"} {
		for _, amount := range []string{"0", "9999999", "10000000", "10000001"} {
			t.Run(action+"/"+amount, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var req struct {
						Method string
						Params []json.RawMessage
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					var call map[string]string
					json.Unmarshal(req.Params[0], &call)
					if req.Method != "eth_call" || call["to"] != token || call["data"] != "0x70a08231"+strings.Repeat("0", 24)+wallet[2:] || string(req.Params[1]) != `"latest"` {
						t.Error("incorrect balance lookup", req, call)
					}
					json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": fmt.Sprintf("0x%064x", 10000000)})
				}))
				defer server.Close()
				c := NewChain(config.Payments{RPC: server.URL, Token: token}, config.Mera{}, "", nil)
				err := c.CheckApproval(context.Background(), wallet, action, amount)
				if amount == "10000001" {
					if !errors.Is(err, ErrInsufficientBalance) || !strings.Contains(err.Error(), "10.000001") {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if (amount == "0" && calls != 0) || (amount != "0" && calls != 1) {
					t.Fatal("unexpected RPC count", calls)
				}
			})
		}
	}
}

func TestApprovalNeverAssumesBalanceOnBadRPCData(t *testing.T) {
	for _, response := range []any{"0x", "0xzz", nil, "0x" + strings.Repeat("g", 64)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": response})
		}))
		c := NewChain(config.Payments{RPC: server.URL}, config.Mera{}, "", nil)
		if err := c.CheckApproval(context.Background(), "0x"+strings.Repeat("1", 40), "payment_approve", "1"); err == nil {
			t.Fatal("accepted malformed balance", response)
		}
		server.Close()
	}
}

func TestConfirmRequiresCanonicalFinalizedExactOperationEvidence(t *testing.T) {
	o := Operation{Wallet: "0x" + strings.Repeat("1", 40), Hash: "0x" + strings.Repeat("2", 64), OperationID: "0x" + strings.Repeat("3", 64), Action: "reserve_deposit", Amount: "10000"}
	sponsor := "0x" + strings.Repeat("4", 40)
	for _, mode := range []string{"success", "pending", "unfinalized", "reorg", "wrong_sender", "missing_log", "wrong_amount", "duplicate_log", "reverted", "relayed_elsewhere"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				var result any
				switch req.Method {
				case "eth_getTransactionReceipt":
					if mode == "pending" {
						break
					}
					log := map[string]any{"address": o.Wallet, "topics": []string{payments.Keccak([]byte("OperationExecuted(bytes32,uint8,uint256)")), o.OperationID}, "data": "0x" + strings.Repeat("0", 63) + "2" + strings.Repeat("0", 60) + "2710"}
					logs := []any{log}
					from, status := sponsor, "0x1"
					switch mode {
					case "wrong_sender":
						from = o.Wallet
					case "missing_log":
						logs = nil
					case "wrong_amount":
						log["data"] = "0x" + strings.Repeat("0", 128)
					case "duplicate_log":
						logs = append(logs, log)
					case "reverted", "relayed_elsewhere":
						status = "0x0"
						logs = nil
					}
					result = map[string]any{"transactionHash": o.Hash, "from": from, "to": o.Wallet, "status": status, "blockNumber": "0x10", "blockHash": "0xabc", "logs": logs}
				case "eth_getBlockByNumber":
					number, hash := "0x10", "0xabc"
					if mode == "unfinalized" {
						number = "0xf"
					}
					if mode == "reorg" {
						hash = "0xdef"
					}
					result = map[string]string{"number": number, "hash": hash}
				case "eth_call":
					result = "0x" + strings.Repeat("0", 64)
					if mode == "relayed_elsewhere" {
						result = "0x" + strings.Repeat("0", 63) + "1"
					}
				default:
					t.Error("unexpected RPC", req.Method)
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			c := NewChain(config.Payments{RPC: server.URL}, config.Mera{Sponsor: sponsor}, "", nil)
			state, err := c.Confirm(context.Background(), o)
			expected := map[string]string{"success": "confirmed", "pending": "", "unfinalized": "", "reorg": "", "wrong_sender": "review", "missing_log": "review", "wrong_amount": "review", "duplicate_log": "review", "reverted": "reverted", "relayed_elsewhere": "review"}[mode]
			if state != expected {
				t.Fatal(state, expected, err)
			}
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreparationNeverOverwritesForeignDelegation(t *testing.T) {
	target := "0x" + strings.Repeat("1", 40)
	for _, mode := range []string{"empty", "ours", "foreign", "pending"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string
					Params []string
				}
				json.NewDecoder(r.Body).Decode(&req)
				result := "0x0"
				if req.Method == "eth_getCode" {
					result = map[string]string{"empty": "0x", "ours": "0xef0100" + target[2:], "foreign": "0xef0100" + strings.Repeat("2", 40), "pending": "0x"}[mode]
				}
				if req.Method == "eth_getTransactionCount" && mode == "pending" && req.Params[1] == "pending" {
					result = "0x1"
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			c := NewChain(config.Payments{RPC: server.URL}, config.Mera{Implementation: target}, "", nil)
			nonce, e := c.Prepare(context.Background(), target)
			if (mode == "foreign" || mode == "pending") && e == nil {
				t.Fatal("unsafe delegation permitted")
			}
			if mode == "empty" && (e != nil || nonce == nil || *nonce != 0) {
				t.Fatal(nonce, e)
			}
			if mode == "ours" && (e != nil || nonce != nil) {
				t.Fatal("unnecessary reauthorization", e)
			}
		})
	}
}
