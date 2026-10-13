package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/testutil"
)

func TestChannelDiscoveryImportAndPostpaidRouting(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	private, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	h := a.Handler()
	admin := &testClient{t: t, handler: h}
	user := admin.login(private, wallet)
	userID := user["id"].(string)
	var discoveryCalls, generationCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("provider credential missing")
		}
		if r.Header.Get("OpenAI-Project") != "fixture-project" {
			t.Error("channel headers were not used for discovery / inference")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/v1/models" {
			discoveryCalls.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"upstream-chat","owned_by":"Fixture"}]}`)
			return
		}
		generationCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("wrong relay path: %s", r.URL.Path)
		}
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		if request["model"] != "upstream-chat" {
			t.Errorf("model mapping lost: %v", request["model"])
		}
		var due string
		if e := a.Store.DB.QueryRow(ctx, "SELECT unpaid_balance::text FROM users WHERE id=$1", userID).Scan(&due); e != nil || !decimal.RequireFromString(due).IsZero() {
			t.Error("due was debited before completion")
		}
		fmt.Fprint(w, `{"id":"fixture","model":"upstream-chat","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer upstream.Close()
	connection := map[string]any{"name": "Primary", "protocol": "openai", "base_url": upstream.URL + "/v1", "secret": "fixture-secret"}
	connection["options"] = map[string]any{"dialect": "openai", "headers": map[string]string{"OpenAI-Project": "fixture-project"}, "request_timeout_seconds": 900, "cache_affinity": true}
	anonymous := &testClient{t: t, handler: h}
	anonymous.call("POST", "/api/admin/channels/fetch-models", connection, 401)
	key2, wallet2 := testutil.Wallet(t)
	other := &testClient{t: t, handler: h}
	other.login(key2, wallet2)
	other.call("POST", "/api/admin/channels/fetch-models", connection, 403)
	csrf := admin.csrf
	admin.csrf = "invalid"
	admin.call("POST", "/api/admin/channels/fetch-models", connection, 403)
	admin.csrf = csrf
	fetched := object(t, admin.call("POST", "/api/admin/channels/fetch-models", connection, 200))
	if len(fetched["data"].([]any)) != 1 {
		t.Fatal("no discovered models")
	}
	var count int
	if e := a.Store.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM models)+(SELECT count(*) FROM channels)+(SELECT count(*) FROM requests)").Scan(&count); e != nil || count != 0 {
		t.Fatal("discovery mutated catalog or billing")
	}
	connection["model_map"] = map[string]string{"public-chat": "upstream-chat"}
	connection["enabled"] = true
	connection["priority"] = 100
	connection["create_missing_models"] = true
	connection["model_defaults"] = map[string]any{"public-chat": map[string]any{"name": "Public Chat", "vendor": "Fixture", "max_input_tokens": 8192, "max_output_tokens": 2048}}
	saved := object(t, admin.call("POST", "/api/admin/channels", connection, 200))
	id := saved["id"].(string)
	stored, err := a.Store.Channel(ctx, id)
	if err != nil || stored.Options.Headers["OpenAI-Project"] != "fixture-project" || stored.Options.RequestTimeoutSeconds != 900 || !stored.Options.CacheAffinity {
		t.Fatal("channel compatibility settings were not persisted")
	}
	if len(saved["created_models"].([]any)) != 1 || saved["revision"] != float64(1) {
		t.Fatal("draft import not reported")
	}
	list := admin.call("GET", "/api/admin/channels", nil, 200)
	if strings.Contains(list.Body.String(), "fixture-secret") || strings.Contains(list.Body.String(), "secret_cipher") {
		t.Fatal("channel key leaked")
	}
	admin.call("POST", "/api/admin/channels/"+id+"/fetch-models", map[string]any{}, 200)
	public := admin.call("GET", "/api/models", nil, 200)
	if strings.Contains(public.Body.String(), "public-chat") {
		t.Fatal("unpriced draft published")
	}
	request := map[string]any{"model": "public-chat", "messages": []map[string]string{{"role": "user", "content": "Hi"}}, "max_completion_tokens": 16}
	admin.call("POST", "/api/admin/channels/"+id+"/test", request, 404)
	if generationCalls.Load() != 0 {
		t.Fatal("unpriced model reached supplier")
	}
	all, e := a.Store.Models(ctx, true)
	if e != nil || len(all) != 1 {
		t.Fatalf("drafts %+v %v", all, e)
	}
	draft := all[0]
	if draft.Enabled || draft.PricingConfigured || draft.Name != "Public Chat" {
		t.Fatalf("unsafe draft %+v", draft)
	}
	draft.Enabled = true
	draft.InputPrice = "1"
	draft.OutputPrice = "2"
	draft.CacheReadPrice = "0.1"
	draft.CacheWritePrice = "1.25"
	draft.CacheWrite1hPrice = "2"
	admin.call("PUT", "/api/admin/models", draft, 200)
	// Importing from another channel must not change shared prices, capabilities or availability.
	connection["name"] = "Fallback"
	connection["priority"] = 10
	repeated := object(t, admin.call("POST", "/api/admin/channels", connection, 200))
	if len(repeated["created_models"].([]any)) != 0 {
		t.Fatal("existing model was imported again")
	}
	configured, e := a.Store.Model(ctx, draft.ID)
	if e != nil || !configured.PricingConfigured || configured.PriceVersion != 2 || !decimal.RequireFromString(configured.InputPrice).Equal(decimal.NewFromInt(1)) {
		t.Fatalf("existing model overwritten %+v %v", configured, e)
	}
	var snapshots int
	a.Store.DB.QueryRow(ctx, "SELECT count(*) FROM model_prices WHERE model_id=$1", draft.ID).Scan(&snapshots)
	if snapshots != 2 {
		t.Fatal("price history changed during import")
	}
	testutil.FundWallet(t, a.Store, userID, "1")
	result := admin.call("POST", "/api/admin/channels/"+id+"/test", request, 200)
	if !strings.Contains(result.Body.String(), `"model":"public-chat"`) {
		t.Fatal("public alias not restored")
	}
	var state, amount, due string
	if e = a.Store.DB.QueryRow(ctx, "SELECT r.state,r.amount::text,u.unpaid_balance::text FROM requests r JOIN users u ON u.id=r.user_id WHERE r.user_id=$1", userID).Scan(&state, &amount, &due); e != nil {
		t.Fatal(e)
	}
	if state != "completed" || !decimal.RequireFromString(amount).Equal(decimal.RequireFromString("0.00003")) || !decimal.RequireFromString(due).Equal(decimal.RequireFromString("0.00003")) {
		t.Fatalf("postpaid result %s %s %s", state, amount, due)
	}
	if discoveryCalls.Load() != 2 || generationCalls.Load() != 1 {
		t.Fatal("metadata fetch triggered extra inference")
	}
	// Saved keys cannot be sent to an edited host, and stale edits cannot replace newer mappings.
	changed := upstream.URL + "/v1"
	changed = strings.Replace(changed, "127.0.0.1", "localhost", 1)
	admin.call("POST", "/api/admin/channels/"+id+"/fetch-models", map[string]any{"base_url": changed}, 400)
	connection["secret"] = ""
	connection["revision"] = 1
	connection["base_url"] = changed
	admin.call("PUT", "/api/admin/channels/"+id, connection, 400)
	connection["base_url"] = upstream.URL + "/v1"
	connection["name"] = "Updated"
	admin.call("PUT", "/api/admin/channels/"+id, connection, 200)
	connection["model_map"] = map[string]string{"must-not-exist": "upstream-chat"}
	admin.call("PUT", "/api/admin/channels/"+id, connection, 409)
	all, e = a.Store.Models(ctx, true)
	if e != nil || len(all) != 1 {
		t.Fatal("stale edit created a model")
	}
	var details string
	if e = a.Store.DB.QueryRow(ctx, "SELECT COALESCE(string_agg(details::text,''),'') FROM audit_events").Scan(&details); e != nil || strings.Contains(details, "fixture-secret") {
		t.Fatal("audit leaked credentials")
	}
}

func TestChannelValidationAndDiscoveryRateLimit(t *testing.T) {
	a := testApp(t)
	private, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	admin := &testClient{t: t, handler: a.Handler()}
	admin.login(private, wallet)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	defer server.Close()
	body := map[string]any{"name": "Saved connection", "protocol": "openai", "base_url": server.URL, "secret": "key", "enabled": false}
	saved := object(t, admin.call("POST", "/api/admin/channels", body, 200))
	id := saved["id"].(string)
	ch, e := a.Store.Channel(context.Background(), id)
	if e != nil || ch.Enabled || len(ch.Models) != 0 {
		t.Fatal("empty disabled connection not saved")
	}
	body["enabled"] = true
	admin.call("POST", "/api/admin/channels", body, 400)
	body["model_map"] = map[string]string{"unknown": "model"}
	admin.call("POST", "/api/admin/channels", body, 400)
	body["create_missing_models"] = true
	body["model_map"] = map[string]string{"bad id": "model"}
	admin.call("POST", "/api/admin/channels", body, 400)
	a.Config.AllowPrivateUpstreams = false
	admin.call("POST", "/api/admin/channels/fetch-models", body, 400)
	a.Config.AllowPrivateUpstreams = true
	for i := 0; i < 10; i++ {
		admin.call("POST", "/api/admin/channels/"+id+"/fetch-models", map[string]any{}, 200)
	}
	admin.call("POST", "/api/admin/channels/"+id+"/fetch-models", map[string]any{}, 429)
}

func TestAdminChannelPolicyValidation(t *testing.T) {
	a := testApp(t)
	private, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	client := &testClient{t: t, handler: a.Handler()}
	client.login(private, wallet)
	for _, options := range []map[string]any{
		{"headers": map[string]string{"Authorization": "Bearer should-not-be-stored"}},
		{"parameter_overrides": map[string]any{"store": true}},
		{"drop_parameters": []string{"max_tokens"}},
		{"request_timeout_seconds": 7200},
	} {
		input := map[string]any{"name": "invalid fixture", "protocol": "openai", "base_url": "https://example.com/v1", "secret": "fixture", "enabled": false, "options": options}
		reply := client.call("POST", "/api/admin/channels", input, 400)
		if !strings.Contains(reply.Body.String(), "invalid_channel_options") {
			t.Fatal(reply.Body.String())
		}
	}
	var count int
	if e := a.Store.DB.QueryRow(context.Background(), "SELECT count(*) FROM channels").Scan(&count); e != nil || count != 0 {
		t.Fatal("invalid policy saved")
	}
}
