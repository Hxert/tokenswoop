package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestCallerTokenParametersWithoutPlatformCeilings(t *testing.T) {
	text := strings.Repeat("long context ", 10000)
	for _, source := range []types.RelayFormat{"openai", "openai_responses", "claude", "gemini"} {
		for _, limit := range []int{0, 500000} {
			t.Run(fmt.Sprintf("%s/%d", source, limit), func(t *testing.T) {
				f := map[string]json.RawMessage{}
				switch source {
				case "openai", "claude":
					put(f, "messages", []map[string]string{{"role": "user", "content": text}})
				case "openai_responses":
					put(f, "input", text)
				case "gemini":
					put(f, "contents", []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": text}}}})
				}
				if limit > 0 {
					switch source {
					case "openai", "claude":
						put(f, "max_tokens", limit)
					case "openai_responses":
						put(f, "max_output_tokens", limit)
					case "gemini":
						put(f, "generationConfig", map[string]int{"maxOutputTokens": limit})
					}
				}
				p, e := BuildPlan([]byte(mustJSON(f)), testutil.Model(), store.Tier{}, source)
				if e != nil || p.MaxOutput != limit {
					t.Fatalf("platform limited request: output=%d %v", p.MaxOutput, e)
				}
				for _, target := range []string{"openai", "openai_responses", "claude", "gemini"} {
					b, _, e := p.request(store.Channel{Protocol: target, Models: map[string]string{p.Model.ID: "upstream"}})
					if target == "claude" && limit == 0 {
						if e == nil || !strings.Contains(e.Error(), "requires an explicit") {
							t.Fatalf("Claude must ask for its required parameter, not inject a ceiling: %s %v", b, e)
						}
						continue
					}
					if e != nil {
						t.Fatalf("%s: %v", target, e)
					}
					var out map[string]json.RawMessage
					json.Unmarshal(b, &out)
					if target == "gemini" {
						json.Unmarshal(out["generationConfig"], &out)
					}
					found := false
					for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxOutputTokens"} {
						if nonNull(out[key]) {
							found = true
							if limit == 0 || string(out[key]) != "500000" {
								t.Fatalf("%s changed caller output parameter: %s", target, b)
							}
						}
					}
					if limit > 0 && !found {
						t.Fatalf("%s lost output parameter: %s", target, b)
					}
				}
			})
		}
	}
}

func TestConfirmedUsageIsNotBoundByTokenConfiguration(t *testing.T) {
	p := Plan{Model: testutil.Model(), MaxOutput: 1}
	u := &dto.Usage{PromptTokens: 20000000, CompletionTokens: 300000, UsageSource: "usage"}
	m, e := MeterUsage(u, p)
	if e != nil || m.Input != 20000000 || m.Output != 300000 || m.Amount.String() != "20.6" {
		t.Fatalf("confirmed usage rejected by a token ceiling: %+v %v", m, e)
	}
}
