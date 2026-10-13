package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

type toolUsage struct {
	calls       map[string]int64
	ids         map[string]bool
	claudeCount bool
	invalid     bool
}

func newToolUsage() *toolUsage { return &toolUsage{calls: map[string]int64{}, ids: map[string]bool{}} }
func (u *toolUsage) observe(raw []byte, protocol string) {
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	if protocol == "openai_responses" {
		if b := f["response"]; nonNull(b) {
			u.observe(b, protocol)
		}
		var output []map[string]any
		json.Unmarshal(f["output"], &output)
		var typ string
		json.Unmarshal(f["type"], &typ)
		if typ == "response.output_item.done" {
			var item map[string]any
			if json.Unmarshal(f["item"], &item) == nil {
				output = append(output, item)
			}
		}
		for _, item := range output {
			typ, _ := item["type"].(string)
			name := ""
			switch typ {
			case "web_search_call":
				name = "web_search"
			case "file_search_call":
				name = "file_search"
			}
			if name == "" {
				if strings.HasSuffix(typ, "_call") && typ != "function_call" && typ != "custom_tool_call" {
					u.invalid = true
				}
				continue
			}
			status, _ := item["status"].(string)
			if status != "completed" {
				if status != "failed" {
					u.invalid = true
				}
				continue
			}
			id, _ := item["id"].(string)
			if id == "" {
				u.invalid = true
				continue
			}
			if !u.ids[id] {
				u.ids[id] = true
				u.calls[name]++
			}
		}
	}
	if protocol == "claude" {
		if b := f["message"]; nonNull(b) {
			u.observe(b, protocol)
		}
		var usage struct {
			Server map[string]json.RawMessage `json:"server_tool_use"`
		}
		json.Unmarshal(f["usage"], &usage)
		if usage.Server != nil {
			if raw, ok := usage.Server["web_search_requests"]; ok {
				var n int64
				if json.Unmarshal(raw, &n) != nil || n < 0 {
					u.invalid = true
				} else {
					u.calls["web_search"] = max(n, u.calls["web_search"])
					u.claudeCount = true
				}
			}
			for key, raw := range usage.Server {
				if key != "web_search_requests" && string(raw) != "0" {
					u.invalid = true
				}
			}
		}
	}
}
func (u *toolUsage) finish(p Plan, protocol string) (map[string]int64, error) {
	if u.invalid {
		return nil, fmt.Errorf("ambiguous hosted tool usage")
	}
	if protocol == "claude" && p.Format == "claude" {
		var f struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		json.Unmarshal(p.Raw, &f)
		for _, tool := range f.Tools {
			if toolName(tool.Type) != "" && !u.claudeCount {
				return nil, fmt.Errorf("hosted tool counter missing")
			}
		}
	}
	return u.calls, nil
}
