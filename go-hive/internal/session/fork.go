package session

// Parity: framework/server/session_fork.py

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/memory"
)

var sourceOnlyMeta = []string{"colony_spawned", "spawned_colony_id", "superseded_by", "colony_fork"}

var notCarried = map[string]bool{
	"tasks.json":          true,
	"summary.json":        true,
	"reminder_state.json": true,
	"timeline.jsonl":      true,
	"timeline_meta.json":  true,
}

func parkedOnUser() map[string]any {
	return map[string]any{
		"questions":           nil,
		"colony_suggestion":   nil,
		"colony_pivot":        nil,
		"credential_form":     nil,
		"emit_client_request": true,
		"reason":              "turn_done",
	}
}

// Parity: framework/server/session_fork.py::ForkError
type ForkError struct{ Message string }

func (e *ForkError) Error() string { return e.Message }

type Cut struct {
	EventIndex *int
	Seq        *int
}

func intPtr(n int) *int { return &n }

func parseEvent(line string) map[string]any {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return nil
	}
	return event
}

// Parity: framework/server/session_fork.py::_find_cut
func findCut(events []map[string]any, beforeInputAt *string) (Cut, error) {
	if beforeInputAt == nil {
		return Cut{}, nil
	}
	want := *beforeInputAt
	for i, event := range events {
		if event == nil || event["type"] != "client_input_received" || event["timestamp"] != want {
			continue
		}
		correlation, _ := event["correlation_id"].(string)
		for _, later := range events[i+1:] {
			if later == nil || later["type"] != "client_input_committed" {
				continue
			}
			if correlation != "" && later["correlation_id"] != correlation {
				continue
			}
			data, _ := later["data"].(map[string]any)
			if seq, ok := jsonInt(data["seq"]); ok {
				return Cut{EventIndex: intPtr(i), Seq: intPtr(seq)}, nil
			}
			break
		}
		return Cut{}, &ForkError{Message: "That message never reached the conversation (it was still queued); fork from an earlier one."}
	}
	return Cut{}, &ForkError{Message: fmt.Sprintf("No message sent at %s in this session.", want)}
}

func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.SplitAfter(string(data), "\n"), nil
}

func copyEntry(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return atomicfs.CopyFile(src, dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyEntry(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Parity: framework/server/session_fork.py::fork_session_dir
func ForkSessionDir(src, newSessionID string, beforeInputAt *string) (string, error) {
	logPath := filepath.Join(src, "events.jsonl")
	lines, err := readLines(logPath)
	if err != nil {
		return "", err
	}
	events := make([]map[string]any, len(lines))
	for i, line := range lines {
		events[i] = parseEvent(line)
	}
	cut, err := findCut(events, beforeInputAt)
	if err != nil {
		return "", err
	}

	partsDir := filepath.Join(src, "conversations", "parts")
	if cut.Seq != nil {
		if _, err := os.Stat(filepath.Join(partsDir, fmt.Sprintf("%010d.json", *cut.Seq))); err != nil {
			if os.IsNotExist(err) {
				return "", &ForkError{Message: "That point is before this conversation was compacted; fork from a later message."}
			}
			return "", err
		}
	}

	dst := filepath.Join(filepath.Dir(src), newSessionID)
	if err := os.Mkdir(dst, 0o755); err != nil {
		return "", err
	}
	if err := writeFork(src, dst, lines, cut, beforeInputAt); err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	return dst, nil
}

func writeFork(src, dst string, lines []string, cut Cut, beforeInputAt *string) error {
	if err := writeConversation(filepath.Join(src, "conversations"), filepath.Join(dst, "conversations"), cut); err != nil {
		return err
	}
	kept := lines
	if cut.EventIndex != nil {
		kept = lines[:*cut.EventIndex]
	}
	if err := atomicfs.WriteFile(filepath.Join(dst, "events.jsonl"), []byte(strings.Join(kept, "")), 0o644); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "conversations" || name == "events.jsonl" || name == "meta.json" || notCarried[name] {
			continue
		}
		if err := copyEntry(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return writeForkMeta(src, dst, beforeInputAt)
}

// Parity: framework/server/session_fork.py::_write_conversation
func writeConversation(src, dst string, cut Cut) error {
	if err := os.MkdirAll(filepath.Join(dst, "parts"), 0o755); err != nil {
		return err
	}
	parts, err := os.ReadDir(filepath.Join(src, "parts"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Name() < parts[j].Name() })
	lastSeq := -1
	for _, part := range parts {
		if part.IsDir() {
			continue
		}
		seq, ok := ParsePartSeq(part.Name())
		if !ok || (cut.Seq != nil && seq >= *cut.Seq) {
			continue
		}
		if err := copyEntry(filepath.Join(src, "parts", part.Name()), filepath.Join(dst, "parts", part.Name())); err != nil {
			return err
		}
		if seq > lastSeq {
			lastSeq = seq
		}
	}

	entries, err := os.ReadDir(src)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "parts" || entry.Name() == "partials" || entry.Name() == "cursor.json" {
			continue
		}
		if err := copyEntry(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}

	cursor := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(src, "cursor.json")); err == nil {
		_ = json.Unmarshal(data, &cursor)
	}
	if cut.Seq != nil {
		cursor["next_seq"] = *cut.Seq
	} else {
		existing := 0
		if n, ok := jsonInt(cursor["next_seq"]); ok {
			existing = n
		}
		next := lastSeq + 1
		if existing > next {
			next = existing
		}
		cursor["next_seq"] = next
	}
	cursor["pending_input"] = parkedOnUser()
	cursor["recent_responses"] = []any{}
	cursor["recent_tool_fingerprints"] = []any{}
	cursor["user_stopped"] = false
	return writeJSON(filepath.Join(dst, "cursor.json"), cursor)
}

// Parity: framework/server/session_fork.py::_write_meta
func writeForkMeta(src, dst string, beforeInputAt *string) error {
	meta := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(src, "meta.json")); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	for _, key := range sourceOnlyMeta {
		delete(meta, key)
	}
	meta["created_at"] = float64(time.Now().UnixNano()) / float64(time.Second)
	meta["forked_from"] = map[string]any{
		"session_id":      filepath.Base(src),
		"before_input_at": nilIfEmptyPtr(beforeInputAt),
	}
	if err := writeJSON(filepath.Join(dst, "meta.json"), meta); err != nil {
		return err
	}
	return memory.MarkExtracted(dst)
}

func nilIfEmptyPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func writeJSON(path string, v any) error {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	return atomicfs.WriteFile(path, []byte(strings.TrimSuffix(buf.String(), "\n")), 0o644)
}

func AsForkError(err error) (*ForkError, bool) {
	var forkErr *ForkError
	if errors.As(err, &forkErr) {
		return forkErr, true
	}
	return nil, false
}
