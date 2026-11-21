package cli

// Real-LLM end-to-end check for the verbatim playbook contract, plus the
// artifact writer the two-sided comparison replays through upstream Python.
//
// Scenario: one `.play.py` script drives a 2-row tracker table through
// converge(); the workers are REAL model turns (report_to_parent), but nothing
// marks the rows done — so the run ends with both rows dead-lettered. The
// real-model latency pushes the run past the fast-result grace window, so the
// queen gets the async "started" acknowledgement plus one [PLAYBOOK_COMPLETE]
// notice whose fields must equal upstream's. The recorded prompts/reports let
// go_hive/scripts/playbook_verbatim_compare.py --real-llm replay the SAME
// script with the SAME worker reports through the real upstream tool and diff
// the fields. (The synchronous completed-payload path is covered field by
// field, against the real upstream tool, by the same script's other scenarios.)
//
// Opt-in: HIVE_REAL_LLM_E2E=1 and a configured HIVE_HOME (default ~/.hive-go).
//
// Parity-exempt: live-model integration test; upstream unit tests mock the
// completer.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// recordingRuntime records the exact dispatch tasks and worker reports the
// bridge sees, so the same inputs can be replayed through the Python tool.
type recordingRuntime struct {
	*colony.ColonyRuntime

	mu      sync.Mutex
	prompts []string
	reports []map[string]any
}

func (r *recordingRuntime) SpawnBatchWithOptions(ctx context.Context, tasks []colony.BatchTask, opts colony.SpawnBatchOptions) ([]string, error) {
	r.mu.Lock()
	for _, task := range tasks {
		r.prompts = append(r.prompts, task.Task)
	}
	r.mu.Unlock()
	return r.ColonyRuntime.SpawnBatchWithOptions(ctx, tasks, opts)
}

func (r *recordingRuntime) WaitForWorkerReports(ctx context.Context, ids []string, timeout time.Duration) ([]map[string]any, error) {
	reports, err := r.ColonyRuntime.WaitForWorkerReports(ctx, ids, timeout)
	r.mu.Lock()
	r.reports = append(r.reports, reports...)
	r.mu.Unlock()
	return reports, err
}

func (r *recordingRuntime) snapshot() ([]string, []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prompts := make([]string, len(r.prompts))
	copy(prompts, r.prompts)
	reports := make([]map[string]any, len(r.reports))
	copy(reports, r.reports)
	return prompts, reports
}

// Parity-exempt: opt-in 真实模型 E2E（HIVE_REAL_LLM_E2E=1）；上游无同名用例，字段对齐由 playbook_verbatim_compare.py 120/120 覆盖
func TestRealLLMPlaybookVerbatimConvergeDeadletterE2E(t *testing.T) {
	if os.Getenv("HIVE_REAL_LLM_E2E") != "1" {
		t.Skip("real LLM playbook E2E is opt-in: set HIVE_REAL_LLM_E2E=1")
	}
	home, err := hivehome.Resolve()
	if err != nil {
		t.Fatalf("resolve HIVE_HOME: %v", err)
	}
	provider, err := resolveChatProvider(home, "")
	if err != nil {
		t.Fatalf("resolve real LLM provider: %v", err)
	}
	counting := &countingCompleter{inner: provider}

	colonyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte(`{"name":"real_llm_verbatim","queen_name":"queen_growth"}`), 0o644); err != nil {
		t.Fatalf("write colony metadata: %v", err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, "real_llm_verbatim")
	if err != nil || binding == nil {
		t.Fatalf("tracker binding: %v (binding=%v)", err, binding)
	}
	if _, err := tracker.ExecuteSQL(binding.TrackerDB,
		"CREATE TABLE jobs (id INTEGER PRIMARY KEY, name TEXT, done INTEGER NOT NULL DEFAULT 0)", 100); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	for i := 1; i <= 2; i++ {
		insert := "INSERT INTO jobs (id, name, done) VALUES (" + strconv.Itoa(i) + ", 'job-" + strconv.Itoa(i) + "', 0)"
		if _, err := tracker.ExecuteSQL(binding.TrackerDB, insert, 100); err != nil {
			t.Fatalf("seed jobs: %v", err)
		}
	}

	systemPrompt := "You are a colony worker. Complete the task and report with the report_to_parent tool."
	spec := agentloop.NewAgentSpec("real_llm_verbatim_worker", "Real LLM Verbatim Worker", "Worker used by the verbatim playbook E2E.")
	spec.SystemPrompt = &systemPrompt
	spec.ToolAccessPolicy = "all"

	h, err := host.New(host.Config{
		ColonyID:             "real_llm_verbatim",
		AgentSpec:            spec,
		StoragePath:          colonyDir,
		LLM:                  counting,
		WorkerLLM:            counting,
		MaxConcurrentWorkers: 1,
	})
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(h.Stop)

	// Deterministic async path: the real model is slower than the default
	// grace window, and pinning the env keeps the assertion stable on a fast one.
	t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", "0")
	notices := make(chan map[string]any, 4)
	subscriptionID, err := h.Runtime().SubscribeToEvents([]eventbus.EventType{eventbus.EventSubagentReport}, func(event *eventbus.AgentEvent) {
		if event == nil || event.Data == nil {
			return
		}
		if _, ok := event.Data["playbook_run_id"]; !ok {
			return
		}
		select {
		case notices <- event.Data:
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.Runtime().Unsubscribe(subscriptionID)

	script := `meta = {"name": "real-llm-verbatim", "description": "converge + deadletter over real model workers", "concurrency": 2}

async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),
        dispatch=lambda row, i: worker(
            task=(
                "Call the report_to_parent tool exactly once with status='success', "
                "summary='REAL_LLM_E2E_OK' and data containing the marker "
                "'REAL_LLM_E2E_OK' for job " + row["name"] + ". Do not do anything else."
            ),
            data={"id": row["id"], "name": row["name"]},
            goal=f"Processing {row['name']}",
            timeout=300),
        max_rounds=1)
    log("converge finished")
    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0")}
`
	recorder := &recordingRuntime{ColonyRuntime: h.Runtime()}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, isErr := colony.RunPlaybook(ctx, recorder, map[string]any{"playbook": script})
	if isErr {
		t.Fatalf("RunPlaybook returned an error payload: %s", raw)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, raw)
	}
	if payload["status"] != "started" {
		t.Fatalf("status = %v, want started (grace is pinned to 0)", payload["status"])
	}
	wantKeys := []string{"concurrency", "log_db", "message", "playbook_name", "playbook_path", "run_id", "status"}
	gotKeys := make([]string, 0, len(payload))
	for key := range payload {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("started payload keys = %v, want the upstream set %v\npayload=%s", gotKeys, wantKeys, raw)
	}
	if payload["playbook_name"] != "real-llm-verbatim" {
		t.Fatalf("playbook_name = %v", payload["playbook_name"])
	}

	// ---- wait for the background run to finish and notify -----------------
	var notice map[string]any
	select {
	case notice = <-notices:
	case <-time.After(8 * time.Minute):
		t.Fatalf("no [PLAYBOOK_COMPLETE] notice; calls=%d payload=%s", counting.callCount(), raw)
	}
	if calls := counting.callCount(); calls < 2 {
		t.Fatalf("worker model calls = %d, want >= 2 (one per row); payload=%s", calls, raw)
	}
	if notice["status"] != "done" {
		t.Fatalf("notice = %#v", notice)
	}
	if emptyNoticeField(notice, "dispatched") != 2 {
		t.Fatalf("notice dispatched = %v, want 2", notice["dispatched"])
	}
	// Nothing marked the tracker rows done, so converge dead-letters both.
	if emptyNoticeField(notice, "dead_lettered") != 2 {
		t.Fatalf("notice dead_lettered = %v, want 2", notice["dead_lettered"])
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(notice["result_json"].(string)), &result); err != nil {
		t.Fatalf("notice result_json: %v (%v)", err, notice["result_json"])
	}
	if result["remaining"] != float64(2) {
		t.Fatalf("result.remaining = %v, want 2", result["remaining"])
	}
	if text, _ := notice["summary"].(string); !strings.HasPrefix(text, "[PLAYBOOK_COMPLETE]") ||
		!strings.Contains(text, "<status>done</status>") ||
		!strings.Contains(text, "<dead_lettered>2</dead_lettered>") ||
		!strings.Contains(text, "converge finished") {
		t.Fatalf("notice text = %q", text)
	}
	if got := h.Runtime().MaxConcurrentWorkers(); got != 1 {
		t.Fatalf("colony cap after the run = %d, want the original 1", got)
	}

	prompts, reports := recorder.snapshot()
	if len(prompts) != 2 || len(reports) != 2 {
		t.Fatalf("recorded %d prompts / %d reports, want 2/2", len(prompts), len(reports))
	}
	for _, report := range reports {
		if report["status"] != "success" {
			t.Fatalf("worker report = %#v, want success", report)
		}
	}

	runs := readPlaybookRunsForArtifact(t, colonyDir)
	saved := readSavedPlaybooksForArtifact(t, colonyDir)

	artifact := map[string]any{
		"script":        script,
		"prompts":       prompts,
		"reports":       reports,
		"payload":       payload,
		"payload_raw":   raw,
		"notice":        notice,
		"notice_text":   notice["summary"],
		"colony_dir":    colonyDir,
		"playbook_runs": runs,
		"saved_files":   saved,
		"model":         "configured via HIVE_HOME",
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
	}
	artifactDir := os.Getenv("PLAYBOOK_VERBATIM_ARTIFACTS")
	if artifactDir == "" {
		artifactDir = filepath.Join("..", "..", "..", "..", "docs", "e2e-assets", "playbook-verbatim", "real-llm")
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	artifactPath := filepath.Join(artifactDir, "run.json")
	if err := os.WriteFile(artifactPath, encoded, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	t.Logf("real LLM playbook payload: %s", raw)
	t.Logf("real LLM [PLAYBOOK_COMPLETE]:\n%s", notice["summary"])
	t.Logf("artifact: %s", artifactPath)
}

func readPlaybookRunsForArtifact(t *testing.T, colonyDir string) []map[string]any {
	t.Helper()
	runs := []map[string]any{}
	listed, err := colony.ListPlaybookRuns(filepath.Join(colonyDir, "colony.db"), 50)
	if err != nil {
		return runs
	}
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
	return runs
}

func readSavedPlaybooksForArtifact(t *testing.T, colonyDir string) map[string]string {
	t.Helper()
	saved := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(colonyDir, "playbooks"))
	if err != nil {
		return saved
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(colonyDir, "playbooks", entry.Name())); err == nil {
			saved[entry.Name()] = string(data)
		}
	}
	return saved
}

func emptyNoticeField(notice map[string]any, key string) int {
	if value, ok := notice[key].(int); ok {
		return value
	}
	if value, ok := notice[key].(float64); ok {
		return int(value)
	}
	return -1
}
