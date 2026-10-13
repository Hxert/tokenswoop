package payments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"tokenswoop/backend/internal/config"
)

func TestReceiptRequiresCanonicalFinalityAndExactTransfer(t *testing.T) {
	b := Bill{ID: "0x" + strings.Repeat("4", 64), Wallet: "0x" + strings.Repeat("1", 40), Provider: "0x" + strings.Repeat("2", 40), Recipient: "0x" + strings.Repeat("7", 40), Token: "0x" + strings.Repeat("3", 40), Contract: "0x" + strings.Repeat("5", 40), Units: "120"}
	hash := "0x" + strings.Repeat("6", 64)
	for _, scenario := range []string{"valid", "unfinalized", "wrong_block", "wrong_amount", "wrong_sender", "missing_transfer", "wrong_recipient", "reverted"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Method string
					Params []json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Fatal("bad RPC request")
				}
				var result any
				if in.Method == "eth_getTransactionReceipt" {
					amount := "0x" + word("78")
					if scenario == "wrong_amount" {
						amount = "0x" + word("79")
					}
					from := b.Provider
					if scenario == "wrong_sender" {
						from = b.Wallet
					}
					status := "0x1"
					if scenario == "reverted" {
						status = "0x0"
					}
					logs := []any{map[string]any{"address": b.Contract, "topics": []string{Keccak([]byte("BatchCharged(address,address,bytes32,uint256)")), "0x" + word(b.Wallet), "0x" + word(b.Provider), b.ID}, "data": amount}}
					if scenario != "missing_transfer" {
						recipient := b.Recipient
						if scenario == "wrong_recipient" {
							recipient = b.Provider
						}
						logs = append(logs, map[string]any{"address": b.Token, "topics": []string{Keccak([]byte("Transfer(address,address,uint256)")), "0x" + word(b.Wallet), "0x" + word(recipient)}, "data": amount})
					}
					result = map[string]any{"transactionHash": hash, "from": from, "to": b.Contract, "status": status, "blockNumber": "0x10", "blockHash": "0xcanonical", "logs": logs}
				} else if in.Method == "eth_getBlockByNumber" {
					var tag string
					json.Unmarshal(in.Params[0], &tag)
					number, blockHash := "0x11", "0xcanonical"
					if scenario == "unfinalized" && tag == "finalized" {
						number = "0xf"
					}
					if scenario == "wrong_block" && tag != "finalized" {
						blockHash = "0xother"
					}
					result = map[string]string{"number": number, "hash": blockHash}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			chain := NewChain(config.Payments{RPC: server.URL})
			c, e := chain.Confirm(context.Background(), b, hash)
			switch scenario {
			case "valid":
				if e != nil || !c.Finalized || !c.Success {
					t.Fatal(c, e)
				}
			case "unfinalized":
				if e != nil || c.Finalized {
					t.Fatal(c, e)
				}
			case "reverted":
				if e != nil || !c.Finalized || c.Success {
					t.Fatal(c, e)
				}
			default:
				if e == nil {
					t.Fatal("unproven receipt accepted")
				}
			}
		})
	}
}

func TestBillScopeIncludesRevenueRecipient(t *testing.T) {
	p := config.Payments{ChainID: 143, Contract: "contract", Token: "token", Provider: "provider", Recipient: "recipient"}
	b := Bill{ChainID: 143, Contract: p.Contract, Token: p.Token, Provider: p.Provider, Recipient: p.Recipient}
	if !b.matches(p) {
		t.Fatal("correct bill rejected")
	}
	b.Recipient = p.Provider
	if b.matches(p) {
		t.Fatal("bill with different recipient accepted")
	}
}

func TestValidationRejectsWrongOnchainRecipient(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		p := config.Payments{ChainID: 143, Contract: "0x" + strings.Repeat("1", 40), Token: "0x" + strings.Repeat("2", 40), Provider: "0x" + strings.Repeat("3", 40), Recipient: "0x" + strings.Repeat("4", 40), ContractCodeHash: Keccak([]byte{0x60, 0}), TokenCodeHash: Keccak([]byte{0x60, 0})}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Method string
				Params []json.RawMessage
			}
			json.NewDecoder(r.Body).Decode(&in)
			var result string
			switch in.Method {
			case "eth_chainId":
				result = "0x8f"
			case "eth_getCode":
				result = "0x6000"
			case "eth_call":
				var call map[string]string
				json.Unmarshal(in.Params[0], &call)
				for sig, value := range map[string]string{"provider()": p.Provider, "recipient()": p.Recipient, "paymentToken()": p.Token, "decimals()": "6"} {
					if call["data"] == calldata(sig) {
						if mismatch && sig == "recipient()" {
							value = p.Provider
						}
						result = "0x" + word(value)
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		}))
		p.RPC = server.URL
		err := NewChain(p).Validate(context.Background())
		server.Close()
		if (err != nil) != mismatch {
			t.Fatal("recipient validation failed", err)
		}
	}
}
