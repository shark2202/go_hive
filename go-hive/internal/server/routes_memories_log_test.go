package server

// Parity: core/tests/test_routes_memories.py — the Memory page's edits are the
// owner's, its deletions retire, and every change can be undone. The field
// lists below are frozen from docs/e2e-assets/memory-notes/golden_api.json
// (Python 1.0.15 capture), so a renamed or dropped key fails here.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
)

const routesLogNote = "---\nname: Tea\ndescription: What Ava drinks\ntype: preference\n---\n\nAva drinks oolong.\n"

func seedRoutesMemory(t *testing.T, home hivehome.Home, rel, content string) string {
	t.Helper()
	dir := filepath.Join(home.MemoriesDir(), filepath.FromSlash(filepath.Dir(rel)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	name := filepath.Base(rel)
	text, err := memory.Write(dir, name, content, memory.WriteOptions{Author: "reflection", Reason: "she said so", Now: time.Date(2026, 3, 2, 9, 30, 0, 0, time.Local)})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	return text
}

func memoryKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func assertKeys(t *testing.T, label string, value map[string]any, want []string) {
	t.Helper()
	got := memoryKeys(value)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s keys = %v, want %v (%#v)", label, got, want, value)
	}
}

// Parity: core/tests/test_routes_memories.py::test_an_edit_on_the_page_makes_the_file_the_owners
func TestAnEditOnThePageMakesTheFileTheOwners(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Ftea.md",
		`{"content":"---\nname: Tea\ntype: preference\n---\n\nAva drinks sencha.\n"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %#v", recorder.Code, body)
	}
	if body["author"] != "owner" {
		t.Fatalf("author = %#v", body["author"])
	}
	if _, err := memory.Write(filepath.Join(home.MemoriesDir(), "global"), "tea.md", routesLogNote,
		memory.WriteOptions{Author: "reflection"}); !memory.IsOwnerWritten(err) {
		t.Fatalf("reflection write after page edit = %v, want OwnerWritten", err)
	}
}

// Parity: core/tests/test_routes_memories.py::test_the_list_says_who_wrote_each_file_and_when_it_was_confirmed
func TestTheListSaysWhoWroteEachFileAndWhenItWasConfirmed(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %#v", recorder.Code, body)
	}
	entries, _ := body["memories"].([]any)
	if len(entries) != 1 {
		t.Fatalf("memories = %#v", body["memories"])
	}
	entry := entries[0].(map[string]any)
	if entry["author"] != "reflection" || entry["confirmed"] != entry["learned"] || entry["confirmed"] == nil {
		t.Fatalf("provenance = %#v", entry)
	}
	assertKeys(t, "list entry", entry, []string{
		"author", "confirmed", "description", "filename", "learned", "mtime", "name", "path", "scope", "scope_name", "type",
	})
}

// Parity: core/tests/test_routes_memories.py::test_the_log_is_not_listed_as_a_memory
func TestTheLogIsNotListedAsAMemory(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	_, body := memoryRequest(t, mux, http.MethodGet, "/api/memories", "")
	entries := body["memories"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["filename"] != "tea.md" {
		t.Fatalf("memories = %#v", entries)
	}
}

// Parity: core/tests/test_routes_memories.py::test_deleting_on_the_page_retires_the_file_and_it_can_be_restored
func TestDeletingOnThePageRetiresAndItCanBeRestored(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	written := seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodDelete, "/api/memories/file?path=global%2Ftea.md", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "global/tea.md" {
		t.Fatalf("delete = %d %#v", recorder.Code, body)
	}
	if _, err := os.Stat(filepath.Join(home.MemoriesDir(), "global", "tea.md")); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}

	recorder, body = memoryRequest(t, mux, http.MethodGet, "/api/memories/retired", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("retired = %d %#v", recorder.Code, body)
	}
	goneList, _ := body["retired"].([]any)
	if len(goneList) != 1 {
		t.Fatalf("retired = %#v", body["retired"])
	}
	gone := goneList[0].(map[string]any)
	if gone["path"] != "global/tea.md" || gone["author"] != "owner" || gone["scope"] != "global" {
		t.Fatalf("retired entry = %#v", gone)
	}
	assertKeys(t, "retired entry", gone, []string{"at", "author", "filename", "n", "path", "reason", "scope", "scope_name"})

	restoreBody, _ := json.Marshal(map[string]any{"n": gone["n"]})
	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Ftea.md", string(restoreBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("restore = %d %#v", recorder.Code, body)
	}
	data, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "tea.md"))
	if string(data) != written {
		t.Fatalf("restored file = %q, want %q", data, written)
	}
	_, body = memoryRequest(t, mux, http.MethodGet, "/api/memories/retired", "")
	if retired, _ := body["retired"].([]any); len(retired) != 0 {
		t.Fatalf("retired after restore = %#v", body["retired"])
	}
}

// Parity: core/tests/test_routes_memories.py::test_history_lists_every_change_to_a_file
func TestHistoryListsEveryChangeToAFile(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, _ := memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Ftea.md",
		`{"content":"---\nname: Tea\ntype: preference\n---\n\nAva drinks sencha.\n"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d", recorder.Code)
	}

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/history?path=global%2Ftea.md", "")
	if recorder.Code != http.StatusOK || body["path"] != "global/tea.md" {
		t.Fatalf("history = %d %#v", recorder.Code, body)
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("changes = %#v", body["changes"])
	}
	first := changes[0].(map[string]any)
	second := changes[1].(map[string]any)
	if first["n"] != float64(0) || first["author"] != "reflection" || first["reason"] != "she said so" {
		t.Fatalf("first change = %#v", first)
	}
	if second["n"] != float64(1) || second["author"] != "owner" || second["reason"] != "edited on the Memory page" || second["op"] != "write" {
		t.Fatalf("second change = %#v", second)
	}
	assertKeys(t, "history change", first, []string{"after", "at", "author", "before", "file", "n", "op", "reason", "sources"})
}

// Parity: core/tests/test_routes_memories.py::test_restoring_an_unknown_change_is_not_found
func TestRestoringAnUnknownChangeIsNotFound(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Ftea.md", `{"n":9}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "tea.md has no change 9 to go back to" {
		t.Fatalf("restore = %d %#v", recorder.Code, body)
	}
}

// Parity: core/tests/test_routes_memories.py::test_restoring_needs_a_change_number
func TestRestoringNeedsAChangeNumber(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	for _, payload := range []string{`{}`, `{"n":"0"}`, `{"n":true}`, `{"n":0.5}`, `["0"]`} {
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Ftea.md", payload)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %#v", payload, recorder.Code, body)
		}
		if body["error"] != "n must be the number of a change in the file's history" {
			t.Fatalf("%s error = %#v", payload, body["error"])
		}
	}

	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Ftea.md", `not json`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid JSON body" {
		t.Fatalf("bad json = %d %#v", recorder.Code, body)
	}
}

// Parity: core/tests/test_routes_memories.py::test_a_seeded_memory_is_nobodys_so_her_reflection_can_build_on_it
func TestASeededMemoryIsNobodysSoReflectionCanBuildOnIt(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fcompany.md",
		`{"name":"Company","body":"Ava runs a tea shop."}`)
	if recorder.Code != http.StatusOK || body["created"] != true {
		t.Fatalf("seed = %d %#v", recorder.Code, body)
	}
	raw, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "company.md"))
	fields := memory.ParseFrontmatter(string(raw))
	if _, hasAuthor := fields["author"]; hasAuthor {
		t.Fatalf("seed frontmatter = %#v", fields)
	}
	if _, err := memory.Write(filepath.Join(home.MemoriesDir(), "global"), "company.md",
		strings.Replace(string(raw), "tea shop", "tea house", 1), memory.WriteOptions{Author: "reflection"}); err != nil {
		t.Fatalf("reflection build on seed: %v", err)
	}
	if fields["learned"] != fields["confirmed"] {
		t.Fatalf("seed dates = %#v", fields)
	}
}

// Parity: core/tests/test_routes_memories.py::test_history_refuses_a_path_outside_the_memories
func TestHistoryRefusesAPathOutsideTheMemories(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/history?path=..%2Fsecrets.md", "")
	if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid path" {
		t.Fatalf("history traversal = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: pins the exact GET /api/memories/file payload fields frozen
// from golden_api.json (the upstream test covers only the list entry).
func TestGetMemoryShowsProvenanceAndContent(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	written := seedRoutesMemory(t, home, "global/tea.md", routesLogNote)
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/file?path=global%2Ftea.md", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get = %d %#v", recorder.Code, body)
	}
	assertKeys(t, "get payload", body, []string{
		"author", "confirmed", "content", "description", "filename", "learned", "mtime", "name", "path", "scope", "scope_name", "type",
	})
	if body["content"] != written || body["author"] != "reflection" {
		t.Fatalf("get payload = %#v", body)
	}
}

// Parity-exempt: pins the retired endpoint's empty shape for a missing
// memories directory (upstream returns {"retired": []}).
func TestRetiredWithoutMemoriesDirectoryIsEmpty(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)
	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/retired", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if retired, ok := body["retired"].([]any); !ok || len(retired) != 0 {
		t.Fatalf("retired = %#v", body["retired"])
	}
}
