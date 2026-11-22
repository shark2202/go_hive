package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeIndexNote(t *testing.T, dir, filename, description, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, filename)
	content := "---\nname: " + filename[:len(filename)-3] + "\ntype: preference\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", filename, err)
	}
	return path
}

// Parity: core/tests/test_memory_reminders.py::test_index_lists_each_scopes_notes
func TestMemoryIndexListsEachScopesNotes(t *testing.T) {
	root := t.TempDir()
	shared, own := filepath.Join(root, "global"), filepath.Join(root, "queen")
	writeIndexNote(t, shared, "tea.md", "What Ava drinks", "")
	writeIndexNote(t, own, "garden.md", "Ava's balcony garden", "")

	got := MemoryIndex([]MemoryIndexScope{
		{Label: "Shared", Dir: shared},
		{Label: "Yours", Dir: own},
		{Label: "Nowhere", Dir: ""},
		{Label: "Empty", Dir: filepath.Join(root, "empty")},
	})
	want := []MemoryIndexScopeListing{
		{Label: "Shared", Folder: shared, Entries: []MemoryIndexEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
		{Label: "Yours", Folder: own, Entries: []MemoryIndexEntry{{Name: "garden.md", Line: "[preference] garden.md: Ava's balcony garden"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index = %#v, want %#v", got, want)
	}
}

// Parity: framework/agents/queen/recall_selector.py::memory_index
// (scan order is newest first; the listing keeps that order).
func TestMemoryIndexKeepsScanOrder(t *testing.T) {
	dir := t.TempDir()
	first := writeIndexNote(t, dir, "tea.md", "What Ava drinks", "")
	second := writeIndexNote(t, dir, "work.md", "Where Ava works", "")
	old := time.Unix(1_000_000, 0)
	newer := time.Unix(2_000_000, 0)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatalf("Chtimes tea: %v", err)
	}
	if err := os.Chtimes(second, newer, newer); err != nil {
		t.Fatalf("Chtimes work: %v", err)
	}

	got := MemoryIndex([]MemoryIndexScope{{Label: "Shared", Dir: dir}})
	wantNames := []string{"work.md", "tea.md"}
	if len(got) != 1 || len(got[0].Entries) != 2 {
		t.Fatalf("index = %#v", got)
	}
	names := []string{got[0].Entries[0].Name, got[0].Entries[1].Name}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("order = %v, want %v", names, wantNames)
	}
}
