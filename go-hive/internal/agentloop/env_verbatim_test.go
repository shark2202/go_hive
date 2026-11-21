package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Evidence for the two debug-log switches: upstream's compaction.py and
// llm_debug_logger.py carry no test module, so these tests drive the real
// turn/compaction paths with the env gates on and assert the files operators
// read.

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "x")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func TestCompactionLevelLabelsMatchUpstream(t *testing.T) {
	cases := []struct {
		before, after float64
		want          string
	}{
		{0.90, 0.895, "prune_only"},
		{0.90, 0.40, "llm"},
		{0.95, 0.80, "structural"},
	}
	for _, testCase := range cases {
		if got := CompactionLevel(testCase.before, testCase.after); got != testCase.want {
			t.Fatalf("CompactionLevel(%v, %v) = %q, want %q", testCase.before, testCase.after, got, testCase.want)
		}
	}
}

// Parity: framework/agent_loop/internals/compaction.py::write_compaction_debug_log
func TestCompactionDebugLogWritesInventoryOnlyWhenEnabled(t *testing.T) {
	unsetEnv(t, "HIVE_COMPACTION_DEBUG")
	home := t.TempDir()
	if path := WriteCompactionDebugLog(home, "agent", "Agent", "stream", 0.9, 0.4, "llm", nil); path != "" {
		t.Fatalf("wrote %s with the gate off", path)
	}
	if _, err := os.Stat(filepath.Join(home, "compaction_log")); !os.IsNotExist(err) {
		t.Fatalf("compaction_log created with the gate off (%v)", err)
	}

	t.Setenv("HIVE_COMPACTION_DEBUG", "1")
	inventory := []map[string]any{
		{"seq": 1, "role": "user", "content_chars": 100, "tool_call_args_chars": 0},
		{"seq": 2, "role": "tool", "content_chars": 40000, "tool": "web_scrape", "is_error": true, "preview": "a large body"},
	}
	path := WriteCompactionDebugLog(home, "agent/one", "Agent", "stream-1", 0.9, 0.4, "llm", inventory)
	if path == "" {
		t.Fatal("compaction debug log was not written with the gate on")
	}
	if filepath.Dir(path) != filepath.Join(home, "compaction_log") {
		t.Fatalf("log path = %s, want under %s/compaction_log", path, home)
	}
	if !strings.Contains(filepath.Base(path), "agent_one") {
		t.Fatalf("file name %q should carry the slash-normalised node id", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{
		"# Compaction Debug — agent/one",
		"**Node:** Agent (`agent/one`)",
		"**Stream:** stream-1",
		"**Level:** llm",
		"**Usage:** 90% → 40%",
		"| 1 | 2 | tool | web_scrape | 40,000 |",
		"error",
		"a large body",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("compaction log missing %q:\n%s", want, raw)
		}
	}
}

// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func TestCompactionDebugLogRunsOnTheLiveCompactionPath(t *testing.T) {
	t.Setenv("HIVE_COMPACTION_DEBUG", "1")
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)

	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "the summary of the earlier work"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 200}, nil)
	agentCtx := midturnAgentContext(completer, "some_tool")
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    200,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage(strings.Repeat("x", 1000), UserMessageOptions{}); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if !conv.NeedsCompaction() {
		t.Fatal("test setup: conversation should need compaction")
	}
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, nil)

	entries, err := os.ReadDir(filepath.Join(home, "compaction_log"))
	if err != nil {
		t.Fatalf("compaction_log missing after the live path ran: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("compaction_log has %d files, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(home, "compaction_log", entries[0].Name()))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(raw), "**Node:** Agent (`agent`)") {
		t.Fatalf("report does not name the node:\n%s", raw)
	}
}

// Parity: framework/tracker/llm_debug_logger.py::log_llm_turn
func TestLLMTurnLogJSONLThroughARealTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_ENABLE_LLM_LOGS", "1")
	resetLLMDebugLogForTest()
	t.Cleanup(resetLLMDebugLogForTest)

	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "some_tool", Arguments: "{\"x\":1}"}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "tool output"}, nil
	})
	agentCtx := midturnAgentContext(completer, "some_tool")
	agentCtx.ExecutionID = "exec-1"
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(home, "llm_logs"))
	if err != nil {
		t.Fatalf("llm_logs missing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("llm_logs has %d files, want one JSONL", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(home, "llm_logs", entries[0].Name()))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a header plus two turn records, got %d lines:\n%s", len(lines), raw)
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header not JSON: %v", err)
	}
	if header["_kind"] != "session_header" || header["execution_id"] != "exec-1" || header["system_prompt"] != "You are a test assistant." {
		t.Fatalf("header = %#v", header)
	}
	// Upstream stamps with datetime.now(UTC).isoformat(); the Go record must
	// stay on the same UTC timeline (RFC3339 ends with "Z").
	if stamp, _ := header["timestamp"].(string); !strings.HasSuffix(stamp, "Z") {
		t.Fatalf("header timestamp = %q, want a UTC RFC3339 stamp", stamp)
	}
	var turn map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &turn); err != nil {
		t.Fatalf("turn not JSON: %v", err)
	}
	if turn["_kind"] != "turn" || turn["iteration"] != float64(1) {
		t.Fatalf("turn = %#v", turn)
	}
	toolResults, _ := turn["tool_results"].([]any)
	if len(toolResults) != 1 {
		t.Fatalf("tool_results = %#v, want one result", turn["tool_results"])
	}
	first, _ := toolResults[0].(map[string]any)
	if first["tool_name"] != "some_tool" || first["content"] != "tool output" {
		t.Fatalf("tool result = %#v", first)
	}
	tokenCounts, _ := turn["token_counts"].(map[string]any)
	if tokenCounts == nil {
		t.Fatalf("token_counts missing: %#v", turn)
	}
}

// Parity: framework/tracker/llm_debug_logger.py::log_llm_turn
func TestLLMTurnLogIsSilentWithoutTheOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_ENABLE_LLM_LOGS", "0")
	resetLLMDebugLogForTest()
	t.Cleanup(resetLLMDebugLogForTest)
	LogLLMTurn("agent", "stream", "exec", 1, "sys", nil, "text", nil, nil, nil, nil)
	if _, err := os.Stat(filepath.Join(home, "llm_logs")); !os.IsNotExist(err) {
		t.Fatalf("llm_logs created for HIVE_ENABLE_LLM_LOGS=0 (%v)", err)
	}
	// "TRUE" (upstream's strip().lower()) still opts in.
	t.Setenv("HIVE_ENABLE_LLM_LOGS", " TRUE ")
	LogLLMTurn("agent", "stream", "exec", 1, "sys", nil, "text", nil, nil, nil, nil)
	if _, err := os.Stat(filepath.Join(home, "llm_logs")); err != nil {
		t.Fatalf("llm_logs missing for HIVE_ENABLE_LLM_LOGS=\" TRUE \": %v", err)
	}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource
func TestTrackerWorkerToolCallsDefaultAndParse(t *testing.T) {
	if TrackerWorkerCadenceToolCalls != 20 {
		t.Fatalf("default cadence = %d, want 20 (WORKER_CADENCE_TOOL_CALLS)", TrackerWorkerCadenceToolCalls)
	}
	cases := []struct {
		value string
		want  int
	}{
		{"7", 7},
		{"1_0", 10},
		{" 12 ", 12},
		{"garbage", 20},
		{"", 20},
	}
	for _, testCase := range cases {
		t.Setenv("HIVE_TRACKER_WORKER_TOOL_CALLS", testCase.value)
		if got := envInt("HIVE_TRACKER_WORKER_TOOL_CALLS", 20); got != testCase.want {
			t.Fatalf("envInt(%q) = %d, want %d", testCase.value, got, testCase.want)
		}
	}
}
