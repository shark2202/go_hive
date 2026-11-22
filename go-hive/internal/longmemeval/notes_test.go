package longmemeval

// Parity: tests/e2e/test_longmemeval.py (HIVE_LME_NOTES=1) and
// tests/e2e/longmemeval.py::build_notes — the Go note build must reflect on
// the seeded sessions oldest first, write into the home's memory scopes, and
// cache the result per question.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

// scriptedNotesWriter stands in for the notes model: it writes one queen
// note per reflection, named after the call, and stops once a tool result is
// in the conversation.
type scriptedNotesWriter struct {
	mu    sync.Mutex
	seen  []string
	calls int
}

func (w *scriptedNotesWriter) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return llm.Completion{Content: "noted"}, nil
		}
	}
	w.calls++
	user := ""
	if len(req.Messages) > 0 {
		user = req.Messages[0].Content
	}
	w.seen = append(w.seen, user)
	name := fmt.Sprintf("note-%d.md", w.calls)
	args, err := json.Marshal(map[string]any{
		"filename": name,
		"scope":    "queen",
		"content":  "---\nname: " + name + "\ndescription: written by the scripted reflection\ntype: preference\n---\nbody\n",
	})
	if err != nil {
		return llm.Completion{}, err
	}
	return llm.Completion{ToolCalls: []llm.ToolCall{{
		ID:        fmt.Sprintf("call_%d", w.calls),
		Name:      "write_memory_file",
		Arguments: string(args),
	}}}, nil
}

func notesFixtureItem() Item {
	return Item{
		QuestionID:   "notes_q1",
		QuestionType: "single-session-user",
		Question:     "where did I put the key?",
		Answer:       TextAnswer("in the drawer"),
		QuestionDate: "2023/05/30 (Tue) 23:40",
		// Deliberately out of order: the build must reflect oldest first.
		HaystackSessionIDs: []string{"s_late", "s_early"},
		HaystackDates:      []string{"2023/05/30 (Tue) 10:00", "2023/05/20 (Sat) 09:00"},
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "late session user"}, {Role: "assistant", Content: "late reply"}},
			{{Role: "user", Content: "early session user"}, {Role: "assistant", Content: "early reply"}},
		},
		AnswerSessionIDs: []string{"s_early"},
	}
}

func TestBuildNotesForHomeReflectsInOrderAndCaches(t *testing.T) {
	writer := &scriptedNotesWriter{}
	cacheDir := filepath.Join(t.TempDir(), "notes_cache")
	cfg := NotesConfig{
		Writer:   writer,
		CacheDir: cacheDir,
		Model:    "fake-notes-model",
		Variant:  VariantS,
		QueenID:  "queen_technology",
	}
	item := notesFixtureItem()

	home := t.TempDir()
	result, err := BuildNotesForHome(context.Background(), cfg, home, item)
	if err != nil {
		t.Fatalf("BuildNotesForHome: %v", err)
	}
	if result.Cached {
		t.Fatalf("first build reported cached=%t", result.Cached)
	}
	if result.Count != 2 {
		t.Fatalf("notes = %d, want 2", result.Count)
	}
	queenDir := memory.QueenMemoryDir(home, "queen_technology")
	entries, err := os.ReadDir(queenDir)
	if err != nil {
		t.Fatalf("queen memory dir: %v", err)
	}
	md := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			md++
		}
	}
	if md != 2 {
		t.Fatalf("queen notes = %d, want 2", md)
	}
	if len(writer.seen) != 2 {
		t.Fatalf("reflections = %d, want 2", len(writer.seen))
	}
	if !strings.Contains(writer.seen[0], "early session user") ||
		!strings.Contains(writer.seen[0], "Timestamp: 2023-05-20T09:00") {
		t.Fatalf("first reflection was not the oldest session: %q", writer.seen[0])
	}
	if !strings.Contains(writer.seen[1], "late session user") {
		t.Fatalf("second reflection was not the later session: %q", writer.seen[1])
	}
	if _, err := os.Stat(NotesCachePath(cfg, item.QuestionID)); err != nil {
		t.Fatalf("cache missing: %v", err)
	}

	warm := t.TempDir()
	cached, err := BuildNotesForHome(context.Background(), cfg, warm, item)
	if err != nil {
		t.Fatalf("BuildNotesForHome (cached): %v", err)
	}
	if !cached.Cached || cached.Count != 2 {
		t.Fatalf("cached result = %+v", cached)
	}
	if writer.calls != 2 {
		t.Fatalf("cache hit still called the model (%d calls)", writer.calls)
	}
	if _, err := os.Stat(filepath.Join(memory.QueenMemoryDir(warm, "queen_technology"), "note-1.md")); err != nil {
		t.Fatalf("cached note not copied into the home: %v", err)
	}
}

func TestNotesTelemetryReadsTheConversation(t *testing.T) {
	home := t.TempDir()
	answer := Answer{Turns: []map[string]any{
		{"role": "user", "content": "[2023-05-30 23:40] [External event] <system-reminder>\n" + agentloop.MemoryRecallHeader + "\n\n--- Global Memories ---\n...\n</system-reminder>"},
		{"role": "system", "content": agentloop.MemoryIndexHeader + "\n\n### note-1.md"},
		{"role": "assistant", "tool_calls": []any{map[string]any{
			"id": "call_1",
			"function": map[string]any{
				"name":      "terminal_exec",
				"arguments": `{"command":"cat memories/agents/queens/queen_technology/note-1.md note-2.md"}`,
			},
		}}},
	}}
	extra := NotesTelemetry(home, NotesResult{Count: 2}, answer)
	if extra["notes"] != 2 {
		t.Fatalf("notes = %v", extra["notes"])
	}
	if extra["notes_mode"] != memory.MemoryRecallIndex {
		t.Fatalf("notes_mode = %v, want index", extra["notes_mode"])
	}
	if extra["notes_recalled"] != true || extra["notes_indexed"] != true {
		t.Fatalf("recall/index telemetry = %v/%v", extra["notes_recalled"], extra["notes_indexed"])
	}
	read, _ := extra["notes_read"].([]string)
	if len(read) != 2 || read[0] != "note-1.md" || read[1] != "note-2.md" {
		t.Fatalf("notes_read = %v", read)
	}
}
