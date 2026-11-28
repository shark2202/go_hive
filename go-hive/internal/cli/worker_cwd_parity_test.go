package cli

// Colony worker execution-root regression test.
//
// Parity: framework/host/worker.py::Worker.run — a worker's tool execution
// context is scoped to its storage directory (<colony>/workers/<id>), so its
// built-in file tools write/read there and its terminal tools default their
// cwd there. This test runs a real worker through the real colony runtime and
// real session tool catalogue; a config-only assertion would not catch the
// root drifting back to the colony workspace.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// workerRootProbeLLM scripts one worker turn sequence (write, read, pwd,
// report) and records every request so the tool results can be inspected.
type workerRootProbeLLM struct {
	mu    sync.Mutex
	steps []llm.ToolCall
	calls []llm.CompletionRequest
	next  int
}

func (m *workerRootProbeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	if m.next >= len(m.steps) {
		return llm.Completion{Content: "Done.", StopReason: "stop"}, nil
	}
	call := m.steps[m.next]
	m.next++
	return llm.Completion{ToolCalls: []llm.ToolCall{call}, StopReason: "tool_calls"}, nil
}

// toolResult returns the tool message the loop sent back for callID, or "" when
// the worker never reached the model after that call.
func (m *workerRootProbeLLM) toolResult(callID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.calls {
		for _, message := range req.Messages {
			if message.Role == "tool" && message.ToolCallID == callID {
				return message.Content
			}
		}
	}
	return ""
}

func (m *workerRootProbeLLM) seenCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func probeToolCall(t *testing.T, id, name string, input map[string]any) llm.ToolCall {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode %s arguments: %v", name, err)
	}
	return llm.ToolCall{ID: id, Name: name, Arguments: string(encoded)}
}

// terminalStdout decodes a terminal_exec envelope and returns its stdout.
func terminalStdout(t *testing.T, content string) string {
	t.Helper()
	var envelope struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("terminal_exec result is not an envelope (%q): %v", content, err)
	}
	return strings.TrimSpace(envelope.Stdout)
}

// TestColonyWorkerToolRootDelegatesNonBuiltinTools pins the seam's fallback:
// only the builtin surface is re-rooted, everything else still runs through
// the colony executor the runtime handed in (a trigger pipeline's registry,
// for example). Without this a fragmented worker catalogue could silently
// shadow tools the worker surface advertises.
//
// Parity-exempt: Go-only runtime seam coverage - pins that executorForRoot
// re-roots only the builtin surface and delegates every other tool to the
// colony executor. framework/host/worker.py has no Python unit test for this
// fallback split.
func TestColonyWorkerToolRootDelegatesNonBuiltinTools(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("fallback")
	workerRoot := filepath.Join(colonyPath, "workers", "w_fallback")
	if err := os.MkdirAll(workerRoot, 0o755); err != nil {
		t.Fatalf("mkdir worker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "probe.txt"), []byte("worker-root"), 0o644); err != nil {
		t.Fatalf("seed worker file: %v", err)
	}
	surface := newColonyWorkerSurface(colonyPath, builtin.New(colonyPath), home)

	var baseCalls []string
	base := func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		baseCalls = append(baseCalls, request.Name)
		return agentloop.ToolResult{Content: "base:" + request.Name}, nil
	}
	executor := surface.executorForRoot(workerRoot, base)

	// A builtin reads from the worker root.
	result, err := executor(agentloop.ToolCallRequest{Name: "read_file", Input: map[string]any{"path": "probe.txt"}})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(result.Content, "worker-root") {
		t.Fatalf("read_file did not read the worker root: %q", result.Content)
	}
	if len(baseCalls) != 0 {
		t.Fatalf("builtin tool fell through to the colony executor: %v", baseCalls)
	}

	// A non-builtin tool still reaches the colony executor.
	result, err = executor(agentloop.ToolCallRequest{Name: "csv", Input: map[string]any{"action": "info"}})
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if result.Content != "base:csv" {
		t.Fatalf("csv result = %q, want the colony executor's result", result.Content)
	}
	if len(baseCalls) != 1 || baseCalls[0] != "csv" {
		t.Fatalf("colony executor calls = %v, want [csv]", baseCalls)
	}
}

// Parity-exempt: Go-only end-to-end regression for the per-worker tool
// execution root - drives a real worker through the colony runtime and real
// session catalogue. The Go root seam (SetWorkerToolExecutorFactory) has no
// Python unit counterpart.
func TestColonyWorkerToolRootIsWorkerStorage(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("cwd_parity")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}

	toolbox := builtin.New(colonyPath)
	surface := newColonyWorkerSurface(colonyPath, toolbox, home)

	probe := &workerRootProbeLLM{steps: []llm.ToolCall{
		probeToolCall(t, "call_write", "write_file", map[string]any{
			"path":    "worker_probe.txt",
			"content": "worker-root",
		}),
		probeToolCall(t, "call_read", "read_file", map[string]any{
			"path": "worker_probe.txt",
		}),
		probeToolCall(t, "call_pwd", "terminal_exec", map[string]any{
			"command": "pwd",
		}),
		probeToolCall(t, "call_report", "report_to_parent", map[string]any{
			"status":  "success",
			"summary": "probe complete",
			"data":    map[string]any{},
		}),
	}}

	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:     agentloop.AgentSpec{ID: "probe_worker", Name: "Probe Worker", AgentType: "event_loop", ToolAccessPolicy: "all"},
		StoragePath:   colonyPath,
		LLM:           probe,
		Tools:         surface.baseTools,
		DeclaredTools: surface.declaredTools,
		ToolExecutor:  surface.execute,
		// Turn the tool-tier split off so every builtin tool is directly
		// available: this test is about the execution root, not discovery.
		WorkerKeepSet: func() map[string]struct{} { return map[string]struct{}{} },
		StreamID:      "cwd_parity",
	})
	runtime.SetWorkerToolExecutorFactory(surface.executorForRoot)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start colony runtime: %v", err)
	}
	t.Cleanup(runtime.Stop)

	ids, err := runtime.SpawnBatch(context.Background(), []colony.BatchTask{{Task: "probe the worker root"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("spawned %d workers, want 1", len(ids))
	}
	workerID := ids[0]
	workerDir := filepath.Join(colonyPath, "workers", workerID)
	deadline := time.Now().Add(20 * time.Second)
	for {
		worker := runtime.GetWorker(workerID)
		if worker != nil && worker.Status().Done() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not finish (calls=%d)", probe.seenCalls())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The worker's relative write landed in its own storage directory.
	written, err := os.ReadFile(filepath.Join(workerDir, "worker_probe.txt"))
	if err != nil {
		t.Fatalf("worker file is missing from its storage root %s: %v", workerDir, err)
	}
	if string(written) != "worker-root" {
		t.Fatalf("worker file content = %q, want %q", string(written), "worker-root")
	}
	// Nothing was written at the colony workspace root.
	if _, err := os.Stat(filepath.Join(colonyPath, "worker_probe.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker write leaked to the colony root (%v)", err)
	}

	// The write succeeded (the builtin reports success text) and the read came
	// back from the same worker-owned file.
	if write := probe.toolResult("call_write"); !strings.HasPrefix(write, "Created worker_probe.txt") {
		t.Fatalf("write_file did not create the worker file: %q", write)
	}
	if read := probe.toolResult("call_read"); !strings.Contains(read, "worker-root") {
		t.Fatalf("read_file did not read from the worker root: %q", read)
	}

	// terminal_exec without an explicit cwd runs in the worker storage dir.
	pwd := terminalStdout(t, probe.toolResult("call_pwd"))
	if pwd == "" {
		t.Fatal("terminal_exec returned no stdout")
	}
	workerDirReal, err := filepath.EvalSymlinks(workerDir)
	if err != nil {
		t.Fatalf("resolve worker root: %v", err)
	}
	colonyRootReal, err := filepath.EvalSymlinks(colonyPath)
	if err != nil {
		t.Fatalf("resolve colony root: %v", err)
	}
	if pwd != workerDir && pwd != workerDirReal {
		t.Fatalf("terminal_exec default cwd = %q, want the worker root %q", pwd, workerDir)
	}
	if pwd == colonyRootReal || pwd == colonyPath {
		t.Fatalf("terminal_exec default cwd is the colony root %q", pwd)
	}
}
