package mcpregistry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	}
	return nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func stringOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func asSlice(v any) []any {
	switch list := v.(type) {
	case []any:
		return list
	case []string:
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = item
		}
		return out
	}
	return nil
}

func asStringSlice(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, asString(item))
		}
		return out
	}
	return nil
}

func asStringMap(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = asString(val)
		}
		return out
	}
	return nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func serversOf(data map[string]any) map[string]any {
	servers := asMap(data["servers"])
	if servers == nil {
		servers = map[string]any{}
		data["servers"] = servers
	}
	return servers
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pyListRepr(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + strings.ReplaceAll(v, "'", "\\'") + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func getTruthy(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func intValue(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(x.String())
		return i, err == nil
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return x.String()
	}
	return fmt.Sprintf("%v", v)
}

func truthyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case map[string]string:
		return len(x) > 0
	}
	return true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func lenOf(v any) (int, bool) {
	switch x := v.(type) {
	case []any:
		return len(x), true
	case []string:
		return len(x), true
	case map[string]any:
		return len(x), true
	case map[string]string:
		return len(x), true
	case string:
		return len(x), true
	}
	return 0, false
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func strPtr(s string) *string { return &s }

func strField(m map[string]any, key string, def *string) *string {
	v, ok := m[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return strPtr(s)
	}
	return nil
}

func stringSliceToAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func stringMapToAny(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
