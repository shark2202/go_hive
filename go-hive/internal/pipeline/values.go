package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// trimFloat renders a float the way Python's str() does for the values that
// reach pipeline messages: integral floats lose the trailing ".0".
func trimFloat(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// pyNumeric reports a JSON value as a float when Python comparison operators
// would accept it: int, float and bool (bool is an int subclass upstream).
func pyNumeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// pythonTypeName is Python's type(value).__name__ for the JSON shapes a
// pipeline context carries. json.Number keeps the writer's shape: an
// integer-syntax number is an int, anything with . / e / E is a float.
func pythonTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return "float"
		}
		return "int"
	case float64, float32:
		return "float"
	case int, int8, int16, int32, int64:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", value)
}

// pythonIsInstance reproduces isinstance(value, expected_type) for the schema
// type names configuration can express. Python quirks are kept: bool is an int
// subclass (so a true/false value satisfies an "int" expectation), while an
// int does not satisfy "float". An unknown type name is an error — upstream
// would raise TypeError from isinstance() here, never pass.
func pythonIsInstance(value any, expectedType string) (bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(expectedType))
	switch normalized {
	case "str", "string":
		_, ok := value.(string)
		return ok, nil
	case "int":
		if _, ok := value.(bool); ok {
			return true, nil
		}
		_, isInt := pyInt(value)
		return isInt, nil
	case "float":
		switch value.(type) {
		case bool:
			return false, nil
		case float64, float32:
			return true, nil
		case json.Number:
			text := value.(json.Number).String()
			return strings.ContainsAny(text, ".eE"), nil
		}
		return false, nil
	case "bool":
		_, ok := value.(bool)
		return ok, nil
	case "list":
		_, ok := value.([]any)
		return ok, nil
	case "dict":
		_, ok := value.(map[string]any)
		return ok, nil
	case "nonetype", "none":
		return value == nil, nil
	}
	return false, fmt.Errorf("unsupported input schema type %q", expectedType)
}

// schemaField is one `key: expected_type` pair of an input schema, kept in a
// slice so the reported failure is deterministic.
type schemaField struct {
	Key  string
	Type string
}

// orderedSchemaFields flattens one entry point's `{key: type}` schema. Go maps
// lose the JSON writer's insertion order, so the keys are sorted: the first
// violation reported is stable, though not necessarily the config's first line.
func orderedSchemaFields(schema map[string]any) []schemaField {
	fields := make([]schemaField, 0, len(schema))
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fields = append(fields, schemaField{Key: key, Type: pyString(schema[key])})
	}
	return fields
}
