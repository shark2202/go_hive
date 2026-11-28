package agentloop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func spilloverToolResult(t *testing.T, payload string, maxChars int) (ToolResult, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	executor := func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == "big_tool" {
			return ToolResult{Content: payload}, nil
		}
		return ToolResult{Content: "ok"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "big_tool", Arguments: `{}`}}},
		{content: "done"},
	}}
	cfg := DefaultLoopConfig()
	cfg.MaxToolResultChars = maxChars
	cfg.SpilloverDir = dir
	ctx := &AgentContext{
		AgentID:        "spill_agent",
		AgentSpec:      NewAgentSpec("spill_agent", "Spill Agent", "spillover"),
		LLM:            completer,
		AvailableTools: []llm.ToolDef{{Name: "big_tool", Parameters: map[string]any{"type": "object"}}},
		StreamID:       "judge",
	}
	conv := NewNodeConversation("You are an agent.")
	loop := NewAgentLoop(cfg, executor)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	for _, message := range conv.Messages() {
		if message.Role == RoleTool {
			part := map[string]any{"message": message, "dir": dir}
			return ToolResult{
				Content:       message.Content,
				IsError:       message.IsError,
				SpilloverPath: derefString(message.SpilloverPath),
			}, part
		}
	}
	t.Fatalf("no tool message recorded; messages = %#v", conv.Messages())
	return ToolResult{}, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// Parity: framework/agent_loop/internals/tool_result_handler.py::truncate_tool_result
//
// Upstream writes EVERY non-error result to the spillover directory when one is
// configured — the disk copy is for debugging — but leaves a small result's
// LLM-visible content unchanged and adds no "[Saved to ...]" trailer (the
// trailer is the autocomplete poison pattern the upstream comment describes).
func TestToolResultUnderThresholdStillSpillsWithoutTrailer(t *testing.T) {
	result, part := spilloverToolResult(t, "short payload", 100)
	dir := part["dir"].(string)
	if result.Content != "short payload" {
		t.Fatalf("content = %q, want the untouched payload", result.Content)
	}
	if strings.Contains(result.Content, "Saved to") || strings.Contains(result.Content, "Full result at:") {
		t.Fatalf("small result grew a spill trailer: %q", result.Content)
	}
	if result.SpilloverPath == "" {
		t.Fatal("spillover path is empty; upstream writes every non-error result")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("spill directory = %v, want exactly one debugging copy", entries)
	}
	data, err := os.ReadFile(result.SpilloverPath)
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if string(data) != "short payload" {
		t.Fatalf("spill file = %q, want the full payload", data)
	}
}

// Parity-exempt: upstream has no standalone test for truncate_tool_result's spill branch; the port pins the model-visible path here.
func TestToolResultOverThresholdSpillsAndCitesPath(t *testing.T) {
	payload := strings.Repeat("x", 500)
	result, part := spilloverToolResult(t, payload, 100)
	dir := part["dir"].(string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("spill directory = %v, want exactly one file", entries)
	}
	if result.SpilloverPath == "" {
		t.Fatal("spillover path is empty on the tool message")
	}
	if !filepath.IsAbs(result.SpilloverPath) {
		t.Fatalf("spillover path = %q, want an absolute path", result.SpilloverPath)
	}
	if !strings.Contains(result.Content, "Full result at: "+result.SpilloverPath) {
		t.Fatalf("content does not cite the spill file: %q", result.Content)
	}
	hint := "For targeted lookup, run `grep -nE \"<pattern>\" \"" + result.SpilloverPath + "\"` via terminal_exec."
	if !strings.Contains(result.Content, hint) {
		t.Fatalf("content is missing the upstream grep recovery hint %q: %q", hint, result.Content)
	}
	extracted := ExtractSpilloverFilename(result.Content)
	if extracted == nil || *extracted != result.SpilloverPath {
		t.Fatalf("ExtractSpilloverFilename = %v, want %q", extracted, result.SpilloverPath)
	}
	data, err := os.ReadFile(result.SpilloverPath)
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("spill file carries %d bytes, want the full %d-byte payload", len(data), len(payload))
	}
}
