package runtimelogs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// topLevelKeys returns the keys of a JSON object in wire order.
func topLevelKeys(t *testing.T, raw string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		t.Fatalf("expected object start, got %v", token)
	}
	keys := []string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatalf("key token: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("expected string key, got %v", keyToken)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatalf("skip value: %v", err)
		}
	}
	return keys
}

func assertKeyOrder(t *testing.T, raw string, want []string) {
	t.Helper()
	got := topLevelKeys(t, raw)
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// nodeStepKeys mirrors the Pydantic declaration order of NodeStepLog.
var nodeStepKeys = []string{
	"node_id", "node_type", "step_index", "llm_text", "tool_calls",
	"input_tokens", "output_tokens", "latency_ms", "verdict", "verdict_feedback",
	"error", "stacktrace", "is_partial", "trace_id", "span_id", "parent_span_id", "execution_id",
}

var toolCallKeys = []string{
	"tool_use_id", "tool_name", "tool_input", "result", "is_error", "start_timestamp", "duration_s",
}

var nodeDetailKeys = []string{
	"node_id", "node_name", "node_type", "success", "error", "stacktrace", "total_steps",
	"tokens_used", "input_tokens", "output_tokens", "latency_ms", "attempt", "exit_status",
	"accept_count", "retry_count", "escalate_count", "continue_count", "needs_attention",
	"attention_reasons", "trace_id", "span_id",
}

var runSummaryKeys = []string{
	"run_id", "agent_id", "goal_id", "status", "total_nodes_executed", "node_path",
	"total_input_tokens", "total_output_tokens", "needs_attention", "attention_reasons",
	"started_at", "duration_ms", "execution_quality", "trace_id", "execution_id",
}

// TestNodeStepLogFieldOrderAndDefaults pins the L3 wire shape: field names,
// declaration order, and the Pydantic defaults that model_dump() always emits
// ([] for lists, {} for the tool input, null for the optional NodeDetail
// error). The Python schema is the contract; a reordered or renamed struct
// field must fail here.
//
// Parity: framework/tracker/runtime_log_schemas.py::NodeStepLog
func TestNodeStepLogFieldOrderAndDefaults(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	runID := "session_20250101_000000_schema01"
	if err := store.EnsureRunDir(runID); err != nil {
		t.Fatal(err)
	}
	// Zero value: every Pydantic default applies.
	if err := store.AppendStep(runID, &NodeStepLog{NodeID: "node-1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.RunDir(runID), "tool_logs.jsonl")
	raw := strings.TrimSpace(readLines(t, path)[0])
	assertKeyOrder(t, raw, nodeStepKeys)
	if !strings.Contains(raw, `"tool_calls":[]`) {
		t.Fatalf("zero tool_calls must render as []; got %s", raw)
	}

	// One typed tool call: its own key order, tool_input {} default.
	step := NodeStepLog{
		NodeID:    "node-1",
		ToolCalls: []ToolCallLog{{ToolUseID: "tc_1", ToolName: "web_search", DurationS: 0.25}},
	}
	if err := store.AppendStep(runID, &step); err != nil {
		t.Fatal(err)
	}
	line := readLines(t, path)[1]
	assertKeyOrder(t, line, nodeStepKeys)
	var decoded struct {
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d", len(decoded.ToolCalls))
	}
	assertKeyOrder(t, string(decoded.ToolCalls[0]), toolCallKeys)
	if !strings.Contains(string(decoded.ToolCalls[0]), `"tool_input":{}`) {
		t.Fatalf("nil tool_input must render as {}; got %s", decoded.ToolCalls[0])
	}
	if !strings.Contains(string(decoded.ToolCalls[0]), `"duration_s":0.25`) {
		t.Fatalf("duration_s must render as a JSON number; got %s", decoded.ToolCalls[0])
	}
}

// TestNodeDetailFieldOrderAndNullError pins the L2 wire shape.
//
// Parity: framework/tracker/runtime_log_schemas.py::NodeDetail
func TestNodeDetailFieldOrderAndNullError(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	runID := "session_20250101_000000_schema02"
	if err := store.EnsureRunDir(runID); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendNodeDetail(runID, &NodeDetail{NodeID: "node-1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.RunDir(runID), "details.jsonl")
	raw := strings.TrimSpace(readLines(t, path)[0])
	assertKeyOrder(t, raw, nodeDetailKeys)
	if !strings.Contains(raw, `"error":null`) {
		t.Fatalf("nil error must render as null; got %s", raw)
	}
	if !strings.Contains(raw, `"attempt":1`) {
		t.Fatalf("default attempt must render as 1; got %s", raw)
	}
	if !strings.Contains(raw, `"attention_reasons":[]`) {
		t.Fatalf("zero attention_reasons must render as []; got %s", raw)
	}
}

// TestRunSummaryFieldOrderAndIndentation pins the L1 document shape: field
// names/order plus json.dumps(indent=2, ensure_ascii=False) formatting.
//
// Parity: framework/tracker/runtime_log_schemas.py::RunSummaryLog
func TestRunSummaryFieldOrderAndIndentation(t *testing.T) {
	tmp := t.TempDir()
	store := NewRuntimeLogStore(filepath.Join(tmp, "logs"))
	runID := "session_20250101_000000_schema03"
	if err := store.SaveSummary(runID, &RunSummaryLog{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunDir(runID), "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	if !strings.HasPrefix(raw, "{\n  \"run_id\":") {
		t.Fatalf("summary must be two-space indented; got %q", raw[:min(len(raw), 60)])
	}
	if strings.HasSuffix(raw, "\n") {
		t.Fatalf("summary must not end with a trailing newline")
	}
	if !strings.Contains(raw, `"node_path": []`) || !strings.Contains(raw, `"attention_reasons": []`) {
		t.Fatalf("list defaults must render as [] with the indent separator; got %s", raw)
	}
	keyOrder := topLevelKeys(t, raw)
	if len(keyOrder) != len(runSummaryKeys) {
		t.Fatalf("keys = %v, want %v", keyOrder, runSummaryKeys)
	}
	for i := range runSummaryKeys {
		if keyOrder[i] != runSummaryKeys[i] {
			t.Fatalf("key[%d] = %q, want %q", i, keyOrder[i], runSummaryKeys[i])
		}
	}
	// ASCII: Python's ensure_ascii=False writes UTF-8 as-is (no \u escapes)
	// and json.dumps never HTML-escapes; encoding/json must not either.
	if err := store.SaveSummary(runID, &RunSummaryLog{
		RunID: runID, AgentID: "agent<&>", ExecutionQuality: "clean",
	}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(store.RunDir(runID), "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "agent<&>") {
		t.Fatalf("HTML characters must not be escaped; got %s", data)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
