package gateway

import (
	"context"
	"encoding/json"
	"github.com/QuantumNous/new-api/relaykit/types"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestNativePreparationAndBoundary(t *testing.T) {
	for _, tc := range []struct {
		format               types.RelayFormat
		raw, model, fragment string
	}{
		{types.RelayFormatClaude, `{"system":[{"type":"text","text":"Instructions","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"opaque"},{"type":"text","text":"Answer"}]},{"role":"user","content":"Hi"}],"max_tokens":2048}`, "claude-sonnet-4-5@thinking:1024", `"budget_tokens":1024`},
		{types.RelayFormatGemini, `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{"x":1}},"thoughtSignature":"opaque"}]},{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":2048}}`, "gemini-3-flash@effort:high", `"thinkingLevel":"high"`},
	} {
		p := compatibilityPlan(t, tc.raw, tc.format)
		f, _ := prepared(t, p, tc.model, string(tc.format), store.ChannelOptions{})
		b := mustJSON(f)
		if !strings.Contains(b, "opaque") || !strings.Contains(b, tc.fragment) {
			t.Fatalf("native data lost: %s", b)
		}
		if _, _, e := p.request(store.Channel{Protocol: "openai", Models: map[string]string{p.Model.ID: "gpt-5"}}); e == nil {
			t.Fatal("opaque native state crossed protocol")
		}
	}
	for _, tc := range []struct {
		format types.RelayFormat
		raw    string
	}{
		{types.RelayFormatClaude, `{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"bytes"}}]}]}`},
		{types.RelayFormatGemini, `{"contents":[{"parts":[{"inlineData":{"data":"bytes"}}]}]}`},
		{types.RelayFormatGemini, `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"responseModalities":["AUDIO"]}}`},
		{types.RelayFormatGemini, `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"codeExecution":{}}]}`},
	} {
		if _, e := BuildPlan([]byte(tc.raw), testutil.Model(), store.Tier{}, tc.format); e == nil {
			t.Fatal("unmetered modality admitted")
		}
	}
}
func TestSpecialOperationsAndHostedTools(t *testing.T) {
	m := testutil.Model()
	m.Pricing.ToolPrices = map[string]string{"web_search": "0.01", "file_search": "0.0025"}
	p, e := BuildOperation([]byte(`{"input":"Hi"}`), m, store.Tier{}, types.RelayFormatOpenAIResponses, "compact")
	if e != nil {
		t.Fatal(e)
	}
	ch := store.Channel{Protocol: "openai_responses", Models: map[string]string{m.ID: "gpt-5"}}
	if _, _, e = p.request(ch); e == nil {
		t.Fatal("compact used unconfigured channel")
	}
	ch.Options.SupportsCompact = true
	body, _, e := p.request(ch)
	if e != nil || strings.Contains(string(body), "max_output_tokens") || strings.Contains(string(body), "store") {
		t.Fatalf("bad compact body %s %v", body, e)
	}
	r := (&Gateway{}).specialResult([]byte(`{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":20,"output_tokens":5}}`), p, ch)
	if r.State != "completed" || r.Meter.Amount.String() != "0.00003" {
		t.Fatalf("compact: %+v", r)
	}
	p, e = BuildOperation([]byte(`{"query":"latest papers"}`), m, store.Tier{}, types.RelayFormatOpenAIResponses, "alpha_search")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.request(ch); e == nil {
		t.Fatal("alpha used unconfigured channel")
	}
	ch.Options.SupportsAlphaSearch = true
	r = (&Gateway{}).specialResult([]byte(`{"results":[]}`), p, ch)
	if r.State != "completed" || r.Meter.Amount.String() != "0.01" || r.Meter.Input != 0 {
		t.Fatalf("alpha: %+v", r)
	}
	r = (&Gateway{}).specialResult([]byte(`{"ok":false}`), p, ch)
	if r.State == "completed" {
		t.Fatal("malformed alpha success charged")
	}
	p, e = BuildPlan([]byte(`{"input":"Search","tools":[{"type":"web_search"},{"type":"file_search","vector_store_ids":["vs_1"]}]}`), m, store.Tier{}, types.RelayFormatOpenAIResponses)
	if e != nil {
		t.Fatal(e)
	}
	tools := newToolUsage()
	tools.observe([]byte(`{"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed"}}`), "openai_responses")
	tools.observe([]byte(`{"response":{"output":[{"id":"ws_1","type":"web_search_call","status":"completed"},{"id":"fs_1","type":"file_search_call","status":"completed"}]}}`), "openai_responses")
	calls, e := tools.finish(p, "openai_responses")
	if e != nil || calls["web_search"] != 1 || calls["file_search"] != 1 {
		t.Fatalf("double counted tools: %v %v", calls, e)
	}
	tools.observe([]byte(`{"output":[{"id":"bad","type":"computer_call","status":"completed"}]}`), "openai_responses")
	if _, e = tools.finish(p, "openai_responses"); e == nil {
		t.Fatal("unknown paid tool charged")
	}
	p, e = BuildPlan([]byte(`{"messages":[{"role":"user","content":"Search"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`), m, store.Tier{}, types.RelayFormatClaude)
	if e != nil {
		t.Fatal(e)
	}
	tools = newToolUsage()
	if _, e = tools.finish(p, "claude"); e == nil {
		t.Fatal("missing Claude tool evidence treated as free")
	}
	tools.observe([]byte(`{"usage":{"server_tool_use":{"web_search_requests":2}}}`), "claude")
	calls, e = tools.finish(p, "claude")
	if e != nil || calls["web_search"] != 2 {
		t.Fatalf("Claude usage %v %v", calls, e)
	}
}
func TestEffectivePriceControlsAndPerformance(t *testing.T) {
	p := compatibilityPlan(t, `{"messages":[{"role":"user","content":"Hi"}],"service_tier":"priority","max_tokens":100,"stream":true}`, types.RelayFormatOpenAI)
	f, _ := prepared(t, p, "gpt-5", "openai", store.ChannelOptions{})
	if v := priceParameters([]byte(mustJSON(f)))["service_tier"]; v != nil {
		t.Fatal("removed control affected bill")
	}
	for _, raw := range []string{`{"choices":[{"delta":{"role":"assistant"}}]}`, `{"usage":{"prompt_tokens":10}}`} {
		var f map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &f)
		if hasModelContent(f, "openai") {
			t.Fatal("metadata counted as model token")
		}
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		defer writer.Close()
		io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		time.Sleep(40 * time.Millisecond)
		io.WriteString(writer, streamFixture("openai"))
	}()
	ch := store.Channel{Protocol: "openai", Models: map[string]string{p.Model.ID: "upstream"}}
	_, meta, e := p.request(ch)
	if e != nil {
		t.Fatal(e)
	}
	p.StartedAt = time.Now()
	r := (&Gateway{}).stream(context.Background(), &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, p, meta, ch, "test", func(_ string, b []byte) error {
		if strings.Contains(string(b), `"finish_reason":"stop"`) {
			t.Error("terminal escaped settlement gate")
		}
		return nil
	})
	if r.State != "completed" || r.Meter.FirstTokenMS == nil || *r.Meter.FirstTokenMS < 30 || r.Meter.GenerationMS == nil {
		t.Fatalf("bad timing: %+v", r)
	}
	if len(r.FinalEvents) == 0 {
		t.Fatal("missing gated terminal")
	}
}
