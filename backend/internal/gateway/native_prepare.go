package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"tokenswoop/backend/internal/store"
)

func nativePrepared(fields map[string]json.RawMessage, req dto.Request, thinking, temperature, topP bool, meta *requestMeta, ch store.Channel, p Plan) ([]byte, *requestMeta, error) {
	converted := rawFields(req)
	if p.Format == types.RelayFormatClaude {
		applyOutputLimit(fields, p.Format, p.MaxOutput)
		for _, k := range []string{"thinking", "output_config", "temperature", "top_p", "top_k"} {
			if thinking || k == "temperature" && temperature || k == "top_p" && topP {
				if nonNull(converted[k]) {
					fields[k] = converted[k]
				} else {
					delete(fields, k)
				}
			}
		}
	} else {
		delete(fields, "model")
		delete(fields, "stream")
		config := map[string]json.RawMessage{}
		json.Unmarshal(fields["generationConfig"], &config)
		if config == nil {
			config = map[string]json.RawMessage{}
		}
		current := map[string]json.RawMessage{}
		json.Unmarshal(converted["generationConfig"], &current)
		delete(config, "maxOutputTokens")
		if p.MaxOutput > 0 {
			put(config, "maxOutputTokens", p.MaxOutput)
		}
		for _, k := range []string{"thinkingConfig", "temperature", "topP"} {
			if k == "thinkingConfig" && thinking || k == "temperature" && temperature || k == "topP" && topP {
				if current[k] != nil {
					config[k] = current[k]
				} else {
					delete(config, k)
				}
			}
		}
		put(fields, "generationConfig", config)
		if v := fields["system_instruction"]; v != nil {
			fields["systemInstruction"] = v
			delete(fields, "system_instruction")
		}
	}
	applyDeepSeekAlias(fields, meta, ch, p.MaxOutput)
	b, e := json.Marshal(fields)
	return b, meta, e
}

// Opaque provider context and settings without an equivalent are never silently
// discarded during conversion. Ordinary text/function/thinking conversions use RelayKit.
func nativeCrossCheck(f map[string]json.RawMessage, from, to types.RelayFormat) error {
	if from == types.RelayFormatClaude {
		if to == types.RelayFormatOpenAIResponses && nonNull(f["stop_sequences"]) {
			return fmt.Errorf("stop_sequences has no Responses mapping")
		}
		for _, k := range []string{"cache_control", "inference_geo", "output_format", "output_config", "metadata", "service_tier", "context_management"} {
			if nonNull(f[k]) {
				return fmt.Errorf("%s requires a Claude channel", k)
			}
		}
		var content any
		json.Unmarshal(f["messages"], &content)
		if containsOpaque(content) {
			return fmt.Errorf("signed or cached Claude history requires a Claude channel")
		}
		json.Unmarshal(f["system"], &content)
		if containsOpaque(content) {
			return fmt.Errorf("cached system requires a Claude channel")
		}
	}
	if from == types.RelayFormatGemini {
		for _, k := range []string{"safetySettings", "cachedContent"} {
			if nonNull(f[k]) {
				return fmt.Errorf("%s requires a Gemini channel", k)
			}
		}
		var config map[string]json.RawMessage
		json.Unmarshal(f["generationConfig"], &config)
		for _, k := range []string{"responseLogprobs", "logprobs", "seed", "presencePenalty", "frequencyPenalty", "responseSchema", "responseJsonSchema", "responseMimeType"} {
			if nonNull(config[k]) {
				return fmt.Errorf("%s has no lossless cross-protocol mapping", k)
			}
		}
		if to == types.RelayFormatOpenAIResponses && (nonNull(config["stopSequences"]) || nonNull(config["topK"])) {
			return fmt.Errorf("Gemini stopSequences/topK has no Responses mapping")
		}
		var stops []string
		json.Unmarshal(config["stopSequences"], &stops)
		if len(stops) > 4 {
			return fmt.Errorf("cross-protocol conversion supports at most four stops")
		}
		var content any
		json.Unmarshal(f["contents"], &content)
		if containsOpaque(content) {
			return fmt.Errorf("signed Gemini history requires a Gemini channel")
		}
	}
	return nil
}
func containsOpaque(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		// Do not inspect opaque client tool arguments or results as protocol parts.
		for k, value := range x {
			if k == "input" || k == "args" || k == "response" {
				continue
			}
			if k == "signature" || k == "thoughtSignature" || k == "cache_control" || k == "type" && value == "redacted_thinking" {
				return true
			}
			if containsOpaque(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if containsOpaque(value) {
				return true
			}
		}
	}
	return false
}
