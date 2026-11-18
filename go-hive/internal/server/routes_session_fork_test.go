package server

// Parity: core/tests/test_session_fork.py

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const (
	forkT1 = "2026-10-17T09:00:00.000001"
	forkT2 = "2026-10-17T09:05:00.000002"
	forkT3 = "2026-10-17T09:10:00.000003"
)

func forkEvent(eventType, timestamp, correlation string, seq int) string {
	event := map[string]any{"type": eventType, "stream_id": "queen", "timestamp": timestamp}
	if correlation != "" {
		event["correlation_id"] = correlation
	}
	if eventType == "client_input_committed" {
		event["data"] = map[string]any{"seq": seq}
	} else if eventType == "client_input_received" {
		event["data"] = map[string]any{"content": fmt.Sprintf("ask %d", seq)}
	} else {
		event["data"] = map[string]any{"snapshot": "reply", "iteration": seq}
	}
	data, _ := json.Marshal(event)
	return string(data) + "\n"
}

func makeForkableSession(t *testing.T, home hivehome.Home, queen, sid string) string {
	t.Helper()
	dir := home.QueenSessionDir(queen, sid)
	parts := filepath.Join(dir, "conversations", "parts")
	if err := os.MkdirAll(parts, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	for seq := 0; seq < 6; seq++ {
		role := "assistant"
		if seq%2 == 0 {
			role = "user"
		}
		body, _ := json.Marshal(map[string]any{"seq": seq, "role": role, "content": fmt.Sprintf("m%d", seq)})
		if err := os.WriteFile(filepath.Join(parts, fmt.Sprintf("%010d.json", seq)), body, 0o644); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	convsMeta, _ := json.Marshal(map[string]any{"system_prompt": "you are x"})
	if err := os.WriteFile(filepath.Join(dir, "conversations", "meta.json"), convsMeta, 0o644); err != nil {
		t.Fatalf("write conversations meta: %v", err)
	}
	cursor, _ := json.Marshal(map[string]any{
		"next_seq":         6,
		"iteration":        9,
		"recent_responses": []string{"m5"},
		"pending_input":    map[string]any{"reason": "ask_user", "questions": []any{map[string]any{"id": "q"}}},
	})
	if err := os.WriteFile(filepath.Join(dir, "conversations", "cursor.json"), cursor, 0o644); err != nil {
		t.Fatalf("write cursor: %v", err)
	}
	log := ""
	for n, pair := range [][2]string{{forkT1, "0"}, {forkT2, "2"}, {forkT3, "4"}} {
		seq := n * 2
		correlation := fmt.Sprintf("c%d", n)
		log += forkEvent("client_input_received", pair[0], correlation, seq)
		log += forkEvent("client_input_committed", pair[0], correlation, seq)
		log += forkEvent("client_output_delta", pair[0], "", n)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{
		"queen_id":      queen,
		"phase":         "independent",
		"title":         "Trip plan",
		"superseded_by": "session_z",
	})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	attachments := filepath.Join(dir, "data", "attachments")
	if err := os.MkdirAll(attachments, 0o755); err != nil {
		t.Fatalf("mkdir attachments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "photo.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("[]"), 0o644); err != nil {
		t.Fatalf("write tasks: %v", err)
	}
	return dir
}

// Parity: core/tests/test_session_fork.py::test_fork_route_returns_the_new_session_which_history_lists
func TestForkRouteReturnsTheNewSessionWhichHistoryLists(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	src := makeForkableSession(t, home, "queen_x", "session_20261017_090000_aaaa1111")
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})

	recorder, created := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/"+filepath.Base(src)+"/fork", `{"before_input_at": "`+forkT2+`"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if created["queen_id"] != "queen_x" {
		t.Fatalf("queen_id = %#v, want queen_x", created["queen_id"])
	}
	createdID, _ := created["session_id"].(string)
	if createdID == "" {
		t.Fatalf("session_id missing: %#v", created)
	}

	_, history := doSessionJSON(t, mux, http.MethodGet, "/api/sessions/history", "")
	sessions, _ := history["sessions"].([]any)
	var listed map[string]any
	for _, raw := range sessions {
		entry, _ := raw.(map[string]any)
		if entry["session_id"] == createdID {
			listed = entry
			break
		}
	}
	if listed == nil {
		t.Fatalf("forked session %q absent from history: %#v", createdID, history)
	}
	if listed["forked_from"] != filepath.Base(src) {
		t.Fatalf("forked_from = %#v, want %s", listed["forked_from"], filepath.Base(src))
	}
	if listed["title"] != "Trip plan" {
		t.Fatalf("title = %#v, want Trip plan", listed["title"])
	}
}

// Parity: core/tests/test_session_fork.py::test_fork_route_rejects_an_unknown_session
func TestForkRouteRejectsAnUnknownSession(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/session_20990101_000000_deadbeef/fork", `{}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "No such queen conversation" {
		t.Fatalf("error = %#v", body["error"])
	}
}
