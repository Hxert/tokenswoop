package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

// This deliberately uses a local metered upstream and a deterministic chain;
// it measures HTTP/SSE, PostgreSQL and billing concurrency without paid models.
type loadChain struct {
	mu    sync.Mutex
	nonce int
	sent  map[string]bool
}

func (f *loadChain) Validate(context.Context) error { return nil }
func (f *loadChain) Snapshot(context.Context, string) (payments.Snapshot, error) {
	return payments.Snapshot{Balance: "1000000000", Allowance: "1000000000", Block: "0x1"}, nil
}
func (f *loadChain) IsCharged(context.Context, payments.Bill) (bool, error) { return false, nil }
func (f *loadChain) Sign(_ context.Context, b payments.Bill) (payments.Signed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := []byte(fmt.Sprintf("%s:%d", b.ID, f.nonce))
	v := payments.Signed{Raw: "0x" + hex.EncodeToString(raw), Hash: payments.Keccak(raw), Nonce: fmt.Sprint(f.nonce)}
	f.nonce++
	return v, nil
}
func (f *loadChain) Broadcast(_ context.Context, raw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := hex.DecodeString(raw[2:])
	f.sent[payments.Keccak(b)] = true
	return nil
}
func (f *loadChain) Confirm(_ context.Context, _ payments.Bill, hash string) (payments.Confirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return payments.Confirmation{Finalized: f.sent[hash], Success: true, Block: "0x2", BlockHash: "0xfinal"}, nil
}

func TestHTTPStreamingSettlementLoad(t *testing.T) {
	if os.Getenv("FUNDS_LOAD_TEST") != "true" {
		t.Skip("set FUNDS_LOAD_TEST=true in an isolated test database")
	}
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := config.Payments{Enabled: true, ChainID: 10143, Contract: "0x1111111111111111111111111111111111111111", Token: "0x2222222222222222222222222222222222222222", Provider: "0x3333333333333333333333333333333333333333"}
	chain := &loadChain{sent: map[string]bool{}}
	ps := payments.New(a.Store, p, chain, a.Config.Secret)
	a.Payments = ps
	a.Config.Payments = p
	cl := &testClient{t: t, handler: a.Handler()}
	key, wallet := testutil.Wallet(t)
	u := cl.login(key, wallet)["id"].(string)
	if _, e := a.Store.DB.Exec(ctx, `UPDATE tiers SET rpm=60000,concurrency=100 WHERE id='free'`); e != nil {
		t.Fatal(e)
	}
	model, e := a.Store.SaveModel(ctx, u, testutil.Model())
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
			for i := 0; i < 5; i++ {
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello \"}}]}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(4 * time.Millisecond)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":5,\"total_tokens\":25}}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"load","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
		}
	}))
	defer upstream.Close()
	encrypted, e := security.Encrypt(a.Config.Secret, "local-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Store.SaveChannel(ctx, u, store.Channel{ID: uuid.NewString(), Name: "Load fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: encrypted, Models: map[string]string{model.ID: "fixture"}, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	projectKey := object(t, cl.call("POST", "/api/keys", map[string]any{"name": "load", "budget": nil}, 201))["key"].(string)
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	stop := make(chan struct{})
	done := make(chan struct{})
	workerErr := make(chan error, 1)
	go func() {
		defer close(done)
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if e := ps.Tick(ctx); e != nil {
					select {
					case workerErr <- e:
					default:
					}
					return
				}
			}
		}
	}()
	const total, concurrent = 1000, 40
	latencies := make([]float64, total)
	failures := make(chan string, total)
	jobs := make(chan int)
	var wg sync.WaitGroup
	started := time.Now()
	for n := 0; n < concurrent; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				route := "/v1/chat/completions"
				body := map[string]any{"model": model.ID, "stream": i%2 == 0, "messages": []map[string]string{{"role": "user", "content": "hello"}}}
				if i%4 >= 2 {
					route = "/v1/responses"
					delete(body, "messages")
					body["input"] = "hello"
				}
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest("POST", server.URL+route, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+projectKey)
				req.Header.Set("Content-Type", "application/json")
				start := time.Now()
				res, e := client.Do(req)
				if e != nil {
					failures <- "HTTP transport"
					continue
				}
				payload, e := io.ReadAll(res.Body)
				res.Body.Close()
				latencies[i] = float64(time.Since(start).Microseconds()) / 1000
				if e != nil || res.StatusCode != 200 {
					failures <- fmt.Sprintf("HTTP %d: %.160s", res.StatusCode, payload)
				} else if i%2 == 0 && !bytes.Contains(payload, []byte("data:")) {
					failures <- "missing SSE"
				}
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(started)
	close(failures)
	close(stop)
	<-done
	for failure := range failures {
		t.Error(failure)
	}
	select {
	case e := <-workerErr:
		t.Error(e)
	default:
	}
	if t.Failed() {
		return
	}
	// Drain all final usage, including the timer-triggered last batch.
	for i := 0; i < 50; i++ {
		if _, e = ps.BuildBill(ctx, u); e != nil {
			t.Fatal(e)
		}
		if e = ps.SettleNext(ctx); e != nil {
			t.Fatal(e)
		}
		var unpaid string
		if e = a.Store.DB.QueryRow(ctx, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u).Scan(&unpaid); e != nil {
			t.Fatal(e)
		}
		if decimal.RequireFromString(unpaid).IsZero() {
			break
		}
	}
	var completed, bills, audits int
	var amount, paid, unpaid string
	if e = a.Store.DB.QueryRow(ctx, `SELECT count(*),sum(amount)::text,sum(wallet_paid_amount)::text,sum(unpaid_amount)::text FROM requests WHERE user_id=$1 AND state='completed'`, u).Scan(&completed, &amount, &paid, &unpaid); e != nil {
		t.Fatal(e)
	}
	if e = a.Store.DB.QueryRow(ctx, `SELECT count(*) FROM bills WHERE user_id=$1 AND state='paid'`, u).Scan(&bills); e != nil {
		t.Fatal(e)
	}
	if e = a.Store.DB.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='bill.paid'`, u).Scan(&audits); e != nil {
		t.Fatal(e)
	}
	expected := decimal.RequireFromString("0.00003").Mul(decimal.NewFromInt(total))
	if completed != total || !decimal.RequireFromString(amount).Equal(expected) || !decimal.RequireFromString(paid).Equal(expected) || !decimal.RequireFromString(unpaid).IsZero() || bills != audits {
		t.Fatal("load ledger mismatch", completed, amount, paid, unpaid, bills, audits)
	}
	sort.Float64s(latencies)
	report := map[string]any{"requests": total, "concurrency": concurrent, "streaming_requests": total / 2, "completed": completed, "errors": 0, "elapsed_seconds": elapsed.Seconds(), "requests_per_second": float64(total) / elapsed.Seconds(), "p50_ms": latencies[total/2], "p95_ms": latencies[total*95/100], "p99_ms": latencies[total*99/100], "usage_amount": amount, "paid_amount": paid, "unpaid_amount": unpaid, "paid_bills": bills, "paid_audit_events": audits, "upstream": "local metered fixture", "chain": "deterministic test double"}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(encoded))
	if file := os.Getenv("FUNDS_LOAD_REPORT"); file != "" {
		if e = os.WriteFile(file, append(encoded, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
