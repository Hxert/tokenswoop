package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"tokenswoop/backend/internal/store"
)

// One goroutine reads the upstream; the calling goroutine owns every downstream
// write. Closing the response body cancels a blocked read on every exit path.
func (g *Gateway) stream(ctx context.Context, response *http.Response, p Plan, meta *requestMeta, ch store.Channel, id string, emit func(string, []byte) error) (result Result) {
	defer response.Body.Close()
	result = Result{State: "usage_unknown", Code: "incomplete_stream", Status: 502, Channel: ch.ID}
	if p.StartedAt.IsZero() {
		p.StartedAt = time.Now()
	}
	var firstContent time.Time
	defer func() {
		if !firstContent.IsZero() {
			ttft := max(int64(0), firstContent.Sub(p.StartedAt).Milliseconds())
			generation := max(int64(1), time.Since(firstContent).Milliseconds())
			result.Meter.FirstTokenMS = &ttft
			result.Meter.GenerationMS = &generation
		}
	}()
	tools := newToolUsage()
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		result.Code = "invalid_stream_content_type"
		return result
	}
	state, err := relayconvert.NewResponseStreamState(types.RelayFormat(ch.Protocol), p.Format, relayconvert.ResponseStreamOptions{ID: "chatcmpl-" + id, Model: p.Model.ID, IncludeUsage: p.IncludeUsage, EmitSequenceNumber: true})
	if err != nil {
		return result
	}
	same := types.RelayFormat(ch.Protocol) == p.Format
	finalEvents := []StreamEvent{}
	closingOutput := false
	thinking := &reasoningTextState{}
	write := func(b []byte) error {
		if ch.Options.ThinkingToContent && p.Format == types.RelayFormatOpenAI {
			var e error
			b, e = thinking.rewrite(b, true)
			if e != nil {
				return e
			}
			meta.note("thinking_to_content")
		}
		var shape struct {
			Type    string            `json:"type"`
			Choices []json.RawMessage `json:"choices"`
			Usage   json.RawMessage   `json:"usage"`
		}
		if json.Unmarshal(b, &shape) != nil {
			return fmt.Errorf("invalid stream payload")
		}
		event := ""
		if p.Format == types.RelayFormatOpenAIResponses || p.Format == types.RelayFormatClaude {
			event = shape.Type
		}
		if p.Format == types.RelayFormatOpenAI && !p.IncludeUsage && nonNull(shape.Usage) {
			if len(shape.Choices) == 0 {
				return nil
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(b, &fields)
			delete(fields, "usage")
			b, _ = json.Marshal(fields)
		}
		closingOutput = closingOutput || terminalPayload(b, p.Format)
		if closingOutput {
			finalEvents = append(finalEvents, StreamEvent{Event: event, Data: b})
			return nil
		}
		return emit(event, b)
	}
	writeItems := func(items []relayconvert.ResponseResult) error {
		for _, item := range items {
			b, e := encodeResult(item.Value, p.Model.ID)
			if e != nil {
				return e
			}
			if e = write(b); e != nil {
				return e
			}
		}
		return nil
	}
	terminal, stop := false, false
	process := func(data, event string) error {
		if strings.TrimSpace(data) == "[DONE]" {
			if ch.Protocol != "openai" {
				return fmt.Errorf("unexpected completion marker")
			}
			terminal, stop = true, true
			return nil
		}
		var shape map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &shape) != nil || shape == nil {
			return fmt.Errorf("invalid stream JSON")
		}
		if nonNull(shape["error"]) || event == "error" {
			return fmt.Errorf("upstream stream error")
		}
		var typ string
		json.Unmarshal(shape["type"], &typ)
		if typ == "error" || typ == "response.failed" || typ == "response.error" {
			return fmt.Errorf("upstream stream failed")
		}
		tools.observe([]byte(data), ch.Protocol)
		if firstContent.IsZero() && hasModelContent(shape, ch.Protocol) {
			firstContent = time.Now()
		}
		if ch.Protocol == "claude" && typ == "message_stop" || ch.Protocol == "openai_responses" && (typ == "response.completed" || typ == "response.incomplete") {
			terminal, stop = true, true
		}
		if ch.Protocol == "gemini" {
			var v struct {
				Candidates []struct {
					Finish string `json:"finishReason"`
				} `json:"candidates"`
			}
			json.Unmarshal([]byte(data), &v)
			for _, candidate := range v.Candidates {
				if candidate.Finish != "" {
					terminal = true
				}
			}
			// Gemini has no separate DONE marker. Keep reading to EOF so providers
			// which send a subsequent usage-only frame are metered accurately.
		}
		value, e := decodeResponse(ch.Protocol, []byte(data), true)
		if e != nil {
			return e
		}
		items, e := relayconvert.ConvertStreamResponseChunk(ctx, meta, state, value)
		if e != nil {
			return e
		}
		for _, item := range items {
			meta.RecordConversionDiagnostics(ctx, item.Diagnostics)
		}
		if same {
			b, e := encodeResult(json.RawMessage(data), p.Model.ID)
			if e != nil {
				return e
			}
			return write(b)
		}
		return writeItems(items)
	}
	type scanLine struct {
		line string
		err  error
		eof  bool
	}
	lines := make(chan scanLine, 1)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	go func() {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 2*1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanLine{line: scanner.Text()}:
			case <-readCtx.Done():
				return
			}
		}
		select {
		case lines <- scanLine{eof: true, err: scanner.Err()}:
		case <-readCtx.Done():
		}
	}()
	idleSeconds := ch.Options.StreamIdleSeconds
	if idleSeconds == 0 {
		idleSeconds = 120
	}
	idle := time.NewTimer(time.Duration(idleSeconds) * time.Second)
	defer idle.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	parts := []string{}
	event := ""
	size := 0
	first := true
	flush := func() error {
		if len(parts) == 0 {
			event = ""
			return nil
		}
		e := process(strings.Join(parts, "\n"), event)
		parts = nil
		event = ""
		return e
	}
readLoop:
	for {
		select {
		case <-ctx.Done():
			result.Code = "upstream_canceled"
			return result
		case <-idle.C:
			result.Code = "stream_idle_timeout"
			return result
		case <-ping.C:
			if e := emit("", nil); e != nil {
				result.Code = "downstream_disconnected"
				return result
			}
		case next := <-lines:
			if next.eof {
				if next.err != nil && next.err != io.EOF {
					return result
				}
				if err = flush(); err != nil {
					return result
				}
				break readLoop
			}
			idle.Reset(time.Duration(idleSeconds) * time.Second)
			line := next.line
			if first {
				line = strings.TrimPrefix(line, "\ufeff")
				first = false
			}
			size += len(line) + 1
			if size > 32*1024*1024 {
				result.Code = "stream_too_large"
				return result
			}
			if line == "" {
				if err = flush(); err != nil {
					return result
				}
				if stop {
					break readLoop
				}
				continue
			}
			if line == "[DONE]" {
				if err = flush(); err != nil {
					return result
				}
				if err = process(line, event); err != nil {
					return result
				}
				break readLoop
			}
			if strings.HasPrefix(line, "data:") {
				parts = append(parts, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
		}
	}
	if !terminal {
		return result
	}
	items, err := relayconvert.FinalizeStreamResponse(ctx, meta, state)
	if err != nil {
		return result
	}
	for _, item := range items {
		meta.RecordConversionDiagnostics(ctx, item.Diagnostics)
	}
	if !same {
		if err = writeItems(items); err != nil {
			return result
		}
	}
	p.ToolCalls, err = tools.finish(p, ch.Protocol)
	if err != nil {
		result.Code = "tool_usage_missing"
		return result
	}
	meter, err := MeterUsage(state.Usage(), p)
	if err != nil {
		result.Code = "usage_missing"
		return result
	}
	return Result{State: "completed", Status: 200, Channel: ch.ID, Meter: meter, FinalEvents: finalEvents}
}
