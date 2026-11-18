package agentloop

// Reliability probes for the conversation part store: a corrupted cursor must
// be rebuilt from the parts on disk, a stale cursor must never hand out a
// sequence number that overwrites existing history, and concurrent writers
// sharing one parts directory must not leave unreadable files.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func seedConversation(t *testing.T, store *FileConversationStore, parts int) {
	t.Helper()
	if err := store.WriteMeta(map[string]any{"system_prompt": "reliability"}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	for seq := 0; seq < parts; seq++ {
		message := Message{Seq: seq, Role: RoleUser, Content: fmt.Sprintf("m%d", seq)}
		if err := store.WritePart(seq, message.ToStorageDict()); err != nil {
			t.Fatalf("write part %d: %v", seq, err)
		}
	}
}

// Parity-exempt: fault-injection probe for a stale cursor; a cursor that lags
// the parts on disk must not cause history to be overwritten on restore.
func TestRestoreConversationStaleCursorDoesNotOverwriteParts(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	seedConversation(t, store, 4)
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		t.Fatalf("write cursor: %v", err)
	}

	conv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if conv.NextSeq() < 4 {
		t.Fatalf("next_seq = %d after restoring 4 parts; a stale cursor regressed the sequence", conv.NextSeq())
	}
	if _, err := conv.AddUserMessage("new", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 5 {
		t.Fatalf("parts = %d after append, want 5 (an overwrite lost one)", len(parts))
	}
	bySeq := map[int]string{}
	for _, part := range parts {
		seq, _ := part["seq"].(float64)
		content, _ := part["content"].(string)
		bySeq[int(seq)] = content
	}
	if bySeq[1] != "m1" {
		t.Fatalf("part 1 = %q, want original m1 (history was overwritten)", bySeq[1])
	}
	if bySeq[4] != "new" {
		t.Fatalf("new message stored at seq %v, want 4", bySeq[4])
	}
}

// Parity-exempt: fault-injection probe for a truncated cursor file; restore
// must fall back to the parts on disk instead of failing or restarting at 0.
func TestRestoreConversationCorruptCursorRebuildsFromParts(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	seedConversation(t, store, 3)
	if err := os.WriteFile(filepath.Join(store.base, "cursor.json"), []byte(`{"next_seq":`), 0o644); err != nil {
		t.Fatalf("corrupt cursor: %v", err)
	}

	conv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation with corrupt cursor: %v", err)
	}
	if conv == nil {
		t.Fatal("restored conversation is nil")
	}
	if conv.MessageCount() != 3 {
		t.Fatalf("message_count = %d, want 3", conv.MessageCount())
	}
	if conv.NextSeq() != 3 {
		t.Fatalf("next_seq = %d, want 3 rebuilt from parts", conv.NextSeq())
	}
}

// Parity-exempt: fault-injection probe for a truncated part file; the corrupt
// part is skipped and the surviving history still restores.
func TestRestoreConversationSkipsCorruptPart(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	seedConversation(t, store, 4)
	if err := os.WriteFile(filepath.Join(store.partsDir, partFileName(1)), []byte(`{"seq":1,"role":"us`), 0o644); err != nil {
		t.Fatalf("corrupt part: %v", err)
	}

	conv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation with corrupt part: %v", err)
	}
	if conv.MessageCount() != 3 {
		t.Fatalf("message_count = %d, want 3 (corrupt part skipped)", conv.MessageCount())
	}
	if conv.NextSeq() < 4 {
		t.Fatalf("next_seq = %d, want >= 4 so the remaining history cannot be overwritten", conv.NextSeq())
	}
}

// Parity-exempt: concurrent writers/readers over one shared parts directory
// (multiple sessions on the same conversation) must always leave valid JSON.
func TestConcurrentPartWritersShareOneDirectory(t *testing.T) {
	base := t.TempDir()
	const writers = 8
	const perWriter = 12

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	stopReader := make(chan struct{})
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)
		reader := NewFileConversationStore(base)
		for {
			select {
			case <-stopReader:
				return
			default:
			}
			parts, err := reader.ReadParts()
			if err != nil {
				errs <- err
				return
			}
			for _, part := range parts {
				if _, ok := part["seq"].(float64); !ok {
					errs <- fmt.Errorf("reader saw a part without an integer seq: %#v", part)
					return
				}
			}
		}
	}()

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			store := NewFileConversationStore(base)
			for i := 0; i < perWriter; i++ {
				seq := writer*perWriter + i
				message := Message{Seq: seq, Role: RoleUser, Content: fmt.Sprintf("w%d-%d", writer, i)}
				if err := store.WritePart(seq, message.ToStorageDict()); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stopReader)
	<-readerDone
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent part store: %v", err)
	}

	parts, err := NewFileConversationStore(base).ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != writers*perWriter {
		t.Fatalf("parts = %d, want %d", len(parts), writers*perWriter)
	}
	for _, part := range parts {
		if _, err := json.Marshal(part); err != nil {
			t.Fatalf("part is not serialisable: %v", err)
		}
	}
}

// Parity-exempt: a read-only parts directory must surface an error from
// WritePart instead of silently dropping the turn.
func TestWritePartReadOnlyDirectoryReportsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := NewFileConversationStore(t.TempDir())
	seedConversation(t, store, 1)
	if err := os.Chmod(store.partsDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(store.partsDir, 0o755)

	err := store.WritePart(1, Message{Seq: 1, Role: RoleUser, Content: "denied"}.ToStorageDict())
	if err == nil {
		t.Fatal("WritePart returned nil for a read-only parts directory")
	}
}
