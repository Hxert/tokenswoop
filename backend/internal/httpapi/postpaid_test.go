package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestRelayDebitsOnlyAfterConfirmedUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			client := &testClient{t: t, handler: a.Handler()}
			private, wallet := testutil.Wallet(t)
			user := client.login(private, wallet)
			userID := user["id"].(string)
			// Both balance and key budget are below actual cost, and far below
			// the old maximum-context estimate. Neither is reserved or debited.
			testutil.FundWallet(t, a.Store, userID, "0.00001")
			model, err := a.Store.SaveModel(ctx, userID, testutil.Model())
			if err != nil {
				t.Fatal(err)
			}
			started, finish := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(finish) }) }
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`+"\n\n")
					w.(http.Flusher).Flush()
				}
				close(started)
				select {
				case <-finish:
				case <-r.Context().Done():
					return
				}
				if stream {
					fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: "+`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`+"\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"postpaid-fixture","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
				}
			}))
			defer upstream.Close()
			defer release()
			secret, err := security.Encrypt(a.Config.Secret, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err = a.Store.SaveChannel(ctx, userID, store.Channel{ID: uuid.NewString(), Name: "postpaid", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: secret, Enabled: true, Models: map[string]string{model.ID: "fixture"}}); err != nil {
				t.Fatal(err)
			}
			key := object(t, client.call("POST", "/api/keys", map[string]string{"name": "postpaid", "budget": "0.00001"}, 201))
			body := fmt.Sprintf(`{"model":"test-model","messages":[{"role":"user","content":"Hello"}],"max_tokens":32,"stream":%v}`, stream)
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+key["key"].(string))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); client.handler.ServeHTTP(w, r) }()
			select {
			case <-started:
			case <-done:
				t.Fatalf("request rejected before upstream: %s", w.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("upstream not reached")
			}
			var balance, spent, amount string
			if err = a.Store.DB.QueryRow(ctx, "SELECT p.balance::text,k.spent::text,r.amount::text FROM users u JOIN payment_accounts p ON p.user_id=u.id JOIN api_keys k ON k.user_id=u.id JOIN requests r ON r.key_id=k.id WHERE u.id=$1 AND r.state='in_flight'", userID).Scan(&balance, &spent, &amount); err != nil {
				t.Fatal(err)
			}
			if !decimal.RequireFromString(balance).Equal(decimal.NewFromInt(10)) || !decimal.RequireFromString(spent).IsZero() || !decimal.RequireFromString(amount).IsZero() {
				t.Fatal("funds changed before usage was returned")
			}
			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish")
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") || (stream && !strings.Contains(w.Body.String(), "[DONE]")) {
				t.Fatalf("completed request was lost: %s", w.Body.String())
			}
			receipt := object(t, client.call("GET", "/api/requests/"+w.Header().Get("X-Request-ID"), nil, 200))
			if _, exists := receipt["credit_amount"]; exists {
				t.Fatal("retired credit split exposed")
			}
			for field, want := range map[string]string{"amount": "0.00003", "wallet_paid_amount": "0", "unpaid_amount": "0.00003", "waived_amount": "0"} {
				if !decimal.RequireFromString(receipt[field].(string)).Equal(decimal.RequireFromString(want)) {
					t.Fatalf("%s mismatch", field)
				}
			}
			// Older schemas could retain a candidate amount on an unknown
			// request. It must not appear as charged spend after migration.
			legacy := uuid.NewString()
			if _, err = a.Store.DB.Exec(ctx, "INSERT INTO requests(id,user_id,model_id,state,amount,price_snapshot,tier_snapshot) VALUES($1,$2,$3,'usage_unknown',0.2,'{}','{}')", legacy, userID, model.ID); err != nil {
				t.Fatal(err)
			}
			unknown := object(t, client.call("GET", "/api/requests/"+legacy, nil, 200))
			if !decimal.RequireFromString(unknown["amount"].(string)).IsZero() {
				t.Fatal("unconfirmed amount presented as a charge")
			}
			statistics := object(t, client.call("GET", "/api/stats", nil, 200))
			stats := statistics["summary"].(map[string]any)
			for field, want := range map[string]string{"spent": "0.00003", "wallet_paid": "0", "unpaid": "0.00003", "waived": "0"} {
				if !decimal.RequireFromString(stats[field].(string)).Equal(decimal.RequireFromString(want)) {
					t.Fatalf("stats %s mismatch", field)
				}
			}
			var dailyInput, dailyOutput float64
			dailyCost := decimal.Zero
			for _, item := range statistics["daily"].([]any) {
				day := item.(map[string]any)
				dailyInput += day["input_tokens"].(float64)
				dailyOutput += day["output_tokens"].(float64)
				dailyCost = dailyCost.Add(decimal.RequireFromString(day["spent"].(string)))
			}
			if dailyInput != 20 || dailyOutput != 5 || !dailyCost.Equal(decimal.RequireFromString("0.00003")) {
				t.Fatal("daily usage does not match confirmed provider usage and charges")
			}
			client.key = key["key"].(string)
			denied := object(t, client.call("POST", "/v1/chat/completions", map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hello"}}}, 402))
			if denied["error"].(map[string]any)["code"] != "key_budget_exceeded" || calls.Load() != 1 {
				t.Fatal("completed key budget was not enforced")
			}
		})
	}
}
