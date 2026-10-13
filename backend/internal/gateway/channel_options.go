package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"tokenswoop/backend/internal/store"
)

const overrideFields = "temperature top_p top_k min_p frequency_penalty presence_penalty seed logprobs top_logprobs logit_bias stop service_tier prompt_cache_key prompt_cache_retention prompt_cache_options reasoning reasoning_effort thinking think enable_thinking thinking_budget chat_template_kwargs reasoning_split thinking_token_budget include_reasoning repetition_penalty min_tokens separate_reasoning stream_reasoning stop_token_ids stop_regex no_stop_trim ignore_eos skip_special_tokens continue_final_message cache_salt verbosity"

func ValidateChannelOptions(o store.ChannelOptions) error {
	if len(o.FileSearchStores) > 32 {
		return fmt.Errorf("at most 32 approved vector stores")
	}
	for _, id := range o.FileSearchStores {
		if !strings.HasPrefix(id, "vs_") || len(id) > 128 || strings.ContainsAny(id, " /?\r\n\t") {
			return fmt.Errorf("invalid vector store ID")
		}
	}
	if o.Dialect != "" && !hasWord("generic openai openrouter deepseek qwen vllm sglang", o.Dialect) {
		return fmt.Errorf("unsupported compatibility dialect")
	}
	if o.HTTPProtocol != "" && !hasWord("auto http1", o.HTTPProtocol) {
		return fmt.Errorf("HTTP protocol must be auto or http1")
	}
	if o.HTTP2Shards < 0 || o.HTTP2Shards > 8 {
		return fmt.Errorf("HTTP/2 shards must be between 1 and 8 (0 uses the default)")
	}
	if o.Weight < 0 || o.Weight > 10000 {
		return fmt.Errorf("weight must be between 1 and 10000 (0 uses the default)")
	}
	if o.RequestTimeoutSeconds != 0 && (o.RequestTimeoutSeconds < 30 || o.RequestTimeoutSeconds > 3600) {
		return fmt.Errorf("request timeout must be 30–3600 seconds")
	}
	if o.StreamIdleSeconds != 0 && (o.StreamIdleSeconds < 15 || o.StreamIdleSeconds > 600) {
		return fmt.Errorf("stream idle timeout must be 15–600 seconds")
	}
	if math.IsNaN(o.ThinkingBudgetRatio) || math.IsInf(o.ThinkingBudgetRatio, 0) || o.ThinkingBudgetRatio != 0 && (o.ThinkingBudgetRatio < 0.1 || o.ThinkingBudgetRatio > 0.95) {
		return fmt.Errorf("thinking budget ratio must be 0.1–0.95 (0 uses the provider default)")
	}
	if len(o.Headers) > 8 {
		return fmt.Errorf("too many upstream headers")
	}
	seen := map[string]bool{}
	for k, v := range o.Headers {
		canonical := http.CanonicalHeaderKey(k)
		if !hasWord("Anthropic-Version Anthropic-Beta Openai-Organization Openai-Project Http-Referer X-Title", canonical) || seen[canonical] || len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("unsupported or invalid upstream header")
		}
		seen[canonical] = true
	}
	if len(o.ParameterOverrides) > 32 || len(o.DropParameters) > 32 {
		return fmt.Errorf("too many parameter rules")
	}
	for k, v := range o.ParameterOverrides {
		if !hasWord(overrideFields, k) || !json.Valid(v) || len(v) > 8192 {
			return fmt.Errorf("unsupported parameter override: %s", k)
		}
	}
	for _, k := range o.DropParameters {
		if !hasWord(overrideFields, k) {
			return fmt.Errorf("unsupported parameter removal: %s", k)
		}
	}
	return nil
}
