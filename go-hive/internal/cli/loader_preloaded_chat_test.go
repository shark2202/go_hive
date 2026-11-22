package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// preloadedChatLLM scripts a preloaded colony session: the queen's first turn
// dispatches one worker through run_worker, workers answer plainly, and any
// auxiliary call (recall, compaction) gets a neutral reply.
type preloadedChatLLM struct {
	mu          sync.Mutex
	queenTurns  int
	workerCalls int
	dispatched  int
	all         []llm.CompletionRequest
}

func hasToolName(defs []llm.ToolDef, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func (m *preloadedChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.all = append(m.all, req)
	if hasToolName(req.Tools, "report_to_parent") {
		m.workerCalls++
		return llm.Completion{Content: "worker finished its unit", StopReason: "stop"}, nil
	}
	if !hasToolName(req.Tools, "run_worker") {
		// Auxiliary call (recall, reflection, judge): never a chat turn.
		return llm.Completion{Content: "", StopReason: "stop"}, nil
	}
	m.queenTurns++
	if m.queenTurns == 1 {
		arguments, _ := json.Marshal(map[string]any{
			"tasks": []any{map[string]any{"task": "count the bees"}},
		})
		m.dispatched++
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "run_worker_1", Name: "run_worker", Arguments: string(arguments)}},
			StopReason: "tool_calls",
		}, nil
	}
	return llm.Completion{Content: "The worker is on it.", StopReason: "stop"}, nil
}

// Parity-exempt: `serve --colony` preloading is Go-only (upstream builds the
// session through SessionManager); pins that the preloaded session is a real
// chat Queen — Chat + queen executor set, and a queued user message can
// dispatch a worker through the bound colony.
func TestPreloadedColonySessionChatsAndDispatches(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := writeColonySpecFixture(t, home, "honeycomb",
		`{"name":"worker","description":"bee counter","system_prompt":"count bees carefully",`+
			`"tools":["read_file"],"goal":{"description":"count the bees"}}`)

	provider := &preloadedChatLLM{}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	var stdout bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stdout}
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)

	record := runner.loadPreloadedColonySession(colonyPath, manager, &liveMu, &live, home, "")
	if record == nil {
		t.Fatalf("preloaded session is nil; output: %s", stdout.String())
	}
	if record.Chat == nil {
		t.Fatalf("preloaded session has no chat runtime; output: %s", stdout.String())
	}
	if record.QueenExecutor == nil || record.QueenExecutor.QueenNode() == nil {
		t.Fatal("preloaded session has no queen executor for triggers")
	}
	if manager.GetSession(record.ID) != record {
		t.Fatal("preloaded session was not registered with the manager")
	}
	host, ok := record.Colony.(*colonyhost.Host)
	if !ok {
		t.Fatalf("session colony type = %T", record.Colony)
	}
	t.Cleanup(func() {
		_ = record.Chat.Close()
		host.Stop()
	})

	if err := record.Chat.Deliver("please dispatch a worker"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	var spawned bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !spawned {
		spawned = host.Runtime().GetWorker("worker_1") != nil
		if !spawned {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !spawned {
		t.Fatalf("preloaded session never dispatched a worker; output: %s", stdout.String())
	}

	workerRunning := false
	for time.Now().Before(deadline) && !workerRunning {
		provider.mu.Lock()
		workerRunning = provider.workerCalls >= 1
		provider.mu.Unlock()
		if !workerRunning {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !workerRunning {
		t.Fatal("dispatched worker never ran")
	}

	provider.mu.Lock()
	queenTurns := provider.queenTurns
	provider.mu.Unlock()
	if queenTurns < 2 {
		t.Fatalf("queen turns = %d, want the tool result turn as well", queenTurns)
	}
}
