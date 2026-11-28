package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// mcpOrderedObject is a JSON object decoded with its key order intact.
//
// Python's HTTP transport hands the model `result["content"][0]` directly to
// json.dumps (core/framework/loader/tool_registry.py::_wrap_result), and
// json.dumps preserves the dict insertion order json.loads produced. A Go
// map[string]any cannot: the decode drops the order and PythonJSONDumps sorts
// keys. The HTTP client therefore swaps a first, non-text content block for
// this type, which renders the server's original order.
type mcpOrderedObject struct {
	keys   []string
	values map[string]any
}

func newMCPOrderedObject() *mcpOrderedObject {
	return &mcpOrderedObject{values: map[string]any{}}
}

// set records one member. Duplicate JSON keys keep the first position and the
// last value, matching Python's json.loads (dict assignment keeps the slot).
func (o *mcpOrderedObject) set(key string, value any) {
	if _, present := o.values[key]; !present {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Lookup reads one member without disturbing the order.
func (o *mcpOrderedObject) Lookup(key string) (any, bool) {
	value, found := o.values[key]
	return value, found
}

// MarshalJSON renders the object in its recorded order. PythonJSONDumps is the
// normal path; this keeps any other encoder (trace log, test helper) from
// emitting "{}" for a struct with unexported fields.
func (o *mcpOrderedObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	writeMCPOrderedObject(&b, o)
	return []byte(b.String()), nil
}

// writeMCPOrderedObject renders an ordered object with Python's json.dumps
// separators (", " / ": "), matching PythonJSONDumps and the frozen probe
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func writeMCPOrderedObject(b *strings.Builder, o *mcpOrderedObject) {
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteString(", ")
		}
		writePythonJSON(b, key)
		b.WriteString(": ")
		writePythonJSON(b, o.values[key])
	}
	b.WriteByte('}')
}

// decodeMCPOrderedJSON decodes one JSON value, turning every object into an
// *mcpOrderedObject and keeping arrays/scalars as ordinary Go values. Numbers
// stay json.Number so PythonJSONDumps writes the server's literal unchanged.
func decodeMCPOrderedJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return decodeMCPOrderedValue(dec)
}

func decodeMCPOrderedValue(dec *json.Decoder) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := newMCPOrderedObject()
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("JSON object key is %T, want string", keyToken)
			}
			value, err := decodeMCPOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			object.set(key, value)
		}
		// Consume the closing '}'.
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		items := []any{}
		for dec.More() {
			item, err := decodeMCPOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		// Consume the closing ']'.
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return items, nil
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

// mcpOrderFirstNonTextContentBlock re-decodes the first content block with key
// order preserved when the legacy HTTP content list starts with a non-text
// object. Python returns that object as-is and serializes it with json.dumps;
// text-first lists return the text value, so their order never reaches the
// model and the plain map stays untouched.
func mcpOrderFirstNonTextContentBlock(body []byte, content []any) []any {
	if len(content) == 0 {
		return content
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		return content
	}
	if _, hasText := block["text"]; hasText {
		return content
	}

	var envelope struct {
		Result struct {
			Content []json.RawMessage `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Result.Content) == 0 {
		return content
	}
	ordered, err := decodeMCPOrderedJSON(envelope.Result.Content[0])
	if err != nil {
		return content
	}
	if _, ok := ordered.(*mcpOrderedObject); !ok {
		return content
	}

	out := make([]any, len(content))
	copy(out, content)
	out[0] = ordered
	return out
}
