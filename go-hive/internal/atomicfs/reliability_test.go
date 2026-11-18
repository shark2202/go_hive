package atomicfs

// Reliability probes for the atomic write primitive: unwritable targets must
// return an error (never a silent success) and leave no temp files behind.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: write-path fault injection for a read-only directory; the
// same temp-create/rename path carries ENOSPC errors on a full disk.
func TestWriteFileReadOnlyDirectoryReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o755)

	err := WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644)
	if err == nil {
		t.Fatal("WriteFile returned nil for a read-only directory")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("temporary file leaked after a failed write: %s", entry.Name())
		}
	}
}

// Parity-exempt: a target whose parent path is a regular file (ENOTDIR) must
// be reported with the failing path in the error.
func TestWriteFileNonDirectoryParentReturnsError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	err := WriteFile(filepath.Join(blocker, "state.json"), []byte("{}"), 0o644)
	if err == nil {
		t.Fatal("WriteFile returned nil when the parent path was a file")
	}
	if !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("error = %q, want the failing path included", err.Error())
	}
}

// Parity-exempt: ReadJSON must surface a decode failure; leniency is the
// caller's policy, not the primitive's.
func TestReadJSONCorruptReturnsDecodeError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor.json")
	if err := os.WriteFile(path, []byte(`{"next_seq":`), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	var target map[string]any
	if err := ReadJSON(path, &target); err == nil {
		t.Fatal("ReadJSON returned nil for a truncated JSON file")
	}
}
