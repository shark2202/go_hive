package session

// Reliability probes for the goal-session store: corrupted state.json / parts
// / cursor files must be skipped or rebuilt instead of crashing a listing, and
// sequence allocation must not reuse a sequence that still has history.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Parity-exempt: fault-injection probe for a truncated state.json; listing
// skips the broken session while a direct read reports the damage.
func TestCorruptSessionStateIsSkippedByList(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	good, _, err := s.Create("goal-good", now)
	if err != nil {
		t.Fatalf("create good: %v", err)
	}
	bad, _, err := s.Create("goal-bad", now)
	if err != nil {
		t.Fatalf("create bad: %v", err)
	}
	badState := filepath.Join(s.base, bad.SessionID, "state.json")
	if err := os.WriteFile(badState, []byte(`{"session_id":`), 0o644); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}

	listed, err := s.List("", 0)
	if err != nil {
		t.Fatalf("List returned an error for one corrupt session: %v", err)
	}
	if len(listed) != 1 || listed[0].SessionID != good.SessionID {
		t.Fatalf("listed = %+v, want only the healthy session", listed)
	}
	if _, err := s.ReadState(bad.SessionID); err == nil {
		t.Fatal("ReadState on a corrupt state.json returned nil error; the damage is hidden")
	}
}

// Parity-exempt: sequence allocation must follow the highest part on disk even
// after gaps (compaction) or a corrupt part, so new history never overwrites.
func TestConversationNextSeqFollowsHighestPart(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("goal", time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	conv := s.Conversations(state.SessionID)
	for _, seq := range []int{0, 1, 2, 5} {
		if err := conv.AddPart(seq, map[string]any{"seq": seq, "role": "user", "content": fmt.Sprintf("m%d", seq)}); err != nil {
			t.Fatalf("add part %d: %v", seq, err)
		}
	}
	next, err := conv.NextSeq()
	if err != nil {
		t.Fatalf("NextSeq: %v", err)
	}
	if next != 6 {
		t.Fatalf("NextSeq = %d after parts 0,1,2,5; want 6", next)
	}

	partsDir := filepath.Join(conv.base, "parts")
	if err := os.WriteFile(filepath.Join(partsDir, "0000000001.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt part: %v", err)
	}
	next, err = conv.NextSeq()
	if err != nil {
		t.Fatalf("NextSeq with corrupt part: %v", err)
	}
	if next != 6 {
		t.Fatalf("NextSeq = %d with a corrupt part; want 6 (len-based counting would reuse seq 5)", next)
	}
}

// Parity-exempt: fault-injection probe for a truncated cursor; the cursor is
// optional derived state, so reads fall back to a zero cursor.
func TestConversationCorruptCursorIsTreatedAsAbsent(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("goal", time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	conv := s.Conversations(state.SessionID)
	if err := conv.WriteCursor(Cursor{Iteration: 3, Turn: 2}); err != nil {
		t.Fatalf("write cursor: %v", err)
	}
	cursorPath := filepath.Join(conv.base, "cursor.json")
	if err := os.WriteFile(cursorPath, []byte(`{"iteration":3,`), 0o644); err != nil {
		t.Fatalf("corrupt cursor: %v", err)
	}
	cursor, err := conv.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor on corrupt cursor returned an error instead of rebuilding: %v", err)
	}
	if cursor.Iteration != 0 || cursor.Turn != 0 {
		t.Fatalf("cursor = %+v, want zero-value fallback", cursor)
	}
}

// Parity-exempt: concurrent writers/readers over one parts directory must not
// crash or leave unreadable JSON.
func TestConcurrentAddPartOnSharedDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("goal", time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const writers = 8
	const perWriter = 10
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := s.Conversations(state.SessionID)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := reader.ReadParts(); err != nil {
				errs <- err
				return
			}
		}
	}()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			conv := s.Conversations(state.SessionID)
			for i := 0; i < perWriter; i++ {
				seq := writer*perWriter + i
				if err := conv.AddPart(seq, map[string]any{"seq": seq, "role": "user", "content": "x"}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-readerDone
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent add part: %v", err)
	}
	parts, err := s.Conversations(state.SessionID).ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != writers*perWriter {
		t.Fatalf("parts = %d, want %d", len(parts), writers*perWriter)
	}
}

// Parity-exempt: an unwritable parts directory must surface as an error from
// AddPart instead of looking like a successful write.
func TestAddPartReadOnlyDirectoryReportsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("goal", time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	conv := s.Conversations(state.SessionID)
	partsDir := filepath.Join(conv.base, "parts")
	if err := os.Chmod(partsDir, 0o555); err != nil {
		t.Fatalf("chmod parts: %v", err)
	}
	if err := os.Chmod(conv.base, 0o555); err != nil {
		t.Fatalf("chmod conversations: %v", err)
	}
	defer os.Chmod(conv.base, 0o755)
	defer os.Chmod(partsDir, 0o755)

	if err := conv.AddPart(0, map[string]any{"seq": 0, "role": "user"}); err == nil {
		t.Fatal("AddPart returned nil for a read-only parts directory")
	}
	if err := conv.WriteCursor(Cursor{Iteration: 1}); err == nil {
		t.Fatal("WriteCursor returned nil for a read-only directory")
	}
}
