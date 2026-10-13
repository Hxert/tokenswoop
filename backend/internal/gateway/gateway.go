package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/pricing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

type Gateway struct {
	Store     *store.Store
	Secret    []byte
	Client    *http.Client
	clientsMu sync.Mutex
	clients   map[int]*http.Client
}
type Plan struct {
	ClientHeaders      map[string]string
	StartedAt          time.Time
	Operation          string
	Stored             bool
	PreviousResponseID string
	PriceParameters    map[string]any
	ToolCalls          map[string]int64
	Model              store.Model
	Tier               store.Tier
	Format             types.RelayFormat
	Raw                []byte
	Stream             bool
	IncludeUsage       bool
	// Only the first-party Playground understands how to discard a partial
	// answer. Public protocol streams cannot retract already delivered events.
	AllowStreamRestart bool
	RoutingScope       string
	MaxOutput          int // Caller-supplied parameter; zero means omitted, never a platform cap.
}
type Result struct {
	Body                 []byte
	FinalEvents          []StreamEvent
	Meter                store.Meter
	State, Code, Channel string
	Status               int
}
type StreamEvent struct {
	Event string
	Data  []byte
}

func New(s *store.Store, secret []byte, private bool) *Gateway {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, e := net.SplitHostPort(address)
			if e != nil {
				return nil, e
			}
			ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
			if e != nil {
				return nil, e
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("upstream has no address")
			}
			for _, ip := range ips {
				if !private && !PublicIP(ip.IP) {
					return nil, fmt.Errorf("private upstream address rejected")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		}}
	return &Gateway{Store: s, Secret: secret, Client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func PublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	v := ip.To4()
	return v == nil || !(v[0] == 100 && v[1] >= 64 && v[1] <= 127)
}
func ValidURL(raw string, private bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme != "https" && !(private && u.Scheme == "http") {
		return false
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !private && !PublicIP(ip) {
		return false
	}
	return true
}

func (g *Gateway) Run(ctx context.Context, id string, p Plan, channels []store.Channel, emit func(event string, data []byte) error) (result Result) {
	if p.StartedAt.IsZero() {
		p.StartedAt = time.Now()
	}
	defer func() {
		m := &result.Meter
		m.Protocol = string(p.Format)
		m.Operation = p.Operation
		if m.Operation == "" {
			m.Operation = "generate"
		}
		m.Stream = p.Stream
		for _, ch := range channels {
			if ch.ID == result.Channel {
				m.UpstreamProtocol = ch.Protocol
				if result.State == "completed" {
					g.rememberResponse(result.Body, p, ch, m)
					for _, event := range result.FinalEvents {
						g.rememberResponse(event.Data, p, ch, m)
					}
				}
				break
			}
		}
	}()
	// Retries share one wall-clock budget, one admission and one final meter.
	totalSeconds := 30
	for _, ch := range channels {
		timeout := ch.Options.RequestTimeoutSeconds
		if timeout == 0 {
			timeout = 600
		}
		totalSeconds = max(totalSeconds, min(timeout, 3600))
	}
	ctx, stop := context.WithTimeout(ctx, time.Duration(totalSeconds)*time.Second)
	defer stop()
	result = Result{State: "failed", Status: 503, Code: "no_available_channel"}
	queue := g.orderChannels(id, p, channels)
	uncertain, restartStream := false, false
	defer func() {
		if uncertain && result.State != "completed" {
			result.State = "usage_unknown"
		}
		if result.Status == 401 || result.Status == 403 {
			result.Status = 502
		}
	}()
	for attempts := 0; len(queue) > 0 && attempts < 1+maxUpstreamRetries; {
		if ctx.Err() != nil {
			break
		}
		channel := queue[0]
		queue = queue[1:]
		body, meta, e := p.requestContext(ctx, channel)
		if e != nil {
			if attempts == 0 {
				result.Code = "unsupported_conversion"
				result.Status = 400
			}
			g.recordAttempt(id, channel.ID, 400, "conversion_rejected", meta.Codes)
			continue
		}
		key, e := security.Decrypt(g.Secret, channel.SecretCipher)
		if e != nil {
			if attempts == 0 {
				result.Code = "channel_credentials_unavailable"
			}
			continue
		}
		if restartStream {
			if e = emit(PlaygroundRetryEvent, []byte(`{"type":"tokenswoop.retry"}`)); e != nil {
				result.Code = "downstream_disconnected"
				return result
			}
			restartStream = false
		}
		delivered, disconnected := false, false
		attemptEmit := func(event string, data []byte) error {
			err := emit(event, data)
			if err != nil {
				disconnected = true
			} else if len(data) > 0 {
				delivered = true
			}
			return err
		}
		attempts++
		result = g.runAttempt(ctx, id, p, channel, body, key, meta, attemptEmit)
		uncertain = uncertain || result.State == "usage_unknown"
		if result.State == "completed" || disconnected || ctx.Err() != nil {
			return result
		}
		if delivered && !p.AllowStreamRestart {
			return result
		}
		retry, reuseChannel := retryUpstream(result)
		if !retry {
			return result
		}
		if reuseChannel {
			// Prefer another eligible channel, then revisit the original one.
			// A sole configured channel still gets the full retry allowance.
			queue = append(queue, channel)
		}
		restartStream = delivered
	}
	return result
}

func (g *Gateway) runAttempt(ctx context.Context, id string, p Plan, channel store.Channel, body []byte, key string, meta *requestMeta, emit func(string, []byte) error) (result Result) {
	// A fresh copy prevents a failed attempt's usage or tool counters from
	// contributing to the next attempt's charge.
	p.PriceParameters = priceParameters(body)
	p.ToolCalls = nil
	result = Result{State: "failed", Status: 502, Channel: channel.ID}
	attempted := false
	defer func() {
		if attempted {
			outcome := result.Code
			if outcome == "" {
				outcome = result.State
			}
			g.recordAttempt(id, channel.ID, result.Status, outcome, meta.Codes)
		}
	}()
	endpoint := strings.TrimRight(channel.BaseURL, "/")
	switch channel.Protocol {
	case "openai":
		endpoint += "/chat/completions"
	case "openai_responses":
		endpoint += "/responses"
	case "claude":
		endpoint += "/messages"
	case "gemini":
		endpoint += "/models/" + url.PathEscape(meta.UpstreamModelName)
		if p.Stream {
			endpoint += ":streamGenerateContent?alt=sse"
		} else {
			endpoint += ":generateContent"
		}
	}
	if p.Operation == "compact" {
		endpoint = strings.TrimRight(channel.BaseURL, "/") + "/responses/compact"
	}
	if p.Operation == "alpha_search" {
		endpoint = strings.TrimRight(channel.BaseURL, "/") + "/alpha/search"
	}
	timeout := channel.Options.RequestTimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	request, e := http.NewRequestWithContext(attemptCtx, "POST", endpoint, bytes.NewReader(body))
	if e != nil {
		cancel()
		return Result{State: "failed", Status: 502, Code: "invalid_upstream_url"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if p.Stream {
		request.Header.Set("Accept", "text/event-stream")
		if channel.Options.Dialect == "qwen" {
			request.Header.Set("X-DashScope-SSE", "enable")
		}
	}
	switch channel.Protocol {
	case "claude":
		request.Header.Set("x-api-key", key)
		request.Header.Set("anthropic-version", "2023-06-01")
		if p.Format == types.RelayFormatClaude {
			for k, v := range p.ClientHeaders {
				if k == "anthropic-version" || k == "anthropic-beta" {
					request.Header.Set(k, v)
				}
			}
		}
	case "gemini":
		request.Header.Set("x-goog-api-key", key)
	default:
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for key, value := range channel.Options.Headers {
		request.Header.Set(key, value)
	}
	deadline, _ := attemptCtx.Deadline()
	if _, e = g.Store.DB.Exec(ctx, "UPDATE requests SET state='in_flight',channel_id=$2,updated_at=now(),upstream_deadline_at=$3 WHERE id=$1 AND state IN('accepted','in_flight')", id, channel.ID, deadline.Add(time.Minute)); e != nil {
		cancel()
		return Result{State: "failed", Status: 503, Code: "database_unavailable"}
	}
	attempted = true
	response, e := g.clientFor(channel, id).Do(request)
	result.Channel = channel.ID
	if e != nil {
		result.State = "usage_unknown"
		result.Status = 502
		result.Code = "upstream_outcome_unknown"
		cancel()
		return result
	}
	result.Status = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
		response.Body.Close()
		cancel()
		result.Code = upstreamErrorCode(response.StatusCode)
		if response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout {
			result.State = "usage_unknown"
		}
		return result
	}
	if p.Stream {
		result = g.stream(attemptCtx, response, p, meta, channel, id, emit)
	} else {
		raw, e := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
		response.Body.Close()
		result = Result{State: "usage_unknown", Status: 502, Code: "invalid_upstream_response", Channel: channel.ID}
		if e == nil && len(raw) <= 8*1024*1024 {
			if p.Operation == "compact" || p.Operation == "alpha_search" {
				result = g.specialResult(raw, p, channel)
				cancel()
				return result
			}
			toolUsage := newToolUsage()
			toolUsage.observe(raw, channel.Protocol)
			var toolErr error
			p.ToolCalls, toolErr = toolUsage.finish(p, channel.Protocol)
			if toolErr != nil {
				result.Code = "tool_usage_missing"
			}
			value, e := decodeResponse(channel.Protocol, raw, false)
			if e == nil && toolErr == nil {
				converted, e := relayconvert.ConvertResponse(ctx, meta, p.Format, value)
				if e == nil {
					meta.RecordConversionDiagnostics(ctx, converted.Diagnostics)
					meter, e := MeterUsage(converted.Usage, p)
					if e != nil {
						result.Code = "usage_unconfirmed"
					}
					if e == nil {
						value := converted.Value
						if types.RelayFormat(channel.Protocol) == p.Format {
							value = json.RawMessage(raw)
						}
						encoded, e := encodeResult(value, p.Model.ID)
						if e == nil && channel.Options.ThinkingToContent && p.Format == types.RelayFormatOpenAI {
							encoded, e = (&reasoningTextState{}).rewrite(encoded, false)
							meta.note("thinking_to_content")
						}
						if e == nil {
							result = Result{Body: encoded, Meter: meter, State: "completed", Status: 200, Channel: channel.ID}
						}
					}
				}
			}
		}
	}
	cancel()
	return result
}

func (g *Gateway) recordAttempt(id, channel string, status int, outcome string, diagnostics []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if diagnostics == nil {
		diagnostics = []string{}
	}
	b, _ := json.Marshal(diagnostics)
	g.Store.DB.Exec(ctx, "INSERT INTO request_attempts(id,request_id,channel_id,status_code,outcome,diagnostics) VALUES($1,$2,$3,$4,$5,$6)", uuid.NewString(), id, channel, status, outcome, b)
	g.Store.ChannelResult(ctx, channel, status, outcome)
}

func decodeResponse(protocol string, b []byte, stream bool) (any, error) {
	if !stream {
		var shape struct {
			Error   json.RawMessage `json:"error"`
			Status  string          `json:"status"`
			Choices []struct {
				Finish string `json:"finish_reason"`
			} `json:"choices"`
			Stop       string `json:"stop_reason"`
			Candidates []struct {
				Finish string `json:"finishReason"`
			} `json:"candidates"`
		}
		if json.Unmarshal(b, &shape) != nil || (len(shape.Error) > 0 && string(shape.Error) != "null") {
			return nil, fmt.Errorf("invalid upstream response")
		}
		valid := false
		switch protocol {
		case "openai":
			valid = len(shape.Choices) == 1 && shape.Choices[0].Finish != ""
		case "openai_responses":
			valid = shape.Status == "completed" || shape.Status == "incomplete"
		case "claude":
			valid = shape.Stop != ""
		case "gemini":
			valid = len(shape.Candidates) == 1 && shape.Candidates[0].Finish != ""
		}
		if !valid {
			return nil, fmt.Errorf("incomplete upstream response")
		}
	}
	var out any
	switch protocol {
	case "openai":
		if stream {
			out = &dto.ChatCompletionsStreamResponse{}
		} else {
			out = &dto.OpenAITextResponse{}
		}
	case "openai_responses":
		if stream {
			out = &dto.ResponsesStreamResponse{}
		} else {
			out = &dto.OpenAIResponsesResponse{}
		}
	case "claude":
		out = &dto.ClaudeResponse{}
	case "gemini":
		out = &dto.GeminiChatResponse{}
	default:
		return nil, fmt.Errorf("unsupported protocol")
	}
	return out, json.Unmarshal(b, out)
}
func encodeResult(v any, model string) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) == nil {
		if _, ok := fields["model"]; ok {
			fields["model"], _ = json.Marshal(model)
		}
		if _, ok := fields["modelVersion"]; ok {
			fields["modelVersion"], _ = json.Marshal(model)
		}
		if nested := fields["message"]; nonNull(nested) {
			if patched, err := encodeResult(json.RawMessage(nested), model); err == nil {
				fields["message"] = patched
			}
		}
		if nested := fields["response"]; len(nested) > 0 && string(nested) != "null" {
			if patched, err := encodeResult(json.RawMessage(nested), model); err == nil {
				fields["response"] = patched
			}
		}
		return json.Marshal(fields)
	}
	return b, nil
}

func MeterUsage(u *dto.Usage, p Plan) (store.Meter, error) {
	m := store.Meter{}
	if u == nil {
		return m, fmt.Errorf("usage missing")
	}
	if u.BillingUsage != nil {
		if u.BillingUsage.Estimated {
			return m, fmt.Errorf("estimated usage cannot be charged")
		}
		if canonical, ok := u.BillingUsage.CanonicalUsage(); ok {
			u = canonical
		}
	}
	input := u.PromptTokens
	if input == 0 && u.UsageSemantic != dto.BillingUsageSemanticAnthropic {
		input = u.InputTokens
	}
	output := u.CompletionTokens
	if output == 0 {
		output = u.OutputTokens
	}
	read := u.PromptTokensDetails.CachedTokens
	if read == 0 && u.InputTokensDetails != nil {
		read = u.InputTokensDetails.CachedTokens
	}
	write := u.PromptTokensDetails.CachedCreationTokens
	hour := u.ClaudeCacheCreation1hTokens
	if input < 0 || output < 0 || read < 0 || write < 0 || hour < 0 || hour > write {
		return m, fmt.Errorf("invalid usage")
	}
	if input == 0 && output == 0 && read == 0 && write == 0 {
		return m, fmt.Errorf("usage missing")
	}
	regular := input
	totalInput := input
	if u.UsageSemantic == dto.BillingUsageSemanticAnthropic {
		maxInt := int(^uint(0) >> 1)
		if read > maxInt-input || write > maxInt-input-read {
			return m, fmt.Errorf("usage counter overflow")
		}
		totalInput += read + write
	} else {
		if read > input || write > input-read {
			return m, fmt.Errorf("cache usage exceeds total input")
		}
		regular -= read + write
	}
	if regular < 0 {
		return m, fmt.Errorf("invalid input usage")
	}
	trace, e := pricing.Charge(p.Model.Pricing, p.Model.Rates(), pricing.Facts{Input: int64(totalInput), Output: int64(output), CacheRead: int64(read), CacheWrite: int64(write), CacheWrite1h: int64(hour), Tools: p.ToolCalls, Parameters: p.PriceParameters}, p.Tier.DiscountBPS)
	if e != nil {
		return m, e
	}
	cost, e := decimal.NewFromString(trace.Amount)
	if e != nil {
		return m, e
	}
	return store.Meter{Input: int64(totalInput), Output: int64(output), CacheRead: int64(read), CacheWrite: int64(write), CacheWrite1h: int64(hour), Amount: cost, Source: "upstream:" + u.UsageSource, Trace: &trace}, nil
}
