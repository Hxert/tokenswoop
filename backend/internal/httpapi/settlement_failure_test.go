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
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

type gatedWalletSnapshot struct {
	emptyWallet
	gate             atomic.Bool
	started, release chan struct{}
}

func (w *gatedWalletSnapshot) Snapshot(ctx context.Context, user string) (payments.Snapshot, error) {
	v, err := w.emptyWallet.Snapshot(ctx, user)
	if w.gate.CompareAndSwap(true, false) {
		close(w.started)
		select {
		case <-w.release:
		case <-ctx.Done():
			return payments.Snapshot{}, ctx.Err()
		}
	}
	return v, err
}

func TestSettlementFailureDuringWalletRefreshRejectsNewRequest(t *testing.T) {
	a := testApp(t)
	a.Config.ChainID = 143
	ctx := context.Background()
	client := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	user := client.login(private, wallet)["id"].(string)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(502)
	}))
	defer upstream.Close()
	model := workspaceModel(t, a, user, upstream.URL)
	chain := &gatedWalletSnapshot{started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(chain.release) }) }
	defer release()
	p := config.Payments{Enabled: true, ChainID: 143, Token: config.MonadUSDC, Contract: "0x1111111111111111111111111111111111111111", Provider: "0x3333333333333333333333333333333333333333", Recipient: "0x7777777777777777777777777777777777777777"}
	a.Payments = payments.New(a.Store, p, chain, a.Config.Secret)
	if _, err := a.Payments.Refresh(ctx, user, true); err != nil {
		t.Fatal(err)
	}
	prior := uuid.NewString()
	if err := a.Store.BeginRequest(ctx, prior, user, "", model, store.Tier{Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Finish(ctx, prior, "completed", store.Meter{Amount: decimal.RequireFromString("0.02"), Source: "upstream:fixture"}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Payments.BuildBill(ctx, user); err != nil {
		t.Fatal(err)
	}
	key := object(t, client.call("POST", "/api/keys", map[string]any{"name": "race fixture"}, 201))
	if _, err := a.Store.DB.Exec(ctx, `DELETE FROM payment_accounts WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	chain.gate.Store(true)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hello"}]}`, model.ID)))
	r.Header.Set("Authorization", "Bearer "+key["key"].(string))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); client.handler.ServeHTTP(w, r) }()
	select {
	case <-chain.started:
	case <-done:
		t.Fatalf("request did not reach its snapshot refresh: %s", w.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("wallet refresh did not start")
	}
	// The fast failure check already passed, but the request has not been
	// admitted. Commit a real worker funding failure before releasing its RPC.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := a.Payments.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		failed, err := a.Store.HasUnpaidSettlementFailure(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker could not commit the funding failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	if w.Code != 402 || object(t, w)["error"].(map[string]any)["message"] != store.ErrSettlementFailed.Error() {
		t.Fatalf("a failure committed during refresh did not block admission: %d %s", w.Code, w.Body.String())
	}
	var requests int
	if err := a.Store.DB.QueryRow(ctx, `SELECT count(*) FROM requests WHERE user_id=$1`, user).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if upstreamCalls.Load() != 0 || requests != 1 {
		t.Fatal("denied request created a ledger entry or reached upstream")
	}
}
