package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strings"

	"tokenswoop/backend/internal/store"
)

func (g *Gateway) clientFor(ch store.Channel, id string) *http.Client {
	shards := max(1, min(ch.Options.HTTP2Shards, 8))
	slot := 0
	if ch.Options.HTTPProtocol == "http1" {
		slot = -1
	} else if shards > 1 {
		sum := sha256.Sum256([]byte(id))
		slot = int(sum[0]) % shards
	}
	if slot == 0 {
		return g.Client
	}
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()
	if client := g.clients[slot]; client != nil {
		return client
	}
	original, ok := g.Client.Transport.(*http.Transport)
	if !ok {
		return g.Client
	}
	transport := original.Clone()
	if slot == -1 {
		transport.ForceAttemptHTTP2 = false
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		transport.Protocols = protocols
	}
	client := *g.Client
	client.Transport = transport
	if g.clients == nil {
		g.clients = map[int]*http.Client{}
	}
	g.clients[slot] = &client
	return &client
}

func (g *Gateway) orderChannels(requestID string, p Plan, channels []store.Channel) []store.Channel {
	result := slices.Clone(channels)
	prefix := affinityPrefix(p)
	// Priority is authoritative. Weighted rendezvous ranking gives a stable
	// fallback order for a cache key without persisting prompts or user IDs.
	scores := map[string]float64{}
	affinityPriority := map[int]bool{}
	for _, ch := range channels {
		if ch.Options.CacheAffinity {
			affinityPriority[ch.Priority] = true
		}
	}
	for _, ch := range channels {
		seed := requestID
		if affinityPriority[ch.Priority] {
			seed = p.RoutingScope + "\x00" + p.Model.ID + "\x00" + prefix
		}
		mac := hmac.New(sha256.New, g.Secret)
		mac.Write([]byte(seed + "\x00" + ch.ID))
		sum := mac.Sum(nil)
		uniform := float64(binary.BigEndian.Uint64(sum[:8])>>11) + 1
		uniform /= float64(uint64(1)<<53) + 1
		scores[ch.ID] = -math.Log(uniform) / float64(max(1, ch.Options.Weight))
	}
	slices.SortStableFunc(result, func(a, b store.Channel) int {
		if a.Priority != b.Priority {
			if a.Priority > b.Priority {
				return -1
			}
			return 1
		}
		if scores[a.ID] < scores[b.ID] {
			return -1
		}
		if scores[a.ID] > scores[b.ID] {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result
}
func affinityPrefix(p Plan) string {
	var f map[string]json.RawMessage
	json.Unmarshal(p.Raw, &f)
	if b := f["prompt_cache_key"]; nonNull(b) {
		return string(b)
	}
	if b := f["instructions"]; nonNull(b) {
		return string(b[:min(len(b), 4096)])
	}
	key := "messages"
	if p.Format == "openai_responses" {
		key = "input"
	}
	var items []json.RawMessage
	if json.Unmarshal(f[key], &items) == nil && len(items) > 0 {
		b := items[0]
		return string(b[:min(len(b), 4096)])
	}
	b := f[key]
	return string(b[:min(len(b), 4096)])
}
func upstreamErrorCode(status int) string {
	switch status {
	case 400, 422:
		return "upstream_invalid_request"
	case 401, 403:
		return "upstream_authentication_failed"
	case 404:
		return "upstream_model_or_endpoint_missing"
	case 408, 504:
		return "upstream_timeout"
	case 429:
		return "upstream_rate_limited"
	default:
		if status >= 500 {
			return "upstream_service_error"
		}
		return "upstream_rejected"
	}
}
