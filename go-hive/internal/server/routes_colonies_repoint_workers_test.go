package server

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: upstream handle_rename_colony leaves the pointers stale;
// Go-only consistency repair so saved workers stay resumable after a rename.
func TestRenameRepointsWorkerColonyIDs(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "go_hive", false)
	oldColonyDir := home.ColonyDir("go_hive")
	newColonyDir := home.ColonyDir("go_hive_v2")

	workerMeta := filepath.Join(oldColonyDir, "workers", "worker_1", "meta.json")
	writeColonyRenameJSON(t, workerMeta, map[string]any{
		"worker_id": "worker_1",
		"colony_id": "go_hive",
		"task":      "collect the numbers",
		"goal":      "report them",
	})
	workerState := filepath.Join(oldColonyDir, "workers", "worker_1", "state.json")
	writeColonyRenameJSON(t, workerState, map[string]any{
		"worker_id":    "worker_1",
		"colony_id":    "go_hive",
		"status":       "running",
		"resume_count": 2,
	})

	// A worker that belongs to another colony must keep its id untouched.
	unrelatedMeta := filepath.Join(oldColonyDir, "workers", "worker_2", "meta.json")
	writeColonyRenameJSON(t, unrelatedMeta, map[string]any{
		"worker_id": "worker_2",
		"colony_id": "other",
		"task":      "stay put",
	})

	// Optional documents: a worker with meta.json but no state.json only
	// repoints the document that exists.
	legacyMeta := filepath.Join(oldColonyDir, "workers", "worker_3", "meta.json")
	writeColonyRenameJSON(t, legacyMeta, map[string]any{
		"worker_id": "worker_3",
		"colony_id": "go_hive",
	})

	recorder, body := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/go_hive/rename", map[string]any{"new_name": "go_hive_v2"}))
	if recorder.Code != http.StatusOK || body["renamed"] != true {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(oldColonyDir); !os.IsNotExist(err) {
		t.Fatalf("old colony dir should be gone: %v", err)
	}

	meta, err := readJSONObject(filepath.Join(newColonyDir, "workers", "worker_1", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if meta["colony_id"] != "go_hive_v2" {
		t.Fatalf("worker meta colony_id = %#v, want go_hive_v2", meta["colony_id"])
	}
	if meta["worker_id"] != "worker_1" || meta["task"] != "collect the numbers" || meta["goal"] != "report them" {
		t.Fatalf("worker meta lost fields: %v", meta)
	}

	state, err := readJSONObject(filepath.Join(newColonyDir, "workers", "worker_1", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state["colony_id"] != "go_hive_v2" || state["status"] != "running" || pythonString(state["resume_count"]) != "2" {
		t.Fatalf("worker state = %v", state)
	}

	other, err := readJSONObject(filepath.Join(newColonyDir, "workers", "worker_2", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if other["colony_id"] != "other" || other["task"] != "stay put" {
		t.Fatalf("unrelated worker was repointed: %v", other)
	}

	legacy, err := readJSONObject(filepath.Join(newColonyDir, "workers", "worker_3", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if legacy["colony_id"] != "go_hive_v2" {
		t.Fatalf("worker meta without state = %v", legacy)
	}
	if _, err := os.Stat(filepath.Join(newColonyDir, "workers", "worker_3", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("missing worker state should stay missing: %v", err)
	}
}

// Parity-exempt: upstream handle_rename_colony leaves the pointers stale;
// Go-only consistency repair so saved workers stay resumable after a rename.
func TestRenameMalformedWorkerDocumentFailsPreflight(t *testing.T) {
	for _, document := range []string{"meta.json", "state.json"} {
		t.Run(document, func(t *testing.T) {
			deps, home := colonyTestDeps(t)
			makeRouteColony(t, home, "broken", false)
			oldColonyDir := home.ColonyDir("broken")

			malformedPath := filepath.Join(oldColonyDir, "workers", "worker_1", document)
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
		})
	}
}
