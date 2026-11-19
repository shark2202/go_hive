package server

// Performance baselines for the server hot paths named in the W22 lane:
// cold session listing, event-bus publication, and one chat turn against a
// fake model. These are Go-only measurements; they have no Python counterpart.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/session"
)

func benchmarkSessionTree(b *testing.B, count int) (string, func()) {
	b.Helper()
	root := b.TempDir()
	queensDir := filepath.Join(root, "queens")
	queenDir := filepath.Join(queensDir, "queen_bench", "sessions")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		b.Fatalf("mkdir sessions: %v", err)
	}
	summary := `{"message_count":3,"last_message":"bench message","last_active_at":1760000000}`
	meta := `{"agent_name":"queen_bench","created_at":1760000000}`
	for i := 0; i < count; i++ {
		sessionDir := filepath.Join(queenDir, fmt.Sprintf("session_20261119_%06d_%08x", i, i))
		if err := os.MkdirAll(filepath.Join(sessionDir, "conversations"), 0o755); err != nil {
			b.Fatalf("mkdir session: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(meta), 0o600); err != nil {
			b.Fatalf("write meta: %v", err)
		}
		// summarizeSessionDir reads <session>/summary.json (a sibling of
		// conversations/); seed it so the benchmark measures the steady-state
		// list, not a one-off summary rebuild.
		if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(summary), 0o600); err != nil {
			b.Fatalf("write session summary: %v", err)
		}
	}
	return queensDir, func() {}
}

// BenchmarkColdSessionList100/1000 measures the disk-backed session list the
// dashboard's /api/sessions/history route renders.
func BenchmarkColdSessionList100(b *testing.B)  { benchmarkColdSessionList(b, 100) }
func BenchmarkColdSessionList1000(b *testing.B) { benchmarkColdSessionList(b, 1000) }

func benchmarkColdSessionList(b *testing.B, count int) {
	queensDir, cleanup := benchmarkSessionTree(b, count)
	defer cleanup()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries := session.ListColdSessions(queensDir)
		if len(entries) != count {
			b.Fatalf("listed %d sessions, want %d", len(entries), count)
		}
	}
}

// BenchmarkEventBusPublish measures one publish with a subscriber attached:
// history retention, subscription matching, and the synchronous handler join.
func BenchmarkEventBusPublish(b *testing.B) {
	bus := eventbus.NewEventBus(eventbus.WithMaxHistory(1000))
	received := 0
	bus.Subscribe([]eventbus.EventType{eventbus.EventLLMTextDelta}, func(event *eventbus.AgentEvent) {
		received++
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event := eventbus.NewAgentEvent(eventbus.EventLLMTextDelta, "queen")
		event.Data = map[string]any{
			"content":      "hello",
			"snapshot":     "hello world",
			"inner_turn":   i,
			"node_id":      "queen",
			"execution_id": "exec-bench",
		}
		bus.Publish(event)
	}
	b.StopTimer()
	if received != b.N {
		b.Fatalf("subscriber saw %d events, want %d", received, b.N)
	}
}

// BenchmarkChatRuntimeTurn measures one fake-model turn including the
// conversation write and transcript persistence in a temp root. Completion is
// signalled by the runtime's execution_completed bus event, so the measurement
// does not include transcript polling.
func BenchmarkChatRuntimeTurn(b *testing.B) {
	root := b.TempDir()
	model := &fakeChatLLM{}
	bus := eventbus.NewEventBus()
	done := make(chan struct{}, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventExecutionCompleted}, func(*eventbus.AgentEvent) {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: model, Root: root, Bus: bus, SessionID: "bench-session"})
	if err != nil {
		b.Fatalf("NewChatRuntime: %v", err)
	}
	defer runtime.Close()

	waitForTurn := func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			b.Fatal("timed out waiting for execution_completed")
		}
	}

	if err := runtime.Deliver("warm up"); err != nil {
		b.Fatalf("Deliver warm up: %v", err)
	}
	waitForTurn()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := runtime.Deliver("bench turn"); err != nil {
			b.Fatalf("Deliver: %v", err)
		}
		waitForTurn()
	}
	b.StopTimer()
	if err := runtime.LastError(); err != nil {
		b.Fatalf("chat runtime turn failed: %v", err)
	}
}
