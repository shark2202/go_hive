package server

// browser_notify_test.go covers the bridge notify → agent session mapping:
// a queen session is matched by session id and the frame text lands in its
// transcript, a worker is matched by colony worker id, and an unmatched profile
// writes nothing.

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// recordingColony is the ColonyRuntime seam for the worker-match tests. The
// live worker id map is replaced by a set of known ids; every other lookup
// fails the way ColonyRuntime.InjectInput does.
type recordingColony struct {
	mu       sync.Mutex
	workers  map[string][]string
	failures int
}

func (c *recordingColony) StreamID() string { return "colony:test" }

func (c *recordingColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "sub-test", nil
}

func (c *recordingColony) InjectInput(workerID, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.workers[workerID]; !known {
		c.failures++
		return errors.New("colony: unknown worker " + workerID)
	}
	c.workers[workerID] = append(c.workers[workerID], content)
	return nil
}

func (c *recordingColony) injected(workerID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.workers[workerID]...)
}

// waitForBrowserNotifyTurn polls the queen runtime's transcript until want
// appears as a user message AND the turn that consumed it produced an
// assistant part. Waiting for both keeps the assertion honest: the injected
// text alone would also match a queue-only write.
func waitForBrowserNotifyTurn(t *testing.T, runtime *ChatRuntime, want string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var parts []map[string]any
	for time.Now().Before(deadline) {
		read, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		parts = read
		foundUser := false
		assistants := 0
		for _, part := range parts {
			if part["role"] == "assistant" {
				assistants++
				continue
			}
			if part["role"] == "user" {
				if content, _ := part["content"].(string); strings.Contains(content, want) {
					foundUser = true
				}
			}
		}
		if foundUser && assistants > 0 {
			return parts
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the injected notify turn in the transcript; parts = %#v", parts)
	return nil
}

// Parity: tests/test_event_loop_node.py::test_inject_event
//
// Upstream's inject_event content appears as a user message in the next
// iteration; the bridge notify path must land there too, because the side panel
// hands a tab to the agent's conversation rather than to a UI-only channel.
func TestDeliverBrowserNotifyInjectsIntoQueenTranscript(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	session := &Session{
		ID:            "session_bridge_queen",
		Chat:          runtime,
		QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": runtime}},
	}
	manager := NewSessionManager()
	manager.AddSession(session)

	text := "The user handed \"Example\" (tab #7) over to you."
	if !manager.DeliverBrowserNotify("session_bridge_queen", text, map[string]any{"tabId": 7, "action": "handed-over"}) {
		t.Fatal("DeliverBrowserNotify reported no live agent for the queen session")
	}
	// The helper already waited for the assistant part: reaching here means the
	// injected text produced a real turn, not just a queue entry.
	waitForBrowserNotifyTurn(t, runtime, text)
}

// Parity-exempt: upstream has no test that drives _on_notify's worker lookup
// (test_serve_bridge_owner.py covers keepalive identity only). This pins the
// Go port's second half of the same lookup: worker.id under each session's
// colony, with is_client_input=True semantics inherited from ColonyRuntime.
func TestDeliverBrowserNotifyInjectsIntoWorker(t *testing.T) {
	colony := &recordingColony{workers: map[string][]string{"worker_ab12": {}}}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_with_worker", Colony: colony})

	if !manager.DeliverBrowserNotify("worker_ab12", "The user detached tab #9 from you.", nil) {
		t.Fatal("DeliverBrowserNotify did not match the colony worker")
	}
	injected := colony.injected("worker_ab12")
	if len(injected) != 1 || !strings.Contains(injected[0], "tab #9") {
		t.Fatalf("worker injections = %#v", injected)
	}
}

// Parity-exempt: no upstream test covers the unmatched-profile path; upstream
// only logs "no live agent matched". The Go port must not fabricate a session
// or write an event anywhere.
func TestDeliverBrowserNotifyWithoutMatchWritesNothing(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	session := &Session{
		ID:            "session_other",
		Chat:          runtime,
		QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": runtime}},
	}
	colony := &recordingColony{workers: map[string][]string{}}
	session.Colony = colony
	manager := NewSessionManager()
	manager.AddSession(session)

	if manager.DeliverBrowserNotify("stranger_profile", "should be dropped", map[string]any{"tabId": 1}) {
		t.Fatal("DeliverBrowserNotify claimed a recipient for an unknown profile")
	}
	if colony.failures != 1 {
		t.Fatalf("worker probe failures = %d, want 1 (one probe per session)", colony.failures)
	}
	parts, err := runtime.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, part := range parts {
		if part["role"] == "user" {
			t.Fatalf("unmatched notify started a queen turn: %#v", part)
		}
		if content, _ := part["content"].(string); strings.Contains(content, "should be dropped") {
			t.Fatalf("unmatched notify leaked into the transcript: %#v", part)
		}
	}
}
