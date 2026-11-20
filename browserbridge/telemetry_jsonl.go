package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JSONLTelemetry writes upstream-shaped browser telemetry to a daily JSONL
// file (browser-YYYY-MM-DD.jsonl). It is opt-in: Config.TelemetryDir must be
// set, which keeps the bridge's default file footprint empty.
type JSONLTelemetry struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	date string
}

// NewJSONLTelemetry creates a logger rooted at dir. A missing directory is
// created lazily on the first write; constructor errors never break startup.
func NewJSONLTelemetry(dir string) *JSONLTelemetry {
	return &JSONLTelemetry{dir: dir}
}

func (t *JSONLTelemetry) LogEvent(entry map[string]any) {
	t.write(entry)
}

func (t *JSONLTelemetry) ToolCall(_ context.Context, profile, tool string, params map[string]any, result any, err error, duration time.Duration) {
	entry := map[string]any{
		"type":        "tool_call",
		"profile":     profile,
		"tool":        tool,
		"params":      sanitizeParams(params),
		"duration_ms": roundMS(duration),
	}
	if result != nil {
		entry["result"] = truncateAny(result, 1000)
		entry["ok"] = resultOK(result)
	}
	if err != nil {
		entry["error"] = err.Error()
		entry["error_type"] = fmt.Sprintf("%T", err)
		entry["ok"] = false
	}
	t.write(entry)
}

func (t *JSONLTelemetry) ConnectionEvent(event, profile string, fields map[string]any) {
	entry := map[string]any{
		"type":    "connection_event",
		"event":   event,
		"profile": profile,
	}
	for k, v := range fields {
		entry[k] = truncateAny(v, 500)
	}
	t.write(entry)
}

func (t *JSONLTelemetry) write(entry map[string]any) {
	if t == nil || t.dir == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.rotateLocked(); err != nil {
		return
	}
	cp := make(map[string]any, len(entry)+2)
	for k, v := range entry {
		cp[k] = v
	}
	if _, ok := cp["ts"]; !ok {
		cp["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return
	}
	_, _ = t.file.Write(append(data, '\n'))
}

func (t *JSONLTelemetry) rotateLocked() error {
	date := time.Now().UTC().Format("2006-01-02")
	if t.file != nil && t.date == date {
		return nil
	}
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(t.dir, "browser-"+date+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	t.file = f
	t.date = date
	return nil
}

// Close releases the current log file.
func (t *JSONLTelemetry) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

func roundMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func resultOK(result any) bool {
	if m, ok := result.(map[string]any); ok {
		if v, exists := m["ok"].(bool); exists {
			return v
		}
	}
	return true
}

func sanitizeParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "password") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "credential") ||
			strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
			out[k] = "***REDACTED***"
			continue
		}
		out[k] = truncateAny(v, 500)
	}
	return out
}

func truncateAny(v any, maxLen int) any {
	switch x := v.(type) {
	case string:
		if len(x) > maxLen {
			return x[:maxLen] + fmt.Sprintf("... (+%d chars)", len(x)-maxLen)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = truncateAny(val, maxLen)
		}
		return out
	case []any:
		limit := len(x)
		if limit > 10 {
			limit = 10
		}
		out := make([]any, 0, limit+1)
		for _, val := range x[:limit] {
			out = append(out, truncateAny(val, maxLen))
		}
		if len(x) > 10 {
			out = append(out, fmt.Sprintf("... (+%d items)", len(x)-10))
		}
		return out
	default:
		return v
	}
}
