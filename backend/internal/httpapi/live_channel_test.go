package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

// Explicit opt-in: these requests consume real upstream quota. Only read the
// configured catalog; authentication, requests and accounting use a disposable
// test schema. No payment worker or on-chain transaction runs in this test.
func TestLiveConfiguredChannel(t *testing.T) {
	channelID := os.Getenv("LIVE_CHANNEL_ID")
	if channelID == "" {
		t.Skip("LIVE_CHANNEL_ID and LIVE_MODEL_IDS are required for paid upstream checks")
	}
	models := strings.Split(os.Getenv("LIVE_MODEL_IDS"), ",")
	checks := strings.Split(os.Getenv("LIVE_CHECKS"), ",")
	if checks[0] == "" {
		checks = []string{"chat", "chat-stream"}
	}
	if models[0] == "" || len(models)*len(checks) > 12 {
		t.Fatal("specify model IDs and at most 12 generation checks per run")
	}
	for _, check := range checks {
		if !slices.Contains([]string{"chat", "chat-stream", "responses", "responses-stream", "claude", "claude-stream", "gemini", "gemini-stream", "tools", "schema"}, check) {
			t.Fatal("unknown live check")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("local application configuration unavailable")
	}
	dbURL, err := url.Parse(cfg.DatabaseURL)
	if err != nil || cfg.Environment == "production" || (dbURL.Hostname() != "127.0.0.1" && dbURL.Hostname() != "localhost") {
		t.Fatal("live checks require a local non-production catalog")
	}
	ctx := context.Background()
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatal("invalid catalog connection")
	}
	pc.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal("catalog connection unavailable")
	}
	defer db.Close()
	source := &store.Store{DB: db}
	channel, err := source.Channel(ctx, channelID)
	if err != nil || !channel.Enabled {
		t.Fatal("configured channel unavailable")
	}
	a := testApp(t)
	a.Config.Secret = cfg.Secret
	a.Config.AllowPrivateUpstreams = cfg.AllowPrivateUpstreams
	a.Gateway = gateway.New(a.Store, cfg.Secret, cfg.AllowPrivateUpstreams)
	c, uid := operatorClient(t, a)
	for _, id := range models {
		model, e := source.Model(ctx, id)
		if e != nil || channel.Models[id] == "" {
			t.Fatal("selected model unavailable on configured channel")
		}
		if _, e = a.Store.SaveModel(ctx, uid, model); e != nil {
			t.Fatal(e)
		}
	}
	// Only the selected models exist in the isolated catalog.
	selected := map[string]string{}
	for _, id := range models {
		selected[id] = channel.Models[id]
	}
	channel.Models = selected
	channel.Revision = 0
	if err = a.Store.SaveChannel(ctx, uid, channel); err != nil {
		t.Fatal(err)
	}
	t.Run("discovery", func(t *testing.T) {
		secret, e := security.Decrypt(cfg.Secret, channel.SecretCipher)
		if e != nil {
			t.Fatal("channel credentials unavailable")
		}
		discovery, e := a.Gateway.DiscoverModels(ctx, channel.Protocol, channel.BaseURL, secret, channel.Options)
		if e != nil {
			t.Fatal(e)
		}
		for _, id := range models {
			if !slices.ContainsFunc(discovery.Models, func(m gateway.DiscoveredModel) bool { return m.ID == channel.Models[id] }) {
				t.Errorf("configured model missing from upstream discovery: %s", id)
			}
		}
		t.Logf("discovered %d models; selected mappings checked", len(discovery.Models))
	})
	for _, id := range models {
		for _, check := range checks {
			t.Run(id+"/"+check, func(t *testing.T) {
				before, e := a.Store.User(ctx, uid)
				if e != nil || decimal.RequireFromString(before.Unpaid).GreaterThan(decimal.NewFromInt(1)) {
					t.Fatal("local test accounting ceiling reached")
				}
				stream := strings.HasSuffix(check, "-stream")
				path := "/v1/chat/completions"
				body := map[string]any{"model": id, "messages": []map[string]string{{"role": "user", "content": "Reply with exactly OK."}}, "max_completion_tokens": 256, "stream": stream}
				switch strings.TrimSuffix(check, "-stream") {
				case "responses":
					path = "/v1/responses"
					body = map[string]any{"model": id, "input": "Reply with exactly OK.", "max_output_tokens": 256, "stream": stream}
				case "claude":
					path = "/v1/messages"
					delete(body, "max_completion_tokens")
					body["max_tokens"] = 256
				case "gemini":
					action := ":generateContent"
					if stream {
						action = ":streamGenerateContent?alt=sse"
					}
					path = "/v1beta/models/" + url.PathEscape(id) + action
					body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "Reply with exactly OK."}}}}, "generationConfig": map[string]int{"maxOutputTokens": 256}}
				case "tools":
					body["messages"] = []map[string]string{{"role": "user", "content": "Call the report_status function with status OK."}}
					body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "report_status", "parameters": liveStatusSchema(), "strict": true}}}
					body["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": "report_status"}}
				case "schema":
					body["messages"] = []map[string]string{{"role": "user", "content": "Return a JSON object with status OK."}}
					body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "status", "schema": liveStatusSchema(), "strict": true}}
				}
				encoded, _ := json.Marshal(body)
				requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)).WithContext(requestCtx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+c.key)
				w := httptest.NewRecorder()
				c.handler.ServeHTTP(w, req)
				requestID := w.Header().Get("X-Request-ID")
				var state, code, amount, usageSource string
				var input, output, latency int64
				var firstToken *int64
				e = a.Store.DB.QueryRow(ctx, `SELECT state,error_code,amount::text,input_tokens,output_tokens,usage_source,latency_ms,first_token_ms FROM requests WHERE id=$1`, requestID).Scan(&state, &code, &amount, &input, &output, &usageSource, &latency, &firstToken)
				if e != nil {
					t.Fatalf("no metered request: HTTP %d", w.Code)
				}
				t.Logf("HTTP=%d state=%s code=%s input=%d output=%d source=%s charge=%s latency_ms=%d", w.Code, state, code, input, output, usageSource, amount, latency)
				if w.Code != 200 || state != "completed" || input <= 0 || output <= 0 || usageSource == "" {
					t.Fatal("generation or confirmed usage failed")
				}
				if stream && (!strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") || firstToken == nil) {
					t.Error("stream or first-token timing missing")
				}
				if !strings.Contains(w.Body.String(), "OK") {
					t.Error("expected visible answer missing")
				}
				if check == "tools" {
					var response struct {
						Choices []struct {
							Message struct {
								ToolCalls []struct {
									Function struct{ Name, Arguments string }
								} `json:"tool_calls"`
							}
						}
					}
					if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
						t.Error("function call missing")
					} else {
						call := response.Choices[0].Message.ToolCalls[0].Function
						var args map[string]string
						if call.Name != "report_status" || json.Unmarshal([]byte(call.Arguments), &args) != nil || args["status"] != "OK" || len(args) != 1 {
							t.Error("function call arguments violate requested schema")
						}
					}
				}
				if check == "schema" {
					var response struct {
						Choices []struct{ Message struct{ Content string } }
					}
					var value map[string]string
					if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Choices) != 1 || json.Unmarshal([]byte(response.Choices[0].Message.Content), &value) != nil || value["status"] != "OK" || len(value) != 1 {
						t.Error("structured result violates requested schema")
					}
				}
				after, e := a.Store.User(ctx, uid)
				if e != nil || !decimal.RequireFromString(after.Unpaid).Sub(decimal.RequireFromString(before.Unpaid)).Equal(decimal.RequireFromString(amount)) {
					t.Error("postpaid debit does not equal confirmed request amount")
				}
				var entries, attempts int
				if e = a.Store.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_events WHERE request_id=$1 AND kind='request.completed'),(SELECT count(*) FROM request_attempts WHERE request_id=$1)`, requestID).Scan(&entries, &attempts); e != nil || entries != 1 || attempts != 1 {
					t.Error("expected one upstream attempt and one postpaid entry")
				}
			})
		}
	}
}

func liveStatusSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "enum": []string{"OK"}}}, "required": []string{"status"}, "additionalProperties": false}
}
