package cli

// The DM fork's seed: the source queen session is copied into the colony's
// canonical session dir, its meta re-stamped, and the inherited transcript
// compacted in the background with a wall-clock cap. These tests pin the copy,
// the meta rewrite, the spawn-lock strip, the compaction marker outcomes, the
// seed_conversation snapshot, and the resumed transcript.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// forkCompactionLLM answers every compaction call with a fixed summary.
type forkCompactionLLM struct {
	mu    sync.Mutex
	calls int
	reply string
}

func (f *forkCompactionLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return llm.Completion{Content: f.reply, Model: "scripted"}, nil
}

func (f *forkCompactionLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// forkBlockingLLM parks the compaction call until the ctx expires (or the test
// releases it), which is how the wall-clock cap gets exercised.
type forkBlockingLLM struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newForkBlockingLLM() *forkBlockingLLM {
	return &forkBlockingLLM{started: make(chan struct{}), release: make(chan struct{})}
}

func (f *forkBlockingLLM) AComplete(ctx context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	case <-f.release:
		return llm.Completion{Content: "late summary", Model: "scripted"}, nil
	}
}

// forkMessage is a storage-shaped conversation part.
func forkMessage(seq int, role, content string) map[string]any {
	return agentloop.Message{Seq: seq, Role: role, Content: content}.ToStorageDict()
}

// writeForkSourceSession materialises a DM queen session on disk: conversations,
// cursor, store meta, events, and the session meta.json the fork re-stamps.
func writeForkSourceSession(t *testing.T, home hivehome.Home, queenName, sessionID string, parts []map[string]any, meta map[string]any) string {
	t.Helper()
	dir := home.QueenSessionDir(queenName, sessionID)
	store := agentloop.NewFileConversationStore(filepath.Join(dir, "conversations"))
	for index, part := range parts {
		if err := store.WritePart(index, part); err != nil {
			t.Fatalf("write source part %d: %v", index, err)
		}
	}
	if err := store.WriteCursor(map[string]any{"next_seq": len(parts)}); err != nil {
		t.Fatalf("write source cursor: %v", err)
	}
	if err := store.WriteMeta(map[string]any{"system_prompt": "", "max_context_tokens": 180_000}); err != nil {
		t.Fatalf("write source store meta: %v", err)
	}
	_ = store.Close()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"),
		[]byte(`{"type":"client_input_received","stream_id":"queen","data":{}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write source events: %v", err)
	}
	if meta == nil {
		meta = map[string]any{"phase": "independent"}
	}
	rendered, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal source meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), rendered, 0o644); err != nil {
		t.Fatalf("write source meta: %v", err)
	}
	return dir
}

// writeForkWindow forces a small queen window so the transcript crosses the
// keep-verbatim threshold and compaction actually runs.
func writeForkWindow(t *testing.T, home hivehome.Home, maxContextTokens int) {
	t.Helper()
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	config := map[string]any{"llm": map[string]any{"max_context_tokens": maxContextTokens}}
	rendered, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal configuration: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), rendered, 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
}

// forkLiveSource registers the DM session the POST asks to fork.
func forkLiveSource(t *testing.T, fixture *lifecycleFixture, sessionID, queenName, queenDir string) *hiveserver.Session {
	t.Helper()
	source := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	source.QueenName = queenName
	source.QueenDir = queenDir
	// The DM fork requires a live queen executor/node (upstream's 503
	// precondition), so every fork fixture carries a registered queen.
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
	fixture.manager.AddSession(source)
	return source
}

func readForkMeta(t *testing.T, dir string) map[string]any {
	t.Helper()
	meta, ok := readJSONObject(filepath.Join(dir, "meta.json"))
	if !ok {
		t.Fatalf("read meta.json under %s: unavailable", dir)
	}
	return meta
}

func readForkParts(t *testing.T, dir string) []map[string]any {
	t.Helper()
	return readForkStoreParts(t, filepath.Join(dir, "conversations"))
}

// readForkStoreParts reads a conversation store rooted at storeDir (the colony
// seed_conversation is itself a store root, not a session dir).
func readForkStoreParts(t *testing.T, storeDir string) []map[string]any {
	t.Helper()
	store := agentloop.NewFileConversationStore(storeDir)
	defer store.Close()
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("read conversation parts under %s: %v", storeDir, err)
	}
	return parts
}

func readForkEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl under %s: %v", dir, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		event := map[string]any{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func waitForkBackground(t *testing.T, fixture *lifecycleFixture) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fixture.lifecycle.forkBackground.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("background fork finalizer did not settle within 10s")
	}
}

// Parity: core/framework/server/routes_execution.py::fork_session_into_colony (copytree dirs_exist_ok)
func TestCopyTreeMergeKeepsDestinationOnlyFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "conversations", "parts", "0000000000.json"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "conversations", "parts", "0000000000.json"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "destination-only.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyTreeMerge(src, dst); err != nil {
		t.Fatalf("copyTreeMerge: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "conversations", "parts", "0000000000.json")); string(got) != "source" {
		t.Fatalf("merged part = %q, want the source file to win", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "destination-only.txt")); string(got) != "kept" {
		t.Fatalf("destination-only file = %q, want it preserved", got)
	}
}

// Parity: core/framework/server/routes_execution.py::fork_session_into_colony
func TestCreateColonyFromSourceCopiesQueenSessionAndRewritesMeta(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	sourceMeta := map[string]any{
		"phase":             "independent",
		"colony_spawned":    true,
		"spawned_colony_id": "parent_colony",
		"spawned_colony_at": "2026-01-01T00:00:00+00:00",
		"custom_flag":       "kept",
	}
	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", "let's start a colony for invoicing"),
			forkMessage(1, "assistant", "agreed, the plan is ready"),
		}, sourceMeta)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("created session id missing from payload: %#v", payload)
	}
	waitForkBackground(t, fixture)

	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	created := fixture.manager.GetSession(sessionID)
	if created == nil {
		t.Fatalf("created session %q is not registered", sessionID)
	}
	if created.QueenDir != destDir {
		t.Fatalf("created session queen dir = %q, want %q", created.QueenDir, destDir)
	}
	if created.ColonyID != "forked" {
		t.Fatalf("created session colony id = %q", created.ColonyID)
	}

	// The source transcript came across verbatim (the transcript is small
	// enough that compaction keeps it).
	parts := readForkParts(t, destDir)
	if len(parts) != 2 {
		t.Fatalf("copied parts = %d, want 2: %#v", len(parts), parts)
	}
	if parts[0]["role"] != "user" || parts[0]["content"] != "let's start a colony for invoicing" {
		t.Fatalf("copied part 0 = %#v", parts[0])
	}
	if parts[1]["role"] != "assistant" || parts[1]["content"] != "agreed, the plan is ready" {
		t.Fatalf("copied part 1 = %#v", parts[1])
	}
	if _, err := os.Stat(filepath.Join(destDir, "events.jsonl")); err != nil {
		t.Fatalf("copied events.jsonl: %v", err)
	}

	// meta.json is re-stamped as the colony's own session and the parent's
	// spawn lock is stripped.
	meta := readForkMeta(t, destDir)
	if meta["agent_path"] != fixture.home.ColonyDir("forked") {
		t.Fatalf("dest meta agent_path = %#v", meta["agent_path"])
	}
	if meta["agent_name"] != "Forked" {
		t.Fatalf("dest meta agent_name = %#v", meta["agent_name"])
	}
	if meta["queen_id"] != "queen_alpha" {
		t.Fatalf("dest meta queen_id = %#v", meta["queen_id"])
	}
	if meta["forked_from"] != "src-dm" {
		t.Fatalf("dest meta forked_from = %#v", meta["forked_from"])
	}
	if meta["colony_fork"] != true {
		t.Fatalf("dest meta colony_fork = %#v", meta["colony_fork"])
	}
	if meta["custom_flag"] != "kept" {
		t.Fatalf("dest meta dropped an unrelated key: %#v", meta)
	}
	for _, key := range []string{"colony_spawned", "spawned_colony_id", "spawned_colony_at"} {
		if _, present := meta[key]; present {
			t.Fatalf("dest meta kept parent lock %q: %#v", key, meta)
		}
	}

	// The source stays locked exactly as the DM path locks it.
	if !source.ColonySpawned || source.SpawnedColonyID != "forked" {
		t.Fatalf("source spawn lock = %v/%q", source.ColonySpawned, source.SpawnedColonyID)
	}
	sourceMetaAfter := readForkMeta(t, sourceDir)
	if sourceMetaAfter["colony_spawned"] != true || sourceMetaAfter["spawned_colony_id"] != "forked" {
		t.Fatalf("source meta lock = %#v", sourceMetaAfter)
	}
	// The source transcript is only read: compaction rewrites the copy.
	if parts := readForkParts(t, sourceDir); len(parts) != 2 {
		t.Fatalf("source transcript changed: %#v", parts)
	}
}

// Parity: core/framework/server/routes_execution.py::fork_session_into_colony (source dir missing)
func TestCreateColonyFromSourceWithoutSourceDirSkipsCompaction(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	source := hiveserver.NewSession("src-dm", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
	fixture.manager.AddSession(source)
	var created []*eventbus.AgentEvent
	source.EventBus.Subscribe([]eventbus.EventType{eventbus.EventColonyCreated}, func(event *eventbus.AgentEvent) {
		created = append(created, event.Clone())
	})

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	waitForkBackground(t, fixture)

	if len(created) != 1 {
		t.Fatalf("colony_created events = %d, want 1", len(created))
	}
	if created[0].Data["compaction_status"] != "skipped" {
		t.Fatalf("compaction_status = %#v, want skipped without a source session dir", created[0].Data["compaction_status"])
	}
	if _, err := os.Stat(fixture.home.ColonyQueensDir("forked")); !os.IsNotExist(err) {
		t.Fatalf("no-copy fork materialised a colony queen tree (stat err = %v)", err)
	}
	sessionID, _ := payload["session_id"].(string)
	if session := fixture.manager.GetSession(sessionID); session == nil || session.ColonyID != "forked" {
		t.Fatalf("created session = %#v", session)
	}
}

// Parity: core/framework/server/routes_execution.py::_compact_inherited_conversation
func TestColonyForkCompactionSummarizesInheritedTranscript(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	completer := &forkCompactionLLM{reply: "the invoice colony plan"}
	source.LLM = completer

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)

	if completer.callCount() != 1 {
		t.Fatalf("compaction LLM calls = %d, want 1", completer.callCount())
	}
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	statusFile := hiveserver.GetStatus(destDir)
	if statusFile["status"] != "done" {
		t.Fatalf("compaction status = %#v, want done", statusFile)
	}

	parts := readForkParts(t, destDir)
	if len(parts) != 1 {
		t.Fatalf("compacted parts = %d, want 1: %#v", len(parts), parts)
	}
	if parts[0]["role"] != "user" || parts[0]["content"] != "the invoice colony plan" {
		t.Fatalf("summary part = %#v", parts[0])
	}
	if parts[0]["inherited_from"] != "src-dm" {
		t.Fatalf("summary inherited_from = %#v, want src-dm", parts[0]["inherited_from"])
	}

	// The cursor restarts after the summary so the queen's next turn appends
	// behind it instead of overwriting the inherited range.
	cursor, ok := readJSONObject(filepath.Join(destDir, "conversations", "cursor.json"))
	if !ok || cursor["next_seq"] != float64(1) {
		t.Fatalf("compacted cursor = %#v", cursor)
	}

	// The fork marker is the boundary the frontend folds the inherited history
	// behind.
	events := readForkEvents(t, destDir)
	var marker map[string]any
	for _, event := range events {
		if event["type"] == "colony_fork_marker" {
			marker = event
		}
	}
	if marker == nil {
		t.Fatalf("colony_fork_marker missing from events: %#v", events)
	}
	data, _ := marker["data"].(map[string]any)
	if data["parent_session_id"] != "src-dm" {
		t.Fatalf("marker parent_session_id = %#v", data["parent_session_id"])
	}
	if data["inherited_message_count"] != float64(2) {
		t.Fatalf("marker inherited_message_count = %#v", data["inherited_message_count"])
	}
	if data["summary_preview"] != "the invoice colony plan" {
		t.Fatalf("marker preview = %#v", data["summary_preview"])
	}

	// The seed snapshot is the compacted conversation, not the raw one.
	seedDir := fixture.home.ColonySeedConversationDir("forked")
	seedParts := readForkStoreParts(t, seedDir)
	if len(seedParts) != 1 || seedParts[0]["content"] != "the invoice colony plan" {
		t.Fatalf("seed conversation = %#v, want the summary", seedParts)
	}
}

// Parity: core/framework/server/routes_execution.py::fork_session_into_colony (timeout cap)
func TestColonyForkCompactionTimeoutLeavesRawTranscript(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)
	// The wall cap fires long before the blocked LLM call could return.
	fixture.lifecycle.forkCompactionTimeout = 50 * time.Millisecond

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	blocking := newForkBlockingLLM()
	defer close(blocking.release)
	source.LLM = blocking

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never started")
	}

	// The fork's bounded await ends when the wall cap marks the compaction
	// failed; the session then opens on the raw copied transcript instead of
	// hanging on the blocked LLM call.
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	statusFile := hiveserver.GetStatus(destDir)
	if statusFile["status"] != "failed" {
		t.Fatalf("compaction status = %#v, want failed", statusFile)
	}
	if message, _ := statusFile["error"].(string); !strings.Contains(message, "timed out") {
		t.Fatalf("compaction failure = %q, want the timeout message", message)
	}

	waitForkBackground(t, fixture)
	// Failure leaves the copied raw transcript untouched and unmarked.
	parts := readForkParts(t, destDir)
	if len(parts) != 2 {
		t.Fatalf("raw transcript after timeout = %d parts, want 2: %#v", len(parts), parts)
	}
	for _, event := range readForkEvents(t, destDir) {
		if event["type"] == "colony_fork_marker" {
			t.Fatalf("timed-out compaction appended a fork marker: %#v", event)
		}
	}
	// The seed snapshot still lands, on the raw transcript.
	if seedParts := readForkStoreParts(t, fixture.home.ColonySeedConversationDir("forked")); len(seedParts) != 2 {
		t.Fatalf("seed conversation after timeout = %d parts, want 2", len(seedParts))
	}
}

// Parity: core/framework/server/routes_execution.py::_compact_inherited_conversation (no LLM)
func TestColonyForkFailsSoftWhenCompactionUnavailable(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	// No stamped LLM: compaction is unavailable.
	forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)

	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	if got := hiveserver.GetStatus(destDir); got["status"] != "done" {
		t.Fatalf("compaction status = %#v, want done", got)
	}
	parts := readForkParts(t, destDir)
	if len(parts) != 2 || parts[0]["content"] != strings.Repeat("a", 700) {
		t.Fatalf("raw transcript changed without a compactor: %#v", parts)
	}
	for _, event := range readForkEvents(t, destDir) {
		if event["type"] == "colony_fork_marker" {
			t.Fatalf("uncompacted transcript was marked as a fork boundary: %#v", event)
		}
	}
	seedDir := fixture.home.ColonySeedConversationDir("forked")
	if seedParts := readForkStoreParts(t, seedDir); len(seedParts) != 2 {
		t.Fatalf("seed conversation = %d parts, want the 2 raw parts", len(seedParts))
	}
}

// Parity: core/framework/server/routes_execution.py::fork_session_into_colony (queen_session_id resume)
func TestCreateColonyFromSourceResumesCopiedTranscript(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", "the DM that decided to fork"),
			forkMessage(1, "assistant", "here is the plan"),
		}, nil)
	forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)

	// A scripted provider lets the colony session build its real ChatRuntime
	// over the copied tree (production resolves the configured main slot).
	provider := &forkCompactionLLM{reply: "ok"}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)

	created := fixture.manager.GetSession(sessionID)
	if created == nil {
		t.Fatalf("created session %q is not registered", sessionID)
	}
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	if created.QueenDir != destDir {
		t.Fatalf("resumed session dir = %q, want the copied tree %q", created.QueenDir, destDir)
	}
	if created.QueenResumeFrom != sessionID {
		t.Fatalf("queen_resume_from = %q, want %q", created.QueenResumeFrom, sessionID)
	}
	if created.Chat == nil {
		t.Fatal("created session has no chat runtime to read the transcript")
	}
	transcript, err := created.Chat.Transcript()
	if err != nil {
		t.Fatalf("read resumed transcript: %v", err)
	}
	if len(transcript) != 2 || transcript[0]["content"] != "the DM that decided to fork" {
		t.Fatalf("resumed transcript = %#v, want the copied DM messages", transcript)
	}

	// A cold boot resolves the same id AND the colony-tree dir, so the copy is
	// not stranded after a restart.
	resumedID, resumedDir, resumed := resolvePreloadedColonySession(fixture.home, "forked", "queen_alpha")
	if !resumed || resumedID != sessionID || resumedDir != destDir {
		t.Fatalf("preloaded colony session = %q/%q resumed=%v, want %q/%q", resumedID, resumedDir, resumed, sessionID, destDir)
	}
}

// forkFailLLM fails every compaction call so the finalizer records a failed
// marker without touching the copied transcript.
type forkFailLLM struct{}

func (f *forkFailLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, errors.New("provider down")
}

// doCreateColony posts the Create Colony popup's request without asserting, so
// a test can run it from a goroutine while the compactor is parked.
func doCreateColony(fixture *lifecycleFixture, colonyID, sourceID, prompt string) (int, map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"colony_id":         colonyID,
		"source_session_id": sourceID,
		"initial_prompt":    prompt,
	})
	if err != nil {
		return 0, nil, err
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)
	payload := map[string]any{}
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response.Code, payload, nil
}

func postCreateColonyWithPrompt(t *testing.T, fixture *lifecycleFixture, colonyID, sourceID, prompt string) (int, map[string]any) {
	t.Helper()
	status, payload, err := doCreateColony(fixture, colonyID, sourceID, prompt)
	if err != nil {
		t.Fatalf("build create colony request: %v", err)
	}
	return status, payload
}

// findCopiedColonySessionDir waits for and returns the single copied queen
// session dir the fork materialises before opening the live session.
func findCopiedColonySessionDir(t *testing.T, fixture *lifecycleFixture, colonyID, queenName string) string {
	t.Helper()
	root := filepath.Join(fixture.home.ColonyQueensDir(colonyID), queenName, "sessions")
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := os.ReadDir(root)
		if err == nil && len(entries) == 1 && entries[0].IsDir() {
			return filepath.Join(root, entries[0].Name())
		}
		if time.Now().After(deadline) {
			t.Fatalf("copied colony session dir never appeared under %s (err = %v)", root, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// forkPartContents reads a fork session's conversation contents, tolerating the
// transient read error a concurrent append can produce.
func forkPartContents(dir string) []string {
	store := agentloop.NewFileConversationStore(filepath.Join(dir, "conversations"))
	defer store.Close()
	parts, err := store.ReadParts()
	if err != nil {
		return nil
	}
	contents := make([]string, 0, len(parts))
	for _, part := range parts {
		if content, ok := part["content"].(string); ok {
			contents = append(contents, content)
		}
	}
	return contents
}

// waitForkPartContent waits for a message to reach the fork session's
// transcript (the initial prompt is delivered asynchronously after the session
// opens, and the relay path may prefix it with the skills catalog block).
func waitForkPartContent(t *testing.T, dir, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, content := range forkPartContents(dir) {
			if strings.Contains(content, want) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("part %q never reached %s (contents %#v)", want, dir, forkPartContents(dir))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Parity: core/framework/server/session_manager.py::_create_session_for_existing_colony (await before _create_session_core)
func TestCreateColonyFromSourceAwaitsCompactionBeforeInitialPrompt(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	blocking := newForkBlockingLLM()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocking.release) }) }
	t.Cleanup(release)
	source.LLM = blocking

	provider := &forkCompactionLLM{reply: "ok"}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	// Observe whether the live colony session is opened while the compactor is
	// still parked on its LLM call.
	originalCreate := fixture.lifecycle.createSession
	createCalled := make(chan struct{})
	var createOnce sync.Once
	fixture.lifecycle.createSession = func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error) {
		createOnce.Do(func() { close(createCalled) })
		return originalCreate(params, built, colonyPath)
	}

	prompt := "compile the weekly digest"
	type forkResult struct {
		status  int
		payload map[string]any
		err     error
	}
	responses := make(chan forkResult, 1)
	go func() {
		status, payload, err := doCreateColony(fixture, "forked", "src-dm", prompt)
		responses <- forkResult{status: status, payload: payload, err: err}
	}()

	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never started")
	}
	destDir := findCopiedColonySessionDir(t, fixture, "forked", "queen_alpha")

	// Parked compactor: the session must not open, because its initial_prompt
	// would write into the transcript the compactor is about to rewrite (and
	// the stale-part sweep would drop it).
	select {
	case <-createCalled:
		t.Fatal("colony session opened while compaction was still in progress")
	case <-time.After(250 * time.Millisecond):
	}
	if got := hiveserver.GetStatus(destDir); got["status"] != "in_progress" {
		t.Fatalf("marker while compactor blocked = %#v, want in_progress", got)
	}
	contents := forkPartContents(destDir)
	if len(contents) != 2 {
		t.Fatalf("transcript while compactor blocked = %#v, want the 2 raw parts", contents)
	}
	for _, content := range contents {
		if strings.Contains(content, prompt) {
			t.Fatalf("initial prompt reached the transcript before compaction finished: %#v", contents)
		}
	}

	release()
	var result forkResult
	select {
	case result = <-responses:
	case <-time.After(10 * time.Second):
		t.Fatal("create colony did not return after compaction finished")
	}
	if result.err != nil {
		t.Fatalf("create colony request: %v", result.err)
	}
	if result.status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", result.status, result.payload)
	}
	waitForkBackground(t, fixture)

	// The compactor finished before the session opened, so the prompt landed
	// behind the summary instead of being swept away by the rewrite.
	waitForkPartContent(t, destDir, prompt)
	parts := readForkParts(t, destDir)
	if len(parts) == 0 || parts[0]["content"] != "late summary" {
		t.Fatalf("compacted transcript = %#v, want the summary at seq 0", parts)
	}
	for index, part := range parts {
		if content, _ := part["content"].(string); strings.Contains(content, prompt) {
			if index == 0 {
				t.Fatal("initial prompt replaced the summary at seq 0")
			}
			return
		}
	}
	t.Fatalf("initial prompt missing after compaction: %#v", parts)
}

// Parity: core/framework/server/session_manager.py::_create_session_for_existing_colony (await timeout fail-soft)
func TestCreateColonyFromSourceAwaitTimeoutOpensRawTranscript(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)
	// The marker stays in_progress past this cap: the fork must proceed on the
	// raw transcript instead of waiting out the production 180s.
	fixture.lifecycle.forkAwaitTimeout = 60 * time.Millisecond

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	blocking := newForkBlockingLLM()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocking.release) }) }
	t.Cleanup(release)
	source.LLM = blocking

	originalCreate := fixture.lifecycle.createSession
	var statusAtCreate string
	var partsAtCreate []string
	fixture.lifecycle.createSession = func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error) {
		dir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", *params.SessionID)
		if status := hiveserver.GetStatus(dir); status != nil {
			statusAtCreate, _ = status["status"].(string)
		}
		partsAtCreate = forkPartContents(dir)
		return originalCreate(params, built, colonyPath)
	}

	start := time.Now()
	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	elapsed := time.Since(start)
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("fork blocked %s on an unsettled marker, want the await cap to bound it", elapsed)
	}
	if statusAtCreate != "in_progress" {
		t.Fatalf("marker at session open = %q, want in_progress after the await cap", statusAtCreate)
	}
	if len(partsAtCreate) != 2 {
		t.Fatalf("transcript at session open = %#v, want the 2 raw parts", partsAtCreate)
	}

	release()
	waitForkBackground(t, fixture)
	sessionID, _ := payload["session_id"].(string)
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	if got := hiveserver.GetStatus(destDir); got["status"] != "done" {
		t.Fatalf("compaction status = %#v, want done after the late compactor finished", got)
	}
	parts := readForkParts(t, destDir)
	if len(parts) != 1 || parts[0]["content"] != "late summary" {
		t.Fatalf("transcript after the late compactor = %#v, want the summary", parts)
	}
}

// Parity: core/framework/server/session_manager.py::_create_session_for_existing_colony (done marker)
func TestCreateColonyFromSourceOpensOnlyAfterCompactionDone(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	source.LLM = &forkCompactionLLM{reply: "the invoice colony plan"}

	provider := &forkCompactionLLM{reply: "ok"}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	prompt := "start the invoice audit"
	originalCreate := fixture.lifecycle.createSession
	var statusAtCreate string
	var partsAtCreate []map[string]any
	fixture.lifecycle.createSession = func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error) {
		dir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", *params.SessionID)
		if status := hiveserver.GetStatus(dir); status != nil {
			statusAtCreate, _ = status["status"].(string)
		}
		partsAtCreate = readForkParts(t, dir)
		return originalCreate(params, built, colonyPath)
	}

	status, payload := postCreateColonyWithPrompt(t, fixture, "forked", "src-dm", prompt)
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	if statusAtCreate != "done" {
		t.Fatalf("marker at session open = %q, want done", statusAtCreate)
	}
	if len(partsAtCreate) != 1 || partsAtCreate[0]["content"] != "the invoice colony plan" {
		t.Fatalf("transcript at session open = %#v, want the compacted summary", partsAtCreate)
	}

	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	waitForkPartContent(t, destDir, prompt)
	parts := readForkParts(t, destDir)
	if len(parts) < 2 || parts[0]["content"] != "the invoice colony plan" {
		t.Fatalf("transcript after prompt delivery = %#v, want the summary first and the prompt behind it", parts)
	}
}

// Parity: core/framework/server/session_manager.py::_create_session_for_existing_colony (failed marker)
func TestCreateColonyFromSourceOpensOnRawTranscriptAfterCompactionFailure(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	writeForkWindow(t, fixture.home, 1000)

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", strings.Repeat("a", 700)),
			forkMessage(1, "assistant", strings.Repeat("b", 700)),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	source.LLM = &forkFailLLM{}

	provider := &forkCompactionLLM{reply: "ok"}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	prompt := "continue on the raw transcript"
	originalCreate := fixture.lifecycle.createSession
	var statusAtCreate string
	var partsAtCreate []string
	fixture.lifecycle.createSession = func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error) {
		dir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", *params.SessionID)
		if status := hiveserver.GetStatus(dir); status != nil {
			statusAtCreate, _ = status["status"].(string)
		}
		partsAtCreate = forkPartContents(dir)
		return originalCreate(params, built, colonyPath)
	}

	status, payload := postCreateColonyWithPrompt(t, fixture, "forked", "src-dm", prompt)
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	if statusAtCreate != "failed" {
		t.Fatalf("marker at session open = %q, want failed", statusAtCreate)
	}
	if len(partsAtCreate) != 2 {
		t.Fatalf("transcript at session open = %#v, want the 2 raw parts", partsAtCreate)
	}

	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)
	destDir := fixture.home.ColonyQueenSessionDir("forked", "queen_alpha", sessionID)
	if got := hiveserver.GetStatus(destDir); got["status"] != "failed" {
		t.Fatalf("compaction status = %#v, want failed", got)
	}
	waitForkPartContent(t, destDir, prompt)
	contents := forkPartContents(destDir)
	if len(contents) < 3 {
		t.Fatalf("transcript after failed compaction = %#v, want the raw parts plus the prompt", contents)
	}
	if contents[0] != strings.Repeat("a", 700) || contents[1] != strings.Repeat("b", 700) {
		t.Fatalf("failed compaction rewrote the raw transcript: %#v", contents)
	}
}

// Parity: core/framework/server/session_manager.py::_create_session_for_existing_colony (no queen_resume_from)
func TestCreateColonyFromSourceWithoutSourceSessionSkipsCompactionAwait(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	source := hiveserver.NewSession("src-dm", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
	fixture.manager.AddSession(source)

	awaits := 0
	fixture.lifecycle.awaitForkCompaction = func(context.Context, string, time.Duration, time.Duration) map[string]any {
		awaits++
		return nil
	}

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != 201 {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	if awaits != 0 {
		t.Fatalf("fork awaited compaction %d time(s) without a copied source session", awaits)
	}
	if _, err := os.Stat(fixture.home.ColonyQueensDir("forked")); !os.IsNotExist(err) {
		t.Fatalf("no-copy fork materialised a colony queen tree (stat err = %v)", err)
	}
	sessionID, _ := payload["session_id"].(string)
	if session := fixture.manager.GetSession(sessionID); session == nil || session.ColonyID != "forked" {
		t.Fatalf("created session = %#v", session)
	}
}
