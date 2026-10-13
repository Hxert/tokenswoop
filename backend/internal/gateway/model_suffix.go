// Adapted from New API relay/helper/reasoning_suffix.go, 789c970199ea527e6a26e071915f4a4cd2c64178.
// AGPL-3.0; see UPSTREAM-NOTICE.txt.
package gateway

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"strings"
)

func parseRequestModelName(name string, opts *convmeta.Options) (parsedModelModifiers, error) {
	if opts.ShouldPreserveThinkingSuffix(name) {
		return parsedModelModifiers{base: name}, nil
	}
	parsed, err := parseExplicitModelModifiers(name)
	if err != nil {
		return parsedModelModifiers{}, err
	}
	if opts.ShouldPreserveThinkingSuffix(parsed.base) {
		return parsed, nil
	}
	legacyBase, legacyIntent, legacyFound, err := parseHostModelSuffix(parsed.base, opts)
	if err != nil {
		return parsedModelModifiers{}, err
	}
	parsed.base = legacyBase
	if legacyFound && !parsed.hasThinking {
		parsed.intent = legacyIntent
		parsed.hasThinking = true
	}
	return parsed, nil
}

func overlayMappedModelModifiers(origin parsedModelModifiers, mapped parsedModelModifiers, diagnostics []types.ConversionDiagnostic) (parsedModelModifiers, []types.ConversionDiagnostic) {
	origin.base = mapped.base
	origin.hasSyntax = origin.hasSyntax || mapped.hasSyntax
	origin.diagnostics = nil
	if mapped.hasThinking {
		if origin.hasThinking && !sameModifierIntent(origin.intent, mapped.intent) {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"thinking",
				"mapped-model thinking modifier overrides the origin-model modifier",
			))
		}
		origin.intent = mapped.intent
		origin.hasThinking = true
	}
	if mapped.hasTemperature {
		if origin.hasTemperature && *origin.temperature != *mapped.temperature {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"temperature",
				"mapped-model temperature modifier overrides the origin-model modifier",
			))
		}
		origin.temperature = mapped.temperature
		origin.hasTemperature = true
	}
	if mapped.hasTopP {
		if origin.hasTopP && *origin.topP != *mapped.topP {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"topp",
				"mapped-model topp modifier overrides the origin-model modifier",
			))
		}
		origin.topP = mapped.topP
		origin.hasTopP = true
	}
	return origin, diagnostics
}

func modifierRequestOverrideDiagnostics(explicit reasoning.Intent, modifier reasoning.Intent) []types.ConversionDiagnostic {
	if !explicit.HasStrength() || sameModifierIntent(explicit, modifier) {
		return nil
	}
	return []types.ConversionDiagnostic{modelModifierDiagnostic(
		"model_modifier_overrode_request",
		"thinking",
		"model thinking modifier overrides structured request reasoning fields",
	)}
}

func sameModifierIntent(left reasoning.Intent, right reasoning.Intent) bool {
	if left.Mode != right.Mode || left.Effort != right.Effort {
		return false
	}
	if left.BudgetTokens == nil || right.BudgetTokens == nil {
		return left.BudgetTokens == nil && right.BudgetTokens == nil
	}
	return *left.BudgetTokens == *right.BudgetTokens
}

func parseHostModelSuffix(name string, opts *convmeta.Options) (string, reasoning.Intent, bool, error) {
	if name == "" {
		return name, reasoning.Intent{}, false, nil
	}
	return parseLegacySuffix(name, opts)
}

func explicitIntentFromRequest(req dto.Request) (reasoning.Intent, []types.ConversionDiagnostic, error) {
	switch r := req.(type) {
	case *dto.ClaudeRequest:
		return reasoning.FromClaude(r)
	case *dto.GeminiChatRequest:
		return reasoning.FromGemini(r)
	case *dto.GeneralOpenAIRequest:
		return reasoning.FromOpenAIChat(r)
	case *dto.OpenAIResponsesRequest:
		return reasoning.FromOpenAIResponses(r)
	default:
		return reasoning.Intent{}, nil, nil
	}
}

func parseLegacySuffix(name string, opts *convmeta.Options) (string, reasoning.Intent, bool, error) {
	prefix, bare := "", name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		prefix, bare = name[:i+1], name[i+1:]
	}
	base, intent, found, err := reasoning.ParseKnownProviderModelSuffix(bare, true)
	if err != nil {
		return name, intent, false, err
	}
	if !found {
		effort, parsed := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(bare, opts.PreserveEffortTail)
		if effort != "" {
			base, found = parsed, true
			intent.Effort, err = reasoning.ParseEffort(effort)
			intent.Source = reasoning.SourceSuffix
			intent.Mode = reasoning.ModeEnabled
			if intent.Effort == reasoning.EffortNone {
				intent.Mode = reasoning.ModeDisabled
			}
		}
	}
	if !found {
		return name, reasoning.Intent{}, false, nil
	}
	return prefix + base, intent, found, err
}
