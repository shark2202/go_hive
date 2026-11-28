package schemas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Timestamp struct {
	time.Time
}

func NewTimestamp(t time.Time) Timestamp { return Timestamp{Time: t} }

func NowTimestamp() Timestamp { return Timestamp{Time: time.Now()} }

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(PythonISOString(t.Time))
}

// PythonISOString renders a datetime the way Python's datetime.isoformat()
// does: microsecond precision (six digits, trailing zeros kept) and the UTC
// offset only when the value is timezone-aware. A naive value — what
// datetime.now() produces and what the schemas write — has no suffix.
func PythonISOString(t time.Time) string {
	base := t.Format("2006-01-02T15:04:05")
	if micro := t.Nanosecond() / 1000; micro != 0 {
		base = fmt.Sprintf("%s.%06d", base, micro)
	}
	// time.Local stands for the naive datetime Python writes; any other
	// location carries an explicit offset (Z parsed as UTC prints +00:00,
	// as fromisoformat(...).isoformat() does).
	if t.Location() == time.Local {
		return base
	}
	return base + t.Format("-07:00")
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if bytes.Equal(trimmed, []byte("null")) {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return err
	}
	parsed, err := ParseISOTime(s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

func (t Timestamp) IsZero() bool { return t.Time.IsZero() }

func ParseISOTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	// Values with an offset keep their instant; naive values are local time,
	// matching Python's datetime.fromisoformat (and the naive datetimes the
	// reference schemas write).
	zoned := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
	}
	for _, layout := range zoned {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	naive := []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range naive {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}

func ISOTimeString(t time.Time) string {
	return PythonISOString(t)
}

func ToDict(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func FromDict(m map[string]any, v any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func unmarshalStruct(data []byte, alias any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, alias); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	known, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	var knownKeys map[string]json.RawMessage
	if err := json.Unmarshal(known, &knownKeys); err != nil {
		return nil, err
	}
	extras := make(map[string]json.RawMessage)
	for k, v := range raw {
		if _, ok := knownKeys[k]; !ok {
			extras[k] = v
		}
	}
	if len(extras) == 0 {
		return nil, nil
	}
	return extras, nil
}

func mergeExtras(base []byte, extras map[string]json.RawMessage) ([]byte, error) {
	if len(extras) == 0 {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range extras {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func OrEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func OrEmptyMapString(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func OrEmptyMapInt(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

// requireFields enforces Pydantic's required-field rule on a decoded object:
// the key must be present and must not be JSON null. Empty strings stay valid
// (Pydantic accepts "" for a `str` field).
func requireFields(raw map[string]json.RawMessage, kind string, fields ...string) error {
	for _, field := range fields {
		value, ok := raw[field]
		if !ok {
			return fmt.Errorf("%s: missing required field %q", kind, field)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s: required field %q must not be null", kind, field)
		}
	}
	return nil
}

// requirePresent is requireFields for a `target: Any` style field, where JSON
// null is a legitimate value.
func requirePresent(raw map[string]json.RawMessage, kind string, fields ...string) error {
	for _, field := range fields {
		if _, ok := raw[field]; !ok {
			return fmt.Errorf("%s: missing required field %q", kind, field)
		}
	}
	return nil
}
