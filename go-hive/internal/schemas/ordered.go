package schemas

// OrderedMap is a JSON object that keeps the key order it was decoded from.
//
// Python dicts preserve insertion order, so a goal's ``context`` renders in the
// order the JSON author wrote it (framework/schemas/goal.py::Goal.
// to_prompt_context iterates ``self.context.items()``). A plain Go
// map loses that order and encoding/json sorts keys on output, so the goal
// schema decodes context through this type instead: decode order in, same
// order out, both for the prompt block and for the marshalled JSON.
//
// The zero value is a valid empty object and marshals as ``{}``. Values are
// kept as decoded by encoding/json (float64 numbers, map[string]any for nested
// objects), the same shape the previous map[string]any field produced.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// OrderedMap is a JSON object with insertion-order keys.
type OrderedMap struct {
	keys   []string
	values map[string]any
}

// NewOrderedMap returns an empty ordered map.
func NewOrderedMap() OrderedMap {
	return OrderedMap{values: map[string]any{}}
}

// Len reports the number of keys.
func (m OrderedMap) Len() int { return len(m.values) }

// Keys returns the keys in insertion order.
func (m OrderedMap) Keys() []string {
	if len(m.keys) == 0 {
		return nil
	}
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

// Get returns the value stored under key.
func (m OrderedMap) Get(key string) (any, bool) {
	value, ok := m.values[key]
	return value, ok
}

// Set stores value under key. A new key is appended, so assignment order
// decides the order callers that build a goal in Go observe.
func (m *OrderedMap) Set(key string, value any) {
	if m.values == nil {
		m.values = map[string]any{}
	}
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Delete removes key, keeping the remaining order.
func (m *OrderedMap) Delete(key string) {
	if _, exists := m.values[key]; !exists {
		return
	}
	delete(m.values, key)
	for i, existing := range m.keys {
		if existing == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			return
		}
	}
}

// UnmarshalJSON decodes an object token by token so key order survives. A JSON
// null decodes to the empty map, matching the old nil-map handling.
func (m *OrderedMap) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*m = OrderedMap{}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("schemas: context must be a JSON object, got %s", string(trimmed))
	}
	ordered := NewOrderedMap()
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("schemas: context key must be a string, got %T", keyToken)
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		ordered.Set(key, value)
	}
	if _, err := decoder.Token(); err != nil { // closing '}'
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("schemas: trailing data after context object")
	}
	*m = ordered
	return nil
}

// MarshalJSON writes the keys in insertion order.
func (m OrderedMap) MarshalJSON() ([]byte, error) {
	if len(m.values) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encodedValue, err := json.Marshal(m.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(encodedValue)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
