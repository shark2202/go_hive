package colony

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

type recordedInjection struct {
	content       string
	isClientInput bool
}

type recordingQueen struct {
	mu        sync.Mutex
	injected  []recordedInjection
	injectErr error
}

func (q *recordingQueen) InjectEvent(_ context.Context, content string, isClientInput bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.injected = append(q.injected, recordedInjection{content: content, isClientInput: isClientInput})
	return q.injectErr
}

func (q *recordingQueen) injections() []recordedInjection {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]recordedInjection, len(q.injected))
	copy(out, q.injected)
	return out
}

type seedTestSession struct {
	id   string
	bus  *eventbus.EventBus
	mu   sync.Mutex
	node QueenNode
}

func (s *seedTestSession) SessionID() string            { return s.id }
func (s *seedTestSession) EventBus() *eventbus.EventBus { return s.bus }
func (s *seedTestSession) LiveQueenNode() QueenNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}
func (s *seedTestSession) setQueen(node QueenNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.node = node
}

func newSeedTestSession(bus *eventbus.EventBus, queen QueenNode) *seedTestSession {
	return &seedTestSession{id: "session_colony", bus: bus, node: queen}
}

// Parity: core/tests/test_colony_seed.py::test_dialog_goal_reaches_the_colony_queen_as_the_users_message
func TestSeedColonyQueenDialogGoalReachesQueen(t *testing.T) {
	bus := eventbus.NewEventBus()
	var received []*eventbus.AgentEvent
	bus.Subscribe([]eventbus.EventType{eventbus.EventClientInputReceived}, func(event *eventbus.AgentEvent) {
		received = append(received, event)
	})

	queen := &recordingQueen{}
	session := newSeedTestSession(bus, queen)

	goal := "Goal: refresh all six repos"
	if ok := SeedColonyQueen(context.Background(), session, "watchlist", &goal, SeedOptions{}); !ok {
		t.Fatal("SeedColonyQueen returned false")
	}

	injections := queen.injections()
	if len(injections) != 1 {
		t.Fatalf("injected %d times, want 1: %#v", len(injections), injections)
	}
	if injections[0].content != goal || !injections[0].isClientInput {
		t.Errorf("injected = %#v, want (%q, true)", injections[0], goal)
	}
	if len(received) != 1 {
		t.Fatalf("subscriber received %d events, want 1", len(received))
	}
	if got := received[0].Data["content"]; got != goal {
		t.Errorf("event content = %v, want %q", got, goal)
	}
	if received[0].StreamID != "queen" {
		t.Errorf("event stream = %q, want queen", received[0].StreamID)
	}
	if received[0].ExecutionID == nil || *received[0].ExecutionID != "session_colony" {
		t.Errorf("event execution = %v, want session_colony", received[0].ExecutionID)
	}
}

// Parity: core/tests/test_colony_seed.py::test_without_a_goal_the_queen_is_told_to_carry_on
func TestSeedColonyQueenWithoutGoalCarriesOn(t *testing.T) {
	bus := eventbus.NewEventBus()
	var received []*eventbus.AgentEvent
	bus.Subscribe([]eventbus.EventType{eventbus.EventClientInputReceived}, func(event *eventbus.AgentEvent) {
		received = append(received, event)
	})

	queen := &recordingQueen{}
	session := newSeedTestSession(bus, queen)

	blank := "   "
	if ok := SeedColonyQueen(context.Background(), session, "watchlist", &blank, SeedOptions{}); !ok {
		t.Fatal("SeedColonyQueen returned false")
	}

	injections := queen.injections()
	if len(injections) != 1 {
		t.Fatalf("injected %d times, want 1", len(injections))
	}
	if !strings.Contains(injections[0].content, "Carry on with the work agreed") {
		t.Errorf("content = %q, want carry-on note", injections[0].content)
	}
	if injections[0].isClientInput {
		t.Error("framework note must not be marked as client input")
	}
	if len(received) != 0 {
		t.Errorf("blank goal published %d events, want 0", len(received))
	}
}

// Parity: core/tests/test_colony_seed.py::test_waits_for_the_queen_loop_to_come_up
func TestSeedColonyQueenWaitsForLoop(t *testing.T) {
	queen := &recordingQueen{}
	session := newSeedTestSession(eventbus.NewEventBus(), nil)

	booted := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		session.setQueen(queen)
		close(booted)
	}()

	if ok := SeedColonyQueen(context.Background(), session, "watchlist", nil, SeedOptions{
		Wait: 2 * time.Second,
		Poll: 5 * time.Millisecond,
	}); !ok {
		t.Fatal("SeedColonyQueen returned false")
	}
	<-booted

	if got := len(queen.injections()); got != 1 {
		t.Errorf("injected %d times, want 1", got)
	}
}

// Parity: core/tests/test_colony_seed.py::test_gives_up_with_a_warning_when_the_queen_never_starts
func TestSeedColonyQueenWarnsWhenQueenNeverStarts(t *testing.T) {
	var warnings []string
	session := newSeedTestSession(eventbus.NewEventBus(), nil)

	ok := SeedColonyQueen(context.Background(), session, "watchlist", nil, SeedOptions{
		Wait: 10 * time.Millisecond,
		Poll: 2 * time.Millisecond,
		Warnf: func(format string, args ...any) {
			warnings = append(warnings, fmt.Sprintf(format, args...))
		},
	})
	if ok {
		t.Fatal("SeedColonyQueen returned true without a queen")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want 1", warnings)
	}
	if !strings.Contains(warnings[0], "colony 'watchlist' queen never came up; not seeded") {
		t.Errorf("warning = %q, want never-came-up text", warnings[0])
	}
}
