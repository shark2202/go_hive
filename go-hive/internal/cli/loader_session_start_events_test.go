package cli

// Cold-session opening events: POST /api/sessions must leave the new session's
// events.jsonl carrying the same five-event opening upstream's queen writes
// before any user turn, so /events/history, /snapshot and /stats answer with
// data on a session that has not chatted yet.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

func readSessionEventLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var events []map[string]any
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event := map[string]any{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return events
}

func createOpeningEventsSession(t *testing.T, home hivehome.Home, manager *hiveserver.SessionManager, live *[]*hiveserver.Session, liveMu *sync.Mutex) *hiveserver.Session {
	t.Helper()
	var output bytes.Buffer
	runner := &Runner{Stdout: &output, Stderr: &output}
	sessionID := layoutSession
	queenName := layoutQueen
	record, err := runner.createChatSession(
		hiveserver.CreateSessionParams{SessionID: &sessionID, QueenName: &queenName},
		manager, liveMu, live, home, "",
	)
	if err != nil {
		t.Fatalf("createChatSession: %v (output: %s)", err, output.String())
	}
	t.Cleanup(func() {
		if record.Chat != nil {
			_ = record.Chat.Close()
		}
	})
	return record
}

// Parity: framework/server/session_manager.py::create_session
func TestCreateChatSessionWritesOpeningEvents(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()
	live := make([]*hiveserver.Session, 0, 1)
	liveMu := &sync.Mutex{}
	record := createOpeningEventsSession(t, home, manager, &live, liveMu)

	eventsPath := filepath.Join(home.QueenSessionDir(layoutQueen, layoutSession), "events.jsonl")
	events := readSessionEventLog(t, eventsPath)
	wantTypes := []string{
		"queen_identity_selected",
		"reminder_injected",
		"node_loop_started",
		"node_loop_iteration",
		"context_usage_updated",
	}
	got := make([]string, 0, len(events))
	for _, event := range events {
		value, _ := event["type"].(string)
		got = append(got, value)
	}
	if strings.Join(got, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("opening events = %v, want %v", got, wantTypes)
	}

	identityData, _ := events[0]["data"].(map[string]any)
	if identityData["queen_id"] != layoutQueen || identityData["name"] != "Steve Jobs" || identityData["title"] != "Head of Growth" {
		t.Fatalf("queen_identity_selected data = %#v", identityData)
	}
	reminderData, _ := events[1]["data"].(map[string]any)
	if reminderData["source"] != "point:session_start" || events[1]["execution_id"] != layoutSession {
		t.Fatalf("reminder_injected = %#v (execution %#v)", reminderData, events[1]["execution_id"])
	}
	usageData, _ := events[4]["data"].(map[string]any)
	if usageData["trigger"] != "iteration_start" || usageData["message_count"] != float64(0) {
		t.Fatalf("context_usage_updated data = %#v", usageData)
	}
	if _, present := usageData["breakdown"]; !present {
		t.Fatalf("context_usage_updated lacks breakdown: %#v", usageData)
	}

	// The resume retry the dashboard sends must reuse the live session without
	// appending a second opening block.
	var output bytes.Buffer
	runner := &Runner{Stdout: &output, Stderr: &output}
	resumeFrom := layoutSession
	queenName := layoutQueen
	retried, err := runner.createChatSession(
		hiveserver.CreateSessionParams{QueenResumeFrom: &resumeFrom, QueenName: &queenName},
		manager, liveMu, &live, home, "",
	)
	if err != nil {
		t.Fatalf("resume retry: %v (output: %s)", err, output.String())
	}
	if retried != record {
		t.Fatal("the resume retry replaced the live session instead of reusing it")
	}
	if got := len(readSessionEventLog(t, eventsPath)); got != len(wantTypes) {
		t.Fatalf("events after the resume retry = %d, want %d", got, len(wantTypes))
	}
	_ = record
}
