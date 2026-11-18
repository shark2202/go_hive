package eventbus

import (
	"fmt"
	"time"
)

type AgentEvent struct {
	Type          EventType
	StreamID      string
	NodeID        *string
	ExecutionID   *string
	Data          map[string]any
	Timestamp     time.Time
	CorrelationID *string
	ColonyID      *string
	RunID         *string
	Seq           int
}

func NewAgentEvent(eventType EventType, streamID string) *AgentEvent {
	return &AgentEvent{
		Type:      eventType,
		StreamID:  streamID,
		Data:      map[string]any{},
		Timestamp: time.Now(),
	}
}

func (e *AgentEvent) EnsureDefaults() {
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
}

func (e *AgentEvent) Clone() *AgentEvent {
	clone := *e
	return &clone
}

func (e *AgentEvent) CloneWithTimestamp(ts time.Time) *AgentEvent {
	clone := *e
	clone.Timestamp = ts
	return &clone
}

func (e *AgentEvent) ToDict() map[string]any {
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	d := map[string]any{
		"type":           string(e.Type),
		"stream_id":      e.StreamID,
		"node_id":        optionalString(e.NodeID),
		"execution_id":   optionalString(e.ExecutionID),
		"data":           data,
		"timestamp":      isoTimestamp(e.Timestamp),
		"correlation_id": optionalString(e.CorrelationID),
		"colony_id":      optionalString(e.ColonyID),
		"seq":            e.Seq,
	}
	if e.RunID != nil {
		d["run_id"] = *e.RunID
	}
	return d
}

func optionalString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func isoTimestamp(t time.Time) string {
	if t.IsZero() {
		return t.Format("2006-01-02T15:04:05")
	}
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s
}

var diskStrippedDataFields = []string{"full_request"}

func eventToDiskDict(event *AgentEvent) map[string]any {
	d := event.ToDict()
	data, ok := d["data"].(map[string]any)
	if !ok {
		return d
	}
	stripped := false
	for _, field := range diskStrippedDataFields {
		if _, present := data[field]; present {
			stripped = true
			break
		}
	}
	if !stripped {
		return d
	}
	trimmed := make(map[string]any, len(data))
	for k, v := range data {
		skip := false
		for _, field := range diskStrippedDataFields {
			if k == field {
				skip = true
				break
			}
		}
		if !skip {
			trimmed[k] = v
		}
	}
	d["data"] = trimmed
	return d
}

func StrPtr(s string) *string       { return &s }
func IntPtr(i int) *int             { return &i }
func Float64Ptr(f float64) *float64 { return &f }
func BoolPtr(b bool) *bool          { return &b }
