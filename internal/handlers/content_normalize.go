package handlers

import (
	"encoding/json"
	"strings"
)

// normalizeOpenAIContent converts an OpenAI chat completion message.content
// value (which may be a string or an array of content parts) into a plain
// string. Non-text parts are ignored.
func normalizeOpenAIContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// array of content parts
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			// string element
			var s string
			if err := json.Unmarshal(p, &s); err == nil {
				if s != "" {
					texts = append(texts, s)
				}
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal(p, &obj); err != nil {
				continue
			}
			t, _ := obj["type"].(string)
			if t != "text" {
				continue
			}
			if txt, ok := obj["text"].(string); ok && txt != "" {
				texts = append(texts, txt)
				continue
			}
			// some providers nest text under a "text" object
			if to, ok := obj["text"].(map[string]any); ok {
				if v, ok := to["value"].(string); ok && v != "" {
					texts = append(texts, v)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}
