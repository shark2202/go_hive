package atomicfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Parity: core/tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write
// Parity: core/tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace
func TestConcurrentWriteFileNeverCorrupts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")
	const writers = 64
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload := map[string]int{"writer": n, "iteration": n * 7}
			b, _ := json.Marshal(payload)
			if err := WriteFile(path, b, 0o644); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final file is corrupt: %v (%q)", err, data)
	}
	if got["iteration"] != got["writer"]*7 {
		t.Fatalf("mismatched payload: %+v", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temporary file leaked: %s", e.Name())
		}
	}
}

// Parity: core/tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write
// Parity: core/tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace
func TestWriteFileReplacesAndPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Fatalf("got %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}
}

// Parity-exempt: Go-only JSON read helper; the missing-file sentinel has no
func TestReadJSONMissing(t *testing.T) {
	var v map[string]any
	err := ReadJSON(filepath.Join(t.TempDir(), "nope.json"), &v)
	if !os.IsNotExist(err) {
		t.Fatalf("expected not-exist, got %v", err)
	}
}

// Parity-exempt: Go-only JSONL append helper; Python has no equivalent writer
func TestAppendJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "details.jsonl")
	for i := 0; i < 3; i++ {
		if err := AppendJSONL(path, map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Fatalf("lines = %d", lines)
	}
}
