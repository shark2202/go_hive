package agentloop

import (
	"encoding/json"
	"strings"
)

// parseToolInput decodes a tool call's raw argument string into a map. An
// empty, malformed or null argument string reads as {} — the call itself
// still runs, exactly as the upstream loop treats it.
func parseToolInput(arguments string) map[string]any {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return map[string]any{}
	}
	if input == nil {
		return map[string]any{}
	}
	return input
}
