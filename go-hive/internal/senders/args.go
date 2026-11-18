package senders

// Input coercion for model-supplied JSON. Values arrive as strings, float64,
// json.Number or nil depending on the transport, and every action must accept
// all of them without inventing data.

import (
	"encoding/json"
	"strconv"
	"strings"
)

func stringArg(input map[string]any, name string) string {
	value, ok := input[name]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(toString(value))
	}
}

func intArg(input map[string]any, name string, fallback int) int {
	value, ok := input[name]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// optionalStringArg distinguishes "not supplied" (nil) from an explicit empty
// string, which is a meaningful opt-out for the text body.
func optionalStringArg(input map[string]any, name string) *string {
	value, ok := input[name]
	if !ok || value == nil {
		return nil
	}
	text := toString(value)
	return &text
}

func hasArg(input map[string]any, name string) bool {
	_, ok := input[name]
	return ok
}

// parseEnabled accepts the strings the tool documents ('true'/'false' and the
// usual spellings) plus real booleans from structured callers.
func parseEnabled(raw string) (bool, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "true", "yes", "1", "on":
		return true, true
	case "false", "no", "0", "off":
		return false, true
	}
	return false, false
}
