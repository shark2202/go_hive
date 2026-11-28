package server

// A chat turn whose tool call times out must probe the shared MCP transport
// before the loop abandons the call: the runtime is the production wiring for
// AgentLoop.ToolProber/ToolKiller, and the loader's registry executor (a
// value, not a closure) is what reaches the loop. A dead verdict must
// force-disconnect the server; an alive one must leave a merely-slow shared
// transport alone.
//
// Parity: core/tests/test_tool_timeout_probe_policy.py (loop-level probe
// policy)
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
// handler: probe_for_tool / kill_for_tool)

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// chatLivenessSpy records the probe/kill pair the loop calls on a timeout and
// answers every probe with one configured verdict.
type chatLivenessSpy struct {
	mu      sync.Mutex
	verdict string
	probes  []string
	kills   []string
}

func (s *chatLivenessSpy) ProbeForTool(toolName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes = append(s.probes, toolName)
	return s.verdict
}

func (s *chatLivenessSpy) KillForTool(toolName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, toolName)
	return nil
}

func (s *chatLivenessSpy) snapshot() (probes, kills []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.probes...), append([]string(nil), s.kills...)
}

// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
// executor.kill_for_tool wired onto the tool executor)
func TestChatRuntimeCarriesToolLivenessHandles(t *testing.T) {
	spy := &chatLivenessSpy{verdict: tools.TimeoutVerdictAlive}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:        &queenChatLLM{},
		Root:       t.TempDir(),
		SessionID:  "session_20260101_000000_live001",
		ToolProber: spy,
		ToolKiller: spy,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.loop.ToolProber != tools.LivenessProber(spy) {
		t.Fatalf("loop ToolProber = %#v, want the injected prober", runtime.loop.ToolProber)
	}
	if runtime.loop.ToolKiller != tools.ToolKiller(spy) {
		t.Fatalf("loop ToolKiller = %#v, want the injected killer", runtime.loop.ToolKiller)
	}

	// A runtime built without the handles keeps upstream's pre-wiring
	// behaviour: probe reads "unknown" and nothing is disconnected.
	bare, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      t.TempDir(),
		SessionID: "session_20260101_000000_live002",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime (no handles): %v", err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	if bare.loop.ToolProber != nil || bare.loop.ToolKiller != nil {
		t.Fatalf("loop handles = (%#v, %#v), want nil", bare.loop.ToolProber, bare.loop.ToolKiller)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_dead_server_is_killed
func TestChatRuntimeProbesTimedOutToolBeforeReclaiming(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	release := make(chan struct{})
	defer close(release)
	spy := &chatLivenessSpy{verdict: tools.TimeoutVerdictDead}
	llmCompleter := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{
			ID:        "call_hung",
			Name:      "web_search",
			Arguments: `{"query":"hive"}`,
		}}},
		{Content: "the search backend is unresponsive"},
	}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: llmCompleter,
		Tools: []llm.ToolDef{{
			Name:        "web_search",
			Description: "evaluate in the page",
			Parameters:  map[string]any{"type": "object"},
		}},
		Execute: func(_ context.Context, _ string, _ map[string]any) (string, bool, error) {
			<-release
			return "late result", false, nil
		},
		Root:       root,
		Bus:        bus,
		SessionID:  "session_20260101_000000_hang001",
		ToolProber: spy,
		ToolKiller: spy,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	// Give the tool a short budget so the timeout path runs inside the test.
	runtime.loop.Config.ToolCallTimeoutSeconds = 0.05

	if err := runtime.Deliver("search the web"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probes, kills := spy.snapshot()
		if len(probes) > 0 && len(kills) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	probes, kills := spy.snapshot()
	if len(probes) != 1 || probes[0] != "web_search" {
		t.Fatalf("probes = %v, want one probe of web_search", probes)
	}
	if len(kills) != 1 || kills[0] != "web_search" {
		t.Fatalf("kills = %v, want the dead server force-disconnected", kills)
	}
	// The model-facing result is upstream's dead-server text, not a silent
	// abandonment: the next turn sees why the tool vanished. The timeout path
	// appends the result after the probe/kill pair, so wait for the transcript
	// write instead of sampling it immediately.
	var reported bool
	for _, message := range runtime.conv.Messages() {
		if message.Role == "tool" && strings.Contains(message.Content, "failed a liveness check") {
			reported = true
		}
	}
	if !reported && !waitForServerCondition(2*time.Second, func() bool {
		for _, message := range runtime.conv.Messages() {
			if message.Role == "tool" && strings.Contains(message.Content, "failed a liveness check") {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("timeout result did not carry the dead-server message: %#v", runtime.conv.Messages())
	}
}
