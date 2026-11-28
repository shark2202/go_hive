package colony

// Every loop a colony builds runs on the one tool executor the host passed,
// so a timed-out worker call must probe that executor's shared MCP transport
// before the loop gives up on it. The executor is a closure and cannot carry
// methods, so the runtime forwards the probe/kill pair onto each loop it
// creates: worker loops (newWorkerAgentLoop) and the persistent overseer
// (runOverseer).
//
// Parity: core/tests/test_tool_timeout_probe_policy.py (loop-level probe
// policy)
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
// handler: probe_for_tool / kill_for_tool)

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// colonyLivenessSpy records the probe/kill pair the loops call.
type colonyLivenessSpy struct {
	mu      sync.Mutex
	verdict string
	probes  []string
	kills   []string
}

func (s *colonyLivenessSpy) ProbeForTool(toolName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes = append(s.probes, toolName)
	return s.verdict
}

func (s *colonyLivenessSpy) KillForTool(toolName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, toolName)
	return nil
}

// newLivenessColony builds the production runtime shape (testAgentSpec, a
// scripted provider, one executor) with the liveness handles a host passes.
func newLivenessColony(t *testing.T, provider llm.Completer, spy *colonyLivenessSpy) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		ToolProber: spy,
		ToolKiller: spy,
		EventBus:   eventbus.NewEventBus(),
		StreamID:   "test_colony_liveness",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
// executor.kill_for_tool wired onto the tool executor)
func TestColonyWorkerLoopCarriesToolLivenessHandles(t *testing.T) {
	spy := &colonyLivenessSpy{verdict: tools.TimeoutVerdictDead}
	colony := newLivenessColony(t, newScriptedLLM(textResponse("idle")), spy)
	worker := NewWorker("w1", "do the thing", "worker:w1")
	worker.Context = colony.buildContext(worker, colony.agentSpec)

	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), nil)
	defer stop()
	loop.Hub.Stop()

	if loop.ToolProber != tools.LivenessProber(spy) {
		t.Fatalf("worker loop ToolProber = %#v, want the runtime's prober", loop.ToolProber)
	}
	if loop.ToolKiller != tools.ToolKiller(spy) {
		t.Fatalf("worker loop ToolKiller = %#v, want the runtime's killer", loop.ToolKiller)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func TestOverseerLoopCarriesToolLivenessHandles(t *testing.T) {
	spy := &colonyLivenessSpy{verdict: tools.TimeoutVerdictAlive}
	provider := newScriptedLLM(textResponse("idle"), textResponse("idle"))
	colony := newLivenessColony(t, provider, spy)

	overseer, err := colony.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loop := overseer.AgentLoop(); loop != nil {
			if loop.ToolProber != tools.LivenessProber(spy) || loop.ToolKiller != tools.ToolKiller(spy) {
				t.Fatalf("overseer loop handles = (%#v, %#v), want the runtime's pair",
					loop.ToolProber, loop.ToolKiller)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("overseer never built its AgentLoop")
}
