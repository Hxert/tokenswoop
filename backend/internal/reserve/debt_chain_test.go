package reserve

import (
	"context"
	"encoding/hex"
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

func TestMainnetReserveValidationPinsVaultCustodyAndRoles(t *testing.T) {
	cfg := config.Reserve{ChainID: 143, Mode: "debt", Contract: "0x" + strings.Repeat("1", 40), Operator: "0x" + strings.Repeat("2", 40), Manager: "0x" + strings.Repeat("3", 40), Custody: "vault", CodeHash: payments.Keccak([]byte{0x60, 0x00})}
	for _, mismatch := range []string{"", "chain", "manager()", "settlementOperator()", "custodyMode()"} {
		t.Run(mismatch, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Method string
					Params []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
					return
				}
				result := "0x"
				switch in.Method {
				case "eth_chainId":
					result = "0x8f"
					if mismatch == "chain" {
						result = "0x279f"
					}
				case "eth_getCode":
					result = "0x6000"
				case "eth_call":
					var call map[string]string
					json.Unmarshal(in.Params[0], &call)
					for sig, address := range map[string]string{"reserveToken()": config.MonadUSDC, "settlementOperator()": cfg.Operator, "manager()": cfg.Manager, "custodyMode()": payments.Keccak([]byte("vault"))} {
						if call["data"] == data(sig) {
							if mismatch == sig {
								address = "0x" + strings.Repeat("9", 40)
							}
							result = "0x" + word(address)
						}
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			chain := NewChain(config.Payments{ChainID: 143, RPC: server.URL, Token: config.MonadUSDC}, cfg)
			if err := chain.Validate(context.Background()); (err == nil) != (mismatch == "") {
				t.Fatal("unexpected validation result", err)
			}
		})
	}
}

func TestDebtConfirmationRequiresExactFinalizedReport(t *testing.T) {
	a := DebtReport{Wallet: "0x" + strings.Repeat("1", 40), Operator: "0x" + strings.Repeat("2", 40), Contract: "0x" + strings.Repeat("3", 40), Epoch: "0", Hash: "0x" + strings.Repeat("4", 64), Items: []DebtItem{{ID: "0x" + strings.Repeat("5", 64), Amount: "7000000"}}}
	var err error
	a.Data, err = debtData(a.Wallet, a.Epoch, a.Items)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(a.Data[2:])
	for _, scenario := range []string{"valid", "unfinalized", "wrong_block", "wrong_epoch", "wrong_debt", "missing_event", "wrong_sender", "reverted"} {
		t.Run(scenario, func(t *testing.T) {
			canonical := "0x" + strings.Repeat("6", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Method string
					Params []json.RawMessage
				}
				json.NewDecoder(r.Body).Decode(&in)
				var result any
				if in.Method == "eth_getTransactionReceipt" {
					from, status, epoch, reportHash := a.Operator, "0x1", "0", payments.Keccak(raw)
					if scenario == "wrong_sender" {
						from = a.Wallet
					}
					if scenario == "wrong_epoch" {
						epoch = "1"
					}
					if scenario == "wrong_debt" {
						reportHash = payments.Keccak([]byte("different charge"))
					}
					if scenario == "reverted" {
						status = "0x0"
					}
					logs := []any{map[string]any{"address": a.Contract, "topics": []string{payments.Keccak([]byte("DebtReported(address,uint256,bytes32)")), "0x" + word(a.Wallet), "0x" + word(epoch)}, "data": reportHash}}
					if scenario == "missing_event" {
						logs = nil
					}
					result = map[string]any{"transactionHash": a.Hash, "from": from, "to": a.Contract, "status": status, "blockNumber": "0x10", "blockHash": canonical, "logs": logs}
				} else {
					var tag string
					json.Unmarshal(in.Params[0], &tag)
					height, hash := "0x11", canonical
					if scenario == "unfinalized" && tag == "finalized" {
						height = "0xf"
					}
					if scenario == "wrong_block" && tag != "finalized" {
						hash = "0x" + strings.Repeat("7", 64)
					}
					result = map[string]string{"number": height, "hash": hash, "timestamp": fmt.Sprintf("0x%x", time.Now().Unix())}
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			chain := NewChain(config.Payments{RPC: server.URL}, config.Reserve{})
			proof, err := chain.ConfirmDebt(context.Background(), a)
			switch scenario {
			case "valid":
				if err != nil || !proof.Finalized || !proof.Success {
					t.Fatal(proof, err)
				}
			case "unfinalized":
				if err != nil || proof.Finalized {
					t.Fatal(proof, err)
				}
			case "reverted":
				if err != nil || !proof.Finalized || proof.Success {
					t.Fatal(proof, err)
				}
			default:
				if err == nil {
					t.Fatal("accepted invalid debt evidence")
				}
			}
		})
	}
}
