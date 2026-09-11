package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func digest(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }

// Extract scans text and tool arguments, never decodes image data or fetches URLs.
// A latest-turn policy includes all tool results following the last user message.
func Extract(body []byte, latest bool, limit int) (string, error) {
	if len(body) > 32<<20 {
		return "", fmt.Errorf("audit_input_too_large")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return "", fmt.Errorf("audit_input_invalid")
	}
	var b strings.Builder
	count := 0
	add := func(s string) error {
		count += len([]rune(s))
		if count > limit {
			return fmt.Errorf("audit_input_too_large")
		}
		b.WriteString(s)
		b.WriteByte('\n')
		return nil
	}
	var walk func(json.RawMessage, int) error
	walk = func(raw json.RawMessage, depth int) error {
		if depth > 64 {
			return fmt.Errorf("audit_input_too_deep")
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return add(s)
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for _, v := range arr {
				if err := walk(v, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			for _, k := range []string{"text", "content", "arguments", "output", "input", "parts", "function", "tool_calls", "functionCall", "functionResponse", "args", "response"} {
				if v, ok := obj[k]; ok {
					if k == "arguments" || k == "output" || k == "args" || k == "response" {
						var decoded any
						_ = json.Unmarshal(v, &decoded)
						text, isString := decoded.(string)
						if !isString {
							normalized, _ := json.Marshal(decoded)
							text = string(normalized)
						}
						if err := add(text); err != nil {
							return err
						}
						continue
					}
					if err := walk(v, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, key := range []string{"instructions", "system", "systemInstruction", "prompt", "messages", "input", "contents", "tools"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		if latest && (key == "messages" || key == "input" || key == "contents") {
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) == nil {
				start := 0
				for i := len(items) - 1; i >= 0; i-- {
					var v struct {
						Role string `json:"role"`
					}
					_ = json.Unmarshal(items[i], &v)
					if v.Role == "user" {
						start = i
						break
					}
				}
				for _, item := range items[start:] {
					if err := walk(item, 0); err != nil {
						return "", err
					}
				}
				continue
			}
		}
		// Tool descriptions are audited along with executable arguments.
		if key == "tools" {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				encoded, _ := json.Marshal(v)
				if err := add(string(encoded)); err != nil {
					return "", err
				}
			}
			continue
		}
		if err := walk(raw, 0); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func chunks(text string, size int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	out := []string{}
	overlap := min(256, size/8)
	for start := 0; start < len(runes); {
		end := min(start+size, len(runes))
		out = append(out, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return out
}
