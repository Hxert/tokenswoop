package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestCanonicalPricesAndCacheSemantics(t *testing.T) {
	p, e := BuildPlan([]byte(`{"model":"test-model","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}`), testutil.Model(), store.Tier{DiscountBPS: 1000}, types.RelayFormatOpenAI)
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		name  string
		usage *dto.Usage
		cost  string
		input int64
	}{
		{"chat cache", relayconvert.UsageFromChatUsage(&dto.Usage{PromptTokens: 100, CompletionTokens: 10, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 40}}), "0.0000756", 100},
		{"claude zero uncached", relayconvert.UsageFromClaudeAPIUsage(&dto.ClaudeUsage{InputTokens: 0, OutputTokens: 10, CacheReadInputTokens: 40}), "0.0000216", 40},
		{"claude cache writes", relayconvert.UsageFromClaudeAPIUsage(&dto.ClaudeUsage{InputTokens: 60, OutputTokens: 10, CacheReadInputTokens: 20, CacheCreationInputTokens: 20}), "0.0000963", 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, e := MeterUsage(test.usage, p)
			if e != nil {
				t.Fatal(e)
			}
			if m.Amount.String() != test.cost || m.Input != test.input {
				t.Fatalf("meter=%+v expected %s input %d", m, test.cost, test.input)
			}
		})
	}
	p.Model.InputPrice = "0.000000000001"
	p.Tier.DiscountBPS = 0
	m, e := MeterUsage(&dto.Usage{PromptTokens: 1}, p)
	if e != nil || !m.Amount.Equal(decimal.RequireFromString("0.000000000000000001")) {
		t.Fatalf("decimal precision lost: %s %v", m.Amount, e)
	}
}
func TestRequestSubsetAndURLValidation(t *testing.T) {
	base := testutil.Model()
	for _, raw := range []string{`{"messages":[],"model":"x"}`, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`, `{"messages":[{"role":"user","content":"hi"}],"background":true}`, `{"input":"hi","previous_response_id":"previous"}`, `{"messages":[{"role":"user","content":"hi"}],"stop":[12]}`} {
		if _, e := BuildPlan([]byte(raw), base, store.Tier{}, types.RelayFormatOpenAI); e == nil {
			t.Fatalf("unsupported request accepted: %s", raw)
		}
	}
	for _, raw := range []string{"http://example.com/v1", "https://127.0.0.1/v1", "https://169.254.169.254/", "https://user:password@example.com", "https://example.com?key=secret"} {
		if ValidURL(raw, false) {
			t.Fatalf("unsafe URL %s", raw)
		}
	}
	if !ValidURL("https://example.com/v1", false) || PublicIP(net.ParseIP("100.100.100.200")) {
		t.Fatal("URL validation")
	}
}

const chatResponse = `{"id":"chatcmpl-fixture","object":"chat.completion","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`
const claudeResponse = `{"id":"msg_fixture","type":"message","role":"assistant","model":"provider-model","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":5}}`
const geminiResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"totalTokenCount":25}}`
const responsesResponse = `{"id":"resp_fixture","object":"response","model":"provider-model","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}`

func streamFixture(protocol string) string {
	switch protocol {
	case "openai":
		return "data: " + `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}` + "\n\ndata: " + `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: " + `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}` + "\n\ndata: [DONE]\n\n"
	case "claude":
		return "data: " + `{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"provider-model","content":[],"usage":{"input_tokens":20,"output_tokens":0}}}` + "\n\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n"
	case "gemini":
		return "data: " + geminiResponse + "\n\n"
	case "openai_responses":
		return "event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}` + "\n\nevent: response.completed\ndata: " + `{"type":"response.completed","response":` + responsesResponse + "}\n\n"
	}
	return ""
}

func TestProtocolMatrixJSONAndSSE(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "10")
	model, e := s.SaveModel(ctx, user, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	secret := []byte(strings.Repeat("a", 32))
	cipher, _ := security.Encrypt(secret, "fixture-key")
	tier := store.Tier{ID: "free", Concurrency: 100}
	for _, protocol := range []string{"openai", "claude", "gemini", "openai_responses"} {
		for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s-stream-%v", protocol, format, stream), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer fixture-key" && r.Header.Get("x-api-key") != "fixture-key" && r.Header.Get("x-goog-api-key") != "fixture-key" {
							t.Error("missing credentials")
						}
						var request map[string]any
						if json.NewDecoder(r.Body).Decode(&request) != nil {
							t.Error("invalid upstream request")
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, streamFixture(protocol))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						body := map[string]string{"openai": chatResponse, "claude": claudeResponse, "gemini": geminiResponse, "openai_responses": responsesResponse}[protocol]
						fmt.Fprint(w, body)
					}))
					defer upstream.Close()
					ch := store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: protocol, BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "provider-model"}}
					if e := s.SaveChannel(ctx, user, ch); e != nil {
						t.Fatal(e)
					}
					raw := fmt.Sprintf(`{"model":"test-model","messages":[{"role":"user","content":"Hello"}],"max_tokens":100,"stream":%v}`, stream)
					if format == types.RelayFormatOpenAIResponses {
						raw = fmt.Sprintf(`{"model":"test-model","input":"Hello","max_output_tokens":100,"stream":%v}`, stream)
					}
					if format == types.RelayFormatGemini {
						raw = fmt.Sprintf(`{"model":"test-model","contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":100},"stream":%v}`, stream)
					}
					plan, e := BuildPlan([]byte(raw), model, tier, format)
					if e != nil {
						t.Fatal(e)
					}
					id := uuid.NewString()
					if e = s.BeginRequest(ctx, id, user, "", model, tier); e != nil {
						t.Fatal(e)
					}
					var output strings.Builder
					result := New(s, secret, true).Run(ctx, id, plan, []store.Channel{ch}, func(event string, b []byte) error { output.Write(b); return nil })
					if result.State != "completed" || result.Meter.Input != 20 || result.Meter.Output != 5 || result.Meter.Amount.String() != "0.00003" {
						t.Fatalf("result=%+v output=%s", result, output.String())
					}
					for _, event := range result.FinalEvents {
						output.Write(event.Data)
					}
					if stream && !strings.Contains(output.String(), "Hello") {
						t.Fatal("text not streamed")
					}
					if !stream && !strings.Contains(string(result.Body), "Hello") {
						t.Fatal("text not returned")
					}
					if e = s.Finish(ctx, id, result.State, result.Meter, ch.ID, result.Status, result.Code, 1); e != nil {
						t.Fatal(e)
					}
				})
			}
		}
	}
}
func TestNoRetryAfterTruncatedStream(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "1")
	model, _ := s.SaveModel(ctx, user, testutil.Model())
	secret := []byte(strings.Repeat("a", 32))
	cipher, _ := security.Encrypt(secret, "key")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
	}))
	defer upstream.Close()
	ch := store.Channel{ID: uuid.NewString(), Name: "truncated", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "x"}}
	s.SaveChannel(ctx, user, ch)
	tier := store.Tier{Concurrency: 2}
	plan, _ := BuildPlan([]byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`), model, tier, types.RelayFormatOpenAI)
	id := uuid.NewString()
	s.BeginRequest(ctx, id, user, "", model, tier)
	result := New(s, secret, true).Run(ctx, id, plan, []store.Channel{ch, ch}, func(string, []byte) error { return nil })
	if result.State != "usage_unknown" || calls.Load() != 1 {
		t.Fatalf("unsafe stream retry: %+v calls %d", result, calls.Load())
	}
	s.Finish(ctx, id, result.State, result.Meter, ch.ID, result.Status, result.Code, 1)
	u, _ := s.User(ctx, user)
	if !decimal.RequireFromString(u.Unpaid).IsZero() {
		t.Fatal("uncertain usage changed account funds")
	}
}

func TestFunctionToolsAndStructuredOutputArePreservedOrRejected(t *testing.T) {
	raw := []byte(`{"model":"test-model","messages":[{"role":"user","content":"weather"}],"max_tokens":64,"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`)
	p, e := BuildPlan(raw, testutil.Model(), store.Tier{}, types.RelayFormatOpenAI)
	if e != nil {
		t.Fatal(e)
	}
	for _, protocol := range []string{"openai", "openai_responses", "claude", "gemini"} {
		b, _, e := p.request(store.Channel{Protocol: protocol, Models: map[string]string{"test-model": "upstream"}})
		if e != nil || !strings.Contains(string(b), "get_weather") || !strings.Contains(string(b), "city") {
			t.Fatalf("tool lost for %s: %s %v", protocol, b, e)
		}
	}
	schema := []byte(`{"model":"test-model","messages":[{"role":"user","content":"Return JSON"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}}`)
	p, e = BuildPlan(schema, testutil.Model(), store.Tier{}, types.RelayFormatOpenAI)
	if e != nil {
		t.Fatal(e)
	}
	for _, protocol := range []string{"openai", "openai_responses", "gemini"} {
		b, _, e := p.request(store.Channel{Protocol: protocol, Models: map[string]string{"test-model": "upstream"}})
		if e != nil || !strings.Contains(string(b), "answer") {
			t.Fatalf("schema lost for %s: %s %v", protocol, b, e)
		}
	}
	if _, _, e = p.request(store.Channel{Protocol: "claude", Models: map[string]string{"test-model": "upstream"}}); e == nil {
		t.Fatal("Claude schema silently dropped")
	}
}

func TestFailoverAfterRejectionsAndServiceErrors(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "10")
	model, _ := s.SaveModel(ctx, user, testutil.Model())
	secret := []byte(strings.Repeat("a", 32))
	cipher, _ := security.Encrypt(secret, "key")
	tier := store.Tier{Concurrency: 100}
	p, _ := BuildPlan([]byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`), model, tier, types.RelayFormatOpenAI)
	for _, status := range []int{429, 401, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, chatResponse)
			}))
			defer upstream.Close()
			ch := store.Channel{ID: uuid.NewString(), Name: "retry fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "upstream"}}
			s.SaveChannel(ctx, user, ch)
			fallback := ch
			fallback.ID = uuid.NewString()
			fallback.Priority = -1
			if e := s.SaveChannel(ctx, user, fallback); e != nil {
				t.Fatal(e)
			}
			id := uuid.NewString()
			if e := s.BeginRequest(ctx, id, user, "", model, tier); e != nil {
				t.Fatal(e)
			}
			result := New(s, secret, true).Run(ctx, id, p, []store.Channel{ch, fallback}, func(string, []byte) error { return nil })
			if calls.Load() != 2 || result.State != "completed" {
				t.Fatalf("rejection failover failed: %+v", result)
			}
		})
	}
}

func TestUsageLimitsAndOneHourCache(t *testing.T) {
	p := Plan{Model: testutil.Model(), Tier: store.Tier{}, MaxOutput: 100}
	u := relayconvert.UsageFromClaudeAPIUsage(&dto.ClaudeUsage{InputTokens: 10, OutputTokens: 5, CacheCreationInputTokens: 20, CacheCreation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 8, Ephemeral1hInputTokens: 12}})
	m, e := MeterUsage(u, p)
	if e != nil || m.Amount.String() != "0.000054" || m.CacheWrite1h != 12 || m.Input != 30 {
		t.Fatalf("cache pricing: %+v %v", m, e)
	}
	u = &dto.Usage{PromptTokens: 9223372036854775807, CompletionTokens: 5, UsageSemantic: dto.BillingUsageSemanticAnthropic, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 10}}
	if _, e = MeterUsage(u, p); e == nil {
		t.Fatal("overflowing usage accepted")
	}
	for _, protocol := range []string{"openai", "openai_responses", "claude", "gemini"} {
		if _, e := decodeResponse(protocol, []byte(`{"usage":{"prompt_tokens":1},"error":{"message":"failed"}}`), false); e == nil {
			t.Fatal("error payload accepted")
		}
	}
}
