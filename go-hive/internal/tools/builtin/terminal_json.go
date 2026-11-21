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

// terminalErr renders the minimal {"error": ...} envelope used for tool-level
// failures whose Python counterpart returns a normal dict (unknown job/session,
// stream unavailable, a refused action). It is deliberately NOT an
// error-marked result: upstream's loop adds "ERROR: " only when a tool raises,
// and these tools return their error dict instead.
func terminalErr(format string, args ...any) Result {
	return terminalEncode(struct {
		Error string `json:"error"`
	}{Error: fmt.Sprintf(format, args...)})
}

// terminalArgErr marks a call that upstream's FastMCP signature validation
// would reject before the tool body runs (missing required argument, wrong
// primitive type). The Python loop surfaces those as tool errors, so the
// Go result stays error-marked even though the payload is JSON.
func terminalArgErr(format string, args ...any) Result {
	res := terminalErr(format, args...)
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
