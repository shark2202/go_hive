package runtimelogs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// newTestLogger wires a store under tmp/logs (the Python fixture's base
// directory; not named runtime_logs, so sessions/ sits under it directly).
func newTestLogger(t *testing.T) (*RuntimeLogStore, *RuntimeLogger, string) {
	t.Helper()
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("test0001"))
	return store, logger, runID
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Parity: core/tests/test_runtime_logger.py::test_start_run_returns_run_id
func TestStartRunReturnsRunID(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	// A supplied session id is the run id verbatim (unified sessions).
	if got := logger.StartRun("goal-1", "session_20250101_000000_explicit"); got != "session_20250101_000000_explicit" {
		t.Fatalf("run id = %q", got)
	}
	// Without one, upstream generates the timestamped old format.
	logger2 := NewRuntimeLogger(store, "test-agent")
	generated := logger2.StartRun("goal-1", "")
	if !regexp.MustCompile(`^\d{8}T\d{6}_[0-9a-f]{8}$`).MatchString(generated) {
		t.Fatalf("generated run id = %q, want <ts>_<8 hex>", generated)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_start_run_creates_directory
func TestStartRunCreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("mkdir001"))
	want := filepath.Join(tmp, "logs", "sessions", runID, "logs")
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("expected directory %s, err=%v", want, err)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_log_step_writes_to_disk_immediately
func TestLogStepWritesToDiskImmediately(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("step0001"))
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		LLMText: "Searching.", InputTokens: 100, OutputTokens: 50,
	}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	path := filepath.Join(tmp, "logs", "sessions", runID, "logs", "tool_logs.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["node_id"] != "node-1" || data["input_tokens"].(float64) != 100 {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_log_node_complete_writes_to_disk_immediately
func TestLogNodeCompleteWritesToDiskImmediately(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("node0001"))
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search", NodeType: "event_loop",
		Success: true, ExitStatus: "success",
	}); err != nil {
		t.Fatalf("LogNodeComplete: %v", err)
	}
	path := filepath.Join(tmp, "logs", "sessions", runID, "logs", "details.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["node_id"] != "node-1" || data["exit_status"] != "success" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_full_lifecycle
func TestFullLifecycle(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("life0001"))

	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		Verdict: "RETRY", VerdictFeedback: "Missing output keys: ['result']",
		ToolCalls: []ToolCallInput{{
			ToolUseID: "tc_1", ToolName: "web_search",
			ToolInput: map[string]any{"query": "test"}, Content: "Found data",
		}},
		LLMText: "Let me search for that.", InputTokens: 100, OutputTokens: 50, LatencyMS: 1000,
	}); err != nil {
		t.Fatalf("LogStep 0: %v", err)
	}
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 1,
		Verdict: "CONTINUE", VerdictFeedback: "Unjudged",
		LLMText: "Processing...", InputTokens: 80, OutputTokens: 30, LatencyMS: 500,
	}); err != nil {
		t.Fatalf("LogStep 1: %v", err)
	}
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 2,
		Verdict: "ACCEPT", VerdictFeedback: "All outputs set",
		LLMText: "Here is your result.", InputTokens: 90, OutputTokens: 40, LatencyMS: 800,
	}); err != nil {
		t.Fatalf("LogStep 2: %v", err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search Node", NodeType: "event_loop",
		Success: true, TotalSteps: 3, TokensUsed: 390, InputTokens: 270, OutputTokens: 120,
		LatencyMS: 2300, ExitStatus: "success",
		AcceptCount: 1, RetryCount: 1, ContinueCount: 1,
	}); err != nil {
		t.Fatalf("LogNodeComplete: %v", err)
	}
	logger.EndRun("success", 2300, []string{"node-1"}, "clean")

	summary, err := store.LoadSummary(runID)
	if err != nil || summary == nil {
		t.Fatalf("LoadSummary: %v, %v", summary, err)
	}
	if summary.Status != "success" || summary.TotalNodesExecuted != 1 ||
		summary.TotalInputTokens != 270 || summary.TotalOutputTokens != 120 ||
		summary.NeedsAttention || summary.DurationMS != 2300 ||
		summary.ExecutionQuality != "clean" || len(summary.NodePath) != 1 || summary.NodePath[0] != "node-1" {
		t.Fatalf("summary = %+v", summary)
	}
	details, _ := store.LoadDetails(runID)
	if details == nil || len(details.Nodes) != 1 || details.Nodes[0].ExitStatus != "success" ||
		details.Nodes[0].AcceptCount != 1 || details.Nodes[0].RetryCount != 1 {
		t.Fatalf("details = %+v", details)
	}
	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || len(toolLogs.Steps) != 3 {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	if toolLogs.Steps[0].ToolCalls[0].ToolName != "web_search" ||
		toolLogs.Steps[0].InputTokens != 100 || toolLogs.Steps[0].Verdict != "RETRY" ||
		toolLogs.Steps[2].Verdict != "ACCEPT" {
		t.Fatalf("steps = %+v", toolLogs.Steps)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_trace_context_populated_in_l1_l2_l3
func TestTraceContextPopulatedInL1L2L3(t *testing.T) {
	previous := TraceContextProvider
	TraceContextProvider = func() (string, string) {
		return "a1b2c3d4e5f6789012345678abcdef01", "b2c3d4e5f6789012345678abcdef0123"
	}
	defer func() { TraceContextProvider = previous }()

	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", LLMText: "Step.", InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: true, ExitStatus: "success"}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("success", 100, []string{"node-1"}, "clean")

	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || len(toolLogs.Steps) != 1 {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	step := toolLogs.Steps[0]
	if step.TraceID != "a1b2c3d4e5f6789012345678abcdef01" ||
		step.ExecutionID != "b2c3d4e5f6789012345678abcdef0123" {
		t.Fatalf("step trace = %q/%q", step.TraceID, step.ExecutionID)
	}
	if len(step.SpanID) != 16 || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(step.SpanID) {
		t.Fatalf("span id = %q", step.SpanID)
	}
	details, _ := store.LoadDetails(runID)
	if details == nil || len(details.Nodes) != 1 || details.Nodes[0].TraceID != step.TraceID || len(details.Nodes[0].SpanID) != 16 {
		t.Fatalf("details = %+v", details)
	}
	summary, _ := store.LoadSummary(runID)
	if summary == nil || summary.TraceID != step.TraceID || summary.ExecutionID != step.ExecutionID {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestTraceContextEmptyWhenNotSet pins the upstream default: with neither a
// per-run binding nor a process provider, records carry empty trace ids.
//
// Parity: core/tests/test_runtime_logger.py::test_trace_context_empty_when_not_set
func TestTraceContextEmptyWhenNotSet(t *testing.T) {
	previous := TraceContextProvider
	TraceContextProvider = nil
	defer func() { TraceContextProvider = previous }()

	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", LLMText: "Step.", InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: true, ExitStatus: "success"}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("success", 100, []string{"node-1"}, "clean")

	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || toolLogs.Steps[0].TraceID != "" || toolLogs.Steps[0].ExecutionID != "" {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	details, _ := store.LoadDetails(runID)
	if details == nil || details.Nodes[0].TraceID != "" {
		t.Fatalf("details = %+v", details)
	}
	summary, _ := store.LoadSummary(runID)
	if summary == nil || summary.TraceID != "" || summary.ExecutionID != "" {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestSetTraceContextOverridesProcessDefault pins the per-logger binding the
// production host uses: a bound logger stamps its own pair on L3/L2/L1 even
// when a process-level provider is configured.
//
// Parity-exempt: upstream keeps one ambient ContextVar per async execution;
// the Go port has no goroutine-scoped equivalent, so the host binds the pair to
// each session's RuntimeLogger. The provider fallback itself is pinned by
// TestTraceContextPopulatedInL1L2L3.
func TestSetTraceContextOverridesProcessDefault(t *testing.T) {
	previous := TraceContextProvider
	TraceContextProvider = func() (string, string) {
		return strings.Repeat("e", 32), strings.Repeat("f", 32)
	}
	defer func() { TraceContextProvider = previous }()

	store, logger, runID := newTestLogger(t)
	traceID := strings.Repeat("a", 32)
	executionID := strings.Repeat("b", 32)
	logger.SetTraceContext(traceID, executionID)

	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", LLMText: "Step."}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Queen", NodeType: "event_loop", Success: true, ExitStatus: "success",
	}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("success", 100, []string{"node-1"}, "clean")

	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || len(toolLogs.Steps) != 1 {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	if got := toolLogs.Steps[0]; got.TraceID != traceID || got.ExecutionID != executionID {
		t.Fatalf("step trace = %q/%q, want the bound pair", got.TraceID, got.ExecutionID)
	}
	details, _ := store.LoadDetails(runID)
	if details == nil || len(details.Nodes) != 1 || details.Nodes[0].TraceID != traceID {
		t.Fatalf("details = %+v", details)
	}
	summary, _ := store.LoadSummary(runID)
	if summary == nil || summary.TraceID != traceID || summary.ExecutionID != executionID {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestTraceContextIsPerLoggerUnderConcurrency pins the concurrency-safety
// property the production wiring relies on: two sessions logging at the same
// time keep their own pairs, and no record carries the other session's ids.
//
// Parity-exempt: upstream scopes trace context to the async execution
// ContextVar; Go has no goroutine-local ContextVar, so the port stores the pair
// on the per-session RuntimeLogger.
func TestTraceContextIsPerLoggerUnderConcurrency(t *testing.T) {
	root := t.TempDir()
	type session struct {
		store       *RuntimeLogStore
		logger      *RuntimeLogger
		runID       string
		traceID     string
		executionID string
	}
	sessions := []*session{
		{
			store:       NewRuntimeLogStore(filepath.Join(root, "a", "runtime_logs")),
			runID:       "session_concurrent_a",
			traceID:     strings.Repeat("1", 32),
			executionID: strings.Repeat("2", 32),
		},
		{
			store:       NewRuntimeLogStore(filepath.Join(root, "b", "runtime_logs")),
			runID:       "session_concurrent_b",
			traceID:     strings.Repeat("3", 32),
			executionID: strings.Repeat("4", 32),
		},
	}
	var wg sync.WaitGroup
	for _, s := range sessions {
		s.logger = NewRuntimeLogger(s.store, "agent")
		s.runID = s.logger.StartRun("", s.runID)
		s.logger.SetTraceContext(s.traceID, s.executionID)
		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			for step := 0; step < 25; step++ {
				_ = s.logger.LogStep(StepInput{NodeID: "node", NodeType: "event_loop", StepIndex: step})
			}
			s.logger.EndRun("success", 1, []string{"node"}, "")
		}(s)
	}
	wg.Wait()

	for i, s := range sessions {
		other := sessions[1-i]
		steps, err := s.store.LoadToolLogs(s.runID)
		if err != nil || steps == nil || len(steps.Steps) != 25 {
			t.Fatalf("session %d steps = %+v, err=%v", i, steps, err)
		}
		for _, step := range steps.Steps {
			if step.TraceID != s.traceID || step.ExecutionID != s.executionID {
				t.Fatalf("session %d step trace = %q/%q, want %q/%q",
					i, step.TraceID, step.ExecutionID, s.traceID, s.executionID)
			}
			if step.TraceID == other.traceID || step.ExecutionID == other.executionID {
				t.Fatalf("session %d step leaked the other session's trace: %q/%q",
					i, step.TraceID, step.ExecutionID)
			}
		}
		summary, err := s.store.LoadSummary(s.runID)
		if err != nil || summary == nil || summary.TraceID != s.traceID || summary.ExecutionID != s.executionID {
			t.Fatalf("session %d summary = %+v, err=%v", i, summary, err)
		}
	}
}

// Parity: core/tests/test_runtime_logger.py::test_multi_node_lifecycle
func TestMultiNodeLifecycle(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", Verdict: "ACCEPT", LLMText: "Done.", InputTokens: 100, OutputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: true,
		TotalSteps: 1, TokensUsed: 150, InputTokens: 100, OutputTokens: 50, ExitStatus: "success", AcceptCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogStep(StepInput{NodeID: "node-2", NodeType: "event_loop", LatencyMS: 50}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-2", NodeName: "Process", NodeType: "event_loop", Success: true, TotalSteps: 1, LatencyMS: 50,
	}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("success", 1000, []string{"node-1", "node-2"}, "clean")

	summary, _ := store.LoadSummary(runID)
	if summary == nil || summary.TotalNodesExecuted != 2 || len(summary.NodePath) != 2 ||
		summary.TotalInputTokens != 100 || summary.TotalOutputTokens != 50 {
		t.Fatalf("summary = %+v", summary)
	}
	details, _ := store.LoadDetails(runID)
	if details == nil || len(details.Nodes) != 2 {
		t.Fatalf("details = %+v", details)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_failed_node_needs_attention
func TestFailedNodeNeedsAttention(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", Verdict: "ESCALATE",
		VerdictFeedback: "Cannot proceed, need human input", LLMText: "I'm stuck.",
		InputTokens: 50, OutputTokens: 20, LatencyMS: 300,
	}); err != nil {
		t.Fatal(err)
	}
	errText := "Judge escalated: Cannot proceed"
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: false,
		Error: &errText, TotalSteps: 1, TokensUsed: 70, LatencyMS: 300,
		ExitStatus: "escalated", EscalateCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("failure", 300, []string{"node-1"}, "failed")

	summary, _ := store.LoadSummary(runID)
	if summary == nil || !summary.NeedsAttention {
		t.Fatalf("summary = %+v", summary)
	}
	found := false
	for _, reason := range summary.AttentionReasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "failed") || strings.Contains(lower, "escalat") {
			found = true
		}
	}
	if !found {
		t.Fatalf("attention reasons = %v", summary.AttentionReasons)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_ensure_node_logged_no_op_if_already_logged
func TestEnsureNodeLoggedNoOpIfAlreadyLogged(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: true, ExitStatus: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.EnsureNodeLogged("node-1", "Search", "event_loop", true, "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	details, _ := store.ReadNodeDetailsSync(runID)
	if len(details) != 1 {
		t.Fatalf("details = %d, want 1", len(details))
	}
}

// Parity: core/tests/test_runtime_logger.py::test_ensure_node_logged_creates_entry_if_missing
func TestEnsureNodeLoggedCreatesEntryIfMissing(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.EnsureNodeLogged("node-1", "Search", "event_loop", false, "Crashed", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	details, _ := store.ReadNodeDetailsSync(runID)
	if len(details) != 1 {
		t.Fatalf("details = %d, want 1", len(details))
	}
	if details[0].Error == nil || *details[0].Error != "Crashed" || !details[0].NeedsAttention {
		t.Fatalf("detail = %+v", details[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_large_data_preserved
func TestLargeDataPreserved(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	longValue := strings.Repeat("x", 2000)
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", Verdict: "ACCEPT",
		ToolCalls: []ToolCallInput{{
			ToolUseID: "tc_1", ToolName: "write_file",
			ToolInput: map[string]any{"content": longValue},
			Content:   strings.Repeat("y", 5000),
		}},
		LLMText: strings.Repeat("z", 5000), InputTokens: 100, OutputTokens: 50, LatencyMS: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Writer", NodeType: "event_loop", Success: true, TotalSteps: 1, ExitStatus: "success",
	}); err != nil {
		t.Fatal(err)
	}
	logger.EndRun("success", 500, []string{"node-1"}, "")

	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || len(toolLogs.Steps) != 1 {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	call := toolLogs.Steps[0].ToolCalls[0]
	if content, _ := call.ToolInput["content"].(string); len(content) != 2000 {
		t.Fatalf("tool_input content length = %d", len(content))
	}
	if len(call.Result) != 5000 || len(toolLogs.Steps[0].LLMText) != 5000 {
		t.Fatalf("large values truncated: result=%d llm=%d", len(call.Result), len(toolLogs.Steps[0].LLMText))
	}
}

// Parity: core/tests/test_runtime_logger.py::test_end_run_does_not_propagate_exceptions
func TestEndRunDoesNotPropagateExceptions(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	logger := NewRuntimeLogger(store, "test-agent")
	runID := logger.StartRun("goal-1", sid("crash000"))
	// Make the atomic rename fail: summary.json exists as a directory.
	summaryPath := filepath.Join(tmp, "logs", "sessions", runID, "logs", "summary.json")
	if err := os.MkdirAll(summaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// EndRun has no error return by design; this must simply survive.
	logger.EndRun("success", 100, nil, "")
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("summary path missing: %v", err)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_crash_resilience_l2_l3_survive
func TestCrashResilienceL2L3Survive(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", StepIndex: 0, LLMText: "Working...", InputTokens: 100, OutputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", StepIndex: 1, LLMText: "Still working...", InputTokens: 80, OutputTokens: 30}); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search", NodeType: "event_loop", Success: true,
		TotalSteps: 2, InputTokens: 180, OutputTokens: 80,
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: end_run() is never called.
	details, _ := store.LoadDetails(runID)
	if details == nil || len(details.Nodes) != 1 || details.Nodes[0].NodeID != "node-1" {
		t.Fatalf("details = %+v", details)
	}
	toolLogs, _ := store.LoadToolLogs(runID)
	if toolLogs == nil || len(toolLogs.Steps) != 2 {
		t.Fatalf("tool logs = %+v", toolLogs)
	}
	summary, _ := store.LoadSummary(runID)
	if summary != nil {
		t.Fatalf("summary = %+v, want nil", summary)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_in_progress_run_visible_in_list
func TestInProgressRunVisibleInList(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogStep(StepInput{NodeID: "node-1", NodeType: "event_loop", StepIndex: 0, LLMText: "Working..."}); err != nil {
		t.Fatal(err)
	}
	runs, _ := store.ListRuns("", nil, 20)
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].Status != "in_progress" {
		t.Fatalf("runs = %+v", runs)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_log_step_with_error_and_stacktrace
func TestLogStepWithErrorAndStacktrace(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	stack := "Traceback (most recent call last):\n  File test.py line 10\n    raise TimeoutError()"
	if err := logger.LogStep(StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		Error: "LLM call failed: Connection timeout", Stacktrace: stack, IsPartial: true,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadToolLogs(runID)
	if loaded == nil || len(loaded.Steps) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	step := loaded.Steps[0]
	if step.Error != "LLM call failed: Connection timeout" || !strings.Contains(step.Stacktrace, "TimeoutError") || !step.IsPartial {
		t.Fatalf("step = %+v", step)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_log_node_complete_with_stacktrace
func TestLogNodeCompleteWithStacktrace(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	errText := "Node crashed"
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Test Node", NodeType: "event_loop", Success: false,
		Error: &errText, Stacktrace: "Traceback (most recent call last):\n  File node.py line 42\n    raise RuntimeError('crash')",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || len(loaded.Nodes) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	node := loaded.Nodes[0]
	if node.Error == nil || *node.Error != "Node crashed" || !strings.Contains(node.Stacktrace, "RuntimeError") {
		t.Fatalf("node = %+v", node)
	}
}

// Parity: core/tests/test_runtime_logger.py::test_attention_flags_excessive_retries
func TestAttentionFlagsExcessiveRetries(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Retry Node", NodeType: "event_loop", Success: true, RetryCount: 5,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || !loaded.Nodes[0].NeedsAttention || !hasReason(loaded.Nodes[0].AttentionReasons, "Excessive retries") {
		t.Fatalf("node = %+v", loaded.Nodes[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_attention_flags_high_latency
func TestAttentionFlagsHighLatency(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Slow Node", NodeType: "event_loop", Success: true, LatencyMS: 65000,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || !loaded.Nodes[0].NeedsAttention || !hasReason(loaded.Nodes[0].AttentionReasons, "High latency") {
		t.Fatalf("node = %+v", loaded.Nodes[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_attention_flags_high_token_usage
func TestAttentionFlagsHighTokenUsage(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Token Heavy Node", NodeType: "event_loop", Success: true, TokensUsed: 150000,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || !loaded.Nodes[0].NeedsAttention || !hasReason(loaded.Nodes[0].AttentionReasons, "High token usage") {
		t.Fatalf("node = %+v", loaded.Nodes[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_attention_flags_many_iterations
func TestAttentionFlagsManyIterations(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Iterative Node", NodeType: "event_loop", Success: true, TotalSteps: 25,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || !loaded.Nodes[0].NeedsAttention || !hasReason(loaded.Nodes[0].AttentionReasons, "Many iterations") {
		t.Fatalf("node = %+v", loaded.Nodes[0])
	}
}

// Parity: core/tests/test_runtime_logger.py::test_guard_failure_exit_status
func TestGuardFailureExitStatus(t *testing.T) {
	store, logger, runID := newTestLogger(t)
	errText := "LLM provider not available"
	if err := logger.LogNodeComplete(NodeCompleteInput{
		NodeID: "node-1", NodeName: "Guard Node", NodeType: "event_loop", Success: false,
		Error: &errText, ExitStatus: "guard_failure",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadDetails(runID)
	if loaded == nil || loaded.Nodes[0].ExitStatus != "guard_failure" || loaded.Nodes[0].Success {
		t.Fatalf("node = %+v", loaded.Nodes[0])
	}
}

func hasReason(reasons []string, needle string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}
