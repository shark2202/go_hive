package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

// reflectionEchoLLM separates the chat turn from the two reflection loops by
// their system prompts. For a reflection call it writes one queen-scoped
// memory file per pass and then answers with text once the tool result comes
// back, which is exactly how a real reflection loop terminates.
type reflectionEchoLLM struct {
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (f *reflectionEchoLLM) ModelName() string { return "reflection-echo" }

func (f *reflectionEchoLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	kind := ""
	switch {
	case strings.Contains(req.System, "periodic housekeeping pass"):
		kind = "long"
	case strings.Contains(req.System, "distills durable knowledge"):
		kind = "short"
	}
	if kind == "" {
		return llm.Completion{Content: "echo"}, nil
	}
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return llm.Completion{Content: "reflection done"}, nil
		}
	}
	filename := "turn-learnings.md"
	if kind == "long" {
		filename = "memory-housekeeping.md"
	}
	payload, _ := json.Marshal(map[string]any{
		"scope":    "queen",
		"filename": filename,
		"content": "---\nname: " + strings.TrimSuffix(filename, ".md") +
			"\ntype: preference\ndescription: reflected queen memory\n---\n" + kind + " pass body",
	})
	return llm.Completion{ToolCalls: []llm.ToolCall{{
		ID:        "tc_" + kind,
		Name:      "write_memory_file",
		Arguments: string(payload),
	}}}, nil
}

func (f *reflectionEchoLLM) reflectionSystems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		if strings.Contains(request.System, "reflection agent") {
			out = append(out, request.System)
		}
	}
	return out
}

func waitForPath(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Parity: core/tests/test_queen_memory.py::test_subscribe_reflection_triggers_runs_housekeeping_for_both_scopes
func TestChatRuntimeTurnDoneReflectsIntoQueenMemory(t *testing.T) {
	homeRoot := t.TempDir()
	// every_turns=1/long_every=1: the first finished turn exercises the short
	// pass and the long housekeeping pass, so one deliver proves the whole
	// chain (turn_done → scheduler → reflection → queen memory dir).
	config := `{"memory_reflection":{"every_turns":1,"long_every":1}}`
	if err := os.WriteFile(filepath.Join(homeRoot, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	model := &reflectionEchoLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		QueenName: "queen_technology",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.reflection == nil {
		t.Fatal("chat runtime built no reflection scheduler")
	}

	if err := runtime.Deliver("remember that I prefer implementation-first answers"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)

	queenDir := memory.QueenMemoryDir(homeRoot, "queen_technology")
	shortBody := waitForPath(t, filepath.Join(queenDir, "turn-learnings.md"), 5*time.Second)
	if !strings.Contains(string(shortBody), "short pass body") {
		t.Fatalf("short memory = %q", string(shortBody))
	}
	longBody := waitForPath(t, filepath.Join(queenDir, "memory-housekeeping.md"), 5*time.Second)
	if !strings.Contains(string(longBody), "long pass body") {
		t.Fatalf("long memory = %q", string(longBody))
	}
	// The long pass must have been handed the queen scope, not a global-only view.
	sawQueenScope := false
	for _, system := range model.reflectionSystems() {
		if strings.Contains(system, "periodic housekeeping pass") &&
			strings.Contains(system, "queen 'queen_technology'") {
			sawQueenScope = true
		}
	}
	if !sawQueenScope {
		t.Fatal("long reflection prompt did not name the queen scope")
	}

	if files := memory.ScanMemoryFiles(memory.GlobalMemoryDir(homeRoot)); len(files) != 0 {
		t.Fatalf("reflection escaped the queen scope into global memory: %v", files)
	}
	if runtime.queenMemoryDir != queenDir {
		t.Fatalf("runtime queen memory dir = %q, want %q", runtime.queenMemoryDir, queenDir)
	}
}

// Parity-exempt: a runtime without HIVE_HOME keeps the pre-memory behaviour
// (no recall, no reflection) — Go-only guard, no Python counterpart.
func TestChatRuntimeWithoutHomeRootSchedulesNoReflection(t *testing.T) {
	model := &reflectionEchoLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: model, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.reflection != nil {
		t.Fatal("reflection scheduler built without a memory root")
	}

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	if systems := model.reflectionSystems(); len(systems) != 0 {
		t.Fatalf("reflection ran without a memory root: %v", systems)
	}
}
