package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

// Explicit opt-in only. This schema is retained, including encrypted signed
// transactions, so interrupted runs resume the same bills and provider nonces.
func TestTestnetWalletBilling(t *testing.T) {
	run := os.Getenv("TESTNET_PAYMENT_RUN")
	if run == "" {
		t.Skip("use node scripts/wallet-payment-test.mjs")
	}
	if !regexp.MustCompile(`^[a-z0-9_]{1,32}$`).MatchString(run) {
		t.Fatal("invalid run name")
	}
	cfg, e := config.Load()
	if e != nil || !cfg.Payments.Enabled || cfg.ChainID != 10143 {
		t.Fatal("testnet payment configuration required", e)
	}
	parsed, _ := url.Parse(cfg.DatabaseURL)
	if parsed == nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("local database required")
	}
	ctx := context.Background()
	root, e := pgxpool.New(ctx, cfg.DatabaseURL)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	schema := "testnet_payment_" + run
	if _, e = root.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); e != nil {
		t.Fatal(e)
	}
	pc, e := pgxpool.ParseConfig(cfg.DatabaseURL)
	if e != nil {
		t.Fatal(e)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &store.Store{DB: db}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	ro, e := redis.ParseURL(cfg.RedisURL)
	if e != nil {
		t.Fatal(e)
	}
	cache := redis.NewClient(ro)
	defer cache.Close()
	cfg.AdminWallets = map[string]bool{}
	cfg.AllowPrivateUpstreams = true
	cfg.Environment = "test"
	chain := payments.NewChain(cfg.Payments)
	service := payments.New(s, cfg.Payments, chain, cfg.Secret)
	if e = chain.ValidateSigner(ctx); e != nil {
		t.Fatal(e)
	}
	a := &App{Config: cfg, Store: s, Redis: cache, Gateway: gateway.New(s, cfg.Secret, true), Payments: service}
	contracts := os.Getenv("TESTNET_CONTRACTS_DIR")
	var wallets struct {
		PasswordFile string
		Accounts     map[string]struct{ Address, Keystore string }
	}
	raw, e := os.ReadFile(filepath.Join(contracts, "deployments/testnet-wallets.json"))
	if e != nil || json.Unmarshal(raw, &wallets) != nil {
		t.Fatal("test wallet manifest unavailable")
	}
	wallet := wallets.Accounts["user"]
	client := &testClient{t: t, handler: a.Handler()}
	challenge := object(t, client.call("POST", "/api/auth/challenge", map[string]string{"wallet": wallet.Address}, 200))
	cmd := exec.CommandContext(ctx, cfg.Payments.Cast, "wallet", "sign", challenge["message"].(string), "--keystore", filepath.Join(contracts, wallet.Keystore), "--password-file", filepath.Join(contracts, wallets.PasswordFile))
	signature, e := cmd.Output()
	if e != nil {
		t.Fatal("encrypted test wallet login signing failed")
	}
	reply := client.call("POST", "/api/auth/login", map[string]string{"id": challenge["id"].(string), "signature": strings.TrimSpace(string(signature))}, 200)
	client.cookies = reply.Result().Cookies()
	for _, cookie := range client.cookies {
		if cookie.Name == "ts_csrf" {
			client.csrf = cookie.Value
		}
	}
	u := object(t, reply)["id"].(string)
	phase := os.Getenv("TESTNET_PAYMENT_PHASE")
	var count int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM requests WHERE user_id=$1`, u).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if phase == "generate" && count == 0 {
		if e = service.Tick(ctx); e != nil {
			t.Fatal(e)
		}
		client.call("POST", "/api/billing/refresh", nil, 200)
		model, e := s.SaveModel(ctx, u, testutil.Model())
		if e != nil {
			t.Fatal(e)
		}
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Stream bool }
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				w.WriteHeader(400)
				return
			}
			if in.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"Wallet billing fixture"},"finish_reason":"stop"}]}`+"\n\ndata: "+`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`+"\n\ndata: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"wallet-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Wallet billing fixture"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
		}))
		defer upstream.Close()
		encrypted, e := security.Encrypt(cfg.Secret, "fixture")
		if e != nil {
			t.Fatal(e)
		}
		if e = s.SaveChannel(ctx, u, store.Channel{ID: uuid.NewString(), Name: "Wallet billing fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: encrypted, Models: map[string]string{model.ID: "fixture"}, Enabled: true}); e != nil {
			t.Fatal(e)
		}
		key := object(t, client.call("POST", "/api/keys", map[string]string{"name": "Wallet postpaid smoke", "budget": "1"}, 201))
		client.key = key["key"].(string)
		for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
			for _, stream := range []bool{false, true} {
				in := map[string]any{"model": model.ID, "stream": stream}
				if path == "/v1/responses" {
					in["input"] = "hello"
					in["max_output_tokens"] = 32
				} else {
					in["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
					in["max_tokens"] = 32
				}
				response := client.call("POST", path, in, 200)
				id := response.Header().Get("X-Request-ID")
				receipt := object(t, client.call("GET", "/api/requests/"+id, nil, 200))
				if receipt["state"] != "completed" {
					t.Fatal("request incomplete")
				}
			}
		}
		user, e := s.User(ctx, u)
		if e != nil || user.Unpaid != "0.000120000000000000" {
			t.Fatal("incorrect postpaid total", e)
		}
	}
	if phase == "blocked" {
		if e = service.Tick(ctx); e != nil {
			t.Fatal(e)
		}
		var state, code string
		if e = db.QueryRow(ctx, `SELECT state,error_code FROM bills WHERE user_id=$1`, u).Scan(&state, &code); e != nil {
			t.Fatal(e)
		}
		if state != "paid" && (state != "blocked" || code != "insufficient_allowance") {
			t.Fatal("revoked allowance did not block payment")
		}
	}
	if phase == "settle" || phase == "verify" {
		var id, state string
		if e = db.QueryRow(ctx, `SELECT id,state FROM bills WHERE user_id=$1`, u).Scan(&id, &state); e != nil {
			t.Fatal(e)
		}
		if state == "blocked" {
			client.call("POST", "/api/billing/bills/"+id+"/retry", nil, 202)
		}
		for n := 0; n < 20 && state != "paid"; n++ {
			if e = service.Tick(ctx); e != nil {
				t.Log("settlement will retry:", e)
			}
			if e = db.QueryRow(ctx, `SELECT state FROM bills WHERE id=$1`, id).Scan(&state); e != nil {
				t.Fatal(e)
			}
			if state != "paid" {
				time.Sleep(2 * time.Second)
			}
		}
		if state != "paid" {
			t.Fatal("payment pending; resume this same named run")
		}
		for i := 0; i < 2; i++ {
			if e = service.Tick(ctx); e != nil {
				t.Fatal(e)
			}
		}
		var due, paid string
		var attempts int
		if e = db.QueryRow(ctx, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u).Scan(&due); e != nil {
			t.Fatal(e)
		}
		if e = db.QueryRow(ctx, `SELECT sum(wallet_paid_amount)::text FROM requests WHERE user_id=$1`, u).Scan(&paid); e != nil {
			t.Fatal(e)
		}
		if e = db.QueryRow(ctx, `SELECT count(*) FROM payment_attempts`).Scan(&attempts); e != nil {
			t.Fatal(e)
		}
		if due != "0.000000000000000000" || paid != "0.000120000000000000" || attempts != 1 {
			t.Fatal("payment ledger mismatch")
		}
		client.call("GET", "/api/billing/bills/"+id, nil, 200)
	}
	bills := object(t, client.call("GET", "/api/billing/bills", nil, 200))
	report := map[string]any{"run": run, "phase": phase, "schema": schema, "wallet": wallet.Address, "chain_id": 10143, "funding": "postpaid onchain wallet settlement", "upstream": "local deterministic HTTP fixture; no paid supplier calls", "bills": bills["data"]}
	out, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("TESTNET_PAYMENT_REPORT"), out, 0600); e != nil {
		t.Fatal(e)
	}
}
