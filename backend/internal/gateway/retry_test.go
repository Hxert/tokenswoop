package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestAutomaticRetriesUseOneRequestAndOnlyWinningUsage(t *testing.T) {
	for _, tc := range []struct {
		name, failure              string
		failures, calls            int
		stream, restart, completed bool
	}{
		{"service error", "503", 1, 2, false, false, true},
		{"request timeout", "408", 1, 2, false, false, true},
		{"third retry succeeds", "429", 3, 4, false, false, true},
		{"retry ceiling", "503", 8, 4, false, false, false},
		{"invalid JSON", "json", 1, 2, false, false, true},
		{"missing usage", "usage", 1, 2, false, false, true},
		{"connection closed", "disconnect", 1, 2, false, false, true},
		{"no stream content", "empty", 1, 2, true, false, true},
		{"wrong content type", "type", 1, 2, true, false, true},
		{"Playground replaces partial", "partial", 1, 2, true, true, true},
		{"Playground retries exhausted", "partial", 8, 4, true, true, false},
		{"public stream cannot retract", "partial", 1, 1, true, false, false},
		{"caller error", "400", 8, 1, false, false, false},
		{"authorization cannot self-heal", "401", 8, 1, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			user := testutil.Account(t, s, "1")
			model, err := s.SaveModel(ctx, user, testutil.Model())
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte(strings.Repeat("r", 32))
			cipher, _ := security.Encrypt(secret, "fixture")
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if int(calls.Add(1)) <= tc.failures {
					switch tc.failure {
					case "503":
						w.WriteHeader(503)
					case "408":
						w.WriteHeader(408)
					case "429":
						w.WriteHeader(429)
					case "400":
						w.WriteHeader(400)
					case "401":
						w.WriteHeader(401)
					case "json":
						fmt.Fprint(w, `{"broken":`)
					case "usage":
						fmt.Fprint(w, `{"choices":[{"message":{"content":"discarded"},"finish_reason":"stop"}]}`)
					case "type":
						fmt.Fprint(w, "not an event stream")
					case "empty":
						w.Header().Set("Content-Type", "text/event-stream")
					case "partial":
						w.Header().Set("Content-Type", "text/event-stream")
						// Even large confirmed counters from an incomplete attempt
						// must never leak into the final attempt's meter.
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Discarded partial\"}}],\"usage\":{\"prompt_tokens\":99999,\"completion_tokens\":999}}\n\n")
					case "disconnect":
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						conn.Close()
					}
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, streamFixture("openai"))
				} else {
					fmt.Fprint(w, chatResponse)
				}
			}))
			defer upstream.Close()
			ch := store.Channel{ID: uuid.NewString(), Name: "retry", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "fixture"}}
			if err = s.SaveChannel(ctx, user, ch); err != nil {
				t.Fatal(err)
			}
			tier := store.Tier{Concurrency: 2}
			p, err := BuildPlan([]byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"hi"}],"stream":%v}`, tc.stream)), model, tier, types.RelayFormatOpenAI)
			if err != nil {
				t.Fatal(err)
			}
			p.AllowStreamRestart = tc.restart
			id := uuid.NewString()
			if err = s.BeginRequest(ctx, id, user, "", model, tier); err != nil {
				t.Fatal(err)
			}
			var visible strings.Builder
			restarts := 0
			r := New(s, secret, true).Run(ctx, id, p, []store.Channel{ch}, func(event string, b []byte) error {
				if event == PlaygroundRetryEvent {
					restarts++
					visible.Reset()
				} else {
					visible.Write(b)
				}
				return nil
			})
			if int(calls.Load()) != tc.calls || (r.State == "completed") != tc.completed {
				t.Fatalf("calls=%d state=%s code=%s", calls.Load(), r.State, r.Code)
			}
			if tc.restart && tc.failure == "partial" && restarts != tc.calls-1 {
				t.Fatalf("restarts=%d", restarts)
			}
			if tc.completed && tc.stream && (strings.Contains(visible.String(), "Discarded") || !strings.Contains(visible.String(), "Hello")) {
				t.Fatal("mixed partial answers", visible.String())
			}
			for i := 0; i < 2; i++ {
				if err = s.Finish(ctx, id, r.State, r.Meter, r.Channel, r.Status, r.Code, 1); err != nil {
					t.Fatal(err)
				}
			}
			var rows, attempts, completions int
			var amount, unpaid string
			if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM requests WHERE user_id=$2),(SELECT count(*) FROM request_attempts WHERE request_id=$1),(SELECT count(*) FROM outbox_events WHERE request_id=$1 AND kind='request.completed'),amount::text,(SELECT unpaid_balance::text FROM users WHERE id=$2) FROM requests WHERE id=$1`, id, user).Scan(&rows, &attempts, &completions, &amount, &unpaid); err != nil {
				t.Fatal(err)
			}
			want := decimal.Zero
			if tc.completed {
				want = decimal.RequireFromString("0.00003")
				if r.Meter.Input != 20 || r.Meter.Output != 5 || completions != 1 {
					t.Fatal("failed-attempt usage or duplicate completion", r.Meter, completions)
				}
			}
			if rows != 1 || attempts != tc.calls || !decimal.RequireFromString(amount).Equal(want) || !decimal.RequireFromString(unpaid).Equal(want) {
				t.Fatalf("rows=%d attempts=%d amount=%s unpaid=%s", rows, attempts, amount, unpaid)
			}
		})
	}
}

func TestCanceledOrDisconnectedCallerDoesNotTriggerRetries(t *testing.T) {
	for _, mode := range []string{"canceled", "disconnected"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.Database(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			user := testutil.Account(t, s, "1")
			model, err := s.SaveModel(ctx, user, testutil.Model())
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte(strings.Repeat("r", 32))
			cipher, _ := security.Encrypt(secret, "fixture")
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "canceled" {
					cancel()
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, streamFixture("openai"))
			}))
			defer upstream.Close()
			ch := store.Channel{ID: uuid.NewString(), Name: "cancellation", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "fixture"}}
			if err = s.SaveChannel(ctx, user, ch); err != nil {
				t.Fatal(err)
			}
			tier := store.Tier{Concurrency: 2}
			p, err := BuildPlan([]byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`), model, tier, types.RelayFormatOpenAI)
			if err != nil {
				t.Fatal(err)
			}
			p.AllowStreamRestart = true
			id := uuid.NewString()
			if err = s.BeginRequest(ctx, id, user, "", model, tier); err != nil {
				t.Fatal(err)
			}
			r := New(s, secret, true).Run(ctx, id, p, []store.Channel{ch}, func(string, []byte) error { return fmt.Errorf("client gone") })
			if calls.Load() != 1 || r.State == "completed" || !r.Meter.Amount.IsZero() {
				t.Fatal("caller cancellation retried or charged", calls.Load(), r)
			}
		})
	}
}
