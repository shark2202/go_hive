package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/soul"
)

// Parity-exempt: no upstream counterpart. Upstream's soul reflection reads
// chats on its own schedule; the Go port sediments a finished memory
// reflection, so this is the wiring that has to be proven here.
func TestChatRuntimeSedimentsReflectionIntoQueenSoul(t *testing.T) {
	homeRoot := t.TempDir()
	// every_turns=1: the first finished turn runs one short reflection, which
	// writes a queen memory file; long_every stays high so exactly one pass
	// sediment is in flight.
	config := `{
  "memory_reflection": {"every_turns": 1, "long_every": 4},
  "soul": {"queens": {"queen_technology": {"enabled": true, "sections": [
    {"key": "now", "title": "Now", "limit": 300}
  ]}}}
}`
	if err := os.WriteFile(filepath.Join(homeRoot, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	queenRoot := filepath.Join(homeRoot, "queens", "queen_technology")
	if err := os.MkdirAll(queenRoot, 0o755); err != nil {
		t.Fatalf("create queen dir: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &reflectionEchoLLM{},
		Root:      t.TempDir(),
		QueenName: "queen_technology",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("remember that I prefer implementation-first answers"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)

	// The reflection writes the memory file first; the soul follows the pass.
	waitForPath(t, filepath.Join(memory.QueenMemoryDir(homeRoot, "queen_technology"), "turn-learnings.md"), 5*time.Second)
	waitForPath(t, filepath.Join(queenRoot, "soul", "soul.json"), 5*time.Second)

	stored := soul.Load(filepath.Join(homeRoot, "queens"), "queen_technology")
	if stored.Version != 1 {
		t.Fatalf("soul version = %d, want 1", stored.Version)
	}
	items := stored.Items("now")
	if len(items) != 1 || items[0].Text != "reflected queen memory" {
		t.Fatalf("soul items = %#v", items)
	}
	if items[0].Date != time.Now().Format("2006-01-02") {
		t.Fatalf("item date = %q", items[0].Date)
	}
	state := soul.LoadSedimentState(filepath.Join(homeRoot, "queens"), "queen_technology")
	if state == nil || state.LastAdded != 1 || len(state.Written) != 1 {
		t.Fatalf("sediment state = %#v", state)
	}

	// The memory file is unchanged, so a second sediment of the same pass must
	// not bump the version or add a duplicate item.
	runtime.sedimentSoul(memory.LoopResult{Success: true, ChangedFiles: []string{"queen:turn-learnings.md"}})
	again := soul.Load(filepath.Join(homeRoot, "queens"), "queen_technology")
	if again.Version != 1 || len(again.Items("now")) != 1 {
		t.Fatalf("unchanged conclusion rewrote the soul: %#v", again)
	}
}

// Parity-exempt: no upstream counterpart; a queen without soul configuration
// must stay untouched, which the Go sediment decides per queen.
func TestChatRuntimeSkipsSoulSedimentWithoutSoulConfig(t *testing.T) {
	homeRoot := t.TempDir()
	config := `{"memory_reflection": {"every_turns": 1, "long_every": 4}}`
	if err := os.WriteFile(filepath.Join(homeRoot, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(homeRoot, "queens", "queen_other"), 0o755); err != nil {
		t.Fatalf("create queen dir: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &reflectionEchoLLM{},
		Root:      t.TempDir(),
		QueenName: "queen_other",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	// Prove the reflection ran before asserting the soul stayed untouched.
	memoryFile := waitForPath(t, filepath.Join(memory.QueenMemoryDir(homeRoot, "queen_other"), "turn-learnings.md"), 5*time.Second)
	if !strings.Contains(string(memoryFile), "short pass body") {
		t.Fatalf("queen memory = %q", string(memoryFile))
	}

	if _, err := os.Stat(filepath.Join(homeRoot, "queens", "queen_other", "soul")); !os.IsNotExist(err) {
		t.Fatalf("queen without soul configuration got a soul dir: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(homeRoot, "queens"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "queen_other" {
		t.Fatalf("queens dir = %v (err %v)", entries, err)
	}
}
