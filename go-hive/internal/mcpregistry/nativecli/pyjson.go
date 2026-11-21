// Package nativecli implements the MCP subset of mcp2cli 3.8.0 in Go.
//
// It is a drop-in replacement for the `mcp2cli` binary as far as
// `hive mcp run` uses it: MCP servers over stdio, streamable HTTP and SSE,
// the tool-listing and tool-calling commands, and the output shapes mcp2cli
// produces. OpenAPI, GraphQL, OAuth and baked wrappers are out of scope and
// reported as unsupported so the caller can fall back to the real binary.
package nativecli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools"
)

// Field is one member of an Ordered object.
type Field struct {
	Key   string
	Value any
}

// Ordered is a JSON object that keeps the key order it was built with.
// Python's json.dumps emits dict insertion order, and mcp2cli's output is
// compared byte-for-byte with it, so a plain Go map cannot be used.
type Ordered []Field

// Lookup returns the value stored under key.
func (o Ordered) Lookup(key string) (any, bool) {
	for _, field := range o {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// ParseJSON decodes a JSON document into Ordered objects, []any slices and
// json.Number scalars, preserving object key order and number literals.
func ParseJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	// Reject trailing garbage the way json.Unmarshal does.
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON: trailing data")
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := Ordered{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON: object key is not a string")
				}
				value, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object = append(object, Field{Key: key, Value: value})
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return array, nil
		}
		return nil, fmt.Errorf("invalid JSON: unexpected delimiter %v", typed)
	default:
		return typed, nil
	}
}

// MarshalPy renders value the way Python's json.dumps does with its default
// separators (", " and ": ") and ensure_ascii=False.
func MarshalPy(value any) string {
	var builder strings.Builder
	appendPyJSON(&builder, value, false, 0)
	return builder.String()
}

// MarshalPyPretty renders value the way Python's json.dumps(indent=2) does.
func MarshalPyPretty(value any) string {
	var builder strings.Builder
	appendPyJSON(&builder, value, true, 0)
	return builder.String()
}

func appendPyJSON(builder *strings.Builder, value any, pretty bool, depth int) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		appendPyString(builder, typed)
	case json.Number:
		builder.WriteString(string(typed))
	case float64:
		builder.WriteString(formatPyFloat(typed))
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case Ordered:
		appendPyObject(builder, typed, pretty, depth)
	case map[string]any:
		appendPyObject(builder, sortedObject(typed), pretty, depth)
	case []any:
		appendPyArray(builder, typed, pretty, depth)
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		appendPyArray(builder, items, pretty, depth)
	default:
		appendPyString(builder, fmt.Sprint(typed))
	}
}

func appendPyObject(builder *strings.Builder, object Ordered, pretty bool, depth int) {
	if len(object) == 0 {
		builder.WriteString("{}")
		return
	}
	if pretty {
		builder.WriteString("{\n")
		for index, field := range object {
			builder.WriteString(strings.Repeat("  ", depth+1))
			appendPyString(builder, field.Key)
			builder.WriteString(": ")
			appendPyJSON(builder, field.Value, pretty, depth+1)
			if index < len(object)-1 {
				builder.WriteString(",")
			}
			builder.WriteString("\n")
		}
		builder.WriteString(strings.Repeat("  ", depth))
		builder.WriteString("}")
		return
	}
	builder.WriteString("{")
	for index, field := range object {
		if index > 0 {
			builder.WriteString(", ")
		}
		appendPyString(builder, field.Key)
		builder.WriteString(": ")
		appendPyJSON(builder, field.Value, pretty, depth)
	}
	builder.WriteString("}")
}

func appendPyArray(builder *strings.Builder, array []any, pretty bool, depth int) {
	if len(array) == 0 {
		builder.WriteString("[]")
		return
	}
	if pretty {
		builder.WriteString("[\n")
		for index, item := range array {
			builder.WriteString(strings.Repeat("  ", depth+1))
			appendPyJSON(builder, item, pretty, depth+1)
			if index < len(array)-1 {
				builder.WriteString(",")
			}
			builder.WriteString("\n")
		}
		builder.WriteString(strings.Repeat("  ", depth))
		builder.WriteString("]")
		return
	}
	builder.WriteString("[")
	for index, item := range array {
		if index > 0 {
			builder.WriteString(", ")
		}
		appendPyJSON(builder, item, pretty, depth)
	}
	builder.WriteString("]")
}

// appendPyString escapes a string exactly as Python's json encoder does:
// quotes, backslashes and C0 controls, with everything else written through
// (ensure_ascii=False).
func appendPyString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(builder, `\u%04x`, r)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
}

// formatPyFloat mirrors Python's repr(float): the shortest round-tripping
// decimal, rendered positionally for exponents in [-4, 16) and in exponent
// form otherwise. The formatter itself lives in internal/tools so the tool
// result serializer and this CLI marshaller cannot drift apart.
func formatPyFloat(value float64) string {
	return tools.FormatPyFloat(value)
}

func sortedObject(source map[string]any) Ordered {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	object := make(Ordered, 0, len(keys))
	for _, key := range keys {
		object = append(object, Field{Key: key, Value: source[key]})
	}
	return object
}
