package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	forkT1 = "2026-10-17T09:00:00.000001"
	forkT2 = "2026-10-17T09:05:00.000002"
	forkT3 = "2026-10-17T09:10:00.000003"
)

func forkEvent(t *testing.T, typ, timestamp string, extra map[string]any) string {
	t.Helper()
	event := map[string]any{"type": typ, "stream_id": "queen", "timestamp": timestamp}
	for k, v := range extra {
		event[k] = v
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(data) + "\n"
}

func makeForkSession(t *testing.T) string {
	t.Helper()
	sid := "session_20261017_090000_aaaa1111"
	dir := filepath.Join(t.TempDir(), "queen_x", "sessions", sid)
	parts := filepath.Join(dir, "conversations", "parts")
	if err := os.MkdirAll(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	for seq := 0; seq < 6; seq++ {
		role := "user"
		if seq%2 != 0 {
			role = "assistant"
		}
		writeJSONFile(t, filepath.Join(parts, pad10(seq)+".json"), map[string]any{
			"seq": seq, "role": role, "content": "m" + itoa(seq),
		})
	}
	writeJSONFile(t, filepath.Join(dir, "conversations", "meta.json"), map[string]any{"system_prompt": "you are x"})
	writeJSONFile(t, filepath.Join(dir, "conversations", "cursor.json"), map[string]any{
		"next_seq":         6,
		"iteration":        9,
		"recent_responses": []any{"m5"},
		"pending_input":    map[string]any{"reason": "ask_user", "questions": []any{map[string]any{"id": "q"}}},
	})

	log := &strings.Builder{}
	for n, pair := range [][2]any{{forkT1, 0}, {forkT2, 2}, {forkT3, 4}} {
		ts := pair[0].(string)
		seq := pair[1].(int)
		correlation := "c" + itoa(n)
		log.WriteString(forkEvent(t, "client_input_received", ts, map[string]any{
			"correlation_id": correlation, "data": map[string]any{"content": "ask " + itoa(n)},
		}))
		log.WriteString(forkEvent(t, "client_input_committed", ts, map[string]any{
			"correlation_id": correlation, "data": map[string]any{"seq": seq},
		}))
		log.WriteString(forkEvent(t, "client_output_delta", ts, map[string]any{
			"data": map[string]any{"snapshot": "reply " + itoa(n), "iteration": n},
		}))
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(log.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dir, "meta.json"), map[string]any{
		"queen_id": "queen_x", "phase": "independent", "title": "Trip plan", "superseded_by": "session_z",
	})
	attachments := filepath.Join(dir, "data", "attachments")
	if err := os.MkdirAll(attachments, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "photo.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

func readEventTypes(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		types = append(types, event["type"].(string))
	}
	return types
}

// Parity: core/tests/test_session_fork.py::test_fork_before_a_message_keeps_only_what_came_before_it
func TestForkBeforeMessageKeepsOnlyWhatCameBeforeIt(t *testing.T) {
	src := makeForkSession(t)
	T2 := forkT2
	dst, err := ForkSessionDir(src, "session_20261017_100000_bbbb2222", &T2)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dst, "conversations", "parts"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "0000000000.json,0000000001.json"; got != want {
		t.Errorf("parts = %s, want %s", got, want)
	}

	types := readEventTypes(t, filepath.Join(dst, "events.jsonl"))
	if got, want := strings.Join(types, ","), "client_input_received,client_input_committed,client_output_delta"; got != want {
		t.Errorf("event types = %s, want %s", got, want)
	}

	cursor := readJSONMap(t, filepath.Join(dst, "conversations", "cursor.json"))
	if next, _ := jsonInt(cursor["next_seq"]); next != 2 {
		t.Errorf("next_seq = %v, want 2", cursor["next_seq"])
	}
	if iter, _ := jsonInt(cursor["iteration"]); iter != 9 {
		t.Errorf("iteration = %v, want 9", cursor["iteration"])
	}
	if responses, ok := cursor["recent_responses"].([]any); !ok || len(responses) != 0 {
		t.Errorf("recent_responses = %v, want []", cursor["recent_responses"])
	}
	pending, _ := cursor["pending_input"].(map[string]any)
	if pending["reason"] != "turn_done" {
		t.Errorf("pending_input.reason = %v, want turn_done", pending["reason"])
	}
}

// Parity: core/tests/test_session_fork.py::test_fork_carries_the_session_files_but_not_its_plan_or_fate
func TestForkCarriesSessionFilesButNotPlanOrFate(t *testing.T) {
	src := makeForkSession(t)
	T3 := forkT3
	dst, err := ForkSessionDir(src, "session_20261017_100000_bbbb2222", &T3)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	photo, err := os.ReadFile(filepath.Join(dst, "data", "attachments", "photo.png"))
	if err != nil || string(photo) != "png" {
		t.Errorf("attachment = %q, %v", photo, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "conversations", "meta.json")); err != nil {
		t.Errorf("conversations/meta.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "tasks.json")); !os.IsNotExist(err) {
		t.Errorf("tasks.json must not be carried, stat err = %v", err)
	}

	meta := readJSONMap(t, filepath.Join(dst, "meta.json"))
	if meta["title"] != "Trip plan" {
		t.Errorf("title = %v, want Trip plan", meta["title"])
	}
	forkedFrom, _ := meta["forked_from"].(map[string]any)
	if forkedFrom["session_id"] != filepath.Base(src) {
		t.Errorf("forked_from.session_id = %v, want %s", forkedFrom["session_id"], filepath.Base(src))
	}
	if forkedFrom["before_input_at"] != T3 {
		t.Errorf("forked_from.before_input_at = %v, want %s", forkedFrom["before_input_at"], T3)
	}
	if _, ok := meta["superseded_by"]; ok {
		t.Error("superseded_by must be stripped from the fork")
	}

	timelineMeta := readJSONMap(t, filepath.Join(dst, "timeline_meta.json"))
	if turns, _ := jsonInt(timelineMeta["user_turns"]); turns != 2 {
		t.Errorf("timeline_meta.user_turns = %v, want 2", timelineMeta["user_turns"])
	}
}

// Parity: core/tests/test_session_fork.py::test_fork_without_a_point_copies_the_whole_session
func TestForkWithoutPointCopiesWholeSession(t *testing.T) {
	src := makeForkSession(t)
	dst, err := ForkSessionDir(src, "session_20261017_100000_bbbb2222", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dst, "conversations", "parts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Errorf("parts = %d, want 6", len(entries))
	}

	srcEvents, _ := os.ReadFile(filepath.Join(src, "events.jsonl"))
	dstEvents, _ := os.ReadFile(filepath.Join(dst, "events.jsonl"))
	if string(srcEvents) != string(dstEvents) {
		t.Error("events.jsonl differs from the source")
	}

	cursor := readJSONMap(t, filepath.Join(dst, "conversations", "cursor.json"))
	if next, _ := jsonInt(cursor["next_seq"]); next != 6 {
		t.Errorf("next_seq = %v, want 6", cursor["next_seq"])
	}
}

// Parity: core/tests/test_session_fork.py::test_unknown_point_is_refused_and_leaves_nothing_behind
func TestUnknownForkPointIsRefusedLeavesNothing(t *testing.T) {
	src := makeForkSession(t)
	unknown := "2026-01-01T00:00:00"
	dst := filepath.Join(filepath.Dir(src), "session_20261017_100000_bbbb2222")

	_, err := ForkSessionDir(src, "session_20261017_100000_bbbb2222", &unknown)
	if err == nil || !strings.Contains(err.Error(), "No message sent at") {
		t.Fatalf("err = %v, want ForkError with No message sent at", err)
	}
	if _, ok := AsForkError(err); !ok {
		t.Errorf("err is not a ForkError: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("fork dir must not exist, stat err = %v", statErr)
	}
}

// Parity: core/tests/test_session_fork.py::test_point_lost_to_compaction_is_refused
func TestForkPointLostToCompactionIsRefused(t *testing.T) {
	src := makeForkSession(t)
	if err := os.Remove(filepath.Join(src, "conversations", "parts", "0000000002.json")); err != nil {
		t.Fatal(err)
	}
	T2 := forkT2
	dst := filepath.Join(filepath.Dir(src), "session_20261017_100000_bbbb2222")

	_, err := ForkSessionDir(src, "session_20261017_100000_bbbb2222", &T2)
	if err == nil || !strings.Contains(err.Error(), "compacted") {
		t.Fatalf("err = %v, want a compaction refusal", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("fork dir must not exist, stat err = %v", statErr)
	}
}
