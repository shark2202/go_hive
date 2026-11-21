package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// searchTestHome builds a queen home with two sessions: one holding the
// needle, one that is the live session the catalogue is bound to.
func searchTestHome(t *testing.T) (home, queen, pastSession, liveSession string) {
	t.Helper()
	home = t.TempDir()
	queen = "queen_search"
	pastSession = "session_20260101_090000_aaaa1111"
	liveSession = "session_20260102_100000_bbbb2222"
	past := filepath.Join(home, "queens", queen, "sessions", pastSession)
	if err := os.MkdirAll(past, 0o755); err != nil {
		t.Fatal(err)
	}
	events := "" +
		`{"type":"client_input_received","data":{"content":"remember the deploy ticket HIVE-731"}}` + "\n" +
		`{"type":"client_output_delta","data":{"snapshot":"I will keep HIVE-731 in mind."}}` + "\n"
	if err := os.WriteFile(filepath.Join(past, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(home, "queens", queen, "sessions", liveSession)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "events.jsonl"),
		[]byte(`{"type":"client_input_received","data":{"content":"hello again"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, queen, pastSession, liveSession
}

func searchTestCatalogue(root, homeRoot string) *sessionToolCatalogue {
	return &sessionToolCatalogue{
		root:         root,
		home:         hivehome.Home{Root: homeRoot},
		builtinNames: map[string]struct{}{},
	}
}

func decodeToolPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("tool payload is not JSON (%q): %v", content, err)
	}
	return payload
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_returns_turn_window
// Parity: docs/key_concepts/queen.md (every past session is searchable)
func TestSessionCatalogueSearchMessagesReadsPastSessions(t *testing.T) {
	home, queen, pastSession, liveSession := searchTestHome(t)
	catalogue := searchTestCatalogue(
		filepath.Join(home, "queens", queen, "sessions", liveSession), home)

	content, isError, err := catalogue.Execute(context.Background(), SearchMessagesToolName,
		map[string]any{"pattern": "HIVE-731"})
	if err != nil || isError {
		t.Fatalf("search_messages: err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)
	matches, _ := payload["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %v, want one per matching message (user + assistant)", payload["matches"])
	}
	for _, raw := range matches {
		match, _ := raw.(map[string]any)
		if match["session"] != pastSession {
			t.Fatalf("session = %v, want %s — the live session holds no hit", match["session"], pastSession)
		}
		if started, _ := match["session_started_at"].(string); started != "2026-01-01T09:00:00" {
			t.Fatalf("session_started_at = %v", match["session_started_at"])
		}
		turn, _ := match["turn"].([]any)
		if len(turn) == 0 {
			t.Fatalf("turn window is empty: %v", match)
		}
	}
	if total, _ := payload["total_matches"].(float64); total != 2 {
		t.Fatalf("total_matches = %v, want 2", payload["total_matches"])
	}
}

// Parity-exempt: the engine enumerates sessions from the queen's own directory;
// this locks that the tool's session filter cannot be turned into a path, so a
// crafted value fails instead of reading another tree.
func TestSessionCatalogueSearchMessagesRejectsPathFilter(t *testing.T) {
	home, queen, _, liveSession := searchTestHome(t)
	catalogue := searchTestCatalogue(
		filepath.Join(home, "queens", queen, "sessions", liveSession), home)
	other := filepath.Join(home, "colonies", "c1", "queens", queen, "sessions", "session_20260101_000000_cccc")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "events.jsonl"),
		[]byte(`{"type":"client_input_received","data":{"content":"HIVE-731 elsewhere"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, session := range []string{"../../../colonies/c1/queens/" + queen + "/sessions/session_20260101_000000_cccc", "..", "a/b"} {
		content, isError, err := catalogue.Execute(context.Background(), SearchMessagesToolName,
			map[string]any{"pattern": "HIVE-731", "session": session})
		if err != nil {
			t.Fatalf("session=%q: transport error: %v", session, err)
		}
		if !isError {
			t.Fatalf("session=%q: traversal filter reported success: %s", session, content)
		}
		payload := decodeToolPayload(t, content)
		if payload["error"] != "filter_invalid" {
			t.Fatalf("session=%q: error = %v, want filter_invalid", session, payload["error"])
		}
	}

	// A well-formed filter that names no session is a normal empty answer.
	content, isError, err := catalogue.Execute(context.Background(), SearchMessagesToolName,
		map[string]any{"pattern": "HIVE-731", "session": "session_20200101_000000_none0000"})
	if err != nil || isError {
		t.Fatalf("unknown session filter should be empty, not an error: %s (err=%v)", content, err)
	}
	if total, _ := decodeToolPayload(t, content)["total_matches"].(float64); total != 0 {
		t.Fatalf("unknown session matched something: %s", content)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_unknown_queen
func TestSessionCatalogueSearchMessagesOutsideQueenTree(t *testing.T) {
	home := t.TempDir()
	catalogue := searchTestCatalogue(filepath.Join(home, "scratch"), home)
	content, isError, err := catalogue.Execute(context.Background(), SearchMessagesToolName,
		map[string]any{"pattern": "anything"})
	if err != nil || !isError {
		t.Fatalf("err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)
	if payload["error"] != "scope_not_found" {
		t.Fatalf("error = %v, want scope_not_found", payload["error"])
	}
}

// Parity: tools/src/memory_tools/timeline.py::search_timeline
func TestSessionCatalogueSearchTimelineFollowsFeatureFlag(t *testing.T) {
	home, queen, pastSession, liveSession := searchTestHome(t)
	past := filepath.Join(home, "queens", queen, "sessions", pastSession)
	timeline := `{"kind":"event","text":"renewed the HIVE-731 domain","start":"2026-01-01","end":"2026-01-01"}` + "\n"
	if err := os.WriteFile(filepath.Join(past, "timeline.jsonl"), []byte(timeline), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogue := searchTestCatalogue(filepath.Join(home, "queens", queen, "sessions", liveSession), home)

	for _, def := range searchToolDefs(catalogue.home) {
		if def.Name == SearchTimelineToolName {
			t.Fatal("search_timeline is advertised while memory_timeline is off")
		}
	}
	if content, isError, _ := catalogue.Execute(context.Background(), SearchTimelineToolName,
		map[string]any{"pattern": "domain"}); !isError {
		t.Fatalf("search_timeline ran with the feature off: %s", content)
	}

	// The flag is read per call (live config, like upstream), so turning it on
	// applies to the running session without rebuilding the catalogue.
	config := `{"memory_timeline": true}`
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, def := range searchToolDefs(catalogue.home) {
		if def.Name == SearchTimelineToolName {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("search_timeline is missing from the catalogue with memory_timeline on")
	}
	content, isError, err := catalogue.Execute(context.Background(), SearchTimelineToolName,
		map[string]any{"pattern": "domain"})
	if err != nil || isError {
		t.Fatalf("search_timeline: err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want the one dated event", payload["items"])
	}
	item, _ := items[0].(map[string]any)
	if item["session"] != pastSession || item["kind"] != "event" {
		t.Fatalf("item = %v", item)
	}
}

// Parity-exempt: assembly guard. Both tools must reach the real session
// catalogue the chat runtime builds, not just a hand-made struct.
func TestSessionCatalogueAdvertisesHistorySearch(t *testing.T) {
	home, queen, _, liveSession := searchTestHome(t)
	catalogue := newSessionToolCatalogue(
		filepath.Join(home, "queens", queen, "sessions", liveSession),
		nil, hivehome.Home{Root: home})
	names := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		names[def.Name] = struct{}{}
	}
	if _, ok := names[SearchMessagesToolName]; !ok {
		t.Fatal("search_messages missing from the session catalogue")
	}
}

// Parity: tools/src/memory_tools/tool.py::search_messages
// Parity: tools/src/memory_tools/timeline.py::search_timeline
func TestHistorySearchDefinitionsMatchPythonContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v) — this check needs the committed artifact", err)
	}
	type contractTool struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	var manifest struct {
		Tools []contractTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	references := map[string]contractTool{}
	for _, tool := range manifest.Tools {
		references[tool.Name] = tool
	}

	for _, def := range []llm.ToolDef{searchMessagesToolDef(), searchTimelineToolDef()} {
		reference, ok := references[def.Name]
		if !ok {
			t.Fatalf("%s missing from contracts/tool-definitions.json", def.Name)
		}
		if def.Description != reference.Description {
			t.Fatalf("%s: description is not the Python contract (got %d bytes, want %d bytes)",
				def.Name, len(def.Description), len(reference.Description))
		}
		got, err := json.Marshal(def.Parameters)
		if err != nil {
			t.Fatalf("%s: marshal Go schema: %v", def.Name, err)
		}
		want, err := json.Marshal(reference.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal Python schema: %v", def.Name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: input schema is not the Python contract\n got: %s\nwant: %s", def.Name, got, want)
		}
	}
}
