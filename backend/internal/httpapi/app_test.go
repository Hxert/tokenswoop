package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/testutil"
)

type testClient struct {
	t         *testing.T
	handler   http.Handler
	cookies   []*http.Cookie
	key, csrf string
}

func (client *testClient) call(method, path string, body any, status int) *httptest.ResponseRecorder {
	client.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", client.csrf)
	for _, cookie := range client.cookies {
		r.AddCookie(cookie)
	}
	if client.key != "" {
		r.Header.Set("Authorization", "Bearer "+client.key)
	}
	w := httptest.NewRecorder()
	client.handler.ServeHTTP(w, r)
	if w.Code != status {
		client.t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func object(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func testApp(t *testing.T) *App {
	t.Helper()
	s := testutil.Database(t)
	dsn := os.Getenv("TEST_REDIS_URL")
	if dsn == "" {
		t.Skip("TEST_REDIS_URL required for HTTP integration tests")
	}
	opts, e := redis.ParseURL(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cache := redis.NewClient(opts)
	t.Cleanup(func() { cache.Close() })
	if e = cache.Ping(context.Background()).Err(); e != nil {
		t.Fatal(e)
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Origin: "http://localhost:5173", Secret: secret, Environment: "test", ChainID: 10143, SessionTTL: time.Hour, AdminWallets: map[string]bool{}, AllowPrivateUpstreams: true}
	return &App{Store: s, Redis: cache, Config: cfg, Gateway: gateway.New(s, secret, true)}
}
func (client *testClient) login(key *secp256k1.PrivateKey, wallet string) map[string]any {
	client.t.Helper()
	challenge := object(client.t, client.call("POST", "/api/auth/challenge", map[string]string{"wallet": wallet}, 200))
	data := map[string]string{"id": challenge["id"].(string), "signature": testutil.Sign(key, challenge["message"].(string))}
	w := client.call("POST", "/api/auth/login", data, 200)
	client.cookies = w.Result().Cookies()
	for _, c := range client.cookies {
		if c.Name == "ts_csrf" {
			client.csrf = c.Value
		}
		if c.Name == "ts_session" && !c.HttpOnly {
			client.t.Fatal("session cookie not HttpOnly")
		}
	}
	client.call("POST", "/api/auth/login", data, 401)
	return object(client.t, w)
}

func TestAuthenticatedProductJourney(t *testing.T) {
	a := testApp(t)
	private, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	h := a.Handler()
	admin := &testClient{t: t, handler: h}
	admin.call("GET", "/health/ready", nil, 200)
	admin.call("GET", "/api/me", nil, 401)
	user := admin.login(private, wallet)
	if user["role"] != "admin" {
		t.Fatal("bootstrap role not assigned")
	}
	if _, exists := user["credit_balance"]; exists {
		t.Fatal("retired credit balance exposed")
	}
	publicConfig := object(t, admin.call("GET", "/api/config", nil, 200))
	for _, field := range []string{"credit_claim_enabled", "turnstile_site_key"} {
		if _, exists := publicConfig[field]; exists {
			t.Fatal("retired claim configuration exposed")
		}
	}
	csrf := admin.csrf
	admin.csrf = "wrong"
	admin.call("POST", "/api/keys", map[string]string{"name": "bad", "budget": "1"}, 403)
	admin.csrf = csrf
	admin.call("POST", "/api/credits/claim", map[string]string{"token": "unused"}, 404)
	testutil.FundWallet(t, a.Store, user["id"].(string), "1")
	model := testutil.Model()
	admin.call("PUT", "/api/admin/models", model, 200)
	var hits atomic.Int32
	var truncated atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		if v["model"] != "fixture-upstream" {
			t.Errorf("mapping missing: %v", v["model"])
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("missing provider key")
		}
		if stream, _ := v["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"id":"chatcmpl-fixture","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":"stop"}]}`+"\n\n")
			if !truncated.Load() {
				fmt.Fprint(w, "data: "+`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`+"\n\ndata: [DONE]\n\n")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-fixture","model":"fixture-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer upstream.Close()
	admin.call("POST", "/api/admin/channels", map[string]any{"name": "fixture", "protocol": "openai", "base_url": upstream.URL, "secret": "upstream-secret", "model_map": map[string]string{model.ID: "fixture-upstream"}, "priority": 10, "enabled": true}, 200)
	channels := admin.call("GET", "/api/admin/channels", nil, 200)
	if strings.Contains(channels.Body.String(), "upstream-secret") || strings.Contains(channels.Body.String(), "secret_cipher") {
		t.Fatal("channel credentials leaked")
	}
	keyReply := object(t, admin.call("POST", "/api/keys", map[string]any{"name": "First project", "budget": "1", "allowed_models": []string{model.ID}}, 201))
	keyID := keyReply["id"].(string)
	bearer := keyReply["key"].(string)
	admin.key = bearer
	keys := admin.call("GET", "/api/keys", nil, 200)
	if strings.Contains(keys.Body.String(), bearer) {
		t.Fatal("key can be retrieved after creation")
	}
	admin.call("GET", "/v1/models", nil, 200)
	req := map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hello"}}, "max_completion_tokens": 32}
	result := admin.call("POST", "/v1/chat/completions", req, 200)
	if !strings.Contains(result.Body.String(), `"model":"test-model"`) {
		t.Fatal("public model alias lost")
	}
	admin.call("GET", "/api/requests/"+result.Header().Get("X-Request-ID"), nil, 200)
	req["stream"] = true
	stream := admin.call("POST", "/api/playground", req, 200)
	if !strings.Contains(stream.Body.String(), "[DONE]") {
		t.Fatal("stream did not complete")
	}
	admin.call("POST", "/v1/responses", map[string]any{"model": model.ID, "input": "Hello", "max_output_tokens": 32}, 200)
	admin.call("GET", "/api/stats", nil, 200)
	admin.call("GET", "/api/usage?model=test-model&key="+keyID, nil, 200)
	admin.call("GET", "/api/credits", nil, 404)
	admin.call("GET", "/api/admin/audit", nil, 200)
	admin.call("GET", "/api/admin/health", nil, 200)
	truncated.Store(true)
	// This fixture puts all content in the terminal frame. It is withheld until
	// usage is confirmed, so truncation can still return a proper HTTP failure.
	uncertain := admin.call("POST", "/v1/chat/completions", req, 502)
	if strings.Contains(uncertain.Body.String(), "[DONE]") || !strings.Contains(uncertain.Body.String(), "incomplete_stream") {
		t.Fatal("cutoff reported as complete")
	}
	unknownID := uncertain.Header().Get("X-Request-ID")
	admin.call("GET", "/api/admin/requests/unknown", nil, 200)
	admin.call("POST", "/api/admin/requests/"+unknownID+"/waive", nil, 204)
	before := hits.Load()
	admin.call("DELETE", "/api/keys/"+keyID, nil, 204)
	admin.call("POST", "/v1/chat/completions", req, 401)
	if hits.Load() != before {
		t.Fatal("revoked key reached upstream")
	}
	// A separate wallet cannot administer or inspect another account's requests.
	otherKey, otherWallet := testutil.Wallet(t)
	other := &testClient{t: t, handler: h}
	other.login(otherKey, otherWallet)
	other.call("GET", "/api/admin/users", nil, 403)
	other.call("GET", "/api/requests/"+unknownID, nil, 404)
	truncated.Store(false)
	// Exhausted automatic retries cool down the fixture channel. Simulate its
	// recovery before exercising the unrelated second-account journey.
	if _, err := a.Store.DB.Exec(context.Background(), "UPDATE channels SET failure_count=0,disabled_until=NULL,last_error=''"); err != nil {
		t.Fatal(err)
	}
	// A fresh account has no settlement failure, so no wallet snapshot is
	// needed to start its first paid request.
	other.call("POST", "/api/playground", req, 200)
	admin.key = ""
	admin.call("POST", "/api/auth/logout", nil, 204)
	admin.call("GET", "/api/me", nil, 401)
}

func TestExpiredChallengesOriginAndServerPermissions(t *testing.T) {
	a := testApp(t)
	client := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	challenge := object(t, client.call("POST", "/api/auth/challenge", map[string]string{"wallet": wallet}, 200))
	a.Store.DB.Exec(context.Background(), "UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE id=$1", challenge["id"])
	client.call("POST", "/api/auth/login", map[string]string{"id": challenge["id"].(string), "signature": testutil.Sign(private, challenge["message"].(string))}, 401)
	r := httptest.NewRequest("POST", "/api/auth/challenge", strings.NewReader(`{"wallet":"`+wallet+`"}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	client.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin auth permitted")
	}
	client.login(private, wallet)
	a.Store.DB.Exec(context.Background(), "UPDATE users SET status='suspended' WHERE wallet=$1", wallet)
	client.call("GET", "/api/me", nil, 403)
}

func TestRateLimitAtomicAndFailClosed(t *testing.T) {
	a := testApp(t)
	for i := 0; i < 4; i++ {
		ok, e := a.allow(context.Background(), "fixture", 3)
		if e != nil {
			t.Fatal(e)
		}
		if ok != (i < 3) {
			t.Fatalf("limiter admission %d = %v", i, ok)
		}
	}
	a.Redis.Close()
	if _, e := a.allow(context.Background(), "new-fixture", 3); e == nil {
		t.Fatal("unavailable Redis accepted request")
	}
}
