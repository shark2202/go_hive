package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// terminalEncode renders a tool result the way the Python harness did:
// compact JSON, insertion (struct field) order preserved and HTML characters
// left unescaped.
func terminalEncode(v any) Result {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return Result{Content: fmt.Sprintf("terminal tool: encode result: %v", err), IsError: true}
	}
	return Result{Content: strings.TrimRight(buf.String(), "\n")}
}

// terminalErr renders the minimal {"error": ...} envelope used for expected
// tool-level failures.
func terminalErr(format string, args ...any) Result {
	res := terminalEncode(struct {
		Error string `json:"error"`
	}{Error: fmt.Sprintf(format, args...)})
	res.IsError = true
	return res
}

// terminalBool reads an optional boolean argument, defaulting to false.
func terminalBool(input map[string]any, key string) bool {
	v, ok := input[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// terminalStringSlice reads an optional []string argument, accepting []any of
// strings as produced by encoding/json.
func terminalStringSlice(input map[string]any, key string) ([]string, bool) {
	v, ok := input[key]
	if !ok || v == nil {
		return nil, true
	}
	switch typed := v.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}
