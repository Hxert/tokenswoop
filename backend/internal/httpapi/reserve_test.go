package httpapi

import (
	"context"
	"fmt"
	"github.com/shopspring/decimal"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/testutil"
)

type workspaceReserveChain struct {
	snapshot    reserve.Snapshot
	unavailable bool
}

func (r *workspaceReserveChain) Validate(context.Context) error { return nil }
func (r *workspaceReserveChain) Snapshot(context.Context, string) (reserve.Snapshot, error) {
	if r.unavailable {
		return r.snapshot, fmt.Errorf("RPC offline")
	}
	return r.snapshot, nil
}
func (r *workspaceReserveChain) SignApproval(context.Context, reserve.Approval) (payments.Signed, error) {
	return payments.Signed{}, fmt.Errorf("API must never sign")
}
func (r *workspaceReserveChain) Broadcast(context.Context, string) error {
	return fmt.Errorf("API must never broadcast")
}
func (r *workspaceReserveChain) Confirm(context.Context, reserve.Approval) (payments.Confirmation, error) {
	return payments.Confirmation{}, nil
}
func TestReserveTierAppliesToActualUsageAndEveryClient(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	private, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	chain := &workspaceReserveChain{snapshot: reserve.Snapshot{Principal: "50000000", Balance: "100000000", Allowance: "0", Block: "0x10", BlockHash: "0xfinal", OperatorVersion: "1", Withdrawal: reserve.Withdrawal{ID: "0", Amount: "0", OperatorVersion: "0"}}}
	a.Reserve = reserve.New(a.Store, config.Reserve{Enabled: true, Contract: "0x1111111111111111111111111111111111111111", Operator: wallet}, "0x2222222222222222222222222222222222222222", chain, a.Config.Secret)
	client := &testClient{t: t, handler: a.Handler()}
	u := client.login(private, wallet)["id"].(string)
	testutil.FundWallet(t, a.Store, u, "1")
	client.call("PUT", "/api/admin/tiers", map[string]any{"id": "builder", "min_principal": "50", "discount_bps": 300, "rpm": 120, "concurrency": 5}, 200)
	client.call("PUT", "/api/admin/tiers", map[string]any{"id": "duplicate", "min_principal": "50", "discount_bps": 300, "rpm": 120, "concurrency": 5}, 409)
	me := object(t, client.call("GET", "/api/me", nil, 200))
	if me["tier"].(map[string]any)["id"] != "builder" {
		t.Fatal("confirmed principal not used")
	}
	var called atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
	}))
	defer upstream.Close()
	m := workspaceModel(t, a, u, upstream.URL)
	key := object(t, client.call("POST", "/api/keys", map[string]any{"name": "Reserve fixture", "budget": nil}, 201))
	client.key = key["key"].(string)
	body := map[string]any{"model": m.ID, "messages": []map[string]string{{"role": "user", "content": "Hello"}}}
	client.call("POST", "/v1/chat/completions", body, 200)
	var amount string
	if e := a.Store.DB.QueryRow(ctx, `SELECT sum(amount)::text FROM requests WHERE user_id=$1`, u).Scan(&amount); e != nil || !decimal.RequireFromString(amount).Equal(decimal.RequireFromString("0.0000291")) {
		t.Fatal("upstream usage was not discounted by 3%", amount, e)
	}
	chain.snapshot.Withdrawal = reserve.Withdrawal{ID: "1", Amount: "50000000", Active: true, OperatorVersion: "0"}
	for _, endpoint := range []string{"/v1/chat/completions", "/api/playground"} {
		r := object(t, client.call("POST", endpoint, body, 409))
		if r["error"].(map[string]any)["code"] != "withdrawal_pending" {
			t.Fatal("wrong freeze reason")
		}
	}
	if called.Load() != 1 {
		t.Fatal("frozen requests reached upstream")
	}
	chain.unavailable = true
	client.call("POST", "/v1/chat/completions", body, 503)
	chain.unavailable = false
	chain.snapshot.Withdrawal.Active = false
	chain.snapshot.Principal = "0"
	client.call("POST", "/v1/chat/completions", body, 200)
	me = object(t, client.call("GET", "/api/me", nil, 200))
	if me["tier"].(map[string]any)["id"] != "free" {
		t.Fatal("withdrawal did not restore Free")
	}
	client.call("PATCH", "/api/admin/users/"+u, map[string]any{"status": "active", "tier": "builder", "tier_mode": "manual"}, 204)
	client.call("POST", "/api/reserve/refresh", map[string]any{}, 200)
	me = object(t, client.call("GET", "/api/me", nil, 200))
	if me["tier"].(map[string]any)["id"] != "builder" {
		t.Fatal("manual override lost")
	}
	client.call("PATCH", "/api/admin/users/"+u, map[string]any{"status": "active", "tier_mode": "auto"}, 204)
	me = object(t, client.call("GET", "/api/me", nil, 200))
	if me["tier"].(map[string]any)["id"] != "free" {
		t.Fatal("automatic assignment not restored")
	}
}
