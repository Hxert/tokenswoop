package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"tokenswoop/backend/internal/store"
)

type requestMeta struct {
	convmeta.Values
	Codes []string
}

func (m *requestMeta) note(code string) {
	if len(m.Codes) < 64 && !slices.Contains(m.Codes, code) {
		m.Codes = append(m.Codes, code)
	}
}
func (m *requestMeta) RecordConversionDiagnostics(_ context.Context, diagnostics []types.ConversionDiagnostic) {
	// Never persist diagnostic prose: it can contain model names or request values.
	for _, d := range diagnostics {
		m.note(d.Code)
	}
}
func put(fields map[string]json.RawMessage, key string, value any) {
	fields[key], _ = json.Marshal(value)
}
func rawFields(value any) map[string]json.RawMessage {
	b, _ := json.Marshal(value)
	var out map[string]json.RawMessage
	json.Unmarshal(b, &out)
	return out
}

func (p Plan) request(ch store.Channel) ([]byte, *requestMeta, error) {
	return p.requestContext(context.Background(), ch)
}
func (p Plan) requestContext(ctx context.Context, ch store.Channel) ([]byte, *requestMeta, error) {
	meta := &requestMeta{Values: convmeta.Values{OriginModelName: p.Model.ID, UpstreamModelName: ch.Models[p.Model.ID], ChannelMetaAttached: true, IsStream: p.Stream}, Codes: []string{}}
	if err := ValidateChannelOptions(ch.Options); err != nil {
		return nil, meta, err
	}
	if meta.UpstreamModelName == "" {
		return nil, meta, fmt.Errorf("missing model mapping")
	}
	target := types.RelayFormat(ch.Protocol)
	if target == types.RelayFormatClaude && p.MaxOutput == 0 {
		return nil, meta, fmt.Errorf("Claude requires an explicit output token parameter from the caller")
	}
	if (p.Stored || p.PreviousResponseID != "") && (target != types.RelayFormatOpenAIResponses || !ch.Options.AllowStoredResponses) {
		return nil, meta, fmt.Errorf("stored context requires an opted-in Responses channel")
	}
	if p.Operation == "compact" && (target != types.RelayFormatOpenAIResponses || !ch.Options.SupportsCompact) {
		return nil, meta, fmt.Errorf("channel does not support Responses compact")
	}
	if p.Operation == "alpha_search" {
		return p.alphaRequest(ch, meta)
	}
	if !slices.Contains([]types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini}, target) {
		return nil, meta, fmt.Errorf("unsupported channel protocol")
	}
	ratioClaude, ratioGemini := 0.8, 0.6
	if ch.Options.ThinkingBudgetRatio != 0 {
		ratioClaude, ratioGemini = ch.Options.ThinkingBudgetRatio, ch.Options.ThinkingBudgetRatio
	}
	meta.Options = &convmeta.Options{
		ToolLossPolicy:         types.ConversionLossPolicyStrict,
		OpenRouterDialect:      ch.Options.Dialect == "openrouter",
		PreserveThinkingSuffix: func(string) bool { return ch.Options.PreserveModelSuffix },
		PreserveEffortTail:     func(name string) bool { return ch.Options.PreserveModelSuffix || name == "gpt-5.1-codex-max" },
		Claude:                 convmeta.ClaudeOptions{ThinkingAdapterEnabled: true, ThinkingAdapterBudgetTokensPercentage: ratioClaude},
		Gemini:                 convmeta.GeminiOptions{ThinkingAdapterEnabled: true, ThinkingAdapterBudgetTokensPercentage: ratioGemini, FunctionCallThoughtSignatureEnabled: ch.Options.GeminiThoughtSignature},
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(p.Raw, &fields); err != nil {
		return nil, meta, err
	}
	if err := validateFileStores(fields, ch.Options.FileSearchStores); err != nil {
		return nil, meta, err
	}
	// extra_body contains only validated LLM parameters. Flatten once so all
	// adapters see the same controls, just as the compatible SDKs do on the wire.
	if b := fields["extra_body"]; nonNull(b) {
		var extra map[string]json.RawMessage
		json.Unmarshal(b, &extra)
		for k, v := range extra {
			if k == "google" {
				continue
			}
			if fields[k] == nil {
				fields[k] = v
			}
		}
		if google := extra["google"]; google != nil {
			put(fields, "extra_body", map[string]json.RawMessage{"google": google})
		} else {
			delete(fields, "extra_body")
		}
	}
	for k, v := range ch.Options.ParameterOverrides {
		fields[k] = v
		meta.note("channel_parameter_override")
	}
	for _, k := range ch.Options.DropParameters {
		delete(fields, k)
		meta.note("channel_parameter_removed")
	}
	if nonNull(fields["service_tier"]) && !ch.Options.AllowServiceTier {
		delete(fields, "service_tier")
		meta.note("service_tier_filtered")
	}
	prepared, _ := json.Marshal(fields)
	if _, err := BuildPlan(prepared, p.Model, p.Tier, p.Format); err != nil {
		return nil, meta, err
	}
	if target != p.Format {
		if err := checkCrossProtocol(fields, p.Format, target); err != nil {
			meta.note("unsupported_cross_protocol_field")
			return nil, meta, err
		}
	}
	var req dto.Request
	switch p.Format {
	case types.RelayFormatOpenAI:
		req = &dto.GeneralOpenAIRequest{}
	case types.RelayFormatOpenAIResponses:
		req = &dto.OpenAIResponsesRequest{}
	case types.RelayFormatClaude:
		req = &dto.ClaudeRequest{}
	case types.RelayFormatGemini:
		req = &dto.GeminiChatRequest{}
	}
	if err := json.Unmarshal(prepared, req); err != nil {
		return nil, meta, fmt.Errorf("invalid LLM parameter types: %w", err)
	}
	origin, err := parseRequestModelName(meta.OriginModelName, meta.Options)
	if err != nil {
		return nil, meta, err
	}
	mapped, err := parseRequestModelName(meta.UpstreamModelName, meta.Options)
	if err != nil {
		return nil, meta, err
	}
	diagnostics := append(origin.diagnostics, mapped.diagnostics...)
	selected, diagnostics := overlayMappedModelModifiers(origin, mapped, diagnostics)
	meta.RecordConversionDiagnostics(ctx, diagnostics)
	meta.UpstreamModelName = selected.base
	req.SetModelName(selected.base)
	if selected.hasThinking {
		explicit, diags, e := explicitIntentFromRequest(req)
		if e != nil {
			return nil, meta, e
		}
		meta.RecordConversionDiagnostics(ctx, diags)
		meta.RecordConversionDiagnostics(ctx, modifierRequestOverrideDiagnostics(explicit, selected.intent))
		if selected.intent.IncludeThoughts == nil {
			selected.intent.IncludeThoughts = explicit.IncludeThoughts
		}
		meta.ReasoningConversion = reasoning.StateFromIntent(selected.intent)
		meta.note("model_reasoning_control")
	}
	if err = applyModelControls(req, selected); err != nil {
		return nil, meta, err
	}
	var maxTokens *uint
	if p.MaxOutput > 0 {
		v := uint(p.MaxOutput)
		maxTokens = &v
	}
	switch r := req.(type) {
	case *dto.GeneralOpenAIRequest:
		r.Store = json.RawMessage("false")
		if r.MaxCompletionTokens != nil {
			r.MaxCompletionTokens = maxTokens
		} else {
			r.MaxTokens = maxTokens
		}
		if p.Stream {
			r.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
		}
	case *dto.OpenAIResponsesRequest:
		if selected.hasThinking {
			prior := r.Reasoning
			if e := reasoning.ApplyToOpenAIResponses(r, selected.intent); e != nil {
				return nil, meta, e
			}
			if prior != nil && r.Reasoning != nil {
				r.Reasoning.Summary = prior.Summary
			}
		}
		r.Store, _ = json.Marshal(p.Stored)
		r.MaxOutputTokens = maxTokens
	case *dto.ClaudeRequest:
		r.MaxTokens = maxTokens
		if selected.hasThinking {
			render, e := reasoning.RenderClaude(selected.base, selected.intent, maxTokens, ratioClaude)
			if e != nil {
				return nil, meta, e
			}
			if render.MaxTokens != nil && maxTokens != nil && *render.MaxTokens > *maxTokens {
				return nil, meta, fmt.Errorf("thinking exceeds requested output limit")
			}
			r.Thinking = render.Thinking
			config := map[string]json.RawMessage{}
			json.Unmarshal(r.OutputConfig, &config)
			if render.OutputEffort != "" {
				put(config, "effort", render.OutputEffort)
			} else {
				delete(config, "effort")
			}
			r.OutputConfig, _ = json.Marshal(config)
			if render.ClearSampling {
				r.Temperature = nil
				r.TopP = nil
				r.TopK = nil
			} else if render.ConstrainThinkingSampling {
				v := 1.0
				r.Temperature = &v
				r.TopP = nil
				r.TopK = nil
			}
			meta.RecordConversionDiagnostics(ctx, render.Diagnostics)
		}
	case *dto.GeminiChatRequest:
		r.GenerationConfig.MaxOutputTokens = maxTokens
		if selected.hasThinking {
			render, e := reasoning.RenderGemini(selected.base, selected.intent, maxTokens, ratioGemini)
			if e != nil {
				return nil, meta, e
			}
			r.GenerationConfig.ThinkingConfig = render.Config
			meta.RecordConversionDiagnostics(ctx, render.Diagnostics)
		}
	}
	// Identity conversions must not serialize the DTO back over the entire body:
	// annotations, cache controls and provider extensions need lossless forwarding.
	if target == p.Format {
		put(fields, "model", meta.UpstreamModelName)
		if target == types.RelayFormatClaude || target == types.RelayFormatGemini {
			return nativePrepared(fields, req, selected.hasThinking, selected.hasTemperature, selected.hasTopP, meta, ch, p)
		}
		put(fields, "store", p.Stored)
		if selected.hasTemperature {
			put(fields, "temperature", selected.temperature)
		}
		if selected.hasTopP {
			put(fields, "top_p", selected.topP)
		}
		if selected.hasThinking {
			converted := rawFields(req)
			for _, key := range []string{"reasoning", "reasoning_effort"} {
				if value := converted[key]; value != nil {
					fields[key] = value
				} else {
					delete(fields, key)
				}
			}
		}
		if target == types.RelayFormatOpenAI {
			applyOutputLimit(fields, target, p.MaxOutput)
			if p.Stream {
				var opts map[string]json.RawMessage
				json.Unmarshal(fields["stream_options"], &opts)
				if opts == nil {
					opts = map[string]json.RawMessage{}
				}
				put(opts, "include_usage", true)
				put(fields, "stream_options", opts)
			}
			if err = normalizeOpenAI(fields, meta, ch.Options.Dialect, selected.hasThinking); err != nil {
				return nil, meta, err
			}
		} else {
			applyOutputLimit(fields, target, p.MaxOutput)
			if selected.hasThinking {
				meta.SetReasoningEffort(string(reasoning.EffectiveEffort(selected.intent)))
			}
		}
		if ch.Options.OmitStreamOptions {
			delete(fields, "stream_options")
			meta.note("stream_options_omitted")
		}
		applyDeepSeekAlias(fields, meta, ch, p.MaxOutput)
		if p.Operation == "compact" {
			delete(fields, "max_output_tokens")
			delete(fields, "stream")
			delete(fields, "store")
		}
		b, e := json.Marshal(fields)
		return b, meta, e
	}
	converted, err := relayconvert.ConvertRequest(ctx, meta, target, req)
	if err != nil {
		return nil, meta, err
	}
	meta.RecordConversionDiagnostics(ctx, converted.Diagnostics)
	output := rawFields(converted.Value)
	completePortableFields(fields, output, target)
	if target == types.RelayFormatOpenAI {
		put(output, "store", false)
		if p.Stream {
			put(output, "stream_options", map[string]bool{"include_usage": true})
		}
		if err = normalizeOpenAI(output, meta, ch.Options.Dialect, true); err != nil {
			return nil, meta, err
		}
	}
	if target == types.RelayFormatOpenAIResponses {
		put(output, "store", false)
	}
	applyDeepSeekAlias(output, meta, ch, p.MaxOutput)
	// RelayKit can increase Claude max_tokens to satisfy a thinking budget. The
	// customer's requested cap is authoritative in this postpaid service.
	if target == types.RelayFormatClaude {
		var limit int
		json.Unmarshal(output["max_tokens"], &limit)
		if limit > p.MaxOutput {
			meta.note("thinking_exceeds_output_limit")
			return nil, meta, fmt.Errorf("thinking requires a larger output token limit")
		}
	}
	applyOutputLimit(output, target, p.MaxOutput)
	if ch.Options.OmitStreamOptions {
		delete(output, "stream_options")
		meta.note("stream_options_omitted")
	}
	b, e := json.Marshal(output)
	return b, meta, e
}

// Only a caller-supplied output parameter is forwarded. Never invent a platform
// ceiling or allow a converter default to turn an omitted parameter into one.
func applyOutputLimit(fields map[string]json.RawMessage, format types.RelayFormat, limit int) {
	key := "max_tokens"
	switch format {
	case types.RelayFormatOpenAI:
		if nonNull(fields["max_completion_tokens"]) {
			key = "max_completion_tokens"
		}
		delete(fields, "max_tokens")
		delete(fields, "max_completion_tokens")
	case types.RelayFormatOpenAIResponses:
		key = "max_output_tokens"
	case types.RelayFormatGemini:
		config := map[string]json.RawMessage{}
		json.Unmarshal(fields["generationConfig"], &config)
		if config == nil {
			config = map[string]json.RawMessage{}
		}
		delete(config, "maxOutputTokens")
		if limit > 0 {
			put(config, "maxOutputTokens", limit)
		}
		if len(config) > 0 || fields["generationConfig"] != nil {
			put(fields, "generationConfig", config)
		}
		return
	}
	delete(fields, key)
	if limit > 0 {
		put(fields, key, limit)
	}
}

// DeepSeek's native Messages and Responses surfaces use different fields for
// the same V4 alias. Match the reference adapter without a Claude budget raise.
func applyDeepSeekAlias(fields map[string]json.RawMessage, meta *requestMeta, ch store.Channel, maxOutput int) {
	if ch.Options.Dialect != "deepseek" || ch.Options.PreserveModelSuffix || ch.Protocol == "gemini" {
		return
	}
	base, mode, effort, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(meta.UpstreamModelName)
	if !ok {
		return
	}
	meta.UpstreamModelName = base
	put(fields, "model", base)
	meta.note("deepseek_reasoning_alias")
	switch ch.Protocol {
	case "openai":
		put(fields, "thinking", map[string]string{"type": mode})
		if effort != "" {
			put(fields, "reasoning_effort", effort)
		} else {
			delete(fields, "reasoning_effort")
		}
	case "openai_responses":
		if mode == "disabled" {
			effort = "none"
		}
		var config map[string]json.RawMessage
		json.Unmarshal(fields["reasoning"], &config)
		if config == nil {
			config = map[string]json.RawMessage{}
		}
		put(config, "effort", effort)
		put(fields, "reasoning", config)
	case "claude":
		put(fields, "max_tokens", maxOutput)
		put(fields, "thinking", map[string]string{"type": mode})
		if effort != "" {
			put(fields, "output_config", map[string]string{"effort": effort})
		} else {
			delete(fields, "output_config")
		}
	}
	meta.SetReasoningEffort(effort)
}

func normalizeOpenAI(fields map[string]json.RawMessage, meta *requestMeta, dialect string, controls bool) error {
	var effort string
	json.Unmarshal(fields["reasoning_effort"], &effort)
	// DeepSeek's legacy V4 aliases are handled by its own New API adapter.
	if dialect == "deepseek" && !meta.Options.ShouldPreserveThinkingSuffix(meta.UpstreamModelName) {
		if base, mode, level, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(meta.UpstreamModelName); ok {
			meta.UpstreamModelName = base
			put(fields, "model", base)
			put(fields, "thinking", map[string]string{"type": mode})
			if level != "" {
				put(fields, "reasoning_effort", level)
			} else {
				delete(fields, "reasoning_effort")
			}
			meta.note("deepseek_reasoning_alias")
			effort = level
		}
	}
	if dialect == "openrouter" || nonNull(fields["reasoning"]) || controls {
		var r dto.GeneralOpenAIRequest
		encoded, _ := json.Marshal(fields)
		if err := json.Unmarshal(encoded, &r); err != nil {
			return err
		}
		r.ReasoningConversion = meta.ReasoningConversion
		intent, diags, err := reasoning.FromOpenAIChat(&r)
		if err != nil {
			return err
		}
		meta.RecordConversionDiagnostics(context.Background(), diags)
		if dialect == "openrouter" {
			if nonNull(fields["thinking"]) && strings.HasPrefix(meta.UpstreamModelName, "anthropic/") {
				var thinking dto.Thinking
				if err = json.Unmarshal(fields["thinking"], &thinking); err != nil {
					return err
				}
				legacy, diagnostics, e := reasoning.FromClaude(&dto.ClaudeRequest{Thinking: &thinking})
				if e != nil {
					return e
				}
				meta.RecordConversionDiagnostics(context.Background(), diagnostics)
				intent, diagnostics, e = reasoning.MergeExplicit(intent, legacy, meta.UpstreamModelName)
				if e != nil {
					return e
				}
				meta.RecordConversionDiagnostics(context.Background(), diagnostics)
				delete(fields, "thinking")
			}
			put(fields, "usage", map[string]bool{"include": true})
			if !intent.IsEmpty() {
				var config map[string]json.RawMessage
				json.Unmarshal(fields["reasoning"], &config)
				if config == nil {
					config = map[string]json.RawMessage{}
				}
				disabled := intent.Mode == reasoning.ModeDisabled || intent.Effort == reasoning.EffortNone
				if intent.HasStrength() {
					put(config, "enabled", !disabled)
				}
				if disabled {
					delete(config, "effort")
					delete(config, "max_tokens")
				} else if intent.BudgetTokens != nil {
					put(config, "max_tokens", *intent.BudgetTokens)
					delete(config, "effort")
				} else if intent.Effort != "" {
					put(config, "effort", intent.Effort)
					delete(config, "max_tokens")
				}
				if intent.IncludeThoughts != nil {
					put(config, "exclude", !*intent.IncludeThoughts)
				}
				put(fields, "reasoning", config)
			}
			delete(fields, "reasoning_effort")
		} else {
			if e := reasoning.EffectiveEffort(intent); e != "" {
				put(fields, "reasoning_effort", e)
			}
			if dialect == "openai" {
				delete(fields, "reasoning")
			}
		}
		effort = string(reasoning.EffectiveEffort(intent))
	}
	meta.SetReasoningEffort(effort)
	name := meta.UpstreamModelName
	// Provider namespaces are opaque to routing but not to known-model rules.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	caps := dto.GetOpenAIChatCapabilities(name, effort)
	if caps.UseMaxCompletionTokens && fields["max_tokens"] != nil {
		fields["max_completion_tokens"] = fields["max_tokens"]
		delete(fields, "max_tokens")
		meta.note("max_completion_tokens_adapted")
	}
	for _, rule := range []struct {
		key       string
		supported bool
	}{{"temperature", caps.SupportsTemperature}, {"top_p", caps.SupportsTopP}, {"logprobs", caps.SupportsLogProbs}, {"top_logprobs", caps.SupportsLogProbs}} {
		if !rule.supported && fields[rule.key] != nil {
			delete(fields, rule.key)
			meta.note("unsupported_sampling_removed")
		}
	}
	if caps.UseDeveloperRole {
		var messages []map[string]json.RawMessage
		if json.Unmarshal(fields["messages"], &messages) == nil && len(messages) > 0 {
			var role string
			json.Unmarshal(messages[0]["role"], &role)
			if role == "system" {
				put(messages[0], "role", "developer")
				put(fields, "messages", messages)
				meta.note("system_role_adapted")
			}
		}
	}
	return nil
}

func checkCrossProtocol(f map[string]json.RawMessage, from, to types.RelayFormat) error {
	if string(f["store"]) == "true" || nonNull(f["previous_response_id"]) {
		return fmt.Errorf("stored context requires the same Responses protocol")
	}
	if err := nativeCrossCheck(f, from, to); err != nil {
		return err
	}
	var tools []map[string]any
	json.Unmarshal(f["tools"], &tools)
	for _, tool := range tools {
		if toolName(fmt.Sprint(tool["type"])) != "" {
			return fmt.Errorf("hosted tools require the matching native protocol")
		}
	}
	// Same-protocol requests preserve every admitted field. Cross-protocol fields
	// without equivalent semantics fail before sending an upstream request.
	blocked := "prediction prefix suffix structured_outputs return_token_ids min_tokens separate_reasoning stream_reasoning regex ebnf stop_token_ids stop_regex no_stop_trim ignore_eos skip_special_tokens continue_final_message prompt_cache_options context_management functions function_call"
	if to != types.RelayFormatGemini && nonNull(f["extra_body"]) {
		return fmt.Errorf("Google thinking config requires a Gemini channel")
	}
	if to == types.RelayFormatClaude {
		blocked += " response_format logprobs top_logprobs logit_bias seed frequency_penalty presence_penalty prompt_cache_key service_tier enable_thinking thinking_budget chat_template_kwargs min_p repetition_penalty cache_salt prompt_cache_retention metadata user safety_identifier verbosity"
		if b := f["text"]; nonNull(b) {
			var text map[string]json.RawMessage
			json.Unmarshal(b, &text)
			if nonNull(text["format"]) {
				return fmt.Errorf("structured output has no Claude mapping in this RelayKit baseline")
			}
		}
	}
	if to == types.RelayFormatOpenAIResponses {
		blocked += " stop seed logit_bias logprobs top_k"
	}
	if to == types.RelayFormatGemini {
		blocked += " logprobs top_logprobs logit_bias prompt_cache_key service_tier enable_thinking thinking_budget chat_template_kwargs min_p repetition_penalty cache_salt prompt_cache_retention metadata user safety_identifier verbosity"
	}
	for _, k := range strings.Fields(blocked) {
		if nonNull(f[k]) {
			return fmt.Errorf("%s has no lossless mapping to %s", k, to)
		}
	}
	if from == types.RelayFormatOpenAIResponses {
		for _, k := range []string{"include", "truncation", "max_tool_calls", "client_metadata"} {
			if nonNull(f[k]) {
				return fmt.Errorf("%s requires a matching Responses channel", k)
			}
		}
		if to != types.RelayFormatOpenAI {
			var text map[string]json.RawMessage
			json.Unmarshal(f["text"], &text)
			if nonNull(text["verbosity"]) {
				return fmt.Errorf("text.verbosity has no mapping to %s", to)
			}
		}
		var items []map[string]json.RawMessage
		json.Unmarshal(f["input"], &items)
		for _, item := range items {
			var typ string
			json.Unmarshal(item["type"], &typ)
			if typ == "reasoning" || typ == "compaction" || typ == "custom_tool_call" || typ == "custom_tool_call_output" {
				return fmt.Errorf("opaque reasoning context requires a matching Responses channel")
			}
		}
	}
	if from == types.RelayFormatOpenAI {
		var messages []map[string]json.RawMessage
		json.Unmarshal(f["messages"], &messages)
		for _, msg := range messages {
			var parts []map[string]json.RawMessage
			json.Unmarshal(msg["content"], &parts)
			for _, part := range parts {
				if nonNull(part["cache_control"]) {
					return fmt.Errorf("cache_control requires a matching Chat channel")
				}
			}
			for _, k := range []string{"prefix", "annotations", "tools", "function_call", "reasoning_content", "reasoning"} {
				if nonNull(msg[k]) {
					return fmt.Errorf("message %s requires a matching OpenAI Chat channel", k)
				}
			}
		}
	}
	return nil
}

// These controls have identical semantics in the destination DTOs but are not
// copied by every converter in the pinned RelayKit release.
func completePortableFields(input, output map[string]json.RawMessage, target types.RelayFormat) {
	if target == types.RelayFormatOpenAI || target == types.RelayFormatOpenAIResponses {
		for _, key := range strings.Fields("service_tier prompt_cache_retention chat_template_kwargs min_p repetition_penalty cache_salt safety_identifier") {
			if nonNull(input[key]) {
				output[key] = input[key]
			}
		}
		if target == types.RelayFormatOpenAIResponses && nonNull(input["verbosity"]) {
			var text map[string]json.RawMessage
			json.Unmarshal(output["text"], &text)
			if text == nil {
				text = map[string]json.RawMessage{}
			}
			text["verbosity"] = input["verbosity"]
			put(output, "text", text)
		}
		if target == types.RelayFormatOpenAI {
			var text map[string]json.RawMessage
			json.Unmarshal(input["text"], &text)
			if nonNull(text["verbosity"]) {
				output["verbosity"] = text["verbosity"]
			}
		}
	}
	if target == types.RelayFormatGemini {
		var config map[string]json.RawMessage
		json.Unmarshal(output["generationConfig"], &config)
		if config == nil {
			config = map[string]json.RawMessage{}
		}
		for source, destination := range map[string]string{"top_k": "topK", "frequency_penalty": "frequencyPenalty", "presence_penalty": "presencePenalty"} {
			if nonNull(input[source]) {
				config[destination] = input[source]
			}
		}
		put(output, "generationConfig", config)
	}
}
