package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

// This opt-in test exports a bill fixture; it NEVER broadcasts a transaction.
// All wallet snapshots, configuration and accounts exist only in a disposable test schema.
// The separate testnet runner pays the same metered amount using valueless kUSDC.
func TestExportTestnetBillingFixture(t *testing.T) {
	output := os.Getenv("TESTNET_BILLING_FIXTURE")
	if output == "" {
		t.Skip("opt-in fixture export: use make testnet-billing-prepare")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("refusing to replace an existing bill fixture")
	}
	a := testApp(t)
	ctx := context.Background()
	contracts := os.Getenv("TESTNET_CONTRACTS_DIR")
	var wallets struct {
		ChainID      int                                           `json:"chainId"`
		PasswordFile string                                        `json:"passwordFile"`
		Accounts     map[string]struct{ Address, Keystore string } `json:"accounts"`
	}
	b, err := os.ReadFile(filepath.Join(contracts, "deployments/testnet-wallets.json"))
	if err != nil || json.Unmarshal(b, &wallets) != nil || wallets.ChainID != 10143 {
		t.Fatal("a Monad Testnet wallet manifest is required")
	}
	wallet := wallets.Accounts["user"]
	if !security.WalletPattern.MatchString(wallet.Address) || wallet.Keystore == "" || wallets.PasswordFile == "" {
		t.Fatal("missing test user wallet")
	}
	client := &testClient{t: t, handler: a.Handler()}
	challenge := object(t, client.call("POST", "/api/auth/challenge", map[string]string{"wallet": wallet.Address}, 200))
	signCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(signCtx, filepath.Join(contracts, ".toolchain/bin/cast"), "wallet", "sign", challenge["message"].(string),
		"--keystore", wallet.Keystore, "--password-file", wallets.PasswordFile)
	cmd.Dir = contracts
	signature, err := cmd.Output()
	if err != nil {
		t.Fatal("could not sign the login challenge with the encrypted test wallet")
	}
	login := map[string]string{"id": challenge["id"].(string), "signature": strings.TrimSpace(string(signature))}
	reply := client.call("POST", "/api/auth/login", login, 200)
	client.cookies = reply.Result().Cookies()
	for _, c := range client.cookies {
		if c.Name == "ts_csrf" {
			client.csrf = c.Value
		}
	}
	user := object(t, reply)
	if user["wallet"] != strings.ToLower(wallet.Address) || user["role"] != "user" {
		t.Fatal("test wallet was not authenticated as an ordinary user")
	}
	client.call("POST", "/api/auth/login", login, 401)
	userID := user["id"].(string)
	testutil.FundWallet(t, a.Store, userID, "1")
	model, err := a.Store.SaveModel(ctx, userID, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-only" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "fixture-upstream" {
			t.Error("incorrect upstream mapping or credentials")
			w.WriteHeader(400)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"id":"chatcmpl-testnet-fixture","model":"fixture-upstream","choices":[{"index":0,"delta":{"content":"Testnet fixture"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-testnet-fixture","model":"fixture-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Testnet fixture"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer upstream.Close()
	cipher, err := security.Encrypt(a.Config.Secret, "fixture-only")
	if err != nil {
		t.Fatal(err)
	}
	channel := store.Channel{ID: uuid.NewString(), Name: "Testnet billing fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "fixture-upstream"}}
	if err = a.Store.SaveChannel(ctx, userID, channel); err != nil {
		t.Fatal(err)
	}
	key := object(t, client.call("POST", "/api/keys", map[string]any{"name": "Testnet fixture", "budget": "1", "allowed_models": []string{model.ID}}, 201))
	client.key = key["key"].(string)
	var requests []map[string]any
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			body := map[string]any{"model": model.ID, "stream": stream}
			if path == "/v1/responses" {
				body["input"], body["max_output_tokens"] = "Hello", 32
			} else {
				body["messages"], body["max_tokens"] = []map[string]string{{"role": "user", "content": "Hello"}}, 32
			}
			response := client.call("POST", path, body, 200)
			if !strings.Contains(response.Body.String(), "Testnet fixture") || (stream && !strings.Contains(response.Body.String(), "[DONE]") && !strings.Contains(response.Body.String(), "response.completed")) {
				t.Fatal("missing content or final stream event")
			}
			id := response.Header().Get("X-Request-ID")
			receipt := object(t, client.call("GET", "/api/requests/"+id, nil, 200))
			if receipt["state"] != "completed" || !decimal.RequireFromString(receipt["unpaid_amount"].(string)).Equal(decimal.RequireFromString("0.00003")) {
				t.Fatal("metered amount differs from 20 input + 5 output tokens at 1 / 2 per million")
			}
			var stored []byte
			err = a.Store.DB.QueryRow(ctx, `SELECT jsonb_build_object('requestId',id,'state',state,'amount',amount::text,'inputTokens',input_tokens,'outputTokens',output_tokens,'usageSource',usage_source,'priceSnapshot',price_snapshot,'tierSnapshot',tier_snapshot) FROM requests WHERE id=$1 AND user_id=$2`, id, userID).Scan(&stored)
			var record map[string]any
			if err != nil || json.Unmarshal(stored, &record) != nil {
				t.Fatal("could not read persisted request pricing")
			}
			record["path"], record["stream"] = path, stream
			requests = append(requests, record)
		}
	}
	var unpaid, spent, usageDebit string
	if err = a.Store.DB.QueryRow(ctx, `SELECT unpaid_balance::text FROM users WHERE id=$1`, userID).Scan(&unpaid); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.DB.QueryRow(ctx, `SELECT spent::text FROM api_keys WHERE id=$1`, key["id"]).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.DB.QueryRow(ctx, `SELECT sum(amount)::text FROM requests WHERE user_id=$1 AND state='completed'`, userID).Scan(&usageDebit); err != nil {
		t.Fatal(err)
	}
	total := decimal.RequireFromString("0.00012")
	if !decimal.RequireFromString(spent).Equal(total) || !decimal.RequireFromString(usageDebit).Equal(total) || !decimal.RequireFromString(unpaid).Equal(total) {
		t.Fatal("wallet payable, key or ledger totals do not reconcile")
	}
	client.call("DELETE", "/api/keys/"+key["id"].(string), nil, 204)
	client.call("GET", "/v1/models", nil, 401)
	if t.Failed() {
		t.Fatal("refusing to export a failed fixture")
	}
	report := map[string]any{"version": 1, "mode": "isolated-gateway-fixture", "chainId": 10143, "wallet": strings.ToLower(wallet.Address), "createdAt": time.Now().UTC(),
		"upstream": "local deterministic HTTP fixture; no paid supplier call", "funding": "simulated confirmed wallet snapshot; exported amount is only an isolated testnet fixture, not a production payable", "requests": requests, "totalAmount": total.String(), "totalUnits": "120",
		"checks": []string{"encrypted-wallet-login", "challenge-replay-rejected", "api-key-auth", "chat-json-sse", "responses-json-sse", "price-snapshots", "ledger-key-payable-reconciled", "postpaid-no-funds-held", "revoked-key-rejected"}}
	b, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("could not save fixture")
	}
	t.Log("Exported four metered requests totaling 0.000120 test kUSDC; no chain transaction sent")
}
