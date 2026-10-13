package httpapi

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"tokenswoop/backend/internal/pricing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func operatorClient(t *testing.T, a *App) (*testClient, string) {
	c := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	if a.Config.AdminWallets == nil {
		a.Config.AdminWallets = map[string]bool{}
	}
	a.Config.AdminWallets[wallet] = true
	u := c.login(private, wallet)
	id := u["id"].(string)
	if _, e := a.Store.DB.Exec(context.Background(), "UPDATE users SET role='admin' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	testutil.FundWallet(t, a.Store, id, "10")
	key := object(t, c.call("POST", "/api/keys", map[string]string{"name": "fixture", "budget": "10"}, 201))
	c.key = key["key"].(string)
	return c, id
}
func TestOperationalPricingAndCleanup(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	ctx := context.Background()
	m := testutil.Model()
	m.Pricing = pricing.Config{Mode: "tiered", Tiers: []pricing.Tier{{Name: "long", Rates: pricing.Rates{Input: "2", Output: "8", CacheRead: "0.2", CacheWrite: "2.5", CacheWrite1h: "4"}}}}
	if _, e := a.Store.SaveModel(ctx, uid, m); e != nil {
		t.Fatal(e)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
	}))
	defer upstream.Close()
	secret, _ := security.Encrypt(a.Config.Secret, "fixture")
	ch := store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: "openai", BaseURL: upstream.URL, Models: map[string]string{m.ID: "upstream"}, SecretCipher: secret, Enabled: true}
	if e := a.Store.SaveChannel(ctx, uid, ch); e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"model": m.ID, "messages": []map[string]string{{"role": "user", "content": "Hi"}}}
	resp := c.call("POST", "/v1/chat/completions", body, 200)
	id := resp.Header().Get("X-Request-ID")
	receipt := object(t, c.call("GET", "/api/requests/"+id, nil, 200))
	trace := receipt["pricing_trace"].(map[string]any)
	if trace["matched_tier"] != "long" || trace["amount"] != "0.00008" {
		t.Fatalf("wrong trace %+v", trace)
	}
	stats := object(t, c.call("GET", "/api/admin/operations?channel="+ch.ID, nil, 200))
	if stats["summary"].(map[string]any)["completed"] != float64(1) || len(stats["data"].([]any)) != 1 {
		t.Fatalf("bad ops response %v", stats)
	}
	c.call("GET", "/api/admin/operations/requests/"+id, nil, 200)
	c.call("GET", "/api/admin/operations?from=bad", nil, 400)
	// Run a durable, bounded cleanup task on old diagnostics; the charge and its
	// frozen price calculation must still be present afterwards.
	if _, e := a.Store.DB.Exec(ctx, "UPDATE requests SET created_at=now()-interval '90 days' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	job := object(t, c.call("POST", "/api/admin/operations/cleanup", map[string]int{"retain_days": 30}, 202))
	c.call("POST", "/api/admin/operations/cleanup", map[string]int{"retain_days": 30}, 409)
	if e := a.Store.ProcessOperations(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	var processed int
	if e := a.Store.DB.QueryRow(ctx, "SELECT state,processed FROM operations_tasks WHERE id=$1", job["id"]).Scan(&state, &processed); e != nil || state != "completed" || processed != 1 {
		t.Fatalf("cleanup %s %d %v", state, processed, e)
	}
	receipt = object(t, c.call("GET", "/api/requests/"+id, nil, 200))
	if receipt["pricing_trace"] == nil || len(receipt["attempts"].([]any)) != 0 || receipt["state"] != "completed" {
		t.Fatal("cleanup lost ledger or did not delete attempts")
	}
	c.call("GET", "/api/admin/operations/tasks", nil, 200)
	// Ordinary users cannot inspect other accounts' requests or queue cleanup.
	if _, e := a.Store.DB.Exec(ctx, "UPDATE users SET role='user' WHERE id=$1", uid); e != nil {
		t.Fatal(e)
	}
	c.call("GET", "/api/admin/operations", nil, 403)
	c.call("POST", "/api/admin/operations/cleanup", map[string]int{"retain_days": 30}, 403)
}
func TestStoredResponsesAreAccountAndChannelScoped(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	ctx := context.Background()
	m, e := a.Store.SaveModel(ctx, uid, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		fmt.Fprintf(w, `{"id":"resp_%d","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":20,"output_tokens":5}}`, n)
	}))
	defer upstream.Close()
	secret, _ := security.Encrypt(a.Config.Secret, "fixture")
	ch := store.Channel{ID: uuid.NewString(), Name: "responses", Protocol: "openai_responses", BaseURL: upstream.URL, SecretCipher: secret, Enabled: true, Models: map[string]string{m.ID: "upstream"}, Options: store.ChannelOptions{AllowStoredResponses: true}}
	if e = a.Store.SaveChannel(ctx, uid, ch); e != nil {
		t.Fatal(e)
	}
	first := object(t, c.call("POST", "/v1/responses", map[string]any{"model": m.ID, "input": "Hello", "store": true}, 200))
	previous := first["id"].(string)
	c.call("POST", "/v1/responses", map[string]any{"model": m.ID, "input": "Continue", "previous_response_id": previous}, 200)
	c.call("POST", "/v1/responses", map[string]any{"model": m.ID, "input": "Continue", "previous_response_id": "unknown"}, 400)
	other, _ := operatorClient(t, a)
	other.call("POST", "/v1/responses", map[string]any{"model": m.ID, "input": "Continue", "previous_response_id": previous}, 400)
	if calls.Load() != 2 {
		t.Fatal("invalid context reached upstream")
	}
	if _, e = a.Store.DB.Exec(ctx, "UPDATE channels SET revision=revision+1 WHERE id=$1", ch.ID); e != nil {
		t.Fatal(e)
	}
	c.call("POST", "/v1/responses", map[string]any{"model": m.ID, "input": "Continue", "previous_response_id": previous}, 503)
	if calls.Load() != 2 {
		t.Fatal("context escaped channel revision binding")
	}
}
func TestNativeAuthAndCatalog(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	m, e := a.Store.SaveModel(context.Background(), uid, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ method, path, header, body, want string }{
		{"GET", "/v1/models/" + m.ID, "Authorization", "", m.ID},
		{"GET", "/v1/models", "x-api-key", "", "display_name"},
		{"GET", "/v1beta/models", "x-goog-api-key", "", "supportedGenerationMethods"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		key := c.key
		if tc.header == "Authorization" {
			key = "Bearer " + key
		}
		r.Header.Set(tc.header, key)
		if tc.header == "x-api-key" {
			r.Header.Set("anthropic-version", "2023-06-01")
		}
		w := httptest.NewRecorder()
		c.handler.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+c.key)
	r.Header.Set("x-api-key", "ts_different")
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambiguous key accepted")
	}
}
func TestComplexPricePreviewDoesNotCharge(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	m := testutil.Model()
	m.Pricing = pricing.Config{Mode: "expression", Expression: `param("service_tier")=="priority" ? fixed(0.01) : p*2+c*8`}
	response := object(t, c.call("POST", "/api/admin/pricing/preview", map[string]any{"model": m, "facts": pricing.Facts{Input: 20, Output: 5, Parameters: map[string]any{"service_tier": "priority"}}}, 200))
	if response["trace"].(map[string]any)["amount"] != "0.01" {
		t.Fatal(response)
	}
	var n int
	var balance string
	a.Store.DB.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM requests),unpaid_balance::text FROM users WHERE id=$1", uid).Scan(&n, &balance)
	if n != 0 || balance != "0.000000000000000000" {
		t.Fatal("preview touched accounting")
	}
}
