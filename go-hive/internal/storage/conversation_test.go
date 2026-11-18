package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_meta_and_cursor_crud
func TestFileConversationStoreMetaAndCursorCRUD(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if meta := store.ReadMeta(); meta != nil {
		t.Fatalf("initial meta = %v, want nil", *meta)
	}
	if err := store.WriteMeta(map[string]any{"system_prompt": "hi"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	meta := store.ReadMeta()
	if meta == nil || (*meta)["system_prompt"] != "hi" {
		t.Fatalf("meta = %v, want system_prompt=hi", meta)
	}

	if err := store.WriteCursor(map[string]any{"next_seq": 5}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	cursor := store.ReadCursor()
	if cursor == nil || jsonIntValue(*cursor, "next_seq") != 5 {
		t.Fatalf("cursor = %v, want next_seq=5", cursor)
	}
}

func jsonIntValue(m map[string]any, key string) int {
	if n, ok := jsonInt(m[key]); ok {
		return n
	}
	return -1
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_write_and_read_parts_in_order
func TestFileConversationStoreWriteAndReadPartsInOrder(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	contents := map[int]string{0: "first", 1: "middle", 2: "second"}
	for _, seq := range []int{2, 0, 1} {
		if err := store.WritePart(seq, map[string]any{"seq": seq, "content": contents[seq]}); err != nil {
			t.Fatalf("WritePart(%d): %v", seq, err)
		}
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("parts len = %d, want 3", len(parts))
	}
	for i, p := range parts {
		if got := jsonIntValue(p, "seq"); got != i {
			t.Fatalf("parts[%d].seq = %d, want %d", i, got, i)
		}
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_delete_parts_before
func TestFileConversationStoreDeletePartsBefore(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	for i := 0; i < 5; i++ {
		if err := store.WritePart(i, map[string]any{"seq": i}); err != nil {
			t.Fatalf("WritePart(%d): %v", i, err)
		}
	}
	if err := store.DeletePartsBefore(3, ""); err != nil {
		t.Fatalf("DeletePartsBefore: %v", err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 2 || jsonIntValue(parts[0], "seq") != 3 || jsonIntValue(parts[1], "seq") != 4 {
		t.Fatalf("parts = %v, want seqs [3 4]", parts)
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_idempotent_write_part
func TestFileConversationStoreIdempotentWritePart(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WritePart(0, map[string]any{"seq": 0, "v": 1}); err != nil {
		t.Fatalf("WritePart: %v", err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "v": 2}); err != nil {
		t.Fatalf("WritePart overwrite: %v", err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts len = %d, want 1", len(parts))
	}
	if jsonIntValue(parts[0], "v") != 2 {
		t.Fatalf("v = %v, want 2", parts[0]["v"])
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_corrupt_part_skipped_on_read
func TestFileConversationStoreCorruptPartSkippedOnRead(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "ok"}); err != nil {
		t.Fatalf("WritePart(0): %v", err)
	}
	if err := store.WritePart(1, map[string]any{"seq": 1, "content": "good"}); err != nil {
		t.Fatalf("WritePart(1): %v", err)
	}
	corrupt := filepath.Join(store.BasePath(), "parts", "0000000000.json")
	if err := os.WriteFile(corrupt, []byte("{truncated"), 0o644); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 1 || jsonIntValue(parts[0], "seq") != 1 {
		t.Fatalf("parts = %v, want only seq 1", parts)
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_directory_structure
func TestFileConversationStoreDirectoryStructure(t *testing.T) {
	base := filepath.Join(t.TempDir(), "conv")
	store := NewFileConversationStore(base)
	if err := store.WriteMeta(map[string]any{"system_prompt": "hi"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 2}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "first"}); err != nil {
		t.Fatalf("WritePart(0): %v", err)
	}
	if err := store.WritePart(1, map[string]any{"seq": 1, "content": "second"}); err != nil {
		t.Fatalf("WritePart(1): %v", err)
	}

	for _, rel := range []string{
		"meta.json",
		"cursor.json",
		filepath.Join("parts", "0000000000.json"),
		filepath.Join("parts", "0000000001.json"),
	} {
		if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_partials_separate_from_parts
func TestFileConversationStorePartialsSeparateFromParts(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "real"}); err != nil {
		t.Fatalf("WritePart: %v", err)
	}
	if err := store.WritePartial(1, map[string]any{"seq": 1, "content": "inflight", "truncated": true}); err != nil {
		t.Fatalf("WritePartial: %v", err)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 1 || jsonIntValue(parts[0], "seq") != 0 {
		t.Fatalf("parts = %v, want only seq 0", parts)
	}

	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatalf("ReadAllPartials: %v", err)
	}
	if len(partials) != 1 || jsonIntValue(partials[0], "seq") != 1 {
		t.Fatalf("partials = %v, want only seq 1", partials)
	}
	if partials[0]["content"] != "inflight" {
		t.Fatalf("partial content = %v, want inflight", partials[0]["content"])
	}

	partial := store.ReadPartial(1)
	if partial == nil || (*partial)["content"] != "inflight" {
		t.Fatalf("ReadPartial(1) = %v, want inflight", partial)
	}
	if missing := store.ReadPartial(99); missing != nil {
		t.Fatalf("ReadPartial(99) = %v, want nil", *missing)
	}

	if err := store.ClearPartial(1); err != nil {
		t.Fatalf("ClearPartial: %v", err)
	}
	partials, err = store.ReadAllPartials()
	if err != nil {
		t.Fatalf("ReadAllPartials: %v", err)
	}
	if len(partials) != 0 {
		t.Fatalf("partials after clear = %v, want empty", partials)
	}
}

// Parity: tests/test_node_conversation.py::TestFileConversationStore::test_partials_dir_does_not_break_parts_glob
func TestFileConversationStorePartialsDirDoesNotBreakPartsGlob(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	for i := 0; i < 3; i++ {
		if err := store.WritePart(i, map[string]any{"seq": i}); err != nil {
			t.Fatalf("WritePart(%d): %v", i, err)
		}
		if err := store.WritePartial(i+100, map[string]any{"seq": i + 100}); err != nil {
			t.Fatalf("WritePartial(%d): %v", i+100, err)
		}
	}
	if err := store.DeletePartsBefore(2, ""); err != nil {
		t.Fatalf("DeletePartsBefore: %v", err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 1 || jsonIntValue(parts[0], "seq") != 2 {
		t.Fatalf("parts = %v, want only seq 2", parts)
	}
	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatalf("ReadAllPartials: %v", err)
	}
	if len(partials) != 3 {
		t.Fatalf("partials len = %d, want 3", len(partials))
	}
	for i, p := range partials {
		if got := jsonIntValue(p, "seq"); got != i+100 {
			t.Fatalf("partials[%d].seq = %d, want %d", i, got, i+100)
		}
	}
}

// Parity: tests/test_node_conversation.py::TestConversationIntegration::test_destroy_removes_all_files
func TestFileConversationStoreDestroyRemovesAllFiles(t *testing.T) {
	base := filepath.Join(t.TempDir(), "doomed_conv")
	store := NewFileConversationStore(base)
	if err := store.WriteMeta(map[string]any{"system_prompt": "temp"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "ephemeral"}); err != nil {
		t.Fatalf("WritePart: %v", err)
	}

	if _, err := os.Stat(base); err != nil {
		t.Fatalf("base missing before destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "meta.json")); err != nil {
		t.Fatalf("meta.json missing before destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "parts")); err != nil {
		t.Fatalf("parts dir missing before destroy: %v", err)
	}

	if err := store.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("base still exists after destroy: %v", err)
	}
}
