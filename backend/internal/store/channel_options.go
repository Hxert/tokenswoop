package store

import "encoding/json"

// ChannelOptions contains only non-secret transport and LLM compatibility settings.
// Zero values retain the defaults used by existing channels.
type ChannelOptions struct {
	FileSearchStores       []string                   `json:"file_search_stores,omitempty"`
	AllowStoredResponses   bool                       `json:"allow_stored_responses,omitempty"`
	SupportsCompact        bool                       `json:"supports_compact,omitempty"`
	SupportsAlphaSearch    bool                       `json:"supports_alpha_search,omitempty"`
	AllowServiceTier       bool                       `json:"allow_service_tier,omitempty"`
	OmitStreamOptions      bool                       `json:"omit_stream_options,omitempty"`
	ThinkingToContent      bool                       `json:"thinking_to_content,omitempty"`
	Dialect                string                     `json:"dialect,omitempty"`
	PreserveModelSuffix    bool                       `json:"preserve_model_suffix,omitempty"`
	ThinkingBudgetRatio    float64                    `json:"thinking_budget_ratio,omitempty"`
	GeminiThoughtSignature bool                       `json:"gemini_thought_signature,omitempty"`
	HTTPProtocol           string                     `json:"http_protocol,omitempty"`
	HTTP2Shards            int                        `json:"http2_shards,omitempty"`
	RequestTimeoutSeconds  int                        `json:"request_timeout_seconds,omitempty"`
	StreamIdleSeconds      int                        `json:"stream_idle_seconds,omitempty"`
	Weight                 int                        `json:"weight,omitempty"`
	CacheAffinity          bool                       `json:"cache_affinity,omitempty"`
	Headers                map[string]string          `json:"headers,omitempty"`
	ParameterOverrides     map[string]json.RawMessage `json:"parameter_overrides,omitempty"`
	DropParameters         []string                   `json:"drop_parameters,omitempty"`
}
