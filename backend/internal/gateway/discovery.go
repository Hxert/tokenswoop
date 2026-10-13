package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"tokenswoop/backend/internal/store"
)

type DiscoveredModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Vendor    string `json:"vendor"`
	MaxInput  int    `json:"max_input_tokens,omitempty"`
	MaxOutput int    `json:"max_output_tokens,omitempty"`
}

type Discovery struct {
	Models  []DiscoveredModel `json:"data"`
	Skipped int               `json:"skipped"`
}

// DiscoveryError never contains provider bodies, URLs or credentials.
type DiscoveryError struct{ Code, Message string }

func (e *DiscoveryError) Error() string { return e.Message }

func discoveryError(code, message string) error { return &DiscoveryError{code, message} }

var discoveryModelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,127}$`)

// DiscoverModels performs metadata GETs only, through the relay's DNS/IP policy.
// Pagination stays on the configured endpoint; provider-supplied URLs are never followed.
func (g *Gateway) DiscoverModels(ctx context.Context, protocol, baseURL, secret string, options ...store.ChannelOptions) (Discovery, error) {
	result := Discovery{Models: []DiscoveredModel{}}
	o := store.ChannelOptions{}
	if len(options) > 0 {
		o = options[0]
	}
	if ValidateChannelOptions(o) != nil {
		return result, discoveryError("invalid_channel_options", "Invalid channel compatibility settings.")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return result, discoveryError("invalid_channel", "Invalid API base URL.")
	}
	if !slices.Contains([]string{"openai", "openai_responses", "claude", "gemini"}, protocol) {
		return result, discoveryError("invalid_protocol", "Unsupported upstream protocol.")
	}
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	totalBytes := 0
	for page := 0; page < 20; page++ {
		query := url.Values{}
		switch protocol {
		case "gemini":
			query.Set("pageSize", "1000")
			if cursor != "" {
				query.Set("pageToken", cursor)
			}
		case "claude":
			query.Set("limit", "1000")
			if cursor != "" {
				query.Set("after_id", cursor)
			}
		default:
			if cursor != "" {
				query.Set("after", cursor)
			}
		}
		endpoint.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return result, discoveryError("invalid_channel", "Invalid API base URL.")
		}
		req.Header.Set("Accept", "application/json")
		switch protocol {
		case "claude":
			req.Header.Set("x-api-key", secret)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "gemini":
			req.Header.Set("x-goog-api-key", secret)
		default:
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		for key, value := range o.Headers {
			req.Header.Set(key, value)
		}
		response, err := g.clientFor(store.Channel{Options: o}, "model-discovery").Do(req)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return result, discoveryError("upstream_timeout", "Model discovery timed out. Try again or add model IDs manually.")
			}
			return result, discoveryError("upstream_unreachable", "Could not reach the upstream. Check its URL, TLS and network access.")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			switch response.StatusCode {
			case 401, 403:
				return result, discoveryError("upstream_auth_failed", "The upstream rejected these credentials. Check the API key and its model-list permissions.")
			case 404, 405:
				return result, discoveryError("model_list_unavailable", "This endpoint does not expose a model list. Check the API base path or add model IDs manually.")
			case 429:
				return result, discoveryError("upstream_rate_limited", "The upstream rate limited model discovery. Try again later.")
			default:
				return result, discoveryError("upstream_rejected", "The upstream did not return a model list. Redirects are not followed; check the final API URL.")
			}
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		totalBytes += len(body)
		if len(body) > 2<<20 || totalBytes > 8<<20 {
			return result, discoveryError("model_list_too_large", "The upstream model list exceeds discovery limits. Add model IDs manually.")
		}
		if err != nil {
			return result, discoveryError("upstream_read_failed", "Could not read the upstream model list. Try again.")
		}
		var envelope struct {
			Data     json.RawMessage `json:"data"`
			Models   json.RawMessage `json:"models"`
			HasMore  bool            `json:"has_more"`
			LastID   string          `json:"last_id"`
			NextPage string          `json:"nextPageToken"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return result, discoveryError("invalid_model_list", "The upstream returned an invalid model list. Check the selected protocol.")
		}
		var items []struct {
			ID                string   `json:"id"`
			Name              string   `json:"name"`
			DisplayName       string   `json:"display_name"`
			GeminiDisplayName string   `json:"displayName"`
			OwnedBy           string   `json:"owned_by"`
			MaxInput          int      `json:"max_input_tokens"`
			MaxOutput         int      `json:"max_tokens"`
			InputLimit        int      `json:"inputTokenLimit"`
			OutputLimit       int      `json:"outputTokenLimit"`
			Methods           []string `json:"supportedGenerationMethods"`
		}
		raw := envelope.Data
		if protocol == "gemini" {
			raw = envelope.Models
		}
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return result, discoveryError("invalid_model_list", "The upstream returned an invalid model list. Check the selected protocol.")
		}
		for _, item := range items {
			m := DiscoveredModel{ID: item.ID, Name: item.DisplayName, Vendor: item.OwnedBy, MaxInput: item.MaxInput, MaxOutput: item.MaxOutput}
			if protocol == "claude" {
				m.Vendor = "Anthropic"
			}
			if protocol == "gemini" {
				if len(item.Methods) > 0 && !slices.Contains(item.Methods, "generateContent") {
					result.Skipped++
					continue
				}
				m = DiscoveredModel{ID: strings.TrimPrefix(item.Name, "models/"), Name: item.GeminiDisplayName, Vendor: "Google", MaxInput: item.InputLimit, MaxOutput: item.OutputLimit}
			}
			if !discoveryModelID.MatchString(m.ID) {
				result.Skipped++
				continue
			}
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			if m.Name == "" || len(m.Name) > 100 {
				m.Name = m.ID[:min(len(m.ID), 100)]
			}
			if len(m.Vendor) > 80 {
				m.Vendor = ""
			}
			m.MaxInput = max(0, m.MaxInput)
			m.MaxOutput = max(0, m.MaxOutput)
			result.Models = append(result.Models, m)
			if len(result.Models) > 2000 {
				return result, discoveryError("model_list_too_large", "More than 2000 models were returned. Add model IDs manually.")
			}
		}
		next := ""
		if protocol == "gemini" {
			next = envelope.NextPage
		} else if envelope.HasMore {
			next = envelope.LastID
			if next == "" {
				return result, discoveryError("invalid_pagination", "The upstream returned an incomplete pagination cursor.")
			}
		}
		if next == "" {
			sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].ID < result.Models[j].ID })
			return result, nil
		}
		if len(next) > 4096 || cursors[next] {
			return result, discoveryError("invalid_pagination", "The upstream returned an invalid or repeated pagination cursor.")
		}
		cursors[next] = true
		cursor = next
	}
	return result, discoveryError("model_list_too_large", "The upstream model list exceeds 20 pages. Add model IDs manually.")
}
