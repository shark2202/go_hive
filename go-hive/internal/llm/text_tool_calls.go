package llm

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Some models (notably Gemini) emit tool invocations as text inside
// `<tool_code>{...}</tool_code>` blocks instead of using the structured
// function-calling API. Parse those blocks into real tool calls and strip
// them from the visible answer.
//
// Parity: core/framework/llm/litellm.py::_extract_text_tool_calls
var textToolCodePattern = regexp.MustCompile(`(?s)<tool_code>\s*(.*?)\s*</tool_code>`)

// textToolCall is one synthetic call recovered from model text.
type textToolCall struct {
	ID        string
	Name      string
	Arguments string
	Input     map[string]any
}

// extractTextToolCalls returns the recovered calls and the text with the
// `<tool_code>` blocks removed. When no block parses into a call, the text is
// returned unchanged.
func extractTextToolCalls(text string) ([]textToolCall, string) {
	matches := textToolCodePattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, text
	}
	calls := make([]textToolCall, 0, len(matches))
	for _, match := range matches {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(match[1]), &payload); err != nil {
			continue
		}
		// Sort names so the synthetic ids are deterministic even though Go
		// map iteration is not.
		names := make([]string, 0, len(payload))
		for name := range payload {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			var input map[string]any
			if err := json.Unmarshal(payload[name], &input); err != nil {
				continue
			}
			arguments, err := json.Marshal(input)
			if err != nil {
				continue
			}
			sum := md5.Sum([]byte(name + ":" + string(arguments)))
			calls = append(calls, textToolCall{
				ID:        "synth_" + hex.EncodeToString(sum[:])[:12],
				Name:      name,
				Arguments: string(arguments),
				Input:     input,
			})
		}
	}
	if len(calls) == 0 {
		return nil, text
	}
	return calls, strings.TrimSpace(textToolCodePattern.ReplaceAllString(text, ""))
}
