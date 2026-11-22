package server

// The conversation cursor has two writers: the chat runtime's park checkpoint
// (pending_input, written when a turn parks or an answer resolves the park) and
// the conversation's message persist (next_seq, written for every appended
// message). They must not be two-step read-modify-writes: a persist that reads
// between the answer's clear and its turn_done checkpoint writes the cleared
// snapshot back and the session restarts believing no answer ever arrived.
//
// The original flake needed a loaded CI machine to widen that window. This test
// forces the window deterministically: the store hands the persist's read
// across the park checkpoint before letting its write continue.

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// interruptedCursorStore widens the cursor read-to-write window of a message
// persist so a park checkpoint can land inside it.
type interruptedCursorStore struct {
	*agentloop.FileConversationStore
	readInFlight chan struct{}
	continueRead chan struct{}
	interrupt    sync.Once
}

func (s *interruptedCursorStore) ReadCursor() (map[string]any, error) {
	cursor, err := s.FileConversationStore.ReadCursor()
	if !cursorReadIsSeqBump() {
		return cursor, err
	}
	// The snapshot is already taken. Hold the write until the test has run the
	// park checkpoint, exactly what a loaded machine does by scheduling luck.
	s.interrupt.Do(func() {
		close(s.readInFlight)
		<-s.continueRead
	})
	return cursor, err
}

// cursorReadIsSeqBump reports whether the current read belongs to the message
// persist's next_seq bump (the non-atomic path this test exists to reject).
func cursorReadIsSeqBump() bool {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "writeNextSeq") {
			return true
		}
		if !more {
			return false
		}
	}
}

// Parity: core/framework/agent_loop/internals/cursor_persistence.py::write_cursor
func TestParkCheckpointSurvivesConcurrentSeqPersist(t *testing.T) {
	store := &interruptedCursorStore{
		FileConversationStore: agentloop.NewFileConversationStore(filepath.Join(t.TempDir(), "conversations")),
		readInFlight:          make(chan struct{}),
		continueRead:          make(chan struct{}),
	}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Store:     store,
		SessionID: "s-park-race",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })

	// The relayed answer clears the old park first, then checkpoints turn_done.
	// A transcript persist runs concurrently, as it does while the overseer's
	// answer turn streams in.
	chatRuntime.clearPendingQuestions()

	persisted := make(chan struct{})
	go func() {
		defer close(persisted)
		chatRuntime.recordExternalAssistant("Answer received.")
	}()

	select {
	case <-store.readInFlight:
		// The persist took its snapshot before the checkpoint. Its write lands
		// after it now; the checkpoint must survive.
		chatRuntime.setParkState(agentloop.ParkReasonTurnDone, true)
		close(store.continueRead)
		<-persisted
	case <-persisted:
		// The cursor update is atomic, so there is no read/write window to
		// pause in; order the checkpoint after the persist.
		chatRuntime.setParkState(agentloop.ParkReasonTurnDone, true)
	case <-time.After(5 * time.Second):
		t.Fatal("message persist neither completed nor reached its cursor read")
	}

	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	pending, _ := cursor[parkPendingInputKey].(map[string]any)
	if pending["reason"] != string(agentloop.ParkReasonTurnDone) {
		t.Fatalf("cursor pending_input after concurrent persist = %#v, want reason turn_done", cursor[parkPendingInputKey])
	}
}
