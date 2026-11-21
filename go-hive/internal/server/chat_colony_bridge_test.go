package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
)

// Parity-exempt: Go-side SSE bridge. A bound colony's lifecycle events live on
// its own bus; the session UI follows the session bus, so the bridge is the
// only thing that makes worker started/completed/failed visible.
func TestChatRuntimeBridgesColonyLifecycleToSessionBus(t *testing.T) {
	colonyBus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         &fakeChatLLM{},
		EventBus:    colonyBus,
		StreamID:    "colony_bridge",
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := colonyRuntime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(colonyRuntime.Stop)

	sessionBus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       sessionBus,
		Root:      t.TempDir(),
		SessionID: "s-colony-bridge",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	var (
		mu            sync.Mutex
		types         = map[eventbus.EventType]bool{}
		bridgedColony string
	)
	subID := sessionBus.Subscribe([]eventbus.EventType{
		eventbus.EventExecutionStarted,
		eventbus.EventExecutionCompleted,
		eventbus.EventExecutionFailed,
		eventbus.EventSubagentReport,
	}, func(event *eventbus.AgentEvent) {
		if event == nil || event.StreamID == chatStreamID {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		types[event.Type] = true
		if event.ColonyID != nil {
			bridgedColony = *event.ColonyID
		}
	})
	t.Cleanup(func() { sessionBus.Unsubscribe(subID) })

	if _, err := colonyRuntime.SpawnBatch(context.Background(), []colony.BatchTask{{Task: "write one row"}}); err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}

	if !waitForServerCondition(5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return types[eventbus.EventExecutionStarted] && (types[eventbus.EventExecutionCompleted] || types[eventbus.EventSubagentReport])
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("session bus events = %v, want worker start + terminal", types)
	}
	mu.Lock()
	defer mu.Unlock()
	if bridgedColony != "colony_bridge" {
		t.Fatalf("bridged colony_id = %q, want colony_bridge", bridgedColony)
	}
}

func waitForServerCondition(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}

// Parity-exempt: Go-side SSE bridge. A colony worker's node_tool_doom_loop is
// published on the colony bus (the worker loop's sink); the session bus and its
// events.jsonl are what the dashboard and GET /events/history read, so the
// bridge must carry it over. It stays on the worker stream (upstream keeps the
// event worker-local): a watching SSE client sees it, and the session log
// records it.
func TestColonyBridgeMirrorsWorkerToolDoomLoop(t *testing.T) {
	colonyBus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         &fakeChatLLM{},
		EventBus:    colonyBus,
		StreamID:    "colony_doom_loop",
		StoragePath: t.TempDir(),
	})
	sessionBus := eventbus.NewEventBus()
	// The chat runtime writes the session event log under its Root; point it at
	// a temp dir and read the same file back.
	root := t.TempDir()
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       sessionBus,
		Root:      root,
		SessionID: "s-colony-doom-loop",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})
	recorder := recordChatEvents(sessionBus)

	executionID := "worker-exec-1"
	workerStream := "worker:w1"
	description := "`task_list` called 3 times in a row with identical arguments and the same result"
	colonyBus.EmitToolDoomLoop(workerStream, workerStream, description, &executionID)

	if !waitForServerCondition(3*time.Second, func() bool {
		return len(recorder.ofType(eventbus.EventNodeToolDoomLoop)) == 1
	}) {
		t.Fatalf("session bus events = %v, want the mirrored node_tool_doom_loop", recorder.types())
	}
	event := recorder.ofType(eventbus.EventNodeToolDoomLoop)[0]
	if event.StreamID != workerStream {
		t.Fatalf("mirrored stream = %q, want the worker stream %q", event.StreamID, workerStream)
	}
	if event.NodeID == nil || *event.NodeID != workerStream {
		t.Fatalf("mirrored node_id = %v, want %q", event.NodeID, workerStream)
	}
	if event.ExecutionID == nil || *event.ExecutionID != executionID {
		t.Fatalf("mirrored execution_id = %v, want %q", event.ExecutionID, executionID)
	}
	if event.ColonyID == nil || *event.ColonyID != "colony_doom_loop" {
		t.Fatalf("mirrored colony_id = %v, want colony_doom_loop", event.ColonyID)
	}
	if _, relayed := event.Data["relayed_from"]; relayed {
		t.Fatalf("doom-loop event must keep its worker identity, got relayed_from=%v", event.Data["relayed_from"])
	}
	if got, _ := event.Data["description"].(string); got != description {
		t.Fatalf("mirrored description = %q, want %q", got, description)
	}

	// The session event log records it for GET /events/history.
	raw, err := os.ReadFile(filepath.Join(root, "events.jsonl"))
	if err != nil {
		t.Fatalf("read session events log: %v", err)
	}
	if !strings.Contains(string(raw), `"node_tool_doom_loop"`) {
		t.Fatalf("session events.jsonl has no node_tool_doom_loop line:\n%s", raw)
	}
}

// Parity: framework/server/routes_events.py::_is_worker_noise
// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
//
// A fan-out worker's text, question, tool and token events are filtered out of
// the session SSE feed by stream. The bridge re-stamps them onto the session
// stream with relayed_from=<worker id> so the dashboard can follow a running
// worker, and it must neither duplicate the overseer's own events nor re-mirror
// a copy that already carries the relay stamp.
func TestColonyBridgeRelaysWorkerProgressWithOrigin(t *testing.T) {
	colonyBus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         &fakeChatLLM{},
		EventBus:    colonyBus,
		StreamID:    "colony_relay_progress",
		StoragePath: t.TempDir(),
	})
	sessionBus := eventbus.NewEventBus()
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       sessionBus,
		Root:      t.TempDir(),
		SessionID: "s-colony-relay-progress",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})
	recorder := recordChatEvents(sessionBus)

	executionID := "worker-exec-1"
	workerStream := "worker:w1"
	colonyBus.EmitClientOutputDelta(workerStream, workerStream, "reading the ledger", "reading the ledger", &executionID, nil, 0)
	colonyBus.EmitClientInputRequested(workerStream, workerStream, &executionID,
		[]map[string]any{{"id": "scope", "prompt": "Which ledger?"}}, nil)
	colonyBus.EmitToolCallStarted(workerStream, workerStream, "call-1", "linear_issue_get", map[string]any{"issue_id": "ENG-1"}, &executionID)
	colonyBus.EmitToolCallCompleted(workerStream, workerStream, "call-1", "linear_issue_get", `{"error":"missing"}`, true, &executionID)
	colonyBus.EmitLLMTurnComplete(workerStream, workerStream, "tool_calls", "fake-chat-model", 11, 7, 2, 0, 0, nil, &executionID, nil, nil, nil, nil, nil)

	// The overseer's own tool/token events are already represented by its
	// client-facing turn, so the bridge must not re-mirror them.
	colonyBus.EmitToolCallCompleted(overseerStreamID, overseerStreamID, "call-2", "linear_issue_get", "ok", false, &executionID)
	colonyBus.EmitLLMTurnComplete(overseerStreamID, overseerStreamID, "stop", "fake-chat-model", 3, 2, 0, 0, 0, nil, &executionID, nil, nil, nil, nil, nil)

	// An event already stamped as a relay is a copy of a mirror, never a new
	// source event: publishing it again must not duplicate the session event.
	alreadyRelayed := eventbus.NewAgentEvent(eventbus.EventClientOutputDelta, workerStream)
	alreadyRelayed.Data = map[string]any{
		"content":      "reading the ledger",
		"snapshot":     "reading the ledger",
		"relayed_from": "w1",
	}
	colonyBus.Publish(alreadyRelayed)

	if !waitForServerCondition(3*time.Second, func() bool {
		return len(recorder.ofType(eventbus.EventClientOutputDelta)) == 1 &&
			len(recorder.ofType(eventbus.EventClientInputRequested)) == 1 &&
			len(recorder.ofType(eventbus.EventToolCallStarted)) == 1 &&
			len(recorder.ofType(eventbus.EventToolCallCompleted)) == 1 &&
			len(recorder.ofType(eventbus.EventLLMTurnComplete)) == 1
	}) {
		t.Fatalf("session bus events = %v, want one relayed copy of each worker progress event",
			recorder.types())
	}

	for _, event := range recorder.snapshot() {
		if event.StreamID != chatStreamID {
			t.Fatalf("relayed event %s stream = %q, want %q", event.Type, event.StreamID, chatStreamID)
		}
		if relayed, _ := event.Data["relayed_from"].(string); relayed != "w1" {
			t.Fatalf("relayed event %s relayed_from = %q, want w1", event.Type, relayed)
		}
		if event.ColonyID == nil || *event.ColonyID != "colony_relay_progress" {
			t.Fatalf("relayed event %s colony_id = %v, want colony_relay_progress", event.Type, event.ColonyID)
		}
	}

	output := recorder.ofType(eventbus.EventClientOutputDelta)[0]
	if snapshot, _ := output.Data["snapshot"].(string); snapshot != "reading the ledger" {
		t.Fatalf("relayed output snapshot = %q", snapshot)
	}
	askUser := recorder.ofType(eventbus.EventClientInputRequested)[0]
	switch questions := askUser.Data["questions"].(type) {
	case []map[string]any:
		if len(questions) != 1 {
			t.Fatalf("relayed ask_user questions = %#v, want one", questions)
		}
	case []any:
		if len(questions) != 1 {
			t.Fatalf("relayed ask_user questions = %#v, want one", questions)
		}
	default:
		t.Fatalf("relayed ask_user questions = %#v, want one", askUser.Data["questions"])
	}
	if name, _ := recorder.ofType(eventbus.EventToolCallStarted)[0].Data["tool_name"].(string); name != "linear_issue_get" {
		t.Fatalf("relayed tool_name = %q, want linear_issue_get", name)
	}
	if tokens, _ := recorder.ofType(eventbus.EventLLMTurnComplete)[0].Data["input_tokens"].(int); tokens != 11 {
		t.Fatalf("relayed input_tokens = %v, want 11", recorder.ofType(eventbus.EventLLMTurnComplete)[0].Data["input_tokens"])
	}
}
