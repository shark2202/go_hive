package hive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity: core/tests/test_memory_log.py::test_a_written_file_says_who_wrote_it_and_when
func TestMemoryWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	written, err := WriteMemory(dir, "note.md", "The sky is blue.", MemoryWriteOptions{Author: "owner", Reason: "test"})
	if err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if written == "" {
		t.Fatal("WriteMemory returned empty text")
	}
	read, err := ReadMemory(dir, "note.md")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if read != written {
		t.Fatalf("ReadMemory = %q, WriteMemory = %q", read, written)
	}
	files := ListMemories(dir)
	if len(files) != 1 || files[0].Filename != "note.md" {
		t.Fatalf("ListMemories = %+v, want note.md", files)
	}
}

// Parity-exempt: Go-only path-safety check over SafeMemoryPath.
func TestMemoryReadRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadMemory(dir, "../outside.md"); err == nil {
		t.Fatal("ReadMemory accepted a traversing filename")
	}
	if _, err := ReadMemory(dir, "sub/note.md"); err == nil {
		t.Fatal("ReadMemory accepted a nested path")
	}
}

// Parity: framework/agents/queen/queen_memory_v2.py::global_memory_dir
func TestMemoryScopeDirs(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	if got, want := GlobalMemoryDir(home), filepath.Join(home.Root, "memories", "global"); got != want {
		t.Fatalf("GlobalMemoryDir = %q, want %q", got, want)
	}
	if got := QueenMemoryDir(home, "queen_technology"); got == "" {
		t.Fatal("QueenMemoryDir returned empty")
	}
	if got := ColonyMemoryDir(home, "c1"); got == "" {
		t.Fatal("ColonyMemoryDir returned empty")
	}
	if GlobalMemoryDir(Home{}) != "" {
		t.Fatal("GlobalMemoryDir on a zero Home should be empty")
	}
}

// Parity: framework/agents/queen/soul.py::Soul
func TestSoulLoadOnAnEmptyHome(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	loaded := LoadSoul(home, "queen_technology")
	if loaded == nil {
		t.Fatal("LoadSoul returned nil")
	}
	if !loaded.IsEmpty() {
		t.Fatalf("empty home soul = %+v, want empty", loaded)
	}
	if LoadSoul(Home{}, "queen_technology") != nil {
		t.Fatal("LoadSoul on a zero Home should be nil")
	}
}

// Parity: core/tests/test_queen_identity_prompt.py::test_identity_prompt_starts_with_plain_identity
func TestQueenProfilesIncludeBuiltins(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	queens := ListQueens(home)
	if len(queens) == 0 {
		t.Fatal("ListQueens returned no built-in queens")
	}
	found := false
	for _, entry := range queens {
		if entry.ID == "queen_technology" {
			found = true
			if entry.Name != "Elon Musk" || entry.Title != "Head of Technology" {
				t.Fatalf("queen_technology = %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("queen_technology missing from %+v", queens)
	}

	profile, err := LoadQueenProfile(home, "queen_technology")
	if err != nil {
		t.Fatalf("LoadQueenProfile: %v", err)
	}
	if profile.ID != "queen_technology" || profile.Name == "" {
		t.Fatalf("profile = %+v", profile)
	}
	if len(profile.ToolCategories) == 0 {
		t.Fatalf("profile has no tool categories: %+v", profile)
	}
	if profile.Persona["name"] != "Elon Musk" {
		t.Fatalf("persona = %#v", profile.Persona)
	}
}

// Parity: core/tests/test_skill_catalog.py::TestSkillCatalog
func TestSkillCatalogRendersPrompt(t *testing.T) {
	catalog := NewSkillCatalog([]*Skill{
		{Name: "zeta", Description: "Zeta skill", BaseDir: t.TempDir()},
		{Name: "alpha", Description: "Alpha skill", BaseDir: t.TempDir()},
	})
	if catalog.Count() != 2 {
		t.Fatalf("Count = %d, want 2", catalog.Count())
	}
	if _, ok := catalog.Get("alpha"); !ok {
		t.Fatal("Get(alpha) missed the skill")
	}
	prompt := catalog.Prompt("")
	if prompt == "" {
		t.Fatal("Prompt returned empty text")
	}
	if !strings.Contains(prompt, "alpha") || !strings.Contains(prompt, "zeta") {
		t.Fatalf("prompt = %q, want both skill names", prompt)
	}
}

// Parity: core/tests/test_session_listing_order.py::test_list_cold_sessions_spans_multiple_queens_in_order
func TestListSessionsAcrossQueens(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	for _, entry := range []struct{ queen, session, title string }{
		{"q1", "s1", "First"},
		{"q2", "s2", "Second"},
	} {
		dir := filepath.Join(home.QueensDir(), entry.queen, "sessions", entry.session)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		meta := `{"title":"` + entry.title + `","created_at": 1.0}`
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	sessions := ListSessions(home)
	if len(sessions) != 2 {
		t.Fatalf("ListSessions = %+v, want 2", sessions)
	}
	if sessions[0].ID != "s2" || sessions[1].ID != "s1" {
		t.Fatalf("session order = %q,%q, want newest id first", sessions[0].ID, sessions[1].ID)
	}
	if sessions[0].QueenID != "q2" || sessions[0].Title != "Second" {
		t.Fatalf("session = %+v", sessions[0])
	}
	if ListSessions(Home{}) != nil {
		t.Fatal("ListSessions on a zero Home should be nil")
	}
}
