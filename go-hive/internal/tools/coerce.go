package tools

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

var stringExtractKeys = []string{"label", "value", "text", "name", "title", "display"}

func CoerceToolInput(tool Tool, rawInput map[string]any) map[string]any {
	if rawInput == nil {
		return map[string]any{}
	}
	schema := tool.Parameters
	if schema == nil {
		return rawInput
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return rawInput
	}
	for key := range rawInput {
		propSchema, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		original := rawInput[key]
		coerced, changed := coerceValue(original, propSchema)
		if changed {
			rawInput[key] = coerced
		}
	}
	return rawInput
}

func coerceValue(value any, schema map[string]any) (any, bool) {
	expected, ok := schema["type"]
	if !ok || expected == nil {
		return value, false
	}

	if types, ok := expected.([]any); ok {
		for _, t := range types {
			sub := copySchemaWithType(schema, t)
			if coerced, changed := coerceValue(value, sub); changed {
				return coerced, true
			}
		}
		return value, false
	}

	name, _ := expected.(string)
	switch name {
	case "integer":
		return coerceInteger(value)
	case "number":
		return coerceNumber(value)
	case "boolean":
		return coerceBoolean(value)
	case "string":
		return coerceString(value)
	case "array":
		return coerceArray(value, schema)
	case "object":
		return coerceObject(value, schema)
	}
	return value, false
}

func copySchemaWithType(schema map[string]any, t any) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	out["type"] = t
	return out
}

func coerceInteger(value any) (any, bool) {
	if _, ok := value.(bool); ok {
		return value, false
	}
	if isInteger(value) {
		return value, false
	}
	if s, ok := value.(string); ok {
		parsed, ok := parseNumber(s)
		if !ok {
			return value, false
		}
		if parsed != math.Trunc(parsed) {
			return value, false
		}
		return int64(parsed), true
	}
	return value, false
}

func coerceNumber(value any) (any, bool) {
	if _, ok := value.(bool); ok {
		return value, false
	}
	if isInteger(value) || isFloat(value) {
		return value, false
	}
	if s, ok := value.(string); ok {
		parsed, ok := parseNumber(s)
		if !ok {
			return value, false
		}
		if parsed == math.Trunc(parsed) && !math.IsInf(parsed, 0) {
			return int64(parsed), true
		}
		return parsed, true
	}
	return value, false
}

func coerceBoolean(value any) (any, bool) {
	if _, ok := value.(bool); ok {
		return value, false
	}
	if s, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return value, false
}

func coerceString(value any) (any, bool) {
	if _, ok := value.(string); ok {
		return value, false
	}
	if obj, ok := value.(map[string]any); ok {
		if extracted, ok := extractStringFromObject(obj); ok {
			return extracted, true
		}
	}
	return value, false
}

func coerceArray(value any, schema map[string]any) (any, bool) {
	changed := false
	if s, ok := value.(string); ok {
		parsed, ok := tryParseJSON(s)
		if list, isList := parsed.([]any); ok && isList {
			value = list
			changed = true
		} else {
			return []any{value}, true
		}
	} else if _, ok := value.([]any); !ok {
		return []any{value}, true
	}

	list := value.([]any)
	itemsSchema, ok := schema["items"].(map[string]any)
	if !ok {
		return value, changed
	}

	coercedItems := make([]any, 0, len(list))
	itemChanged := false
	for _, item := range list {
		c, ch := coerceValue(item, itemsSchema)
		if ch {
			itemChanged = true
		}
		coercedItems = append(coercedItems, c)
	}
	if itemChanged {
		return coercedItems, true
	}
	return value, changed
}

func coerceObject(value any, schema map[string]any) (any, bool) {
	changed := false
	if s, ok := value.(string); ok {
		parsed, ok := tryParseJSON(s)
		if obj, isObj := parsed.(map[string]any); ok && isObj {
			value = obj
			changed = true
		} else {
			return value, false
		}
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return value, false
	}

	subProps, ok := schema["properties"].(map[string]any)
	if !ok {
		return value, changed
	}
	for k := range obj {
		subSchema, ok := subProps[k].(map[string]any)
		if !ok {
			continue
		}
		original := obj[k]
		coerced, ch := coerceValue(original, subSchema)
		if ch {
			obj[k] = coerced
			changed = true
		}
	}
	return value, changed
}

func extractStringFromObject(obj map[string]any) (string, bool) {
	for _, k := range stringExtractKeys {
		if v, ok := obj[k].(string); ok && v != "" {
			return v, true
		}
	}
	if len(obj) == 1 {
		for _, only := range obj {
			if s, ok := only.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func tryParseJSON(raw string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return fromJSON(v), true
}

func fromJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = fromJSON(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = fromJSON(item)
		}
		return out
	default:
		return v
	}
}

func parseNumber(raw string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func isInteger(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func isFloat(v any) bool {
	switch v.(type) {
	case float32, float64:
		return true
	}
	return false
}

func pythonRepr(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
