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
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
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
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}

func ISOTimeString(t time.Time) string {
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000")
	}
	return t.Format("2006-01-02T15:04:05")
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
