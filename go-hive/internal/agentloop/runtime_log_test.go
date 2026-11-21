package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/runtimelogs"
)

// newRuntimeLogFixture wires the L3/L2 sink the way a host does: a store
// under <root>/runtime_logs and a session id as the run id.
func newRuntimeLogFixture(t *testing.T) (*runtimelogs.RuntimeLogger, string) {
	t.Helper()
	root := t.TempDir()
	store := runtimelogs.NewRuntimeLogStore(filepath.Join(root, "runtime_logs"))
	logger := runtimelogs.NewRuntimeLogger(store, "agent")
	runID := logger.StartRun("", "session_20250101_000000_looptest")
	return logger, filepath.Join(root, "sessions", runID, "logs")
}

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	records := []map[string]any{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("unmarshal %s: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// TestExecuteTurnWritesToolLogStep pins the loop trigger: a tool-call
// iteration through ExecuteTurn appends one L3 step to tool_logs.jsonl with
// the batch's tool calls, timing, tokens and the skip-judge RETRY verdict.
//
// Parity-exempt: no upstream unit test drives EventLoopNode's log_step call
// sites with a stub model; the record shapes are pinned by
// core/tests/test_runtime_logger.py, and this test pins the loop wiring.
func TestExecuteTurnWritesToolLogStep(t *testing.T) {
	logger, logsDir := newRuntimeLogFixture(t)
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "echo_line", Arguments: `{"text":"hi"}`}}},
		{content: "done"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "tool-output-" + req.Name}, nil
	}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, executor)
	ctx := midturnAgentContext(completer, "echo_line")
	ctx.RuntimeLogger = logger
	conv := NewNodeConversation("You are a test assistant.")
	tools := []llm.ToolDef{{Name: "echo_line", Parameters: map[string]any{"type": "object"}}}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, tools)
	if err != nil || !result.Success {
		t.Fatalf("ExecuteTurn: %v, success=%v", err, result.Success)
	}

	records := readJSONLines(t, filepath.Join(logsDir, "tool_logs.jsonl"))
	if len(records) != 1 {
		t.Fatalf("steps = %d, want 1 (the tool-call iteration)", len(records))
	}
	step := records[0]
	if step["node_id"] != "agent" || step["node_type"] != "event_loop" {
		t.Fatalf("step identity = %v/%v", step["node_id"], step["node_type"])
	}
	if step["verdict"] != "RETRY" {
		t.Fatalf("verdict = %v, want RETRY (skip-judge short circuit)", step["verdict"])
	}
	calls, ok := step["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v", step["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["tool_use_id"] != "c1" || call["tool_name"] != "echo_line" ||
		call["result"] != "tool-output-echo_line" || call["is_error"] != false {
		t.Fatalf("call = %v", call)
	}
	if started, _ := call["start_timestamp"].(string); started == "" {
		t.Fatalf("start_timestamp missing: %v", call)
	}
	if _, ok := call["duration_s"].(float64); !ok {
		t.Fatalf("duration_s missing: %v", call)
	}
	if input := call["tool_input"].(map[string]any); input["text"] != "hi" {
		t.Fatalf("tool_input = %v", input)
	}
}

// TestExecuteTurnLogsLLMFailureStep pins the loop's error trigger: a failed
// model call writes a partial step plus a failed node detail.
//
// Parity-exempt: no upstream unit test drives the loop's LLM-error branch with
// a stub model; the record shapes are pinned by
// core/tests/test_runtime_logger.py::test_log_step_with_error_and_stacktrace.
func TestExecuteTurnLogsLLMFailureStep(t *testing.T) {
	logger, logsDir := newRuntimeLogFixture(t)
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{err: context.DeadlineExceeded},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2}, func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{}, nil
	})
	ctx := midturnAgentContext(completer)
	ctx.RuntimeLogger = logger
	conv := NewNodeConversation("You are a test assistant.")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err == nil {
		t.Fatalf("ExecuteTurn should fail on the model error")
	}

	steps := readJSONLines(t, filepath.Join(logsDir, "tool_logs.jsonl"))
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if steps[0]["is_partial"] != true || steps[0]["error"] == "" {
		t.Fatalf("step = %v", steps[0])
	}
	if !strings.Contains(steps[0]["error"].(string), "LLM call failed:") {
		t.Fatalf("error = %v", steps[0]["error"])
	}
	nodes := readJSONLines(t, filepath.Join(logsDir, "details.jsonl"))
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0]["success"] != false || nodes[0]["exit_status"] != "failure" {
		t.Fatalf("node = %v", nodes[0])
	}
	reasons, _ := nodes[0]["attention_reasons"].([]any)
	if len(reasons) == 0 {
		t.Fatalf("attention_reasons = %v", nodes[0]["attention_reasons"])
	}
}

// TestWorkerStallTurnsLogContinueAndFailSteps pins the worker stall trigger:
// grace turns log CONTINUE steps, the exhausted worker logs FAIL and stops.
//
// Parity-exempt: no upstream unit test drives the worker stall policy through
// the loop with a stub model; feedback strings mirror
// framework/agent_loop/agent_loop.py (6h worker stall).
func TestWorkerStallTurnsLogContinueAndFailSteps(t *testing.T) {
	logger, logsDir := newRuntimeLogFixture(t)
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "thinking one"},
		{content: "thinking two"},
		{content: "thinking three"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 8, WorkerEscalationGraceTurns: 2}, executor)
	ctx := midturnAgentContext(completer)
	ctx.StreamID = "worker:w1"
	ctx.StopAfterToolBatch = func() bool { return false }
	ctx.RuntimeLogger = logger
	conv := NewNodeConversation("You are a test worker.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("auto-failed worker terminates cleanly, got %+v", result)
	}
	steps := readJSONLines(t, filepath.Join(logsDir, "tool_logs.jsonl"))
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}
	if steps[0]["verdict"] != "CONTINUE" || steps[0]["verdict_feedback"] != "Worker stall grace (1/2)" {
		t.Fatalf("grace step 1 = %v", steps[0])
	}
	if steps[1]["verdict"] != "CONTINUE" || steps[1]["verdict_feedback"] != "Worker stall grace (2/2)" {
		t.Fatalf("grace step 2 = %v", steps[1])
	}
	if steps[2]["verdict"] != "FAIL" || steps[2]["verdict_feedback"] != "Auto-failed: stall 3/2" {
		t.Fatalf("auto-fail step = %v", steps[2])
	}
}

// TestCompactionLogsCompactionStep pins the compaction trigger shape: node id
// = agent id, step_index -1, verdict COMPACTION, the upstream text/feedback.
//
// Parity-exempt: no upstream unit test pins log_compaction's runtime-log
// fields directly; the step shape mirrors
// framework/agent_loop/internals/compaction.py::log_compaction.
func TestCompactionLogsCompactionStep(t *testing.T) {
	logger, logsDir := newRuntimeLogFixture(t)
	loop := NewAgentLoop(LoopConfig{}, func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{}, nil
	})
	ctx := midturnAgentContext(&scriptedCompleter{})
	ctx.RuntimeLogger = logger
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "You are a test assistant.",
		MaxContextTokens: 10000,
	})
	for i := 0; i < 40; i++ {
		if _, err := conv.AddUserMessage(strings.Repeat("m", 500), UserMessageOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	ratioBefore := conv.UsageRatio()
	loop.logCompactionStep(ctx, conv, ratioBefore, 10000)

	steps := readJSONLines(t, filepath.Join(logsDir, "tool_logs.jsonl"))
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	step := steps[0]
	if step["node_id"] != "agent" || step["step_index"].(float64) != -1 || step["verdict"] != "COMPACTION" {
		t.Fatalf("step = %v", step)
	}
	llmText, _ := step["llm_text"].(string)
	if !strings.HasPrefix(llmText, "Context compacted (") || !strings.Contains(llmText, "→") {
		t.Fatalf("llm_text = %q", llmText)
	}
	feedback, _ := step["verdict_feedback"].(string)
	if !strings.HasPrefix(feedback, "level=") || !strings.Contains(feedback, "before=") || !strings.Contains(feedback, "after=") {
		t.Fatalf("verdict_feedback = %q", feedback)
	}
}
