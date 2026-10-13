package gateway

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/store"
)

// LLM fields present in the pinned RelayKit DTOs. Media and background jobs
// are outside this service's metering contract.
const commonFields = "model stream temperature top_p tools tool_choice parallel_tool_calls store metadata user service_tier prompt_cache_key prompt_cache_retention safety_identifier frequency_penalty presence_penalty stream_options enable_thinking thinking_budget chat_template_kwargs top_k min_p repetition_penalty cache_salt"
const chatFields = "messages max_tokens max_completion_tokens response_format stop n reasoning_effort verbosity seed logprobs top_logprobs logit_bias functions function_call prediction reasoning thinking think reasoning_split thinking_token_budget include_reasoning structured_outputs return_token_ids min_tokens separate_reasoning stream_reasoning regex ebnf stop_token_ids stop_regex no_stop_trim ignore_eos skip_special_tokens continue_final_message prefix suffix extra_body"
const responsesFields = "input instructions max_output_tokens text reasoning include truncation max_tool_calls top_logprobs safety_identifier client_metadata context_management prompt_cache_options previous_response_id"

func hasWord(words, key string) bool { return slices.Contains(strings.Fields(words), key) }
func nonNull(b json.RawMessage) bool { return len(b) > 0 && string(b) != "null" }

func BuildPlan(raw []byte, m store.Model, t store.Tier, format types.RelayFormat) (Plan, error) {
	p := Plan{Raw: raw, Model: m, Tier: t, Format: format, Operation: "generate"}
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return p, fmt.Errorf("request must be a JSON object")
	}
	if format == types.RelayFormatClaude || format == types.RelayFormatGemini {
		if format == types.RelayFormatGemini {
			if err := canonicalGeminiFields(fields); err != nil {
				return p, err
			}
			p.Raw, _ = json.Marshal(fields)
		}
		return buildNativePlan(p, fields)
	}
	allowed := commonFields
	switch format {
	case types.RelayFormatOpenAI:
		allowed += " " + chatFields
	case types.RelayFormatOpenAIResponses:
		allowed += " " + responsesFields
	default:
		return p, fmt.Errorf("unsupported request protocol")
	}
	for k := range fields {
		if !hasWord(allowed, k) {
			return p, fmt.Errorf("unsupported field: %s", k)
		}
	}
	if b := fields["stream"]; b != nil {
		if string(b) == "null" || json.Unmarshal(b, &p.Stream) != nil {
			return p, fmt.Errorf("stream must be boolean")
		}
	}
	if b := fields["store"]; b != nil {
		if json.Unmarshal(b, &p.Stored) != nil || string(b) == "null" || p.Stored && format != types.RelayFormatOpenAIResponses {
			return p, fmt.Errorf("store is supported only by Responses channels")
		}
	}
	if b := fields["previous_response_id"]; nonNull(b) {
		if json.Unmarshal(b, &p.PreviousResponseID) != nil || len(p.PreviousResponseID) < 1 || len(p.PreviousResponseID) > 512 {
			return p, fmt.Errorf("invalid previous_response_id")
		}
	}
	if b := fields["n"]; b != nil && string(b) != "1" {
		return p, fmt.Errorf("only n=1 is supported")
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if b := fields[key]; nonNull(b) {
			if json.Unmarshal(b, &p.MaxOutput) != nil || p.MaxOutput < 1 {
				return p, fmt.Errorf("invalid output token limit")
			}
		}
	}
	if fields["max_tokens"] != nil && fields["max_completion_tokens"] != nil {
		return p, fmt.Errorf("choose one output token limit")
	}
	if b := fields["stop"]; nonNull(b) {
		var text string
		var list []string
		if json.Unmarshal(b, &text) != nil && (json.Unmarshal(b, &list) != nil || len(list) > 4) {
			return p, fmt.Errorf("stop must be a string or up to four strings")
		}
	}
	var content any
	key := "messages"
	if format == types.RelayFormatOpenAIResponses {
		key = "input"
	}
	if json.Unmarshal(fields[key], &content) != nil {
		return p, fmt.Errorf("%s is required", key)
	}
	if format == types.RelayFormatOpenAI {
		messages, ok := content.([]any)
		if !ok || len(messages) == 0 {
			return p, fmt.Errorf("messages must be a non-empty array")
		}
		for _, v := range messages {
			msg, ok := v.(map[string]any)
			if !ok {
				return p, fmt.Errorf("invalid message")
			}
			role, _ := msg["role"].(string)
			if !hasWord("system developer user assistant tool function", role) {
				return p, fmt.Errorf("unsupported message role")
			}
			for k := range msg {
				if !hasWord("role content name tool_calls tool_call_id function_call reasoning_content reasoning prefix annotations tools", k) {
					return p, fmt.Errorf("unsupported message field: %s", k)
				}
			}
			if msg["content"] == nil {
				if role != "assistant" || (msg["tool_calls"] == nil && msg["function_call"] == nil && msg["reasoning_content"] == nil) {
					return p, fmt.Errorf("message content required")
				}
			} else if !textContent(msg["content"]) {
				return p, fmt.Errorf("only text message content is supported")
			}
			if calls, exists := msg["tool_calls"]; exists {
				list, ok := calls.([]any)
				if !ok {
					return p, fmt.Errorf("invalid tool calls")
				}
				for _, call := range list {
					obj, ok := call.(map[string]any)
					if !ok || !hasWord("function custom", fmt.Sprint(obj["type"])) {
						return p, fmt.Errorf("only function tool calls are supported")
					}
				}
			}
		}
	} else if e := validateResponsesInput(content); e != nil {
		return p, e
	}
	if b := fields["stream_options"]; nonNull(b) {
		var opts map[string]json.RawMessage
		if json.Unmarshal(b, &opts) != nil || opts == nil {
			return p, fmt.Errorf("invalid stream_options")
		}
		for k, v := range opts {
			var enabled bool
			if !hasWord("include_usage include_obfuscation", k) || string(v) == "null" || json.Unmarshal(v, &enabled) != nil {
				return p, fmt.Errorf("unsupported stream option")
			}
			if k == "include_usage" {
				p.IncludeUsage = enabled
			}
		}
	}
	if b := fields["tools"]; nonNull(b) {
		var tools []map[string]any
		if json.Unmarshal(b, &tools) != nil {
			return p, fmt.Errorf("invalid tools")
		}
		if len(tools) > 0 && !m.Tools {
			return p, fmt.Errorf("model does not support tools")
		}
		for _, tool := range tools {
			if err := validateTool(fmt.Sprint(tool["type"]), p); err != nil {
				return p, err
			}
		}
	}
	if b := fields["functions"]; nonNull(b) {
		var functions []map[string]any
		if json.Unmarshal(b, &functions) != nil || len(functions) > 0 && !m.Tools {
			return p, fmt.Errorf("invalid function definitions or model does not support tools")
		}
	}
	if b := fields["tool_choice"]; nonNull(b) {
		var choice any
		if json.Unmarshal(b, &choice) != nil {
			return p, fmt.Errorf("invalid tool_choice")
		}
		switch c := choice.(type) {
		case string:
			if !hasWord("auto none required", c) {
				return p, fmt.Errorf("unsupported tool_choice")
			}
		case map[string]any:
			if err := validateTool(fmt.Sprint(c["type"]), p); err != nil {
				return p, fmt.Errorf("only client-executed tool choices are supported")
			}
		default:
			return p, fmt.Errorf("invalid tool_choice")
		}
	}
	structured := nonNull(fields["response_format"]) || nonNull(fields["structured_outputs"])
	if b := fields["text"]; nonNull(b) {
		var text map[string]json.RawMessage
		if json.Unmarshal(b, &text) != nil {
			return p, fmt.Errorf("invalid text settings")
		}
		structured = structured || nonNull(text["format"])
	}
	if structured && !m.StructuredOutput {
		return p, fmt.Errorf("model does not support structured output")
	}
	if b := fields["include"]; nonNull(b) {
		var includes []string
		if json.Unmarshal(b, &includes) != nil {
			return p, fmt.Errorf("invalid include")
		}
		for _, s := range includes {
			if !hasWord("reasoning.encrypted_content message.output_text.logprobs web_search_call.action.sources file_search_call.results", s) {
				return p, fmt.Errorf("unsupported include")
			}
		}
	}
	// extra_body is used by compatible SDKs. Restrict it to the same non-routing
	// LLM controls as channel overrides; it must never smuggle paid modalities.
	if b := fields["extra_body"]; nonNull(b) {
		var extra map[string]json.RawMessage
		if json.Unmarshal(b, &extra) != nil || extra == nil {
			return p, fmt.Errorf("invalid extra_body")
		}
		for k, v := range extra {
			if k == "google" {
				if err := validateGoogleThinking(v); err != nil {
					return p, err
				}
				continue
			}
			if !hasWord(overrideFields, k) {
				return p, fmt.Errorf("unsupported extra_body field: %s", k)
			}
		}
	}
	for _, v := range []string{m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice, m.CacheWrite1hPrice} {
		n, e := decimal.NewFromString(v)
		if e != nil || n.IsNegative() {
			return p, fmt.Errorf("invalid model pricing")
		}
	}
	if e := m.Pricing.Validate(); e != nil {
		return p, e
	}
	return p, nil
}

// Only protocol content parts are inspected. JSON schemas, function arguments
// and tool-result strings may legitimately contain keys named type or image_url.
func textContent(v any) bool {
	switch value := v.(type) {
	case string:
		return true
	case []any:
		for _, part := range value {
			obj, ok := part.(map[string]any)
			if !ok || !hasWord("text input_text output_text refusal", fmt.Sprint(obj["type"])) {
				return false
			}
			if obj["type"] == "refusal" {
				if _, ok := obj["refusal"].(string); !ok {
					return false
				}
			} else if _, ok := obj["text"].(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}
func validateResponsesInput(v any) error {
	if text, ok := v.(string); ok {
		if text != "" {
			return nil
		}
		return fmt.Errorf("input cannot be empty")
	}
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return fmt.Errorf("input must be text or a non-empty item array")
	}
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid input item")
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "", "message":
			if !hasWord("user assistant system developer", fmt.Sprint(item["role"])) || !textContent(item["content"]) {
				return fmt.Errorf("only text input messages are supported")
			}
		case "function_call":
			if _, ok := item["name"].(string); !ok {
				return fmt.Errorf("function name required")
			}
			if _, ok := item["arguments"].(string); !ok {
				return fmt.Errorf("function arguments must be a JSON string")
			}
		case "custom_tool_call":
			if _, ok := item["input"].(string); !ok {
				return fmt.Errorf("custom tool input must be text")
			}
		case "function_call_output", "custom_tool_call_output":
			if !textContent(item["output"]) {
				return fmt.Errorf("function output must be text")
			}
		case "reasoning", "compaction": // Encrypted, stateless continuation is opaque.
		default:
			return fmt.Errorf("unsupported input item: %s", typ)
		}
	}
	return nil
}

func validateGoogleThinking(raw json.RawMessage) error {
	var google map[string]json.RawMessage
	if json.Unmarshal(raw, &google) != nil || google == nil {
		return fmt.Errorf("invalid extra_body.google")
	}
	for k, v := range google {
		if k != "thinking_config" {
			return fmt.Errorf("only google.thinking_config is supported")
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(v, &config) != nil || config == nil {
			return fmt.Errorf("invalid google.thinking_config")
		}
		for key := range config {
			if !hasWord("thinking_budget thinking_level include_thoughts", key) {
				return fmt.Errorf("unsupported Google thinking option")
			}
		}
	}
	return nil
}
