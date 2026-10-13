package httpapi

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

type walletEvidence struct{ payments.Chain }

// No signer is provided: an insufficient-balance settlement must fail before
// signing, and request admission must never invoke any payment RPC method.
type emptyWallet struct {
	payments.Chain
	snapshotCalls atomic.Int32
	unavailable   atomic.Bool
	revoked       atomic.Bool
}

func (*emptyWallet) Validate(context.Context) error                         { return nil }
func (*emptyWallet) IsCharged(context.Context, payments.Bill) (bool, error) { return false, nil }
func (w *emptyWallet) Snapshot(context.Context, string) (payments.Snapshot, error) {
	w.snapshotCalls.Add(1)
	if w.unavailable.Load() {
		return payments.Snapshot{}, fmt.Errorf("fixture payment RPC unavailable")
	}
	allowance := "1000000"
	if w.revoked.Load() {
		allowance = "0"
	}
	return payments.Snapshot{Balance: "0", Allowance: allowance, Block: "0x10"}, nil
}

func (walletEvidence) Validate(context.Context) error { return nil }
func (walletEvidence) Snapshot(context.Context, string) (payments.Snapshot, error) {
	return payments.Snapshot{Balance: "1000000", Allowance: "1000000", Block: "0x10"}, nil
}
func TestWalletAdmissionReceiptsAndBillOwnership(t *testing.T) {
	for _, chainID := range []int64{10143, 143} {
		t.Run(fmt.Sprint(chainID), func(t *testing.T) { walletAdmissionReceiptsAndBillOwnership(t, chainID) })
	}
}

func walletAdmissionReceiptsAndBillOwnership(t *testing.T, chainID int64) {
	a := testApp(t)
	a.Config.ChainID = chainID
	ctx := context.Background()
	p := config.Payments{Enabled: true, ChainID: chainID, Contract: "0x1111111111111111111111111111111111111111", Token: "0x2222222222222222222222222222222222222222", Provider: "0x3333333333333333333333333333333333333333"}
	if chainID == 143 {
		p.Token = config.MonadUSDC
	}
	a.Config.Payments = p
	a.Payments = payments.New(a.Store, p, walletEvidence{}, a.Config.Secret)
	if e := a.Payments.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	// Other packages exercise the worker's database-wide advisory lock in
	// parallel schemas. This admission test supplies the worker evidence itself.
	if _, e := a.Store.DB.Exec(ctx, "INSERT INTO worker_heartbeats(id) VALUES('settlement') ON CONFLICT(id) DO UPDATE SET last_seen=now()"); e != nil {
		t.Fatal(e)
	}
	client := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	u := client.login(private, wallet)["id"].(string)
	client.call("POST", "/api/billing/refresh", nil, 200)
	public := object(t, client.call("GET", "/api/config", nil, 200))
	billing := object(t, client.call("GET", "/api/billing", nil, 200))
	if public["chain_id"] != float64(chainID) || billing["symbol"] != config.NetworkFor(chainID).Symbol || billing["token"] != p.Token {
		t.Fatal("workspace configuration differs from payment chain")
	}
	model, e := a.Store.SaveModel(ctx, u, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer upstream.Close()
	defer once.Do(func() { close(release) })
	encrypted, e := security.Encrypt(a.Config.Secret, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Store.SaveChannel(ctx, u, store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: encrypted, Models: map[string]string{model.ID: "fixture"}, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	key := object(t, client.call("POST", "/api/keys", map[string]string{"name": "wallet only", "budget": "1"}, 201))
	client.key = key["key"].(string)
	var reply *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() {
		defer close(done)
		reply = client.call("POST", "/v1/chat/completions", map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 32}, 200)
	}()
	select {
	case <-started:
	case <-done:
		t.Fatal("request rejected before reaching upstream")
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach upstream")
	}
	var amount, due string
	var bills int
	if e = a.Store.DB.QueryRow(ctx, `SELECT amount::text FROM requests WHERE user_id=$1`, u).Scan(&amount); e != nil {
		t.Fatal(e)
	}
	if e = a.Store.DB.QueryRow(ctx, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u).Scan(&due); e != nil {
		t.Fatal(e)
	}
	if e = a.Store.DB.QueryRow(ctx, `SELECT count(*) FROM bills`).Scan(&bills); e != nil {
		t.Fatal(e)
	}
	if amount != "0.000000000000000000" || due != "0.000000000000000000" || bills != 0 {
		t.Fatal("request charged before upstream completion")
	}
	once.Do(func() { close(release) })
	<-done
	id, e := a.Payments.BuildBill(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	detail := object(t, client.call("GET", "/api/billing/bills/"+id, nil, 200))
	if len(detail["items"].([]any)) != 1 {
		t.Fatal("bill missing request allocation")
	}
	var billChain int64
	var billToken string
	if e := a.Store.DB.QueryRow(ctx, `SELECT chain_id,token FROM bills WHERE id=$1`, id).Scan(&billChain, &billToken); e != nil || billChain != chainID || billToken != p.Token {
		t.Fatal("bill recorded in wrong network", e)
	}
	receipt := object(t, client.call("GET", "/api/requests/"+reply.Header().Get("X-Request-ID"), nil, 200))
	if len(receipt["bills"].([]any)) != 1 {
		t.Fatal("receipt missing bill")
	}
	client.call("POST", "/api/billing/bills/"+id+"/retry", nil, 202)
	csrf := client.csrf
	client.csrf = "wrong"
	client.call("POST", "/api/billing/bills/"+id+"/retry", nil, 403)
	client.csrf = csrf
	other := &testClient{t: t, handler: a.Handler()}
	private, wallet = testutil.Wallet(t)
	other.login(private, wallet)
	other.call("GET", "/api/billing/bills/"+id, nil, 404)
	other.call("POST", "/api/billing/bills/"+id+"/retry", nil, 404)
	unauthenticated := &testClient{t: t, handler: a.Handler()}
	unauthenticated.call("GET", "/api/billing", nil, 401)
}
