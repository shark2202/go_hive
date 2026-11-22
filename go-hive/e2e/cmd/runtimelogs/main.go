// Command runtimelogs is the Go half of the three-level runtime-log parity
// run (lane tool-logs-runtime). It is a test harness, not a shipped binary:
// the Python half lives in go_hive/scripts/runtime_logs_python_probe.py and
// both sides are driven by go_hive/scripts/runtime_logs_dual_run.sh.
//
// Both halves replay the same deterministic scenario through the production
// logger APIs:
//
//	run 1  session_20250101_000000_dualog01 — two steps (a RETRY with one
//	       timed web_search tool call, an ACCEPT), a successful node, end_run
//	       success/clean.
//	run 2  session_20250101_000000_dualog02 — one partial error step
//	       (stacktrace, is_partial) and a failed node; end_run failure/failed.
//
// The trace context is pinned to fixed ids on both sides so only the random
// span_id (16 hex chars per record) differs between the two trees; the diff
// script compares its shape, not its value.
//
// Usage:
//
//	go run ./e2e/cmd/runtimelogs --out <dir>
//
// Outputs the JSONL/JSON files under <dir>/sessions/<run_id>/logs/ (the
// upstream store layout for a base path that is <dir>/runtime_logs) and
// prints a JSON manifest of the written paths.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
)

const (
	run1ID = "session_20250101_000000_dualog01"
	run2ID = "session_20250101_000000_dualog02"

	traceID     = "a1b2c3d4e5f6789012345678abcdef01"
	executionID = "b2c3d4e5f6789012345678abcdef0123"

	toolStart = "2026-01-01T00:00:00.000000+00:00"
	timeouts  = "Traceback (most recent call last):\n  File \"test.py\", line 10, in run\n    raise TimeoutError()\nTimeoutError: Connection timeout"
)

func main() {
	out := flag.String("out", "", "output root (the store lands at <out>/sessions/<run_id>/logs/)")
	read := flag.String("read", "", "read mode: load an existing store under <dir> and print its readback")
	flag.Parse()
	if *read != "" {
		readBack(*read)
		return
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "runtimelogs: --out is required")
		os.Exit(2)
	}
	runtimelogs.TraceContextProvider = func() (string, string) { return traceID, executionID }

	base := filepath.Join(*out, "runtime_logs")
	store := runtimelogs.NewRuntimeLogStore(base)

	// ---- run 1: the happy path -------------------------------------------
	logger1 := runtimelogs.NewRuntimeLogger(store, "agent-parity")
	logger1.StartRun("goal-parity", run1ID)
	step0 := runtimelogs.StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		LLMText: "Searching for the answer.",
		ToolCalls: []runtimelogs.ToolCallInput{{
			ToolUseID: "tc_1", ToolName: "web_search",
			ToolInput:      map[string]any{"query": "hive parity"},
			Content:        "Found 3 results",
			IsError:        false,
			StartTimestamp: toolStart,
			DurationS:      0.125,
		}},
		InputTokens: 100, OutputTokens: 50, LatencyMS: 1200,
		Verdict: "RETRY", VerdictFeedback: "Missing output keys: ['result']",
	}
	step1 := runtimelogs.StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 1,
		LLMText:     "Here is the answer.",
		InputTokens: 90, OutputTokens: 40, LatencyMS: 800,
		Verdict: "ACCEPT", VerdictFeedback: "All outputs set",
	}
	for _, step := range []runtimelogs.StepInput{step0, step1} {
		if err := logger1.LogStep(step); err != nil {
			fail(err)
		}
	}
	if err := logger1.LogNodeComplete(runtimelogs.NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search Node", NodeType: "event_loop",
		Success: true, TotalSteps: 2, TokensUsed: 280, InputTokens: 190, OutputTokens: 90,
		LatencyMS: 2000, Attempt: 1, ExitStatus: "success",
		AcceptCount: 1, RetryCount: 1,
	}); err != nil {
		fail(err)
	}
	logger1.EndRun("success", 2000, []string{"node-1"}, "clean")

	// ---- run 2: the failure path ------------------------------------------
	logger2 := runtimelogs.NewRuntimeLogger(store, "agent-parity")
	logger2.StartRun("goal-parity", run2ID)
	errorMsg := "LLM call failed: Connection timeout"
	if err := logger2.LogStep(runtimelogs.StepInput{
		NodeID: "node-1", NodeType: "event_loop", StepIndex: 0,
		Error: errorMsg, Stacktrace: timeouts, IsPartial: true, LatencyMS: 1500,
	}); err != nil {
		fail(err)
	}
	if err := logger2.LogNodeComplete(runtimelogs.NodeCompleteInput{
		NodeID: "node-1", NodeName: "Search Node", NodeType: "event_loop",
		Success: false, Error: &errorMsg, Stacktrace: timeouts,
		TotalSteps: 1, LatencyMS: 1500, Attempt: 1, ExitStatus: "failure",
	}); err != nil {
		fail(err)
	}
	logger2.EndRun("failure", 1500, []string{"node-1"}, "failed")

	writeManifest(*out, store)
}

func writeManifest(out string, store *runtimelogs.RuntimeLogStore) {
	manifest := map[string]any{"out": out, "runs": []map[string]any{}}
	for _, runID := range []string{run1ID, run2ID} {
		dir := store.RunDir(runID)
		manifest["runs"] = append(manifest["runs"].([]map[string]any), map[string]any{
			"run_id":    runID,
			"dir":       dir,
			"tool_logs": filepath.Join(dir, "tool_logs.jsonl"),
			"details":   filepath.Join(dir, "details.jsonl"),
			"summary":   filepath.Join(dir, "summary.json"),
		})
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "runtimelogs: %v\n", err)
	os.Exit(1)
}

// readBack loads a store tree through the Go store API and prints a stable
// readback (no random ids), so the same JSON can be produced by the Python
// loader and compared — the cross-implementation read evidence.
func readBack(root string) {
	store := runtimelogs.NewRuntimeLogStore(filepath.Join(root, "runtime_logs"))
	runs, err := store.ListRuns("", nil, 20)
	if err != nil {
		fail(err)
	}
	type runRead struct {
		RunID     string   `json:"run_id"`
		Status    string   `json:"status"`
		Steps     int      `json:"steps"`
		Details   int      `json:"details"`
		Verdicts  []string `json:"verdicts"`
		ToolNames []string `json:"tool_names"`
		Attention bool     `json:"needs_attention"`
	}
	out := []runRead{}
	for _, summary := range runs {
		record := runRead{RunID: summary.RunID, Status: summary.Status, Attention: summary.NeedsAttention}
		if logs, _ := store.LoadToolLogs(summary.RunID); logs != nil {
			record.Steps = len(logs.Steps)
			for _, step := range logs.Steps {
				record.Verdicts = append(record.Verdicts, step.Verdict)
				for _, call := range step.ToolCalls {
					record.ToolNames = append(record.ToolNames, call.ToolName)
				}
			}
		}
		if details, _ := store.LoadDetails(summary.RunID); details != nil {
			record.Details = len(details.Nodes)
		}
		if record.Verdicts == nil {
			record.Verdicts = []string{}
		}
		if record.ToolNames == nil {
			record.ToolNames = []string{}
		}
		out = append(out, record)
	}
	encoded, err := json.MarshalIndent(map[string]any{"runs": out}, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}
