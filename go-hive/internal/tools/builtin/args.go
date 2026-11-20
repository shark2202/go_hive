package builtin

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// stringArg extracts a string argument. Numbers are not coerced: a path or
// command must be a JSON string.
func stringArg(input map[string]any, key string) (string, bool) {
	v, ok := input[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// numberArg extracts a numeric argument, accepting the shapes produced by
// encoding/json, jq-style helpers and direct Go callers.
func numberArg(input map[string]any, key string) (float64, bool) {
	v, ok := input[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// finiteNumberArg reports the argument as present only when it is a finite
// number.
func finiteNumberArg(input map[string]any, key string) (float64, bool) {
	f, ok := numberArg(input, key)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// intArg extracts an integer argument, truncating float representations.
func intArg(input map[string]any, key string) (int, bool) {
	f, ok := finiteNumberArg(input, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// boolArgOr extracts a boolean argument, accepting the shapes JSON, YAML-ish
// callers and direct Go callers produce. Missing or unparseable values fall
// back to fallback.
func boolArgOr(input map[string]any, key string, fallback bool) bool {
	v, ok := input[key]
	if !ok || v == nil {
		return fallback
	}
	switch value := v.(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fallback
		}
		return parsed
	case json.Number:
		if f, err := value.Float64(); err == nil {
			return f != 0
		}
	case float64:
		return value != 0
	case float32:
		return value != 0
	case int:
		return value != 0
	case int64:
		return value != 0
	case int32:
		return value != 0
	}
	return fallback
}
