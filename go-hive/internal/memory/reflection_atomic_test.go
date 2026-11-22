package memory

// The reflection memory write path is hardened beyond the upstream
// Path.write_text: a reader polling the memory directory (the turn loop, the
// recall builder, a test watchdog) must never observe an empty or half-written
// file. The old in-place os.WriteFile left a truncate-to-empty window; these
// tests pin the same-directory temp + fsync + rename contract and the failure
// cleanup that comes with it.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Parity-exempt: no upstream test drives a concurrent reader against a
// reflection memory write (core/tests/test_queen_memory.py covers write
// behaviour, never a torn read; upstream writes with Path.write_text too), so
// this is the Go write-path regression for the empty-file race that made
// TestChatRuntimeTurnDoneReflectsIntoQueenMemory flake. The expected texts
// carry the memory_log frontmatter stamp the write path adds.
func TestWriteMemoryFileNeverExposesPartialContent(t *testing.T) {
	dir := t.TempDir()
	dirs := []ScopeDir{{Scope: "global", Dir: dir}}
	target := filepath.Join(dir, "atomic.md")

	base := "---\nname: atomic\ntype: preference\ndescription: atomic write regression\n---\n"
	bodyA := base + strings.Repeat("A", 4000-len(base))
	bodyB := base + strings.Repeat("B", 4000-len(base))
	if got := ExecuteTool("write_memory_file", map[string]any{"filename": "atomic.md", "content": bodyA}, dirs, ""); strings.HasPrefix(got, "ERROR") {
		t.Fatalf("seed write: %s", got)
	}
	stampedA, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := ExecuteTool("write_memory_file", map[string]any{"filename": "atomic.md", "content": bodyB}, dirs, ""); strings.HasPrefix(got, "ERROR") {
		t.Fatalf("seed write B: %s", got)
	}
	stampedB, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(stampedA) == string(stampedB) {
		t.Fatal("fixture bodies are indistinguishable after stamping")
	}
	if got := ExecuteTool("write_memory_file", map[string]any{"filename": "atomic.md", "content": bodyA}, dirs, ""); strings.HasPrefix(got, "ERROR") {
		t.Fatalf("reset write: %s", got)
	}

	var stop atomic.Bool
	var invalid, reads atomic.Int64
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				data, err := os.ReadFile(target)
				if err != nil {
					continue
				}
				reads.Add(1)
				if text := string(data); text != string(stampedA) && text != string(stampedB) {
					invalid.Add(1)
				}
			}
		}()
	}

	// 80 rewrites against spinning readers: with the old in-place write the
	// truncate window exposed empty/partial content to ~20% of reads (measured
	// on this tree); an atomic rename can never publish a partial document.
	const writes = 80
	for i := 0; i < writes; i++ {
		content := bodyA
		if i%2 == 1 {
			content = bodyB
		}
		if got := ExecuteTool("write_memory_file", map[string]any{"filename": "atomic.md", "content": content}, dirs, ""); strings.HasPrefix(got, "ERROR") {
			t.Fatalf("write %d: %s", i, got)
		}
	}
	stop.Store(true)
	wg.Wait()

	if bad := invalid.Load(); bad != 0 {
		t.Fatalf("readers observed %d/%d partial or empty memory files", bad, reads.Load())
	}
}

// Parity-exempt: no upstream test covers a failed reflection write; the
// interruption contract (error surfaced, temp file cleaned up, existing
// memory untouched) is Go-only.
func TestWriteMemoryFileInterruptionKeepsMemoryIntact(t *testing.T) {
	dir := t.TempDir()
	dirs := []ScopeDir{{Scope: "global", Dir: dir}}
	good := "---\nname: stable\ntype: preference\ndescription: stable memory\n---\nkept"
	if got := ExecuteTool("write_memory_file", map[string]any{"filename": "stable.md", "content": good}, dirs, ""); strings.HasPrefix(got, "ERROR") {
		t.Fatalf("seed write: %s", got)
	}
	stampedGood, err := os.ReadFile(filepath.Join(dir, "stable.md"))
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}

	// A directory sitting at the target path forces the final rename to fail:
	// the write must surface the error and remove its temp file.
	if err := os.Mkdir(filepath.Join(dir, "blocked.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := ExecuteTool("write_memory_file", map[string]any{
		"filename": "blocked.md",
		"content":  "---\nname: blocked\ntype: preference\ndescription: never lands\n---\nbody",
	}, dirs, "")
	if !strings.HasPrefix(blocked, "ERROR") {
		t.Fatalf("write whose rename must fail returned %q", blocked)
	}

	// An interrupted writer can also leave its dot-prefixed temp file behind;
	// that litter must stay invisible to the memory surface.
	stale := filepath.Join(dir, ".stable.md.crashed.tmp")
	if err := os.WriteFile(stale, []byte("half-written"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") && name != filepath.Base(stale) {
			t.Fatalf("failed write leaked temp file %s", name)
		}
		if strings.HasSuffix(name, ".md") && name != "stable.md" && name != "blocked.md" {
			t.Fatalf("interrupted write left unexpected .md file %s", name)
		}
	}

	files := ScanMemoryFiles(dir)
	if len(files) != 1 || files[0].Filename != "stable.md" {
		t.Fatalf("scan after interrupted write = %+v", files)
	}
	if body := ExecuteTool("read_memory_file", map[string]any{"filename": "stable.md", "scope": "global"}, dirs, ""); body != string(stampedGood) {
		t.Fatalf("stable memory changed: %q", body)
	}
}
