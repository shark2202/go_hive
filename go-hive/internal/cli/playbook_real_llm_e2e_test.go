package cli

// Real-LLM end-to-end check for the Python .play.py bridge. It is opt-in:
// HIVE_REAL_LLM_E2E=1 and a HIVE_HOME whose configuration.json resolves to a
// reachable OpenAI-compatible model. The test exercises the same path a queen
// takes: RunPlaybook -> Python bridge -> dispatch_one -> colony worker -> real
// model -> report_to_parent -> bridge result.
//
// Parity-exempt: integration test against a live model; the Python suite mocks
// the worker completer.

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
	"github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// Parity-exempt: integration test against a live model; the Python suite mocks
// the worker completer, so there is no upstream test to reference.
func TestRealLLMPlaybookPythonWorkerE2E(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte(`{"name":"real_llm_e2e","queen_name":"queen_growth"}`), 0o644); err != nil {
		t.Fatalf("write colony metadata: %v", err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, "real_llm_e2e")
	if err != nil || binding == nil {
		t.Fatalf("tracker binding: %v (binding=%v)", err, binding)
	}

	systemPrompt := "You are a colony worker. Complete the task and report with the report_to_parent tool."
	spec := agentloop.NewAgentSpec("real_llm_e2e_worker", "Real LLM E2E Worker", "Worker used by the real-model playbook E2E.")
	spec.SystemPrompt = &systemPrompt
	spec.ToolAccessPolicy = "all"

	h, err := host.New(host.Config{
		ColonyID:             "real_llm_e2e",
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

	script := `meta = {"name": "real-llm-e2e", "concurrency": 1}

async def run(args):
    report = await worker(
        task="Call the report_to_parent tool exactly once with status='success', summary='REAL_LLM_E2E_OK' and data={'marker': 'REAL_LLM_E2E_OK'}. Do not do anything else.",
        timeout=300,
    )
    return {"report": report}
`
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	raw, isErr := colony.RunPlaybook(ctx, h.Runtime(), map[string]any{
		"playbook": script,
	})
	if isErr {
		t.Fatalf("RunPlaybook returned an error payload: %s", raw)
	}
	if calls := counting.callCount(); calls < 1 {
		t.Fatalf("worker never called the real LLM (calls=%d); payload=%s", calls, raw)
	}
	for i, reply := range counting.lastReplies() {
		t.Logf("real LLM reply %d: stop=%q content=%q tool_calls=%+v", i, reply.StopReason, reply.Content, reply.ToolCalls)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("playbook result is not JSON: %v\n%s", err, raw)
	}
	if got := out["status"]; got != "success" && got != "completed" {
		t.Fatalf("playbook status = %v, want success/completed; payload=%s", got, raw)
	}
	if dispatched, _ := out["dispatched"].(float64); dispatched < 1 {
		t.Fatalf("dispatched = %v, want >= 1; payload=%s", out["dispatched"], raw)
	}
	if !strings.Contains(raw, "REAL_LLM_E2E_OK") {
		t.Fatalf("worker report did not carry the model's summary; payload=%s", raw)
	}
	t.Logf("real LLM playbook payload: %s", raw)
}

// countingCompleter counts real model calls so the E2E can prove the worker
// actually went to the configured LLM instead of a short-circuit path.
type countingCompleter struct {
	inner llm.Completer

	mu      sync.Mutex
	calls   int
	replies []llm.Completion
}

func (c *countingCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	completion, err := c.inner.AComplete(ctx, req)
	c.mu.Lock()
	c.replies = append(c.replies, completion)
	c.mu.Unlock()
	return completion, err
}

func (c *countingCompleter) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingCompleter) lastReplies() []llm.Completion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Completion(nil), c.replies...)
}
