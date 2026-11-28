package image

// Type aliases, schema/argument primitives and the Python-compatible JSON
// serializer the moved implementation calls. This package depends on
// integrationkit and the standard library only.

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

type (
	Tool         = integrationkit.Tool
	Action       = integrationkit.Action
	Runtime      = integrationkit.Runtime
	Registry     = integrationkit.Registry
	ActionFunc   = integrationkit.ActionFunc
	ModuleConfig = integrationkit.ModuleConfig
)

// param builds a JSON-schema property with only a type.
func param(typ string) map[string]any { return integrationkit.Param(typ) }

// paramDefault builds a JSON-schema property with an upstream default.
func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func intArg(input map[string]any, name string, fallback int) int {
	return integrationkit.IntArg(input, name, fallback)
}

func stringSliceArg(input map[string]any, name string) []string {
	return integrationkit.StringSliceArg(input, name)
}

// imageBlock is the image content block (data URL) the shared registry turns
// into ToolResult.ImageContent, the same shape mcp_client emits upstream for
// ImageContent with an image mime.
func imageBlock(mime string, rawBytes []byte) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(rawBytes),
		},
	}
}

// pythonJSONDumps mirrors internal/tools.PythonJSONDumps for the payload types
// this tool returns: maps sort their keys, arrays keep order, floats use
// Python's repr spelling and separators are ", " / ": ". The image summary is
// model-visible, so it must stay byte-identical after the package move.
func pythonJSONDumps(v any) string {
	var b strings.Builder
	writePythonJSON(&b, v)
	return b.String()
}

func writePythonJSON(b *strings.Builder, v any) {
	switch value := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if value {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(value)
		b.Write(encoded)
	case json.Number:
		b.WriteString(value.String())
	case int:
		b.WriteString(strconv.Itoa(value))
	case int8:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int64:
		b.WriteString(strconv.FormatInt(value, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint8:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint16:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(value, 10))
	case float32:
		b.WriteString(formatPyFloat(float64(value)))
	case float64:
		b.WriteString(formatPyFloat(value))
	case []any:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case []map[string]any:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case map[string]string:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, k)
			b.WriteString(": ")
			writePythonJSON(b, value[k])
		}
		b.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, k)
			b.WriteString(": ")
			writePythonJSON(b, value[k])
		}
		b.WriteByte('}')
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			b.WriteString(strconv.Quote(strconv.FormatInt(0, 10)))
			return
		}
		b.Write(encoded)
	}
}

// formatPyFloat renders a float64 the way Python's repr(float) does.
func formatPyFloat(value float64) string {
	shortest := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(shortest, "eE") {
		if !strings.Contains(shortest, ".") {
			return shortest + ".0"
		}
		return shortest
	}
	exponent, err := strconv.Atoi(shortest[strings.IndexAny(shortest, "eE")+1:])
	if err != nil || exponent < -4 || exponent >= 16 {
		return fixPyExponent(strconv.FormatFloat(value, 'e', -1, 64))
	}
	positional := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(positional, ".") {
		positional += ".0"
	}
	return positional
}

// fixPyExponent pads the exponent to at least two digits ("1e+6" -> "1e+06").
func fixPyExponent(value string) string {
	index := strings.IndexAny(value, "eE")
	if index < 0 {
		return value
	}
	mantissa := value[:index]
	exponent := value[index+1:]
	sign := ""
	if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
		sign = exponent[:1]
		exponent = exponent[1:]
	}
	if len(exponent) == 1 {
		exponent = "0" + exponent
	}
	return mantissa + "e" + sign + exponent
}
