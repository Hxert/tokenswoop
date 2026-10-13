package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/shopspring/decimal"
	"strings"
	"tokenswoop/backend/internal/pricing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

func BuildOperation(raw []byte, m store.Model, t store.Tier, format types.RelayFormat, operation string) (Plan, error) {
	if operation == "" || operation == "generate" {
		return BuildPlan(raw, m, t, format)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return Plan{}, fmt.Errorf("invalid request")
	}
	if operation == "compact" {
		for k := range fields {
			if !hasWord("model input instructions previous_response_id tools parallel_tool_calls reasoning service_tier prompt_cache_key prompt_cache_options prompt_cache_retention text", k) {
				return Plan{}, fmt.Errorf("unsupported compact field: %s", k)
			}
		}
		p, e := BuildPlan(raw, m, t, types.RelayFormatOpenAIResponses)
		p.Operation = operation
		return p, e
	}
	if operation != "alpha_search" {
		return Plan{}, fmt.Errorf("unsupported operation")
	}
	for k := range fields {
		if !hasWord("model query queries search_context_size user_location filters max_results", k) {
			return Plan{}, fmt.Errorf("unsupported alpha search field: %s", k)
		}
	}
	var query string
	var queries []string
	json.Unmarshal(fields["query"], &query)
	json.Unmarshal(fields["queries"], &queries)
	if strings.TrimSpace(query) == "" && len(queries) == 0 {
		return Plan{}, fmt.Errorf("a search query is required")
	}
	if len(queries) > 50 {
		return Plan{}, fmt.Errorf("too many search queries")
	}
	if _, ok := m.Pricing.ToolPrices["web_search"]; !ok || !m.Tools {
		return Plan{}, fmt.Errorf("alpha search requires an explicit web_search price")
	}
	return Plan{Raw: raw, Model: m, Tier: t, Format: types.RelayFormatOpenAIResponses, Operation: operation}, m.Pricing.Validate()
}
func (p Plan) alphaRequest(ch store.Channel, meta *requestMeta) ([]byte, *requestMeta, error) {
	if !ch.Options.SupportsAlphaSearch || !hasWord("openai openai_responses", ch.Protocol) {
		return nil, meta, fmt.Errorf("channel does not support alpha search")
	}
	fields := map[string]json.RawMessage{}
	json.Unmarshal(p.Raw, &fields)
	put(fields, "model", meta.UpstreamModelName)
	b, e := json.Marshal(fields)
	return b, meta, e
}
func (g *Gateway) specialResult(raw []byte, p Plan, ch store.Channel) Result {
	result := Result{State: "usage_unknown", Status: 502, Code: "invalid_upstream_response", Channel: ch.ID}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || nonNull(fields["error"]) {
		return result
	}
	if p.Operation == "compact" {
		var object string
		json.Unmarshal(fields["object"], &object)
		var output []json.RawMessage
		if object != "response.compaction" || json.Unmarshal(fields["output"], &output) != nil || len(output) == 0 {
			return result
		}
		var usage dto.Usage
		if json.Unmarshal(fields["usage"], &usage) != nil {
			return result
		}
		meter, e := MeterUsage(&usage, p)
		if e != nil {
			result.Code = "usage_missing"
			return result
		}
		result.Meter = meter
	} else {
		// Alpha search's documented unit is one successful endpoint invocation.
		// Require a result envelope, never treat arbitrary HTTP 200 JSON as success.
		if !nonNull(fields["results"]) && !nonNull(fields["data"]) && !nonNull(fields["output"]) {
			return result
		}
		trace, e := pricing.Charge(pricing.Config{ToolPrices: p.Model.Pricing.ToolPrices}, pricing.Rates{Input: "0", Output: "0", CacheRead: "0", CacheWrite: "0", CacheWrite1h: "0"}, pricing.Facts{Tools: map[string]int64{"web_search": 1}}, p.Tier.DiscountBPS)
		if e != nil {
			return result
		}
		amount, _ := decimal.NewFromString(trace.Amount)
		result.Meter = store.Meter{Trace: &trace, Amount: amount, Source: "upstream:alpha_search_completed"}
	}
	result.Body, _ = encodeResult(json.RawMessage(raw), p.Model.ID)
	result.State = "completed"
	result.Status = 200
	result.Code = ""
	return result
}
func (g *Gateway) rememberResponse(raw []byte, p Plan, ch store.Channel, m *store.Meter) {
	if !p.Stored || ch.Protocol != "openai_responses" {
		return
	}
	var shape struct {
		ID       string `json:"id"`
		Response *struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return
	}
	if shape.Response != nil {
		shape.ID = shape.Response.ID
	}
	if len(shape.ID) < 1 || len(shape.ID) > 512 {
		return
	}
	m.ResponseHandle = &store.ResponseHandle{Digest: security.Digest(g.Secret, "response-handle", p.RoutingScope+":"+p.Model.ID+":"+shape.ID), ChannelRevision: ch.Revision}
}

// Freeze only non-secret scalar controls from the effective upstream body.
func priceParameters(raw []byte) map[string]any {
	var f map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	dec.Decode(&f)
	out := map[string]any{}
	for _, k := range strings.Fields("service_tier reasoning_effort max_tokens max_completion_tokens max_output_tokens temperature top_p") {
		if v, ok := f[k]; ok {
			switch v.(type) {
			case string, json.Number, bool:
				out[k] = v
			}
		}
	}
	for _, k := range []string{"reasoning", "output_config"} {
		if config, ok := f[k].(map[string]any); ok {
			if v, ok := config["effort"].(string); ok {
				out["reasoning_effort"] = v
			}
		}
	}
	if config, ok := f["generationConfig"].(map[string]any); ok {
		for src, dst := range map[string]string{"temperature": "temperature", "topP": "top_p", "maxOutputTokens": "max_output_tokens"} {
			if v, ok := config[src]; ok {
				out[dst] = v
			}
		}
	}
	return out
}
