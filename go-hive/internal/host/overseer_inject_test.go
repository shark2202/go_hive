package host

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
)

type injectRecordLLM struct {
	mu      sync.Mutex
	replies []string
	calls   []llm.CompletionRequest
}

func (m *injectRecordLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	reply := "idle"
	if index := len(m.calls) - 1; index < len(m.replies) {
		reply = m.replies[index]
	}
	return llm.Completion{Content: reply, StopReason: "stop"}, nil
}

func (m *injectRecordLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *injectRecordLLM) sawUserText(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.calls {
		for _, message := range req.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func TestHostOverseerConsumesInjectedMessage(t *testing.T) {
	provider := &injectRecordLLM{replies: []string{"idle", "checking the fences"}}
	built, err := New(Config{
		ColonyID:    "colony_overseer_inject",
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "worker", AgentType: "event_loop"},
		StoragePath: t.TempDir(),
		LLM:         provider,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := built.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(built.Stop)

	prompt := "You are the queen of this test colony."
	queenSpec := agentloop.AgentSpec{ID: "queen", Name: "Queen", AgentType: "event_loop", SystemPrompt: &prompt}
	overseer, err := built.StartOverseer(context.Background(), queenSpec, nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitUntil(time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}
	if !overseer.Inject("check the fences") {
		t.Fatal("Inject returned false on a live overseer")
	}
	if !waitUntil(2*time.Second, func() bool { return provider.sawUserText("check the fences") }) {
		t.Fatal("host-composed overseer never consumed the injected message")
	}
}

func waitUntil(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}
