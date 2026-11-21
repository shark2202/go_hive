package cloudflare

// Decoded-JSON accessors shared by the shapes and custom actions.

import (
	"encoding/json"
	"strconv"
	"strings"
)

// sliceValue views a decoded value as an array, returning nil when
// the value is not an array (an empty array stays non-nil).
func sliceValue(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

// mapAt reads an object field, returning an empty object when the
// field is absent or not an object (the upstream `.get(key, {})` default).
func mapAt(source map[string]any, key string) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	if object := asMap(source[key]); object != nil {
		return object
	}
	return map[string]any{}
}

// sliceAt reads an array field, returning an empty array when the
// field is absent or not an array.
func sliceAt(source map[string]any, key string) []any {
	if source == nil {
		return []any{}
	}
	if items := sliceValue(source[key]); items != nil {
		return items
	}
	return []any{}
}

// textAt reads a string field.
func textAt(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	return asString(source[key])
}

// intAt reads an integer field with a fallback.
func intAt(source map[string]any, key string, fallback int) int {
	if source == nil {
		return fallback
	}
	return intValue(source[key], fallback)
}

// intValue converts a decoded JSON number to int.
func intValue(value any, fallback int) int {
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
		return fallback
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// optionalInt distinguishes an absent/null argument from 0.
func optionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	return intValue(value, 0), true
}

// truncateText caps provider text the way upstream slices [:500].
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// payloadError reads an error string out of a payload.
func payloadError(payload map[string]any) string {
	return textAt(payload, "error")
}

// resultArray applies the upstream "result is a list, or a single
// object with an id" normalisation.
func resultArray(value any) []any {
	if items := sliceValue(value); items != nil {
		return items
	}
	if object := asMap(value); object != nil {
		if _, present := object["id"]; present {
			return []any{object}
		}
	}
	return []any{}
}
