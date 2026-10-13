package gateway

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/relaykit/types"
	"strings"
)

func terminalPayload(b []byte, format types.RelayFormat) bool {
	var f struct {
		Type    string `json:"type"`
		Choices []struct {
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Delta struct {
			Stop string `json:"stop_reason"`
		} `json:"delta"`
	}
	json.Unmarshal(b, &f)
	switch format {
	case types.RelayFormatOpenAI:
		for _, choice := range f.Choices {
			if choice.Finish != "" {
				return true
			}
		}
	case types.RelayFormatOpenAIResponses:
		return f.Type == "response.completed" || f.Type == "response.incomplete"
	case types.RelayFormatClaude:
		return f.Type == "message_stop" || f.Type == "message_delta" && f.Delta.Stop != ""
	case types.RelayFormatGemini:
		return geminiTerminal(b)
	}
	return false
}

func geminiTerminal(b []byte) bool {
	var f struct {
		Candidates []struct {
			Finish string `json:"finishReason"`
		} `json:"candidates"`
	}
	json.Unmarshal(b, &f)
	for _, c := range f.Candidates {
		if c.Finish != "" {
			return true
		}
	}
	return false
}
func hasModelContent(f map[string]json.RawMessage, protocol string) bool {
	switch protocol {
	case "openai":
		var choices []struct {
			Delta struct {
				Content    string            `json:"content"`
				Reasoning  string            `json:"reasoning_content"`
				Reasoning2 string            `json:"reasoning"`
				Tools      []json.RawMessage `json:"tool_calls"`
				Function   json.RawMessage   `json:"function_call"`
			} `json:"delta"`
		}
		json.Unmarshal(f["choices"], &choices)
		for _, c := range choices {
			if c.Delta.Content != "" || c.Delta.Reasoning != "" || c.Delta.Reasoning2 != "" || len(c.Delta.Tools) > 0 || nonNull(c.Delta.Function) {
				return true
			}
		}
	case "openai_responses":
		var typ, delta string
		json.Unmarshal(f["type"], &typ)
		json.Unmarshal(f["delta"], &delta)
		return strings.HasSuffix(typ, ".delta") && delta != "" && (strings.Contains(typ, "text") || strings.Contains(typ, "reasoning") || strings.Contains(typ, "arguments"))
	case "claude":
		var delta struct {
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			JSON     string `json:"partial_json"`
		}
		json.Unmarshal(f["delta"], &delta)
		if delta.Text != "" || delta.Thinking != "" || delta.JSON != "" {
			return true
		}
		var block struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		json.Unmarshal(f["content_block"], &block)
		return block.Text != "" || block.Thinking != "" || block.Type == "tool_use" || block.Type == "server_tool_use"
	case "gemini":
		var candidates []struct {
			Content struct {
				Parts []struct {
					Text string          `json:"text"`
					Call json.RawMessage `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		}
		json.Unmarshal(f["candidates"], &candidates)
		for _, c := range candidates {
			for _, part := range c.Content.Parts {
				if part.Text != "" || nonNull(part.Call) {
					return true
				}
			}
		}
	}
	return false
}
