package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/runtimehealth"
)

// Parity-exempt: framework/server/routes_sessions.py::handle_get_session_snapshot
func TestSessionSnapshotAnswersProjectionOrNotFound(t *testing.T) {
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_live", EventBus: eventbus.NewEventBus()})
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})

	recorder, snapshot := doSessionJSON(t, mux, http.MethodGet, "/api/sessions/session_live/snapshot", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, key := range []string{"is_executing", "awaiting_input", "current_tool_calls", "snapshot_seq"} {
		if _, present := snapshot[key]; !present {
			t.Fatalf("snapshot missing %q: %#v", key, snapshot)
		}
	}
	if snapshot["is_executing"] != false {
		t.Fatalf("is_executing = %#v, want false on a fresh bus", snapshot["is_executing"])
	}

	missing, body := doSessionJSON(t, mux, http.MethodGet, "/api/sessions/session_absent/snapshot", "")
	if missing.Code != http.StatusNotFound || body["error"] != "session not found" {
		t.Fatalf("unknown session = %d %#v", missing.Code, body)
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_live_sessions_stream
func TestLiveSessionsStreamEmitsSnapshotFrame(t *testing.T) {
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_live", EventBus: eventbus.NewEventBus()})
	deps := SessionRouteDeps{
		Manager:          manager,
		LivePollInterval: 10 * time.Millisecond,
		RuntimeNetwork: func() runtimehealth.Snapshot {
			return runtimehealth.Snapshot{Degraded: true, Reason: strPtr("offline")}
		},
	}
	server := httptest.NewServer(sessionRoutesMux(deps))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sessions/live", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET live: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(response.Body)
	var lines []string
	for len(lines) < 2 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v (got %#v)", err, lines)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	cancel()

	if lines[0] != "event: snapshot" {
		t.Fatalf("event line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("data line = %q", lines[1])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	sessions, _ := payload["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v, want one row", payload["sessions"])
	}
	row, _ := sessions[0].(map[string]any)
	if row["session_id"] != "session_live" {
		t.Fatalf("session_id = %#v", row["session_id"])
	}
	if row["active_worker_count"] != float64(0) {
		t.Fatalf("active_worker_count = %#v, want 0", row["active_worker_count"])
	}
	network, _ := payload["network"].(map[string]any)
	if network["degraded"] != true || network["reason"] != "offline" {
		t.Fatalf("network row = %#v", payload["network"])
	}
}
