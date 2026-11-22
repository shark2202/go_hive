package colony

// Cross-language harness for the verbatim comparison script
// (go_hive/scripts/playbook_verbatim_compare.py). It runs the REAL Go
// `RunPlaybook` over a fixture colony and dumps the observable surface:
// tool payload, [PLAYBOOK_COMPLETE] notice, dispatched worker prompts, saved
// library files, playbook_runs rows, and the temporary colony-cap moves.
//
// Opt-in via PLAYBOOK_VERBATIM_FIXTURE/PLAYBOOK_VERBATIM_OUT so the normal test
// run stays hermetic.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

type harnessFixture struct {
	Script   string         `json:"script"`
	Input    string         `json:"input"` // "path" or "inline"
	Rows     int            `json:"rows"`
	DoneRows []int          `json:"done_rows"`
	Cap      int            `json:"cap"`
	Grace    string         `json:"grace"`
	Args     map[string]any `json:"args"`
	Mode     string         `json:"mode"` // completed | started | load_error | run_error
}

type harnessRuntime struct {
	*fakePlaybookRuntime
	mu       sync.Mutex
	capMoves []int
	notices  []map[string]any
}

func (h *harnessRuntime) SetMaxConcurrentWorkers(capacity int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fakePlaybookRuntime.mu.Lock()
	defer h.fakePlaybookRuntime.mu.Unlock()
	previous := h.fakePlaybookRuntime.max
	h.capMoves = append(h.capMoves, capacity)
	h.fakePlaybookRuntime.max = capacity
	return previous
}

func (h *harnessRuntime) NotifyPlaybookComplete(notice map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, notice)
}

func (h *harnessRuntime) snapshot() (caps []int, notices []map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	caps = make([]int, len(h.capMoves))
	copy(caps, h.capMoves)
	notices = make([]map[string]any, len(h.notices))
	copy(notices, h.notices)
	return caps, notices
}

// TestPlaybookVerbatimHarness is the Go half of the two-sided comparison. It is
// skipped unless the compare script points it at a fixture.
// Parity-exempt: 双跑对照 harness（真实上游 PlaybookRun vs Go RunPlaybook），非单侧行为测试
func TestPlaybookVerbatimHarness(t *testing.T) {
	fixtureDir := os.Getenv("PLAYBOOK_VERBATIM_FIXTURE")
	outPath := os.Getenv("PLAYBOOK_VERBATIM_OUT")
	if fixtureDir == "" || outPath == "" {
		t.Skip("cross-language harness is driven by go_hive/scripts/playbook_verbatim_compare.py")
	}
	requirePlaybookPython(t)

	raw, err := os.ReadFile(filepath.Join(fixtureDir, "fixture.json"))
	if err != nil {
		t.Fatalf("read fixture.json: %v", err)
	}
	var fixture harnessFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture.json: %v", err)
	}
	if fixture.Grace != "" {
		t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", fixture.Grace)
	}
	scriptText, err := os.ReadFile(filepath.Join(fixtureDir, fixture.Script))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}

	colonyDir := filepath.Join(fixtureDir, "go-colony")
	if err := os.MkdirAll(filepath.Join(colonyDir, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, "harness")
	if err != nil || binding == nil {
		t.Fatalf("binding: %v", err)
	}
	if _, err := tracker.ExecuteSQL(binding.TrackerDB,
		"CREATE TABLE jobs (id INTEGER PRIMARY KEY, name TEXT, done INTEGER NOT NULL DEFAULT 0)", 100); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	for i := 1; i <= fixture.Rows; i++ {
		insert := fmt.Sprintf("INSERT INTO jobs (id, name, done) VALUES (%d, 'job-%d', 0)", i, i)
		if _, err := tracker.ExecuteSQL(binding.TrackerDB, insert, 100); err != nil {
			t.Fatalf("seed jobs: %v", err)
		}
	}
	done := map[int]bool{}
	for _, id := range fixture.DoneRows {
		done[id] = true
	}
	runtime := &harnessRuntime{fakePlaybookRuntime: &fakePlaybookRuntime{
		binding: binding, max: fixture.Cap, doneIDs: done,
	}}

	input := map[string]any{}
	if fixture.Input == "inline" {
		input["playbook"] = string(scriptText)
	} else {
		input["playbook_path"] = filepath.Join(fixtureDir, fixture.Script)
	}
	if fixture.Args != nil {
		input["args"] = fixture.Args
	}

	content, isError := RunPlaybook(context.Background(), runtime, input)

	// A background run finishes on its own goroutine; wait for the notice so
	// the artifact is complete.
	deadline := time.Now().Add(15 * time.Second)
	for strings.Contains(content, `"status":"started"`) {
		runtime.mu.Lock()
		got := len(runtime.notices)
		runtime.mu.Unlock()
		if got > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (%s)", err, content)
	}
	caps, notices := runtime.snapshot()
	runtime.mu.Lock()
	spawned := append([]BatchTask(nil), runtime.spawned...)
	runtime.mu.Unlock()
	prompts := make([]string, 0, len(spawned))
	schemas := make([]any, 0, len(spawned))
	goals := make([]any, 0, len(spawned))
	for _, task := range spawned {
		prompts = append(prompts, task.Task)
		schemas = append(schemas, task.ReportSchema)
		if task.Goal == "" {
			goals = append(goals, nil)
		} else {
			goals = append(goals, task.Goal)
		}
	}

	saved := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(colonyDir, "playbooks"))
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(colonyDir, "playbooks", entry.Name()))
		if err == nil {
			saved[entry.Name()] = string(data)
		}
	}

	runs := []map[string]any{}
	if listed, err := ListPlaybookRuns(filepath.Join(colonyDir, "colony.db"), 50); err == nil {
		for _, run := range listed {
			row := map[string]any{
				"run_id": run.RunID, "status": run.Status,
				"dispatched": run.Dispatched, "dead_lettered": run.DeadLettered,
				"created_at": run.CreatedAt,
			}
			if run.Name != nil {
				row["name"] = *run.Name
			} else {
				row["name"] = nil
			}
			if run.Error != nil {
				row["error"] = *run.Error
			} else {
				row["error"] = nil
			}
			if run.ResultJSON != nil {
				row["result_json"] = *run.ResultJSON
			} else {
				row["result_json"] = nil
			}
			if run.LogsJSON != nil {
				row["logs_json"] = *run.LogsJSON
			} else {
				row["logs_json"] = nil
			}
			runs = append(runs, row)
		}
	}

	artifact := map[string]any{
		"payload":       payload,
		"is_error":      isError,
		"payload_raw":   content,
		"prompts":       prompts,
		"schemas":       schemas,
		"goals":         goals,
		"cap_moves":     caps,
		"cap_after":     runtime.MaxConcurrentWorkers(),
		"saved_files":   saved,
		"playbook_runs": runs,
		"notices":       notices,
		"colony_dir":    colonyDir,
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
}
