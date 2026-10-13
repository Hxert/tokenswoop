package gateway

import "encoding/json"

// Optional compatibility for Chat clients that only display content. This
// transforms reasoning returned by the upstream; usage still comes from the
// unmodified upstream response and no reasoning is fabricated.
type reasoningTextState struct{ open map[int]bool }

func (s *reasoningTextState) rewrite(raw []byte, stream bool) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(fields["choices"], &choices); err != nil {
		return raw, nil
	}
	if s.open == nil {
		s.open = map[int]bool{}
	}
	changed := false
	for _, choice := range choices {
		key := "message"
		if stream {
			key = "delta"
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(choice[key], &message) != nil || message == nil {
			continue
		}
		var index int
		json.Unmarshal(choice["index"], &index)
		var thought, content, finish string
		json.Unmarshal(message["reasoning_content"], &thought)
		if thought == "" {
			json.Unmarshal(message["reasoning"], &thought)
		}
		json.Unmarshal(message["content"], &content)
		json.Unmarshal(choice["finish_reason"], &finish)
		if thought == "" && !s.open[index] {
			continue
		}
		output := ""
		if thought != "" {
			if !s.open[index] {
				output = "<think>\n"
				s.open[index] = true
			}
			output += thought
		}
		if s.open[index] && (!stream || content != "" || finish != "") {
			output += "\n</think>\n"
			s.open[index] = false
		}
		output += content
		if output != "" {
			put(message, "content", output)
		}
		delete(message, "reasoning_content")
		delete(message, "reasoning")
		put(choice, key, message)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	put(fields, "choices", choices)
	return json.Marshal(fields)
}
