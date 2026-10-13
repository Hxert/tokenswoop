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
	"tokenswoop/backend/internal/store"
)

const retryChatJSON = `{"choices":[{"index":0,"message":{"role":"assistant","content":"Recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`
const retryChatSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Recovered\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":5,\"total_tokens\":25}}\n\ndata: [DONE]\n\n"

func TestRetriesAcrossClientProtocolsChargeOnce(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "claude", "gemini"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				a := testApp(t)
				c, user := operatorClient(t, a)
				ctx := context.Background()
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var due string
					if err := a.Store.DB.QueryRow(ctx, "SELECT unpaid_balance::text FROM users WHERE id=$1", user).Scan(&due); err != nil {
						t.Error(err)
					}
					if !decimal.RequireFromString(due).IsZero() {
						t.Error("charged an intermediate attempt", due)
					}
					if calls.Add(1) < 4 {
						w.WriteHeader(503)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, retryChatSSE)
					} else {
						fmt.Fprint(w, retryChatJSON)
					}
				}))
				defer upstream.Close()
				m := workspaceModel(t, a, user, upstream.URL)
				key := object(t, c.call("POST", "/api/keys", map[string]any{"name": "retry budget", "budget": "0.00001"}, 201))
				c.key = key["key"].(string)
				body := map[string]any{"model": m.ID, "stream": stream}
				path := "/v1/chat/completions"
				switch protocol {
				case "chat":
					body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
					body["max_completion_tokens"] = 64
				case "responses":
					path = "/v1/responses"
					body["input"] = "hi"
					body["max_output_tokens"] = 64
				case "claude":
					path = "/v1/messages"
					body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
					body["max_tokens"] = 64
				case "gemini":
					path = "/v1beta/models/" + m.ID + ":generateContent"
					if stream {
						path = "/v1beta/models/" + m.ID + ":streamGenerateContent?alt=sse"
					}
					body["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "hi"}}}}
					body["generationConfig"] = map[string]int{"maxOutputTokens": 64}
				}
				response := c.call("POST", path, body, 200)
				if calls.Load() != 4 || !strings.Contains(response.Body.String(), "Recovered") || strings.Contains(response.Body.String(), "tokenswoop.retry") || strings.Contains(response.Body.String(), "upstream_service_error") {
					t.Fatalf("retry leaked or failed: calls=%d body=%s", calls.Load(), response.Body.String())
				}
				id := response.Header().Get("X-Request-ID")
				assertRetryCharge(t, a, user, id, 4, true)
				var spent string
				if err := a.Store.DB.QueryRow(ctx, "SELECT spent::text FROM api_keys WHERE id=$1", key["id"]).Scan(&spent); err != nil || !decimal.RequireFromString(spent).Equal(decimal.RequireFromString("0.00003")) {
					t.Fatal("key charged multiple times", spent, err)
				}
			})
		}
	}
}

func TestPlaygroundRetriesInsideOneStreamAndOneReceipt(t *testing.T) {
	for _, path := range []string{"/api/playground", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			a := testApp(t)
			c, user := operatorClient(t, a)
			ctx := context.Background()
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				messages, _ := input["messages"].([]any)
				if len(messages) != 1 {
					t.Error("retry sent failed output back upstream", input)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Discarded partial\",\"reasoning_content\":\"Discarded thinking\"}}],\"usage\":{\"prompt_tokens\":100000,\"completion_tokens\":2000}}\n\n")
					return
				}
				fmt.Fprint(w, retryChatSSE)
			}))
			defer upstream.Close()
			m := workspaceModel(t, a, user, upstream.URL)
			response := c.call("POST", path, map[string]any{"model": m.ID, "messages": []map[string]string{{"role": "user", "content": "hi"}}, "stream": true}, 200)
			data := response.Body.String()
			completed := path == "/api/playground"
			want := 1
			if completed {
				want = 2
			}
			if int(calls.Load()) != want {
				t.Fatal("unexpected retry count", calls.Load())
			}
			if completed {
				reset := strings.Index(data, "event: tokenswoop.retry")
				if reset < 0 || strings.Contains(data[reset:], "Discarded") || !strings.Contains(data[reset:], "Recovered") || strings.Count(data, "[DONE]") != 1 || strings.Contains(data, `"error"`) {
					t.Fatal("stream restart did not isolate responses", data)
				}
			} else if strings.Contains(data, "tokenswoop.retry") || strings.Contains(data, "Recovered") || strings.Contains(data, "[DONE]") || !strings.Contains(data, "incomplete_stream") {
				t.Fatal("public stream replayed or hid its failure", data)
			}
			id := response.Header().Get("X-Request-ID")
			assertRetryCharge(t, a, user, id, want, completed)
			if completed {
				if err := a.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.00003")}, "", 200, "", 1); err != nil {
					t.Fatal(err)
				}
				assertRetryCharge(t, a, user, id, want, true)
			}
		})
	}
}

func assertRetryCharge(t *testing.T, a *App, user, id string, attempts int, completed bool) {
	t.Helper()
	var rows, hits, events int
	var amount, unpaid, state string
	var input, output int64
	err := a.Store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM requests WHERE user_id=$2),(SELECT count(*) FROM request_attempts WHERE request_id=$1),(SELECT count(*) FROM outbox_events WHERE request_id=$1 AND kind='request.completed'),state,amount::text,input_tokens,output_tokens,(SELECT unpaid_balance::text FROM users WHERE id=$2) FROM requests WHERE id=$1`, id, user).Scan(&rows, &hits, &events, &state, &amount, &input, &output, &unpaid)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.Zero
	if completed {
		want = decimal.RequireFromString("0.00003")
		if state != "completed" || events != 1 || input != 20 || output != 5 {
			t.Fatal("attempts were added to final usage", state, events, input, output)
		}
	} else if state != "usage_unknown" || events != 0 || input != 0 || output != 0 {
		t.Fatal("failed stream charged", state, events, input, output)
	}
	if rows != 1 || hits != attempts || !decimal.RequireFromString(amount).Equal(want) || !decimal.RequireFromString(unpaid).Equal(want) {
		t.Fatalf("rows=%d attempts=%d amount=%s unpaid=%s", rows, hits, amount, unpaid)
	}
}
