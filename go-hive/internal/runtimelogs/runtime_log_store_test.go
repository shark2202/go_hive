package runtimelogs

import (
	"os"
	"path/filepath"
	"testing"
)

func sid(suffix string) string { return "session_20250101_000000_" + suffix }

// Parity: core/tests/test_runtime_logger.py::test_ensure_run_dir_creates_directory
func TestEnsureRunDirCreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("test0001")); err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	want := filepath.Join(tmp, "logs", "sessions", sid("test0001"), "logs")
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("expected directory %s, err=%v", want, err)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_append_and_load_details
func TestAppendAndLoadDetails(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("test0002")); err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	detail1 := NodeDetail{
		NodeID: "node-1", NodeName: "Search Node", NodeType: "event_loop",
		Success: true, TotalSteps: 2, ExitStatus: "success", AcceptCount: 1, RetryCount: 1,
	}
	detail2 := NodeDetail{NodeID: "node-2", NodeName: "Process Node", NodeType: "event_loop", Success: true, TotalSteps: 1}
	if err := store.AppendNodeDetail(sid("test0002"), &detail1); err != nil {
		t.Fatalf("AppendNodeDetail: %v", err)
	}
	if err := store.AppendNodeDetail(sid("test0002"), &detail2); err != nil {
		t.Fatalf("AppendNodeDetail: %v", err)
	}
	loaded, err := store.LoadDetails(sid("test0002"))
	if err != nil || loaded == nil {
		t.Fatalf("LoadDetails: %v, %v", loaded, err)
	}
	if len(loaded.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(loaded.Nodes))
	}
	if loaded.Nodes[0].NodeID != "node-1" || loaded.Nodes[0].ExitStatus != "success" {
		t.Fatalf("node[0] = %+v", loaded.Nodes[0])
	}
	if loaded.Nodes[1].NodeType != "event_loop" {
		t.Fatalf("node[1] = %+v", loaded.Nodes[1])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_append_and_load_tool_logs
func TestAppendAndLoadToolLogs(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("test0003")); err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	step := NodeStepLog{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		LLMText: "I will search for the data.",
		ToolCalls: []ToolCallLog{{
			ToolUseID: "tc_1", ToolName: "web_search",
			ToolInput: map[string]any{"query": "test"}, Result: "Found 3 results",
		}},
		InputTokens: 100, OutputTokens: 50, LatencyMS: 1200, Verdict: "CONTINUE",
	}
	if err := store.AppendStep(sid("test0003"), &step); err != nil {
		t.Fatalf("AppendStep: %v", err)
	}
	loaded, err := store.LoadToolLogs(sid("test0003"))
	if err != nil || loaded == nil {
		t.Fatalf("LoadToolLogs: %v, %v", loaded, err)
	}
	if len(loaded.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(loaded.Steps))
	}
	if loaded.Steps[0].ToolCalls[0].ToolName != "web_search" ||
		loaded.Steps[0].InputTokens != 100 || loaded.Steps[0].NodeID != "node-1" {
		t.Fatalf("step = %+v", loaded.Steps[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_save_and_load_summary
func TestSaveAndLoadSummary(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	summary := RunSummaryLog{
		RunID: sid("test0001"), AgentID: "agent-a", GoalID: "goal-1", Status: "success",
		TotalNodesExecuted: 3, NodePath: []string{"node-1", "node-2", "node-3"},
		StartedAt: "2025-01-01T00:00:00", DurationMS: 5000, ExecutionQuality: "clean",
	}
	if err := store.SaveSummary(sid("test0001"), &summary); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	loaded, err := store.LoadSummary(sid("test0001"))
	if err != nil || loaded == nil {
		t.Fatalf("LoadSummary: %v, %v", loaded, err)
	}
	if loaded.RunID != sid("test0001") || loaded.Status != "success" ||
		loaded.TotalNodesExecuted != 3 || loaded.GoalID != "goal-1" || loaded.ExecutionQuality != "clean" {
		t.Fatalf("summary = %+v", loaded)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_load_missing_run_returns_none
func TestLoadMissingRunReturnsNone(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if got, err := store.LoadSummary(sid("missing00")); got != nil || err != nil {
		t.Fatalf("LoadSummary = %v, %v", got, err)
	}
	if got, err := store.LoadDetails(sid("missing00")); got != nil || err != nil {
		t.Fatalf("LoadDetails = %v, %v", got, err)
	}
	if got, err := store.LoadToolLogs(sid("missing00")); got != nil || err != nil {
		t.Fatalf("LoadToolLogs = %v, %v", got, err)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_list_runs_empty
func TestListRunsEmpty(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	runs, err := store.ListRuns("", nil, 20)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs = %v, err=%v", runs, err)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_list_runs_with_filter
func TestListRunsWithFilter(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("runok000")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSummary(sid("runok000"), &RunSummaryLog{
		RunID: sid("runok000"), Status: "success", StartedAt: "2025-01-01T00:00:01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRunDir(sid("runfail0")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSummary(sid("runfail0"), &RunSummaryLog{
		RunID: sid("runfail0"), Status: "failure", NeedsAttention: true, StartedAt: "2025-01-01T00:00:02",
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListRuns("", nil, 20)
	if len(all) != 2 {
		t.Fatalf("all runs = %d, want 2", len(all))
	}
	success, _ := store.ListRuns("success", nil, 20)
	if len(success) != 1 || success[0].RunID != sid("runok000") {
		t.Fatalf("success runs = %+v", success)
	}
	attention, _ := store.ListRuns("needs_attention", nil, 20)
	if len(attention) != 1 || attention[0].RunID != sid("runfail0") {
		t.Fatalf("attention runs = %+v", attention)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_list_runs_sorted_by_timestamp_desc
func TestListRunsSortedByTimestampDesc(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	for i := 0; i < 5; i++ {
		runID := "session_20250101_0000" + twoDigits(i) + "_run" + fourDigits(i)
		if err := store.EnsureRunDir(runID); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSummary(runID, &RunSummaryLog{
			RunID: runID, Status: "success", StartedAt: "2025-01-01T00:00:" + twoDigits(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := store.ListRuns("", nil, 20)
	if len(runs) != 5 {
		t.Fatalf("runs = %d, want 5", len(runs))
	}
	if runs[0].RunID != "session_20250101_000004_run0004" || runs[4].RunID != "session_20250101_000000_run0000" {
		t.Fatalf("order: first=%s last=%s", runs[0].RunID, runs[4].RunID)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_list_runs_limit
func TestListRunsLimit(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	for i := 0; i < 10; i++ {
		runID := "session_20250101_0000" + twoDigits(i) + "_run" + fourDigits(i)
		if err := store.EnsureRunDir(runID); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSummary(runID, &RunSummaryLog{
			RunID: runID, Status: "success", StartedAt: "2025-01-01T00:00:" + twoDigits(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := store.ListRuns("", nil, 3)
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
}

// Parity: core/tests/test_runtime_logger.py::test_list_runs_includes_in_progress
func TestListRunsIncludesInProgress(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("rundone0")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSummary(sid("rundone0"), &RunSummaryLog{
		RunID: sid("rundone0"), Status: "success", StartedAt: "2025-01-01T00:00:01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRunDir(sid("runactiv0")); err != nil {
		t.Fatal(err)
	}
	runs, _ := store.ListRuns("", nil, 20)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	found := false
	for _, run := range runs {
		if run.RunID == sid("runactiv0") {
			found = true
			if run.Status != "in_progress" {
				t.Fatalf("status = %q, want in_progress", run.Status)
			}
		}
	}
	if !found {
		t.Fatalf("in-progress run missing from %+v", runs)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_read_node_details_sync
func TestReadNodeDetailsSync(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("testsync0")); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendNodeDetail(sid("testsync0"), &NodeDetail{
		NodeID: "n1", NodeName: "A", Success: true, InputTokens: 100, OutputTokens: 50,
	}); err != nil {
		t.Fatal(err)
	}
	errText := "oops"
	if err := store.AppendNodeDetail(sid("testsync0"), &NodeDetail{
		NodeID: "n2", NodeName: "B", Success: false, Error: &errText,
	}); err != nil {
		t.Fatal(err)
	}
	details, err := store.ReadNodeDetailsSync(sid("testsync0"))
	if err != nil || len(details) != 2 {
		t.Fatalf("details = %d, err=%v", len(details), err)
	}
	if details[0].NodeID != "n1" || details[1].Error == nil || *details[1].Error != "oops" {
		t.Fatalf("details = %+v", details)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_corrupt_jsonl_line_skipped
func TestCorruptJSONLLineSkipped(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	if err := store.EnsureRunDir(sid("corrupt00")); err != nil {
		t.Fatal(err)
	}
	valid1 := `{"node_id": "n1", "node_name": "A", "success": true}`
	valid2 := `{"node_id": "n2", "node_name": "B", "success": true}`
	jsonlPath := filepath.Join(tmp, "logs", "sessions", sid("corrupt00"), "logs", "details.jsonl")
	content := valid1 + "\n{corrupt line\n" + valid2 + "\n"
	if err := os.WriteFile(jsonlPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	details, err := store.ReadNodeDetailsSync(sid("corrupt00"))
	if err != nil || len(details) != 2 {
		t.Fatalf("details = %d, err=%v", len(details), err)
	}
	if details[0].NodeID != "n1" || details[1].NodeID != "n2" {
		t.Fatalf("details = %+v", details)
	}
}

// twoDigits/fourDigits keep the session-id fixtures byte-identical to the
// Python test's f-strings without pulling fmt into every assertion.
func twoDigits(v int) string { return string([]byte{byte('0' + v/10), byte('0' + v%10)}) }

func fourDigits(v int) string {
	return "000" + string([]byte{byte('0' + v%10)})
}
