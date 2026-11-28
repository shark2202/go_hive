package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeColonyRenameJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeColonyRenameRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Parity-exempt: upstream handle_rename_colony leaves stale metadata/session
// pointers; Go-only consistency repair for the reported chat-history loss.
func TestRenameRepointsColonyReferences(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "go_hive", false)
	oldColonyDir := home.ColonyDir("go_hive")
	newColonyDir := home.ColonyDir("go_hive_v2")

	writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "metadata.json"), map[string]any{
		"colony_id":         "go_hive",
		"name":              "go_hive",
		"queen_name":        "queen_custom_72601",
		"source_session_id": "session_source",
		"created_at":        "2026-01-02T03:04:05+00:00",
	})

	colonySessionMeta := filepath.Join(oldColonyDir, "queens", "queen_custom_72601", "sessions", "session_colony", "meta.json")
	writeColonyRenameJSON(t, colonySessionMeta, map[string]any{
		"agent_path": oldColonyDir,
		"queen_id":   "queen_custom_72601",
		"title":      "history",
	})
	unrelatedColonyMeta := filepath.Join(oldColonyDir, "queens", "queen_custom_72601", "sessions", "session_unrelated", "meta.json")
	writeColonyRenameJSON(t, unrelatedColonyMeta, map[string]any{
		"agent_path": "/somewhere/else",
		"queen_id":   "queen_custom_72601",
	})

	writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "runs", "run_1.json"), map[string]any{
		"id":        "run_1",
		"colony_id": "go_hive",
		"status":    "running",
	})
	writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "runs", "run_2.json"), map[string]any{
		"id":        "run_2",
		"colony_id": "other",
		"status":    "done",
	})

	sourceMetaPath := filepath.Join(home.QueensDir(), "queen_custom_72601", "sessions", "session_source", "meta.json")
	writeColonyRenameJSON(t, sourceMetaPath, map[string]any{
		"agent_path":        oldColonyDir,
		"spawned_colony_id": "go_hive",
		"colony_spawned":    true,
		"queen_id":          "queen_custom_72601",
	})
	unrelatedSourcePath := filepath.Join(home.QueensDir(), "queen_custom_72601", "sessions", "session_other", "meta.json")
	writeColonyRenameJSON(t, unrelatedSourcePath, map[string]any{
		"agent_path":        oldColonyDir,
		"spawned_colony_id": "go_hive",
	})

	recorder, body := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/go_hive/rename", map[string]any{"new_name": "go_hive_v2"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if body["renamed"] != true || body["old_name"] != "go_hive" || body["new_name"] != "go_hive_v2" {
		t.Fatalf("body = %v", body)
	}
	if _, err := os.Stat(oldColonyDir); !os.IsNotExist(err) {
		t.Fatalf("old colony dir should be gone: %v", err)
	}
	if info, err := os.Stat(newColonyDir); err != nil || !info.IsDir() {
		t.Fatalf("new colony dir missing: %v", err)
	}

	metadata, err := readJSONObject(filepath.Join(newColonyDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata["colony_id"] != "go_hive_v2" || metadata["name"] != "go_hive_v2" {
		t.Fatalf("metadata colony_id/name = %#v/%#v", metadata["colony_id"], metadata["name"])
	}
	if metadata["queen_name"] != "queen_custom_72601" || metadata["source_session_id"] != "session_source" ||
		metadata["created_at"] != "2026-01-02T03:04:05+00:00" {
		t.Fatalf("metadata lost unrelated fields: %v", metadata)
	}

	colonyMeta, err := readJSONObject(filepath.Join(newColonyDir, "queens", "queen_custom_72601", "sessions", "session_colony", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if colonyMeta["agent_path"] != newColonyDir {
		t.Fatalf("colony session agent_path = %#v, want %q", colonyMeta["agent_path"], newColonyDir)
	}
	if colonyMeta["queen_id"] != "queen_custom_72601" || colonyMeta["title"] != "history" {
		t.Fatalf("colony session meta lost fields: %v", colonyMeta)
	}

	unrelatedColony, err := readJSONObject(filepath.Join(newColonyDir, "queens", "queen_custom_72601", "sessions", "session_unrelated", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedColony["agent_path"] != "/somewhere/else" {
		t.Fatalf("unrelated colony session was repointed: %v", unrelatedColony)
	}

	run, err := readJSONObject(filepath.Join(newColonyDir, "runs", "run_1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if run["colony_id"] != "go_hive_v2" || run["status"] != "running" {
		t.Fatalf("run record = %v", run)
	}
	otherRun, err := readJSONObject(filepath.Join(newColonyDir, "runs", "run_2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if otherRun["colony_id"] != "other" {
		t.Fatalf("unrelated run record was repointed: %v", otherRun)
	}

	sourceMeta, err := readJSONObject(sourceMetaPath)
	if err != nil {
		t.Fatal(err)
	}
	if sourceMeta["agent_path"] != newColonyDir || sourceMeta["spawned_colony_id"] != "go_hive_v2" {
		t.Fatalf("source session meta = %v", sourceMeta)
	}
	if sourceMeta["colony_spawned"] != true || sourceMeta["queen_id"] != "queen_custom_72601" {
		t.Fatalf("source session meta lost fields: %v", sourceMeta)
	}

	unrelatedSource, err := readJSONObject(unrelatedSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedSource["agent_path"] != oldColonyDir || unrelatedSource["spawned_colony_id"] != "go_hive" {
		t.Fatalf("unrelated queen session was touched: %v", unrelatedSource)
	}
}

// Parity-exempt: upstream handle_rename_colony leaves stale metadata/session
// pointers; Go-only consistency repair for the reported chat-history loss.
func TestRenameWithoutSourceSessionSucceeds(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "plain", false)
	oldColonyDir := home.ColonyDir("plain")
	newColonyDir := home.ColonyDir("plain_v2")

	writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "metadata.json"), map[string]any{"colony_id": "plain"})
	// Empty optional trees: a runs directory without records and a session
	// directory without meta.json are no-ops for the repoint.
	if err := os.MkdirAll(filepath.Join(oldColonyDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(oldColonyDir, "queens", "queen_x", "sessions", "session_empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	recorder, body := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/plain/rename", map[string]any{"new_name": "plain_v2"}))
	if recorder.Code != http.StatusOK || body["renamed"] != true {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(oldColonyDir); !os.IsNotExist(err) {
		t.Fatalf("old colony dir should be gone: %v", err)
	}
	metadata, err := readJSONObject(filepath.Join(newColonyDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata["colony_id"] != "plain_v2" || metadata["name"] != "plain_v2" {
		t.Fatalf("metadata = %v", metadata)
	}
	if _, err := os.Stat(filepath.Join(newColonyDir, "queens", "queen_x", "sessions", "session_empty")); err != nil {
		t.Fatalf("empty session dir should survive the rename: %v", err)
	}
}

// Parity-exempt: upstream handle_rename_colony leaves stale metadata/session
// pointers; Go-only consistency repair for the reported chat-history loss.
func TestRenameMalformedJSONFailsPreflight(t *testing.T) {
	targets := []string{"metadata", "colony_session", "run", "source_session"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			deps, home := colonyTestDeps(t)
			makeRouteColony(t, home, "broken", false)
			oldColonyDir := home.ColonyDir("broken")
			writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "metadata.json"), map[string]any{
				"colony_id":         "broken",
				"name":              "broken",
				"source_session_id": "session_source",
			})

			var malformedPath string
			switch target {
			case "metadata":
				malformedPath = filepath.Join(oldColonyDir, "metadata.json")
			case "colony_session":
				malformedPath = filepath.Join(oldColonyDir, "queens", "queen_x", "sessions", "session_1", "meta.json")
			case "run":
				malformedPath = filepath.Join(oldColonyDir, "runs", "run_1.json")
			case "source_session":
				malformedPath = filepath.Join(home.QueensDir(), "queen_x", "sessions", "session_source", "meta.json")
			}
			malformed := []byte(`{"colony_id": "broken",`)
			writeColonyRenameRaw(t, malformedPath, malformed)

			recorder, body := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/broken/rename", map[string]any{"new_name": "broken_v2"}))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body.String())
			}
			message, _ := body["error"].(string)
			if !strings.Contains(message, malformedPath) {
				t.Fatalf("error %q does not name %s", message, malformedPath)
			}
			if data, err := os.ReadFile(malformedPath); err != nil || !bytes.Equal(data, malformed) {
				t.Fatalf("malformed document changed: %q, err %v", data, err)
			}
			if info, err := os.Stat(oldColonyDir); err != nil || !info.IsDir() {
				t.Fatalf("old colony dir should survive preflight failure: %v", err)
			}
			if _, err := os.Stat(home.ColonyDir("broken_v2")); !os.IsNotExist(err) {
				t.Fatalf("new colony dir should not exist: %v", err)
			}
			if target != "metadata" {
				metadata, err := readJSONObject(filepath.Join(oldColonyDir, "metadata.json"))
				if err != nil {
					t.Fatal(err)
				}
				if metadata["colony_id"] != "broken" || metadata["name"] != "broken" {
					t.Fatalf("metadata was repointed before the move: %v", metadata)
				}
			}
		})
	}
}

// Parity-exempt: upstream handle_rename_colony leaves stale metadata/session
// pointers; Go-only consistency repair for the reported chat-history loss.
func TestRenameRepointsAgentPathUnderColony(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "nested", false)
	oldColonyDir := home.ColonyDir("nested")
	newColonyDir := home.ColonyDir("nested_v2")
	writeColonyRenameJSON(t, filepath.Join(oldColonyDir, "metadata.json"), map[string]any{"colony_id": "nested", "name": "nested"})
	sessionMeta := filepath.Join(oldColonyDir, "queens", "queen_x", "sessions", "session_nested", "meta.json")
	writeColonyRenameJSON(t, sessionMeta, map[string]any{
		"agent_path": filepath.Join(oldColonyDir, "worker", "nested"),
	})

	recorder, _ := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/nested/rename", map[string]any{"new_name": "nested_v2"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	meta, err := readJSONObject(filepath.Join(newColonyDir, "queens", "queen_x", "sessions", "session_nested", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(newColonyDir, "worker", "nested")
	if meta["agent_path"] != want {
		t.Fatalf("agent_path = %#v, want %q", meta["agent_path"], want)
	}
}
