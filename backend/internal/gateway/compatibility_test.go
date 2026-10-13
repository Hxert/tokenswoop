package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/google/uuid"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func compatibilityPlan(t *testing.T, raw string, format types.RelayFormat) Plan {
	t.Helper()
	p, e := BuildPlan([]byte(raw), testutil.Model(), store.Tier{Concurrency: 100}, format)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func prepared(t *testing.T, p Plan, model, protocol string, options store.ChannelOptions) (map[string]json.RawMessage, *requestMeta) {
	t.Helper()
	b, m, e := p.request(store.Channel{Protocol: protocol, Models: map[string]string{p.Model.ID: model}, Options: options})
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]json.RawMessage
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f, m
}
func TestMappedOpenAIModelCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, effort, role, limit string
		temperature, topP         bool
	}{
		{"gpt-5.2", "none", "developer", "max_completion_tokens", true, true},
		{"gpt-5.2", "high", "developer", "max_completion_tokens", false, false},
		{"gpt-5.2-codex", "none", "developer", "max_completion_tokens", false, false},
		{"gpt-5.1-codex-max", "high", "developer", "max_completion_tokens", false, false},
		{"o3", "high", "developer", "max_completion_tokens", false, true},
		{"o1-mini", "", "system", "max_completion_tokens", false, true},
		{"gpt-future", "", "system", "max_tokens", true, true},
		{"qwen-max", "", "system", "max_tokens", true, true},
	} {
		t.Run(tc.name+tc.effort, func(t *testing.T) {
			p := compatibilityPlan(t, fmt.Sprintf(`{"messages":[{"role":"system","content":"Instruction","annotations":{"test":"preserve"}},{"role":"user","content":"Hi"}],"temperature":0.7,"top_p":0.8,"reasoning_effort":%q,"max_tokens":256}`, tc.effort), types.RelayFormatOpenAI)
			f, m := prepared(t, p, tc.name, "openai", store.ChannelOptions{})
			if string(f[tc.limit]) != "256" || nonNull(f["temperature"]) != tc.temperature || nonNull(f["top_p"]) != tc.topP || m.UpstreamModelName != tc.name {
				t.Fatalf("capability mismatch: %s", mustJSON(f))
			}
			if !strings.Contains(string(f["messages"]), `"role":"`+tc.role+`"`) || !strings.Contains(string(f["messages"]), `"test":"preserve"`) {
				t.Fatalf("role or extension lost: %s", f["messages"])
			}
		})
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestModelModifiersAndProviderReasoning(t *testing.T) {
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"low","temperature":0.2,"max_tokens":2048}`, types.RelayFormatOpenAI)
	p.Model.ID = "public@effort:medium@temperature:0.3"
	f, m := prepared(t, p, "gpt-5.2@effort:none@temperature:0.8", "openai", store.ChannelOptions{})
	if string(f["model"]) != `"gpt-5.2"` || string(f["reasoning_effort"]) != `"none"` || string(f["temperature"]) != "0.8" || !slices.Contains(m.Codes, "mapped_model_modifier_override") {
		t.Fatalf("modifier precedence: %s %+v", mustJSON(f), m.Codes)
	}
	f, _ = prepared(t, p, "gpt-5.2-high", "openai", store.ChannelOptions{PreserveModelSuffix: true})
	if string(f["model"]) != `"gpt-5.2-high"` || string(f["reasoning_effort"]) != `"low"` {
		t.Fatalf("suffix exemption: %s", mustJSON(f))
	}
	p.Model.ID = "test-model"
	for _, tc := range []struct{ protocol, model, fragment string }{
		{"claude", "claude-sonnet-4-5@thinking:1024", `"budget_tokens":1024`},
		{"gemini", "gemini-2.5-flash@thinking:1024", `"thinkingBudget":1024`},
		{"gemini", "gemini-3-flash-preview@effort:high", `"thinkingLevel":"high"`},
		{"openai_responses", "gpt-5.2@effort:high", `"effort":"high"`},
	} {
		t.Run(tc.model, func(t *testing.T) {
			f, _ := prepared(t, p, tc.model, tc.protocol, store.ChannelOptions{})
			if !strings.Contains(mustJSON(f), tc.fragment) {
				t.Fatalf("reasoning not rendered: %s", mustJSON(f))
			}
		})
	}
	small := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"high","max_tokens":100}`, types.RelayFormatOpenAI)
	if _, _, e := small.request(store.Channel{Protocol: "claude", Models: map[string]string{small.Model.ID: "claude-sonnet-4-5"}}); e == nil {
		t.Fatal("thinking raised the customer's output cap")
	}
}
func TestLLMExtensionsAreLosslessAndBodiesIsolated(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","content":null,"reasoning_content":"prior visible reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"type\":\"image_url\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"{\"type\":\"object\",\"image_url\":\"business data\"}"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"type":{"type":"string"}}},"x_extension":"keep"}}],"enable_thinking":true,"thinking_budget":1536,"chat_template_kwargs":{"enable_thinking":true},"prompt_cache_key":"cache","prompt_cache_retention":"24h","prediction":{"type":"content","content":"prefix"},"logprobs":true,"seed":4}`
	p := compatibilityPlan(t, raw, types.RelayFormatOpenAI)
	f, _ := prepared(t, p, "qwen3", "openai", store.ChannelOptions{ParameterOverrides: map[string]json.RawMessage{"temperature": json.RawMessage("0.4")}})
	for _, fragment := range []string{`"x_extension":"keep"`, `"reasoning_content":"prior visible reasoning"`, `"thinking_budget":1536`, `"prompt_cache_retention":"24h"`, `"temperature":0.4`} {
		if !strings.Contains(mustJSON(f), fragment) {
			t.Fatalf("native extension lost: %s", fragment)
		}
	}
	if string(p.Raw) != raw {
		t.Fatal("shared request mutated")
	}
	if _, _, e := p.request(store.Channel{Protocol: "gemini", Models: map[string]string{p.Model.ID: "gemini-2.5-flash"}}); e == nil {
		t.Fatal("vendor-only fields silently dropped")
	}
	for _, raw := range []string{`{"messages":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com"}]}]}`, `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"web_search"}]}`, `{"messages":[{"role":"user","content":"Hi"}],"extra_body":{"modalities":["audio"]}}`, `{"messages":[{"role":"user","content":"Hi"}],"extra_body":{"google":{"image_config":{}}}}`} {
		if _, e := BuildPlan([]byte(raw), testutil.Model(), store.Tier{}, types.RelayFormatOpenAI); e == nil {
			t.Fatal("excluded modality accepted")
		}
	}
}
func TestResponsesReasoningContextAndVerbosity(t *testing.T) {
	p := compatibilityPlan(t, `{"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"{\"type\":\"object\"}"}],"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"text":{"verbosity":"low"}}`, types.RelayFormatOpenAIResponses)
	f, _ := prepared(t, p, "gpt-5.2", "openai_responses", store.ChannelOptions{})
	for _, fragment := range []string{`"encrypted_content":"opaque"`, `"summary":"auto"`, `"verbosity":"low"`} {
		if !strings.Contains(mustJSON(f), fragment) {
			t.Fatalf("Responses continuation lost: %s", mustJSON(f))
		}
	}
	if _, _, e := p.request(store.Channel{Protocol: "openai", Models: map[string]string{p.Model.ID: "upstream"}}); e == nil {
		t.Fatal("encrypted context silently dropped")
	}
	m := testutil.Model()
	m.StructuredOutput = false
	if _, e := BuildPlan([]byte(`{"input":"Hi","text":{"verbosity":"low"}}`), m, store.Tier{}, types.RelayFormatOpenAIResponses); e != nil {
		t.Fatal(e)
	}
}
func TestGoogleThinkingConfigAndStreamUsage(t *testing.T) {
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"max_tokens":2048,"extra_body":{"google":{"thinking_config":{"thinking_budget":1024,"include_thoughts":true}}}}`, types.RelayFormatOpenAI)
	f, _ := prepared(t, p, "gemini-2.5-flash", "gemini", store.ChannelOptions{})
	if !strings.Contains(mustJSON(f), `"thinkingBudget":1024`) || !strings.Contains(mustJSON(f), `"includeThoughts":true`) {
		t.Fatalf("Google reasoning lost: %s", mustJSON(f))
	}
	p = compatibilityPlan(t, `{"input":"Hi","stream":true}`, types.RelayFormatOpenAIResponses)
	f, _ = prepared(t, p, "upstream", "openai", store.ChannelOptions{})
	if !strings.Contains(string(f["stream_options"]), `"include_usage":true`) {
		t.Fatalf("upstream usage was not requested: %s", mustJSON(f))
	}
}
func TestSSETerminalAndLosslessWire(t *testing.T) {
	for _, protocol := range []string{"openai", "openai_responses", "claude"} {
		t.Run(protocol, func(t *testing.T) {
			p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"max_tokens":2048,"stream":true,"stream_options":{"include_usage":true}}`, types.RelayFormatOpenAI)
			if protocol == "openai_responses" {
				p = compatibilityPlan(t, `{"input":"Hi","stream":true}`, types.RelayFormatOpenAIResponses)
			}
			ch := store.Channel{Protocol: protocol, Models: map[string]string{p.Model.ID: "upstream"}}
			_, meta, e := p.request(ch)
			if e != nil {
				t.Fatal(e)
			}
			reader, writer := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer writer.Close()
				io.WriteString(writer, "\ufeff: comment\r\n\r\n"+strings.ReplaceAll(streamFixture(protocol), "\n", "\r\n"))
				io.WriteString(writer, ": connection-still-open\n\n")
				<-time.After(100 * time.Millisecond)
			}()
			response := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, Body: reader}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var wire strings.Builder
			result := (&Gateway{}).stream(ctx, response, p, meta, ch, "fixture", func(event string, data []byte) error { wire.Write(data); return nil })
			if result.State != "completed" || result.Meter.Output != 5 {
				t.Fatalf("terminal stream not finalized: %+v", result)
			}
			if protocol == "openai_responses" && (len(result.FinalEvents) != 1 || strings.Contains(wire.String(), "response.completed")) {
				t.Fatal("completion escaped ledger gate")
			}
			<-done
		})
	}
}
func TestSSEUsageOptOutAndErrorsNeverBecomeCharges(t *testing.T) {
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"stream":true}`, types.RelayFormatOpenAI)
	ch := store.Channel{Protocol: "openai", Models: map[string]string{p.Model.ID: "upstream"}}
	_, meta, _ := p.request(ch)
	for _, tc := range []struct{ name, body, want string }{
		{"complete", streamFixture("openai"), "completed"},
		{"error", strings.ReplaceAll(streamFixture("openai"), "data: [DONE]", "event: error\ndata: {\"type\":\"error\"}"), "usage_unknown"},
		{"truncated", strings.ReplaceAll(streamFixture("openai"), "data: [DONE]\n\n", ""), "usage_unknown"},
		{"empty", "data: [DONE]\n\n", "usage_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := (&Gateway{}).stream(context.Background(), &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, p, meta, ch, "fixture", func(_ string, b []byte) error { out.Write(b); return nil })
			if r.State != tc.want {
				t.Fatalf("%+v", r)
			}
			if strings.Contains(out.String(), `"usage"`) {
				t.Fatal("include_usage=false ignored")
			}
			if r.State != "completed" && !r.Meter.Amount.IsZero() {
				t.Fatal("uncertain stream charged")
			}
		})
	}
}
func TestRoutingWeightsAffinityAndTransport(t *testing.T) {
	g := New(nil, []byte("fixture-key"), true)
	p := compatibilityPlan(t, `{"messages":[{"role":"system","content":"same prefix"},{"role":"user","content":"Hi"}]}`, types.RelayFormatOpenAI)
	p.RoutingScope = "user-1"
	channels := []store.Channel{{ID: "a", Priority: 1, Options: store.ChannelOptions{Weight: 1}}, {ID: "b", Priority: 1, Options: store.ChannelOptions{Weight: 3}}}
	count := 0
	for i := 0; i < 1000; i++ {
		if g.orderChannels(fmt.Sprint(i), p, channels)[0].ID == "b" {
			count++
		}
	}
	if count < 680 || count > 820 {
		t.Fatalf("weight distribution: %d", count)
	}
	channels[0].Options.CacheAffinity = true
	first := g.orderChannels("request-1", p, channels)[0].ID
	for i := 0; i < 20; i++ {
		if g.orderChannels(fmt.Sprint(i), p, channels)[0].ID != first {
			t.Fatal("cache affinity unstable")
		}
	}
	channels = append(channels, store.Channel{ID: "priority", Priority: 2})
	if g.orderChannels("request", p, channels)[0].ID != "priority" {
		t.Fatal("weight overrode priority")
	}
	c := g.clientFor(store.Channel{Options: store.ChannelOptions{HTTPProtocol: "http1"}}, "1")
	if c == g.Client || c.Transport.(*http.Transport).Protocols.HTTP2() {
		t.Fatal("HTTP/1 compatibility not applied")
	}
	if g.Client.Timeout != 0 {
		t.Fatal("global timeout defeats per-channel reasoning timeout")
	}
}
func TestConversionSkipsDoNotConsumeRetriesAndPostpaidWire(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "10")
	model, e := s.SaveModel(ctx, user, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	secret := []byte(strings.Repeat("a", 32))
	cipher, _ := security.Encrypt(secret, "fixture-key")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var due string
		s.DB.QueryRow(ctx, "SELECT unpaid_balance::text FROM users WHERE id=$1", user).Scan(&due)
		if due != "0.000000000000000000" {
			t.Errorf("precharge detected: %s", due)
		}
		fmt.Fprint(w, strings.Replace(chatResponse, `"object":"chat.completion"`, `"object":"chat.completion","provider_extension":{"preserved":true}`, 1))
	}))
	defer upstream.Close()
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}}}`, types.RelayFormatOpenAI)
	p.Model = model
	var channels []store.Channel
	for i := 0; i < 5; i++ {
		ch := store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: "claude", BaseURL: upstream.URL, SecretCipher: cipher, Priority: 10 - i, Enabled: true, Models: map[string]string{model.ID: "upstream"}}
		if i == 4 {
			ch.Protocol = "openai"
		}
		if e := s.SaveChannel(ctx, user, ch); e != nil {
			t.Fatal(e)
		}
		channels = append(channels, ch)
	}
	id := uuid.NewString()
	if e := s.BeginRequest(ctx, id, user, "", model, p.Tier); e != nil {
		t.Fatal(e)
	}
	r := New(s, secret, true).Run(ctx, id, p, channels, func(string, []byte) error { return nil })
	if r.State != "completed" || calls.Load() != 1 || !strings.Contains(string(r.Body), `"provider_extension":{"preserved":true}`) {
		t.Fatalf("fallback or passthrough failed: %+v", r)
	}
	if e = s.Finish(ctx, id, r.State, r.Meter, r.Channel, r.Status, r.Code, 1); e != nil {
		t.Fatal(e)
	}
	var attempts, charges int
	s.DB.QueryRow(ctx, "SELECT count(*) FROM request_attempts WHERE request_id=$1", id).Scan(&attempts)
	s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE request_id=$1 AND kind='request.completed'", id).Scan(&charges)
	if attempts != 5 || charges != 1 {
		t.Fatalf("attempts %d charges %d", attempts, charges)
	}
}

func TestOpenRouterAndNativeReasoningControls(t *testing.T) {
	for _, tc := range []struct{ model, body, want string }{
		{"upstream@thinking:on", `{"messages":[{"role":"user","content":"Hi"}]}`, `"reasoning_effort":"high"`},
		{"upstream@thinking:off", `{"messages":[{"role":"user","content":"Hi"}]}`, `"reasoning_effort":"none"`},
	} {
		p := compatibilityPlan(t, tc.body, types.RelayFormatOpenAI)
		f, _ := prepared(t, p, tc.model, "openai", store.ChannelOptions{Dialect: "openai"})
		if !strings.Contains(mustJSON(f), tc.want) {
			t.Fatalf("mode was lost: %s", mustJSON(f))
		}
	}
	for _, tc := range []struct{ body, want string }{
		{`{"reasoning_effort":"none"}`, `"enabled":false`},
		{`{"reasoning":{"max_tokens":1200,"exclude":true}}`, `"max_tokens":1200`},
		{`{"thinking":{"type":"enabled","budget_tokens":1200}}`, `"max_tokens":1200`},
	} {
		var fields map[string]json.RawMessage
		json.Unmarshal([]byte(tc.body), &fields)
		fields["messages"] = json.RawMessage(`[{"role":"user","content":"Hi"}]`)
		p := compatibilityPlan(t, mustJSON(fields), types.RelayFormatOpenAI)
		f, _ := prepared(t, p, "anthropic/claude-sonnet-4-5", "openai", store.ChannelOptions{Dialect: "openrouter"})
		if !strings.Contains(mustJSON(f), tc.want) || nonNull(f["reasoning_effort"]) || nonNull(f["thinking"]) || string(f["usage"]) != `{"include":true}` {
			t.Fatalf("OpenRouter config: %s", mustJSON(f))
		}
	}
	p := compatibilityPlan(t, `{"input":"Hi","reasoning":{"summary":"auto"}}`, types.RelayFormatOpenAIResponses)
	f, _ := prepared(t, p, "gpt-5.2@thinking:on", "openai_responses", store.ChannelOptions{})
	if string(f["reasoning"]) != `{"effort":"high","summary":"auto"}` {
		t.Fatalf("Responses mode/summary: %s", f["reasoning"])
	}
	p = compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"provider-future-level"}`, types.RelayFormatOpenAI)
	f, _ = prepared(t, p, "provider-future-model", "openai", store.ChannelOptions{Dialect: "openai"})
	if string(f["reasoning_effort"]) != `"provider-future-level"` {
		t.Fatal("native future effort rejected or changed")
	}
}
func TestChannelPolicyProtectedFieldsAndPortableOptions(t *testing.T) {
	for _, options := range []store.ChannelOptions{
		{Headers: map[string]string{"Authorization": "Bearer secret"}},
		{Headers: map[string]string{"OpenAI-Project": "bad\r\nheader"}},
		{ParameterOverrides: map[string]json.RawMessage{"model": json.RawMessage(`"bypass"`)}},
		{ParameterOverrides: map[string]json.RawMessage{"max_tokens": json.RawMessage(`9999999`)}},
		{DropParameters: []string{"store"}},
		{HTTP2Shards: 9}, {RequestTimeoutSeconds: 10}, {StreamIdleSeconds: 10000},
	} {
		if ValidateChannelOptions(options) == nil {
			t.Fatalf("unsafe channel policy accepted: %+v", options)
		}
	}
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"stream":true,"service_tier":"priority","verbosity":"low","prompt_cache_retention":"24h"}`, types.RelayFormatOpenAI)
	f, meta := prepared(t, p, "upstream", "openai_responses", store.ChannelOptions{})
	if nonNull(f["service_tier"]) || !slices.Contains(meta.Codes, "service_tier_filtered") || !strings.Contains(string(f["text"]), `"verbosity":"low"`) || string(f["prompt_cache_retention"]) != `"24h"` {
		t.Fatalf("portable controls: %s", mustJSON(f))
	}
	f, _ = prepared(t, p, "upstream", "openai", store.ChannelOptions{AllowServiceTier: true, OmitStreamOptions: true})
	if nonNull(f["stream_options"]) || string(f["service_tier"]) != `"priority"` {
		t.Fatalf("channel opt-in: %s", mustJSON(f))
	}
}
func TestThinkingToContentKeepsUsageAndOrderedChunks(t *testing.T) {
	state := &reasoningTextState{}
	var text strings.Builder
	for _, delta := range []string{`{"reasoning_content":"first "}`, `{"reasoning":"second"}`, `{"content":"answer"}`} {
		b, e := state.rewrite([]byte(`{"choices":[{"index":0,"delta":`+delta+`}],"usage":{"prompt_tokens":20}}`), true)
		if e != nil {
			t.Fatal(e)
		}
		var v struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		json.Unmarshal(b, &v)
		text.WriteString(v.Choices[0].Delta.Content)
		if !strings.Contains(string(b), `"prompt_tokens":20`) || strings.Contains(string(b), "reasoning_content") {
			t.Fatalf("rewritten payload: %s", b)
		}
	}
	if text.String() != "<think>\nfirst second\n</think>\nanswer" {
		t.Fatalf("reasoning order: %q", text.String())
	}
}
func TestClientToolHistoryAndSignatureCompatibility(t *testing.T) {
	p := compatibilityPlan(t, `{"max_tokens":2048,"messages":[{"role":"user","content":"weather"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`, types.RelayFormatOpenAI)
	for _, protocol := range []string{"openai", "openai_responses", "claude", "gemini"} {
		f, _ := prepared(t, p, "upstream", protocol, store.ChannelOptions{GeminiThoughtSignature: true})
		for _, fragment := range []string{"lookup", "Paris", "sunny"} {
			if !strings.Contains(mustJSON(f), fragment) {
				t.Fatalf("%s lost %s", protocol, fragment)
			}
		}
		if protocol == "gemini" && !strings.Contains(mustJSON(f), "thoughtSignature") {
			t.Fatal("Gemini signature option ignored")
		}
	}
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses} {
		raw := `{"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"custom","custom":{"name":"code","format":{"type":"text"}}}]}`
		if format == types.RelayFormatOpenAIResponses {
			raw = `{"input":"Hi","tools":[{"type":"custom","name":"code","format":{"type":"text"}}]}`
		}
		p := compatibilityPlan(t, raw, format)
		f, _ := prepared(t, p, "upstream", string(format), store.ChannelOptions{})
		if !strings.Contains(mustJSON(f), `"type":"custom"`) {
			t.Fatal("custom client tool lost")
		}
	}
}

func TestStreamHeartbeatDoesNotHideUpstreamIdle(t *testing.T) {
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"stream":true}`, types.RelayFormatOpenAI)
	ch := store.Channel{Protocol: "openai", Options: store.ChannelOptions{StreamIdleSeconds: 16}, Models: map[string]string{p.Model.ID: "upstream"}}
	_, meta, e := p.request(ch)
	if e != nil {
		t.Fatal(e)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	heartbeats := 0
	result := (&Gateway{}).stream(ctx, &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, p, meta, ch, "heartbeat", func(event string, b []byte) error {
		if event == "" && b == nil {
			heartbeats++
		}
		return nil
	})
	if result.State != "usage_unknown" || result.Code != "stream_idle_timeout" || heartbeats < 1 || !result.Meter.Amount.IsZero() {
		t.Fatalf("idle was reset by downstream pings: %+v, pings %d", result, heartbeats)
	}
}

func TestDeepSeekV4AliasesAcrossNativeSurfaces(t *testing.T) {
	p := compatibilityPlan(t, `{"max_tokens":2048,"messages":[{"role":"user","content":"Hi"}]}`, types.RelayFormatOpenAI)
	for _, protocol := range []string{"openai", "openai_responses", "claude"} {
		for _, suffix := range []string{"max", "none"} {
			f, meta := prepared(t, p, "deepseek-v4-pro-"+suffix, protocol, store.ChannelOptions{Dialect: "deepseek"})
			if string(f["model"]) != `"deepseek-v4-pro"` || meta.UpstreamModelName != "deepseek-v4-pro" {
				t.Fatalf("model alias forwarded: %s", mustJSON(f))
			}
			if protocol == "openai_responses" {
				if !strings.Contains(string(f["reasoning"]), `"effort":"`+suffix+`"`) {
					t.Fatalf("Responses effort lost: %s", mustJSON(f))
				}
			} else {
				mode := "enabled"
				if suffix == "none" {
					mode = "disabled"
				}
				if string(f["thinking"]) != `{"type":"`+mode+`"}` {
					t.Fatalf("thinking lost: %s", mustJSON(f))
				}
			}
		}
	}
}

func TestResponseConversionDiagnosticsReachReceipts(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	user := testutil.Account(t, s, "10")
	model, err := s.SaveModel(ctx, user, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("a", 32))
	cipher, _ := security.Encrypt(secret, "fixture-key")
	for _, stream := range []bool{false, true} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				body := strings.Replace(streamFixture("claude"), `{"type":"message_delta"`, `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"private-opaque-context"}}`+"\n\ndata: "+`{"type":"content_block_stop","index":1}`+"\n\ndata: "+`{"type":"message_delta"`, 1)
				fmt.Fprint(w, body)
				return
			}
			fmt.Fprint(w, strings.Replace(claudeResponse, `"content":[`, `"content":[{"type":"redacted_thinking","data":"private-opaque-context"},`, 1))
		}))
		ch := store.Channel{ID: uuid.NewString(), Name: "diagnostics", Protocol: "claude", BaseURL: upstream.URL, SecretCipher: cipher, Enabled: true, Models: map[string]string{model.ID: "upstream"}}
		if err = s.SaveChannel(ctx, user, ch); err != nil {
			t.Fatal(err)
		}
		p := compatibilityPlan(t, fmt.Sprintf(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":2048,"stream":%v}`, stream), types.RelayFormatOpenAI)
		p.Model = model
		id := uuid.NewString()
		if err = s.BeginRequest(ctx, id, user, "", model, p.Tier); err != nil {
			t.Fatal(err)
		}
		result := New(s, secret, true).Run(ctx, id, p, []store.Channel{ch}, func(string, []byte) error { return nil })
		upstream.Close()
		if result.State != "completed" {
			t.Fatalf("stream=%v: %+v", stream, result)
		}
		var codes string
		if err = s.DB.QueryRow(ctx, "SELECT diagnostics::text FROM request_attempts WHERE request_id=$1", id).Scan(&codes); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(codes, "continuation_state_lost") || strings.Contains(codes, "private-opaque-context") {
			t.Fatalf("stream=%v: missing or unsafe response diagnostics: %s", stream, codes)
		}
		if err = s.Finish(ctx, id, result.State, result.Meter, ch.ID, result.Status, result.Code, 1); err != nil {
			t.Fatal(err)
		}
	}
}
