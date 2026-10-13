package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func workspaceModel(t *testing.T, a *App, userID, upstreamURL string) store.Model {
	t.Helper()
	model, err := a.Store.SaveModel(context.Background(), userID, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := security.Encrypt(a.Config.Secret, "workspace-fixture")
	if err != nil {
		t.Fatal(err)
	}
	err = a.Store.SaveChannel(context.Background(), userID, store.Channel{
		ID: uuid.NewString(), Name: "Workspace test", Protocol: "openai", BaseURL: upstreamURL,
		SecretCipher: secret, Enabled: true, Models: map[string]string{model.ID: "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestUnlimitedKeyBudget(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	client := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	userID := client.login(private, wallet)["id"].(string)
	testutil.FundWallet(t, a.Store, userID, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer upstream.Close()
	model := workspaceModel(t, a, userID, upstream.URL)
	for _, budget := range []any{"", "0", "-1", "NaN", "Infinity", "1000000000001", true} {
		client.call("POST", "/api/keys", map[string]any{"name": "Invalid", "budget": budget}, 400)
	}
	unlimited := object(t, client.call("POST", "/api/keys", map[string]any{"name": "Unlimited", "budget": nil, "allowed_models": []string{model.ID}}, 201))
	omitted := object(t, client.call("POST", "/api/keys", map[string]any{"name": "Default unlimited"}, 201))
	limited := object(t, client.call("POST", "/api/keys", map[string]any{"name": "Limited", "budget": "0.00001"}, 201))
	if err := a.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	list := object(t, client.call("GET", "/api/keys", nil, 200))["data"].([]any)
	for _, row := range list {
		key := row.(map[string]any)
		if key["id"] == unlimited["id"] || key["id"] == omitted["id"] {
			if key["budget"] != nil {
				t.Fatal("unlimited budget must round-trip as null")
			}
		} else if !decimal.RequireFromString(key["budget"].(string)).Equal(decimal.RequireFromString("0.00001")) {
			t.Fatal("existing finite budget changed")
		}
	}
	// A real absence of a cap must work even beyond the largest finite API budget.
	if _, err := a.Store.DB.Exec(ctx, "UPDATE api_keys SET spent=2000000000000 WHERE id=$1", unlimited["id"]); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hello"}}}
	client.key = unlimited["key"].(string)
	for i := 0; i < 2; i++ {
		client.call("POST", "/v1/chat/completions", body, 200)
	}
	var spent string
	if err := a.Store.DB.QueryRow(ctx, "SELECT spent::text FROM api_keys WHERE id=$1", unlimited["id"]).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if !decimal.RequireFromString(spent).Equal(decimal.RequireFromString("2000000000000.00006")) {
		t.Fatal("unlimited key did not record actual charges")
	}
	client.key = limited["key"].(string)
	client.call("POST", "/v1/chat/completions", body, 200)
	denied := object(t, client.call("POST", "/v1/chat/completions", body, 402))
	if denied["error"].(map[string]any)["code"] != "key_budget_exceeded" {
		t.Fatal("finite budget not enforced")
	}
	client.key = unlimited["key"].(string)
	testutil.FundWallet(t, a.Store, userID, "0")
	client.call("POST", "/v1/chat/completions", body, 200)
	client.call("DELETE", "/api/keys/"+unlimited["id"].(string), nil, 204)
	client.call("POST", "/v1/chat/completions", body, 401)
}

func TestPlaygroundDeliversTextAndFinishesAfterSettlementFailure(t *testing.T) {
	for _, chainID := range []int64{10143, 143} {
		for _, revoke := range []bool{false, true} {
			t.Run(fmt.Sprintf("chain=%d/revoke=%v", chainID, revoke), func(t *testing.T) {
				playgroundFinishesAfterSettlementFailure(t, chainID, revoke)
			})
		}
	}
}

func playgroundFinishesAfterSettlementFailure(t *testing.T, chainID int64, revoke bool) {
	a := testApp(t)
	a.Config.ChainID = chainID
	ctx := context.Background()
	client := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	userID := client.login(private, wallet)["id"].(string)
	testutil.FundWallet(t, a.Store, userID, "1")
	finish := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		if request["stream"] != true {
			t.Error("playground did not request streaming")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"First fragment"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":" then the rest"},"finish_reason":"stop"}]}`+"\n\ndata: "+`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	defer release()
	model := workspaceModel(t, a, userID, upstream.URL)
	chain := &emptyWallet{}
	a.Payments = payments.New(a.Store, config.Payments{Enabled: true, ChainID: chainID, Provider: "0x3333333333333333333333333333333333333333", Recipient: "0x7777777777777777777777777777777777777777"}, chain, a.Config.Secret)
	if _, err := a.Payments.Refresh(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	prior := uuid.NewString()
	if err := a.Store.BeginRequest(ctx, prior, userID, "", model, store.Tier{Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Finish(ctx, prior, "completed", store.Meter{Amount: decimal.RequireFromString("0.02"), Source: "upstream:fixture"}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	bill, err := a.Payments.BuildBill(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	key := object(t, client.call("POST", "/api/keys", map[string]any{"name": "postpaid"}, 201))
	server := httptest.NewServer(client.handler)
	defer func() { release(); server.Close() }()
	request, err := http.NewRequest("POST", server.URL+"/api/playground", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hello"}],"stream":true,"stream_options":{"include_usage":true}}`, model.ID)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", a.Config.Origin)
	request.Header.Set("X-CSRF-Token", client.csrf)
	for _, cookie := range client.cookies {
		request.AddCookie(cookie)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("stream failed: %s", raw)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "First fragment") {
			break
		}
	}
	var amount, state string
	if err := a.Store.DB.QueryRow(ctx, "SELECT amount::text,state FROM requests WHERE id=$1", response.Header.Get("X-Request-ID")).Scan(&amount, &state); err != nil {
		t.Fatal(err)
	}
	if state != "in_flight" || !decimal.RequireFromString(amount).IsZero() {
		t.Fatal("first fragment was buffered until settlement")
	}
	if chain.snapshotCalls.Load() == 0 {
		t.Fatal("request admission did not verify allowance")
	}
	// Fail settlement while the stream is open. A zero balance must block new
	// requests even if the user keeps a sufficient payment allowance.
	chain.revoked.Store(revoke)
	// Wait for the shared signing
	// lane if another integration test owns it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := a.Payments.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		blocked, err := a.Store.PaidCallsBlocked(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("settlement fixture could not acquire the signing lane")
		}
		time.Sleep(10 * time.Millisecond)
	}
	chain.unavailable.Store(true)
	// An expired snapshot must not hide a durable failure behind an RPC error.
	if _, err := a.Store.DB.Exec(ctx, `UPDATE payment_accounts SET checked_at=now()-interval '1 minute' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	checked := chain.snapshotCalls.Load()
	client.key = key["key"].(string)
	for _, path := range []string{"/api/playground", "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/" + model.ID + ":generateContent", "/v1beta/models/" + model.ID + ":streamGenerateContent?alt=sse"} {
		denied := object(t, client.call("POST", path, map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hello"}}, "input": "Hello", "contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "Hello"}}}}, "stream": true}, 402))
		paymentError := denied["error"].(map[string]any)
		if strings.HasPrefix(path, "/v1beta/") {
			if paymentError["code"] != float64(402) || paymentError["status"] != "PAYMENT_REQUIRED" {
				t.Fatal("Gemini did not report the payment failure", denied)
			}
		} else if paymentError["code"] != "payment_required" {
			t.Fatal("new calls were not blocked by the balance failure", path, denied)
		}
		if paymentError["message"] != store.ErrSettlementFailed.Error() {
			t.Fatal("settlement failure was presented as an allowance-only failure")
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatal("a rejected request reached the provider")
	}
	if chain.snapshotCalls.Load() != checked {
		t.Fatal("failure admission tried a payment RPC refresh")
	}
	if _, err := a.Store.DB.Exec(ctx, `DELETE FROM payment_accounts WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	billing := object(t, client.call("GET", "/api/billing", nil, 200))
	if billing["calls_blocked"] != true || billing["chain_error"] == nil {
		t.Fatal("billing lost durable admission status during an RPC outage")
	}
	detail := object(t, client.call("GET", "/api/billing/bills/"+bill, nil, 200))["bill"].(map[string]any)
	if detail["insufficient_balance_at"] == nil {
		t.Fatal("bill does not expose the balance failure")
	}
	release()
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "then the rest") || !strings.Contains(string(rest), "[DONE]") {
		t.Fatal("stream did not complete")
	}
	receipt := object(t, client.call("GET", "/api/requests/"+response.Header.Get("X-Request-ID"), nil, 200))
	if receipt["state"] != "completed" || !decimal.RequireFromString(receipt["amount"].(string)).Equal(decimal.RequireFromString("0.00003")) {
		t.Fatal("confirmed stream usage was not settled")
	}
}
