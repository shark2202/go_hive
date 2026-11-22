package server

// Session-start recall seed (background mode): the first message of a fresh
// session warms the recall cache the way the upstream queen identity hook does
// at session_start — global scope only, up to 5 picks (the selector default;
// the refresh picks 3 per scope), awaited at most 3s, silent on failure. It
// runs after the first message's own delivery, so it never changes what that
// message carries; it is the fallback that keeps a slow or empty first refresh
// from leaving the next message recall-less. Reference behaviour is measured by
// go_hive/scripts/recall_seed_probe.py (S1-S9) and written to
// docs/e2e-assets/memory-notes-1015/recall-seed-probe.json.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

// seedAwareChatLLM answers aux picks the way the probe's picker does and can
// hold a pick whose manifest carries blockManifest open, so a test can keep the
// scoped refresh unfinished and observe the seed alone.
type seedAwareChatLLM struct {
	mu            sync.Mutex
	requests      []llm.CompletionRequest
	blockManifest string
	block         chan struct{}
	blockedOnce   sync.Once
	blocked       chan struct{}
}

func newSeedAwareChatLLM() *seedAwareChatLLM {
	return &seedAwareChatLLM{block: make(chan struct{}), blocked: make(chan struct{})}
}

func (s *seedAwareChatLLM) ModelName() string { return "seed-aware-chat" }

func (s *seedAwareChatLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	if req.System == memory.SelectMemoriesSystemPrompt {
		last := ""
		for _, message := range req.Messages {
			if message.Role == "user" {
				last = message.Content
			}
		}
		if s.blockManifest != "" && strings.Contains(last, s.blockManifest) {
			s.blockedOnce.Do(func() { close(s.blocked) })
			select {
			case <-s.block:
			case <-ctx.Done():
				return llm.Completion{}, ctx.Err()
			}
		}
		name := firstMemoryManifestFilename(last)
		if strings.Contains(last, "tea.md") {
			name = "tea.md"
		}
		payload := `{"selected_memories": []}`
		if name != "" {
			payload = `{"selected_memories": ["` + name + `"]}`
		}
		return llm.Completion{Content: payload, Model: "seed-aware-chat"}, nil
	}
	return llm.Completion{Content: "ok", Model: "seed-aware-chat"}, nil
}

func (s *seedAwareChatLLM) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// selectRequests returns the aux-pick requests in arrival order.
func (s *seedAwareChatLLM) selectRequests() []llm.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.CompletionRequest, 0, len(s.requests))
	for _, request := range s.requests {
		if request.System == memory.SelectMemoriesSystemPrompt {
			out = append(out, request)
		}
	}
	return out
}

func seedRecallRequestText(request llm.CompletionRequest) string {
	parts := make([]string, 0, len(request.Messages))
	for _, message := range request.Messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func readRecallCache(t *testing.T, runtime *ChatRuntime) (string, string) {
	t.Helper()
	runtime.recallMu.Lock()
	defer runtime.recallMu.Unlock()
	return runtime.recallGlobalCache, runtime.recallQueenCache
}

// lastRecallReminder returns the newest recall reminder in the transcript
// (recallReminder returns the first, which is an earlier message's).
func lastRecallReminder(parts []map[string]any) string {
	reminder := ""
	for _, part := range parts {
		if part["role"] != "user" {
			continue
		}
		content, _ := part["content"].(string)
		if strings.Contains(content, "<system-reminder>") && strings.Contains(content, "Recalled memories") {
			reminder = content
		}
	}
	return reminder
}

// Parity: core/tests/test_queen_memory.py::test_select_memories_with_files
// Parity: core/tests/test_queen_memory.py::test_profiles_never_crowd_out_relevant_memories
func TestChatRuntimeSeedPicksGlobalOnlyWithFiveCap(t *testing.T) {
	t.Setenv("HIVE_MEMORY_RECALL", "background")
	homeRoot := t.TempDir()
	globalDir := memory.GlobalMemoryDir(homeRoot)
	// Six profile pins plus one preference note: the seed's default cap keeps
	// five, the model's pick first.
	for i := 0; i < 6; i++ {
		writeChatMemoryFile(t, filepath.Join(globalDir, "char"+string(rune('0'+i))+".md"), "profile body")
	}
	writeChatMemoryNote(t, globalDir, "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	writeChatMemoryFile(t, filepath.Join(memory.QueenMemoryDir(homeRoot, "queen_ava"), "ava-secret.md"), "queen only body")

	model := newSeedAwareChatLLM()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if recallSeedTimeout != 3*time.Second {
		t.Fatalf("recallSeedTimeout = %s, want upstream's asyncio.wait_for(..., 3.0)", recallSeedTimeout)
	}
	runtime.seedRecallWithin("what tea do I like?", recallSeedTimeout)

	global, queen := readRecallCache(t, runtime)
	if !strings.HasPrefix(global, "--- Global Memories ---") || !strings.Contains(global, "### tea.md") {
		t.Fatalf("global seed cache = %q, want the rendered Global Memories block", global)
	}
	if strings.Count(global, "### ") != 5 {
		t.Fatalf("global seed cache carries %d notes, want the 5-pick cap:\n%s", strings.Count(global, "### "), global)
	}
	if queen != "" {
		t.Fatalf("the seed wrote the queen cache = %q, want global scope only", queen)
	}
	selects := model.selectRequests()
	if len(selects) != 1 {
		t.Fatalf("seed made %d pick calls, want 1", len(selects))
	}
	request := seedRecallRequestText(selects[0])
	if !strings.Contains(request, "what tea do I like?") || !strings.Contains(request, "tea.md") {
		t.Fatalf("seed pick did not see the session message and global manifest:\n%s", request)
	}
	if strings.Contains(request, "ava-secret.md") {
		t.Fatalf("the seed's pick saw a queen-scoped note:\n%s", request)
	}
}

// Parity-exempt: the 3s wait_for timeout has no upstream test; go_hive/scripts/recall_seed_probe.py S3 measures it (TimeoutError at ~3.0s, cache unchanged).
func TestChatRuntimeSeedTimesOutSilently(t *testing.T) {
	t.Setenv("HIVE_MEMORY_RECALL", "background")
	homeRoot := t.TempDir()
	writeChatMemoryNote(t, memory.GlobalMemoryDir(homeRoot), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &gatedChatLLM{release: make(chan struct{})}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	// A cached block left over from an earlier pick is what a cancelled seed
	// must keep: upstream's wait_for cancels the coroutine before the
	// assignment, so the TimeoutError branch writes nothing.
	runtime.recallMu.Lock()
	runtime.recallGlobalCache = "existing block"
	runtime.recallMu.Unlock()

	start := time.Now()
	runtime.seedRecallWithin("what tea do I like?", 40*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("seedRecallWithin returned after %s, want the injected cap", elapsed)
	}
	global, _ := readRecallCache(t, runtime)
	if global != "existing block" {
		t.Fatalf("timed-out seed cache = %q, want the pre-seed block unchanged", global)
	}
	if model.callCount() != 1 {
		t.Fatalf("seed made %d calls, want 1 (the cancelled pick)", model.callCount())
	}
}

// Parity: core/framework/server/queen_orchestrator.py::create_queen (the seed is gated on background mode)
func TestChatRuntimeSeedIsSilentOutsideBackgroundMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "index"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	runtime.seedRecallWithin("what tea do I like?", recallSeedTimeout)
	if global, _ := readRecallCache(t, runtime); global != "" {
		t.Fatalf("index mode seeded the cache = %q", global)
	}

	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "wait"}}`)
	runtime.seedRecallWithin("what tea do I like?", recallSeedTimeout)
	if global, _ := readRecallCache(t, runtime); global != "" {
		t.Fatalf("wait mode seeded the cache = %q", global)
	}
	if model.callCount() != 0 {
		t.Fatalf("index/wait modes made %d model calls, want 0", model.callCount())
	}
}

// Parity: core/framework/server/queen_orchestrator.py::create_queen (the seed is gated on the session's initial message and background mode)
func TestChatRuntimeSeedSurfacesWhenTheFirstRefreshStalls(t *testing.T) {
	t.Setenv("HIVE_MEMORY_RECALL", "background")
	homeRoot := t.TempDir()
	writeChatMemoryNote(t, memory.GlobalMemoryDir(homeRoot), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	writeChatMemoryFile(t, filepath.Join(memory.QueenMemoryDir(homeRoot, "queen_ava"), "ava-secret.md"), "queen only body")

	model := newSeedAwareChatLLM()
	// Hold the refresh's queen-scope pick open: build_scoped_recall_blocks
	// stores nothing until both scopes return, so only the seed can be in the
	// cache while this pick is in flight.
	model.blockManifest = "ava-secret.md"
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	// The first message is the create-session initial prompt: the seed fires
	// on it, but only after its own (empty) delivery.
	if err := runtime.Deliver("what tea do I like?"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 1)
	if reminder := recallReminder(parts); reminder != "" {
		t.Fatalf("the first message carried recall: %q", reminder)
	}

	// Wait for the refresh to be parked on its queen pick, then for the seed
	// to land: the only cache writer left is the seed.
	select {
	case <-model.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the scoped refresh never reached its queen pick")
	}
	if !waitForChatRecallCache(t, runtime, 2*time.Second) {
		t.Fatal("the seed never landed in the cache")
	}
	global, queen := readRecallCache(t, runtime)
	if !strings.Contains(global, "### tea.md") {
		t.Fatalf("seed cache = %q, want the global pick", global)
	}
	if queen != "" {
		t.Fatalf("queen cache = %q before the refresh landed, want empty", queen)
	}

	// The next message rides the seed: global block only.
	if err := runtime.Deliver("tell me again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts = waitForAssistantParts(t, runtime, 2)
	reminder := recallReminder(parts)
	if !strings.Contains(reminder, "--- Global Memories ---") || !strings.Contains(reminder, "### tea.md") {
		t.Fatalf("second message remind = %q, want the seed's global block", reminder)
	}
	if strings.Contains(reminder, "Queen Memories") {
		t.Fatalf("second message reminder = %q, want no queen block before the refresh landed", reminder)
	}

	// Release the refresh: it replaces both scope blocks, and the third
	// message rides the scoped pick instead of the seed.
	close(model.block)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, queen = readRecallCache(t, runtime)
		if queen != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queen == "" {
		t.Fatal("the released refresh never replaced the seed")
	}
	if err := runtime.Deliver("and now?"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts = waitForAssistantParts(t, runtime, 3)
	reminder = lastRecallReminder(parts)
	if !strings.Contains(reminder, "--- Global Memories ---") || !strings.Contains(reminder, "--- Queen Memories: queen_ava ---") {
		t.Fatalf("third message reminder = %q, want the scoped refresh's pair", reminder)
	}

	// The seed's own pick saw the global scope only, even though the session
	// has a queen-scoped note.
	selects := model.selectRequests()
	sawGlobalOnly := false
	for _, selectRequest := range selects {
		text := seedRecallRequestText(selectRequest)
		if strings.Contains(text, "ava-secret.md") {
			continue
		}
		sawGlobalOnly = sawGlobalOnly || strings.Contains(text, "tea.md")
	}
	if !sawGlobalOnly {
		t.Fatalf("no global-only pick request among %d select calls", len(selects))
	}
}

// Parity: core/framework/server/queen_orchestrator.py::create_queen (no initial message means no trigger, so no seed)
func TestChatRuntimeOpeningTurnDoesNotSeed(t *testing.T) {
	t.Setenv("HIVE_MEMORY_RECALL", "background")
	homeRoot := t.TempDir()
	writeChatMemoryNote(t, memory.GlobalMemoryDir(homeRoot), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := newSeedAwareChatLLM()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	// A fresh session with no initial prompt runs its opening iteration only:
	// upstream's trigger is empty there, so the seed is skipped.
	runtime.StartOpeningTurn()
	waitForAssistantParts(t, runtime, 1)
	if selects := model.selectRequests(); len(selects) != 0 {
		t.Fatalf("opening turn made %d recall picks, want 0", len(selects))
	}
	if global, queen := readRecallCache(t, runtime); global != "" || queen != "" {
		t.Fatalf("opening turn seeded the cache: global=%q queen=%q", global, queen)
	}
}
