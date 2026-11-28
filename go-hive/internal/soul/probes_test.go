package soul

import (
	"path/filepath"
	"strings"
	"testing"
)

// Parity: framework/agents/queen/soul_probes.py::related_memories
func TestRelatedMemoriesFormatsRecallInjection(t *testing.T) {
	queenDir := filepath.Join(t.TempDir(), "queen")
	globalDir := filepath.Join(t.TempDir(), "global")
	writeReflectionTestText(t, filepath.Join(queenDir, "ann.md"), "  Ann is the person at 12345.\n")
	writeReflectionTestText(t, filepath.Join(queenDir, "stranger.md"), "Bob is at 67890.\n")
	writeReflectionTestText(t, filepath.Join(globalDir, "people.md"), "Ann is also 12345 in the global scope.\n")

	got := RelatedMemories([]string{queenDir, globalDir}, "[Ann(12345)]: are you well?")
	want := "<recalled_memories>\n" +
		"### ann.md\nAnn is the person at 12345.\n\n" +
		"### people.md\nAnn is also 12345 in the global scope.\n" +
		"</recalled_memories>"
	if got != want {
		t.Fatalf("related memories = %q, want %q", got, want)
	}

	// A question with no speaker id (or an id under five digits) brings
	// nothing: recall only knows people whose header carries their id.
	for _, question := range []string{"are you well?", "[Ann(1234)]: are you well?"} {
		if got := RelatedMemories([]string{queenDir, globalDir}, question); got != "" {
			t.Fatalf("question %q injected %q, want nothing", question, got)
		}
	}
}

// Parity: framework/agents/queen/soul_probes.py::related_memories
func TestRelatedMemoriesCapsAtThreeHerScopeFirst(t *testing.T) {
	queenDir := filepath.Join(t.TempDir(), "queen")
	globalDir := filepath.Join(t.TempDir(), "global")
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		writeReflectionTestText(t, filepath.Join(queenDir, name), "12345 is here.\n")
	}
	writeReflectionTestText(t, filepath.Join(globalDir, "global.md"), "12345 is here too.\n")

	got := RelatedMemories([]string{queenDir, globalDir}, "[Ann(12345)]: hi")
	for _, want := range []string{"### a.md", "### b.md", "### c.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("related memories missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"### d.md", "### global.md"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("related memories read past the cap (%q): %q", unwanted, got)
		}
	}
}

// Parity: framework/agents/queen/soul_probes.py::probe
func TestProbeAnswersCarryRelatedMemories(t *testing.T) {
	home := newReflectionTestHome(t)
	writeReflectionTestJSON(t, filepath.Join(SoulDir(home.queensDir, reflectionTestQueen), "probes.json"), []any{
		map[string]any{"q": "[Ann(12345)]: what now?", "anchor": "the lantern"},
		map[string]any{"q": "what now?"},
	})
	proposal := &Proposal{
		ID:          "2026-10-21-050000",
		BaseVersion: 0,
		Edits:       []Edit{{Op: "add", Section: "now", Text: "she keeps a lantern"}},
		Status:      "pending",
	}
	if err := SaveProposal(home.queensDir, reflectionTestQueen, proposal); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	queenMemoryDir := filepath.Join(home.root, "memories", "agents", "queens", reflectionTestQueen)
	globalMemoryDir := filepath.Join(home.root, "memories", "global")
	writeReflectionTestText(t, filepath.Join(queenMemoryDir, "ann.md"), "Ann is 12345.\n")
	writeReflectionTestText(t, filepath.Join(globalMemoryDir, "people.md"), "12345 introduced herself.\n")

	var systems []string
	answer := func(system, question string) (string, error) {
		systems = append(systems, system)
		return "answered: " + question, nil
	}
	if _, err := Probe(ProbeOptions{
		QueensDir:  home.queensDir,
		QueenID:    reflectionTestQueen,
		Config:     home.soulConfig(t),
		Proposal:   proposal,
		Identity:   "IDENTITY",
		MemoryDirs: []string{queenMemoryDir, globalMemoryDir},
		Answer:     answer,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(systems) != 4 {
		t.Fatalf("answer systems = %d, want before/after for two questions", len(systems))
	}
	for _, system := range systems[:2] {
		if !strings.Contains(system, "<recalled_memories>\n### ann.md\nAnn is 12345.\n\n### people.md\n12345 introduced herself.\n</recalled_memories>") {
			t.Fatalf("probe system is missing the recalled memories block: %q", system)
		}
		if !strings.HasSuffix(system, "</recalled_memories>") {
			t.Fatalf("recalled memories are not the tail of the system prompt: %q", system)
		}
	}
	for _, system := range systems[2:] {
		if strings.Contains(system, "recalled_memories") {
			t.Fatalf("a question with no speaker id injected memories: %q", system)
		}
	}

	fresh := LoadProposal(home.queensDir, reflectionTestQueen, proposal.ID)
	if fresh == nil || len(fresh.Probes) != 2 {
		t.Fatalf("stored probes = %#v", fresh)
	}
	if fresh.Probes[0]["before"] != "answered: [Ann(12345)]: what now?" {
		t.Fatalf("stored before answer = %#v", fresh.Probes[0]["before"])
	}
	if fresh.Probes[1]["after"] != "answered: what now?" {
		t.Fatalf("stored after answer = %#v", fresh.Probes[1]["after"])
	}
}
