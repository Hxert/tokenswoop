package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/types"
	"slices"
	"strings"
)

func validateFileStores(fields map[string]json.RawMessage, approved []string) error {
	var tools []struct {
		Type   string   `json:"type"`
		Stores []string `json:"vector_store_ids"`
	}
	if json.Unmarshal(fields["tools"], &tools) != nil {
		return nil
	}
	for _, tool := range tools {
		if tool.Type != "file_search" {
			continue
		}
		if len(tool.Stores) == 0 || len(tool.Stores) > 32 {
			return fmt.Errorf("file_search requires approved vector store IDs")
		}
		for _, id := range tool.Stores {
			if !slices.Contains(approved, id) {
				return fmt.Errorf("vector store is not approved for this channel")
			}
		}
	}
	return nil
}

func toolName(typ string) string {
	switch {
	case typ == "web_search" || typ == "web_search_preview" || strings.HasPrefix(typ, "web_search_"):
		return "web_search"
	case typ == "file_search":
		return "file_search"
	}
	return ""
}

func canonicalGeminiFields(f map[string]json.RawMessage) error {
	var config map[string]json.RawMessage
	if json.Unmarshal(f["generationConfig"], &config) != nil {
		return nil
	}
	for snake, camel := range map[string]string{"top_p": "topP", "top_k": "topK", "max_output_tokens": "maxOutputTokens", "candidate_count": "candidateCount", "stop_sequences": "stopSequences", "response_mime_type": "responseMimeType", "response_schema": "responseSchema", "response_json_schema": "responseJsonSchema", "thinking_config": "thinkingConfig", "presence_penalty": "presencePenalty", "frequency_penalty": "frequencyPenalty", "response_logprobs": "responseLogprobs"} {
		if v, ok := config[snake]; ok {
			if config[camel] != nil {
				return fmt.Errorf("ambiguous Gemini generation control: %s", snake)
			}
			config[camel] = v
			delete(config, snake)
		}
	}
	if config != nil {
		put(f, "generationConfig", config)
	}
	return nil
}
func validateTool(typ string, p Plan) error {
	if hasWord("function custom", typ) {
		return nil
	}
	name := toolName(typ)
	if name == "" || p.Format == types.RelayFormatOpenAI || p.Format == types.RelayFormatGemini {
		return fmt.Errorf("unsupported hosted tool: %s", typ)
	}
	if _, ok := p.Model.Pricing.ToolPrices[name]; !ok {
		return fmt.Errorf("hosted tool %s needs an explicit model price", name)
	}
	return nil
}

func buildNativePlan(p Plan, f map[string]json.RawMessage) (Plan, error) {
	allowed := "model stream"
	if p.Format == types.RelayFormatClaude {
		allowed += " messages system max_tokens stop_sequences temperature top_p top_k tools tool_choice thinking output_config output_format metadata cache_control context_management service_tier inference_geo"
	} else {
		allowed += " contents systemInstruction system_instruction safetySettings generationConfig tools toolConfig cachedContent"
	}
	for k := range f {
		if !hasWord(allowed, k) {
			return p, fmt.Errorf("unsupported native LLM field: %s", k)
		}
	}
	if b := f["stream"]; b != nil {
		if json.Unmarshal(b, &p.Stream) != nil || string(b) == "null" {
			return p, fmt.Errorf("stream must be boolean")
		}
	}
	if p.Format == types.RelayFormatClaude {
		if b := f["max_tokens"]; nonNull(b) {
			if json.Unmarshal(b, &p.MaxOutput) != nil || p.MaxOutput < 1 {
				return p, fmt.Errorf("invalid max_tokens")
			}
		}
		var messages []map[string]any
		if json.Unmarshal(f["messages"], &messages) != nil || len(messages) == 0 {
			return p, fmt.Errorf("messages are required")
		}
		for _, msg := range messages {
			if !hasWord("user assistant", fmt.Sprint(msg["role"])) || !claudeContent(msg["content"], false) {
				return p, fmt.Errorf("only text, thinking and client tool history are supported")
			}
		}
		if b := f["system"]; nonNull(b) {
			var v any
			json.Unmarshal(b, &v)
			if !claudeContent(v, true) {
				return p, fmt.Errorf("system must contain text")
			}
		}
		var tools []map[string]any
		if b := f["tools"]; nonNull(b) {
			if json.Unmarshal(b, &tools) != nil || len(tools) > 0 && !p.Model.Tools {
				return p, fmt.Errorf("invalid or unsupported tools")
			}
		}
		for _, tool := range tools {
			typ, _ := tool["type"].(string)
			if typ != "" && typ != "custom" {
				if err := validateTool(typ, p); err != nil {
					return p, err
				}
			}
		}
		if b := f["tool_choice"]; nonNull(b) {
			var choice struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &choice) != nil || !hasWord("auto any tool none", choice.Type) {
				return p, fmt.Errorf("unsupported tool choice")
			}
		}
		if (nonNull(f["output_format"]) || nonNull(f["output_config"]) && strings.Contains(string(f["output_config"]), `"format"`)) && !p.Model.StructuredOutput {
			return p, fmt.Errorf("model does not support structured output")
		}
	} else {
		if nonNull(f["cachedContent"]) {
			return p, fmt.Errorf("externally stored Gemini caches are not account-scoped; use inline history")
		}
		var contents []map[string]any
		if json.Unmarshal(f["contents"], &contents) != nil || len(contents) == 0 {
			return p, fmt.Errorf("contents are required")
		}
		for _, content := range contents {
			role, _ := content["role"].(string)
			if !hasWord("user model function", role) && role != "" || !geminiParts(content["parts"], false) {
				return p, fmt.Errorf("only text and client tool history are supported")
			}
		}
		for _, key := range []string{"systemInstruction", "system_instruction"} {
			if b := f[key]; nonNull(b) {
				var v map[string]any
				if json.Unmarshal(b, &v) != nil || !geminiParts(v["parts"], true) {
					return p, fmt.Errorf("systemInstruction must contain text")
				}
			}
		}
		if f["systemInstruction"] != nil && f["system_instruction"] != nil {
			return p, fmt.Errorf("choose one system instruction spelling")
		}
		var config map[string]json.RawMessage
		if b := f["generationConfig"]; nonNull(b) {
			if json.Unmarshal(b, &config) != nil {
				return p, fmt.Errorf("invalid generationConfig")
			}
		}
		for k, b := range config {
			if !hasWord("temperature topP topK maxOutputTokens candidateCount stopSequences responseMimeType responseSchema responseJsonSchema thinkingConfig presencePenalty frequencyPenalty seed responseLogprobs logprobs", k) {
				return p, fmt.Errorf("unsupported generation control: %s", k)
			}
			if k == "candidateCount" && string(b) != "1" {
				return p, fmt.Errorf("only one candidate is supported")
			}
			if k == "maxOutputTokens" && nonNull(b) && (json.Unmarshal(b, &p.MaxOutput) != nil || p.MaxOutput < 1) {
				return p, fmt.Errorf("invalid maxOutputTokens")
			}
		}
		if (nonNull(config["responseSchema"]) || nonNull(config["responseJsonSchema"]) || string(config["responseMimeType"]) == `"application/json"`) && !p.Model.StructuredOutput {
			return p, fmt.Errorf("model does not support structured output")
		}
		var tools []map[string]json.RawMessage
		if b := f["tools"]; nonNull(b) {
			if json.Unmarshal(b, &tools) != nil || len(tools) > 0 && !p.Model.Tools {
				return p, fmt.Errorf("invalid or unsupported tools")
			}
		}
		// Google grounding counters differ by model/billing generation. Do not infer
		// a billable invocation from search-query text or grounding chunks.
		for _, tool := range tools {
			for k := range tool {
				if k != "functionDeclarations" {
					return p, fmt.Errorf("Gemini supports client functionDeclarations only")
				}
			}
		}
	}
	if err := p.Model.Pricing.Validate(); err != nil {
		return p, err
	}
	return p, nil
}

func claudeContent(value any, system bool) bool {
	if _, ok := value.(string); ok {
		return true
	}
	parts, ok := value.([]any)
	if !ok {
		return false
	}
	for _, part := range parts {
		p, ok := part.(map[string]any)
		if !ok {
			return false
		}
		typ, _ := p["type"].(string)
		if system && typ != "text" {
			return false
		}
		switch typ {
		case "text":
			if _, ok := p["text"].(string); !ok {
				return false
			}
		case "thinking":
			if _, ok := p["thinking"].(string); !ok {
				return false
			}
		case "redacted_thinking":
			if _, ok := p["data"].(string); !ok {
				return false
			}
		case "tool_use":
			if _, ok := p["name"].(string); !ok {
				return false
			}
		case "tool_result":
			if !claudeContent(p["content"], true) {
				return false
			}
		case "server_tool_use":
			if p["name"] != "web_search" {
				return false
			}
		case "web_search_tool_result": // Provider search results remain opaque text metadata.
		default:
			return false
		}
	}
	return true
}
func geminiParts(value any, system bool) bool {
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		p, ok := part.(map[string]any)
		if !ok {
			return false
		}
		for key := range p {
			if !hasWord("text thought thoughtSignature functionCall functionResponse", key) {
				return false
			}
		}
		if text, ok := p["text"].(string); ok {
			_ = text
			continue
		}
		if system {
			return false
		}
		if _, ok := p["functionCall"].(map[string]any); ok {
			continue
		}
		if _, ok := p["functionResponse"].(map[string]any); ok {
			continue
		}
		return false
	}
	return true
}
