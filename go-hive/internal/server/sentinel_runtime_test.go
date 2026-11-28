package server

// Sentinel wiring: a parked colony queen must be classified (continue → nudge,
// needs_human → escalate, done → completion report) instead of sitting silent.
// These tests drive the source the chat runtime registers on its reminder hub,
// the process-global manager it escalates through, and the on-disk record that
// survives a notifier with no credentials.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

type sentinelSend struct {
	channel string
	text    string
	meta    map[string]any
}

type failingSentinelNotifier struct {
	mu    sync.Mutex
	sends []sentinelSend
}

func (n *failingSentinelNotifier) Send(_ context.Context, channel string, _ map[string]any, text string, _ map[string]any, meta map[string]any) sentinel.NotifierResult {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sends = append(n.sends, sentinelSend{channel: channel, text: text, meta: meta})
	// The credential-less adapter shape: the record is the durable artifact,
	// delivery reports failure instead of fabricating a message id.
	return sentinel.NotifierResult{Error: "no credentials configured"}
}

func (n *failingSentinelNotifier) sendCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sends)
}

func (n *failingSentinelNotifier) lastSend(t *testing.T) sentinelSend {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.sends) == 0 {
		t.Fatal("notifier was never called")
	}
	return n.sends[len(n.sends)-1]
}

// newSentinelWiringRuntime builds a chat runtime for a colony-queen session
// with a real Sentinel manager installed process-wide, so NewChatRuntime
// registers the escalation source exactly as `hive serve` does.
func newSentinelWiringRuntime(t *testing.T, colonyID string, notifier sentinel.Notifier) (*ChatRuntime, *sentinel.Manager, string) {
	t.Helper()
	home := t.TempDir()

	t.Setenv("HIVE_HOME", home)
	previousStore := tasks.GetTaskStore()
	tasks.SetDefaultTaskStore(tasks.NewTaskStore(home))
	tasks.ResetSessionDirCache()
	t.Cleanup(func() {
		tasks.SetDefaultTaskStore(previousStore)
		tasks.ResetSessionDirCache()
	})

	store := sentinel.NewStore(home)
	colonyDir := filepath.Join(home, "colonies", colonyID)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	config := map[string]any{
		"sentinel_enabled": true,
		"channel":          "hive",
		"target":           map[string]any{"inbox": true},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal notifications: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "notifications.json"), encoded, 0o600); err != nil {
		t.Fatalf("write notifications: %v", err)
	}

	sessionID := "session_sentinel_" + colonyID
	manager := sentinel.NewManager(sentinel.ManagerOptions{
		Store:    store,
		Notifier: notifier,
		Sessions: sentinelSessionManager{manager: NewSessionManager()},
	})
	sentinel.SetSentinelManager(manager)
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(nil)
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("sentinel start: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		Bus:       eventbus.NewEventBus(),
		SessionID: sessionID,
		Colony: colonyHostStub{runtime: colony.NewColonyRuntime(colony.RuntimeOptions{
			EventBus: eventbus.NewEventBus(),
			StreamID: colonyID,
		})},
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.sentinelSource == nil {
		t.Fatal("chat runtime did not register a sentinel escalation source")
	}
	runtime.mu.Lock()
	runtime.pendingQuestions = []map[string]any{{"id": "next", "prompt": "Which next?"}}
	runtime.mu.Unlock()
	runtime.setParkState(agentloop.ParkReasonAskUser, true)

	if _, err := tasks.GetTaskStore().CreateTask(sessionID, tasks.CreateTaskSpec{Subject: "finish the report"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return runtime, manager, home
}

func askUserSignals() *agentloop.LoopSignals {
	reason := agentloop.ParkReasonAskUser
	return &agentloop.LoopSignals{IdleSeconds: 600, AwaitingInput: true, ParkReason: &reason}
}

func classifyAs(verdict string) func(context.Context, sentinel.ParkContext, llm.Completer) sentinel.ClassifierVerdict {
	return func(context.Context, sentinel.ParkContext, llm.Completer) sentinel.ClassifierVerdict {
		return sentinel.ClassifierVerdict{Verdict: verdict, Reason: "test decision"}
	}
}

// collectIdleReminders is the hub's IDLE_TICK consultation — the same call the
// ticker makes — so the assertions prove the source is in the bound list, not
// just that its Render works when called directly.
func collectIdleReminders(runtime *ChatRuntime) []agentloop.Reminder {
	return runtime.hub.CollectWithSignals(
		agentloop.IDLE_TICK,
		agentloop.NewReminderAgentContext(runtime.agentCtx),
		askUserSignals(),
		nil,
		"",
	)
}

func sentinelReminder(items []agentloop.Reminder) *agentloop.Reminder {
	for index := range items {
		if items[index].Source == "sentinel_escalation" {
			return &items[index]
		}
	}
	return nil
}

func waitForEscalationRecord(t *testing.T, manager *sentinel.Manager, colonyID string) map[string]any {
	t.Helper()
	dir := manager.Store.EscalationsDir(colonyID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					continue
				}
				record := map[string]any{}
				if json.Unmarshal(data, &record) == nil {
					return record
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no escalation record appeared under %s", dir)
	return nil
}

// Parity: core/tests/test_sentinel_escalation_source.py::test_nudge_returns_reminder
func TestSentinelWiringNudgesParkedQueen(t *testing.T) {
	notifier := &failingSentinelNotifier{}
	runtime, manager, _ := newSentinelWiringRuntime(t, "colony_nudge", notifier)
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictContinue)

	reminder := sentinelReminder(collectIdleReminders(runtime))
	if reminder == nil {
		t.Fatal("hub did not consult the sentinel source on IDLE_TICK")
	}
	if !strings.Contains(reminder.Body, "You paused, but the goal is not finished") {
		t.Fatalf("nudge body = %q, want the autonomous-continue instruction", reminder.Body)
	}
	if !strings.Contains(reminder.Body, "finish the report") {
		t.Fatalf("nudge body = %q, want the open task list", reminder.Body)
	}

	// The progress report is still the durable Inbox artifact. Its kind rides
	// the notifier meta: the record schema carries no kind field upstream.
	record := waitForEscalationRecord(t, manager, "colony_nudge")
	if record["status"] != sentinel.StatusOpen {
		t.Fatalf("status = %v, want open", record["status"])
	}
	if kind := notifier.lastSend(t).meta["kind"]; kind != sentinel.ReportProgress {
		t.Fatalf("reported kind = %v, want progress", kind)
	}

	// Delivering the nudge is what wakes the queen: exactly one reminder turn.
	runtime.hub.Post(*reminder)
	runtime.wakeForReminder()
	content := waitForTranscriptContent(t, runtime, "You paused, but the goal is not finished", 5*time.Second)
	if !strings.Contains(content, "<system-reminder>") {
		t.Fatalf("nudge was not injected as a system reminder:\n%s", content)
	}

	// One nudge per budget window: the source self-rate-limits the next tick.
	if _, again := runtime.sentinelSource.RenderReminder(agentloop.ReminderContext{
		Point:    agentloop.IDLE_TICK,
		AgentCtx: agentloop.NewReminderAgentContext(runtime.agentCtx),
		Signals:  askUserSignals(),
	}); again {
		t.Fatal("sentinel nudged twice inside the same budget window")
	}
}

// Parity: core/tests/test_sentinel_escalation_source.py::test_escalate_side_effect_and_none
func TestSentinelWiringEscalatesToRecord(t *testing.T) {
	notifier := &failingSentinelNotifier{}
	runtime, manager, _ := newSentinelWiringRuntime(t, "colony_escalate", notifier)
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictNeedsHuman)

	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder != nil {
		t.Fatal("needs_human must escalate instead of nudging")
	}

	record := waitForEscalationRecord(t, manager, "colony_escalate")
	if kind := notifier.lastSend(t).meta["kind"]; kind != sentinel.EscalateBlocker {
		t.Fatalf("reported kind = %v, want blocker", kind)
	}
	if record["park_reason"] != string(agentloop.ParkReasonAskUser) {
		t.Fatalf("park_reason = %v, want ask_user", record["park_reason"])
	}
	if record["status"] != sentinel.StatusOpen {
		t.Fatalf("status = %v, want open", record["status"])
	}
	if text, _ := record["question_text"].(string); !strings.Contains(text, "Which next?") {
		t.Fatalf("question_text = %q, want the parked question", text)
	}
	if !strings.HasPrefix(record["escalation_id"].(string), "esc_") {
		t.Fatalf("escalation_id = %v, want the esc_ prefix", record["escalation_id"])
	}
	if notifier.sendCount() != 1 {
		t.Fatalf("notifier sends = %d, want 1", notifier.sendCount())
	}
	// Delivery failed (no credentials), so nothing may look delivered.
	if anchor, _ := record["thread_ref"].(map[string]any); len(anchor) != 0 {
		t.Fatalf("thread_ref = %v, want empty on a failed send", anchor)
	}
}

// Parity: core/tests/test_sentinel_escalation_source.py::test_done_reports_completion
func TestSentinelWiringReportsDone(t *testing.T) {
	notifier := &failingSentinelNotifier{}
	runtime, manager, _ := newSentinelWiringRuntime(t, "colony_done", notifier)
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictDone)

	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder != nil {
		t.Fatal("done must report completion instead of nudging")
	}

	record := waitForEscalationRecord(t, manager, "colony_done")
	if record["status"] != sentinel.StatusOpen {
		t.Fatalf("status = %v, want open", record["status"])
	}
	if kind := notifier.lastSend(t).meta["kind"]; kind != sentinel.ReportDone {
		t.Fatalf("reported kind = %v, want done", kind)
	}
}

// Parity-exempt: proves the credential-less notifier adapter still persists the
// record; upstream covers the notifier failure shape in test_sentinel_notifier.
func TestSentinelWiringPersistsWithoutCredentials(t *testing.T) {
	notifier := sentinel.NewHTTPNotifier(nil)
	notifier.Getenv = func(string) string { return "" }
	runtime, manager, _ := newSentinelWiringRuntime(t, "colony_offline", notifier)
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictNeedsHuman)

	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder != nil {
		t.Fatal("needs_human must not nudge")
	}

	// No cloud JWT/base is configured: the send fails, but the record is the
	// durable artifact and must still be on disk.
	record := waitForEscalationRecord(t, manager, "colony_offline")
	if record["status"] != sentinel.StatusOpen {
		t.Fatalf("status = %v, want open", record["status"])
	}
	if text, _ := record["question_text"].(string); !strings.Contains(text, "Which next?") {
		t.Fatalf("question_text = %q, want the parked question", text)
	}
	if anchor, _ := record["thread_ref"].(map[string]any); len(anchor) != 0 {
		t.Fatalf("thread_ref = %v, want empty (nothing was delivered)", anchor)
	}
}

// Parity-exempt: proves the late colony attach flips Sentinel's gate and
// re-filters the hub sources; upstream binds the colony before constructing the
// loop, so it has no late-bind test.
func TestSentinelGateFollowsColonyBinding(t *testing.T) {
	home := t.TempDir()
	store := sentinel.NewStore(home)
	colonyID := "colony_late"
	colonyDir := filepath.Join(home, "colonies", colonyID)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	encoded, _ := json.Marshal(map[string]any{"sentinel_enabled": true, "channel": "hive", "target": map[string]any{"inbox": true}})
	if err := os.WriteFile(filepath.Join(colonyDir, "notifications.json"), encoded, 0o600); err != nil {
		t.Fatalf("write notifications: %v", err)
	}

	manager := sentinel.NewManager(sentinel.ManagerOptions{Store: store, Notifier: &failingSentinelNotifier{}})
	sentinel.SetSentinelManager(manager)
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(nil)
	})

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		SessionID: "session_sentinel_late",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.sentinelSource == nil {
		t.Fatal("sentinel source missing before the colony bound")
	}
	if runtime.sentinelAutopilotEnabled() {
		t.Fatal("autopilot enabled without a colony")
	}

	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		EventBus: eventbus.NewEventBus(),
		StreamID: colonyID,
	})
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})
	t.Cleanup(func() { runtime.BindColony(nil) })

	if !runtime.sentinelAutopilotEnabled() {
		t.Fatal("autopilot stayed off after the colony bound")
	}
	if !runtime.sentinelSource.AppliesTo(agentloop.NewReminderAgentContext(runtime.agentCtx)) {
		t.Fatal("sentinel source does not apply after the colony bound")
	}
}

// Keep the fake LLM's interface assertion local to this package's tests: the
// classifier override above never calls it, but the runtime still owns one.
var _ llm.Completer = (*fakeChatLLM)(nil)

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_cold_restore_when_session_not_live
//
// A sentinel reply can target a session the live table no longer holds. The
// restorer must reopen it with the record's colony and resume target, and under
// the queen that wrote it (resolved from colony metadata, as the create route
// does), because a colony-bound resume otherwise looks in the wrong tree.
func TestSentinelSessionRestorerBuildsResumeParams(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colonyDir := filepath.Join(home.ColoniesDir(), "colony_resume")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte(`{"queen_name":"queen_growth"}`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	var captured CreateSessionParams
	restore := NewSentinelSessionRestorer(home, func(params CreateSessionParams) (*Session, error) {
		captured = params
		return NewSession("s1", eventbus.NewEventBus()), nil
	})
	if restore == nil {
		t.Fatal("restorer is nil with a create hook")
	}
	session, err := restore(context.Background(), "colony_resume", "s1")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if session == nil {
		t.Fatal("restore returned no live session")
	}
	if captured.ColonyID == nil || *captured.ColonyID != "colony_resume" {
		t.Fatalf("colony_id = %v", captured.ColonyID)
	}
	if captured.QueenResumeFrom == nil || *captured.QueenResumeFrom != "s1" {
		t.Fatalf("queen_resume_from = %v", captured.QueenResumeFrom)
	}
	if captured.QueenName == nil || *captured.QueenName != "queen_growth" {
		t.Fatalf("queen_name = %v, want the colony's recorded queen", captured.QueenName)
	}
	if session.SSEClientCount() != 0 || session.QueenNode() != nil {
		t.Fatalf("fresh session surface = sse %d node %v", session.SSEClientCount(), session.QueenNode())
	}
}

// Parity-exempt: negative regression for the cold-restore wiring gap. The bare
// server adapter cannot rebuild a session, and its unsupported error is what
// stops a reply from being reported as delivered when no host factory exists.
func TestSentinelSessionAdapterWithoutRestorerIsUnsupported(t *testing.T) {
	adapter := sentinelSessionManager{manager: NewSessionManager()}
	if _, err := adapter.CreateSession(context.Background(), "c1", "s1"); !errors.Is(err, ErrSentinelColdRestoreUnsupported) {
		t.Fatalf("err = %v, want ErrSentinelColdRestoreUnsupported", err)
	}
	if NewSentinelSessionRestorer(hivehome.Home{Root: t.TempDir()}, nil) != nil {
		t.Fatal("restorer must be nil without a create hook")
	}
}

// Parity-exempt: cold-restore error propagation has no Python unit test; a
// failed create must stay a failed resume.
func TestSentinelSessionRestorerPropagatesCreateFailure(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	restore := NewSentinelSessionRestorer(home, func(CreateSessionParams) (*Session, error) {
		return nil, errors.New("provider unavailable")
	})
	if _, err := restore(context.Background(), "c1", "s1"); err == nil {
		t.Fatal("restore error was swallowed")
	}
	empty := NewSentinelSessionRestorer(home, func(CreateSessionParams) (*Session, error) { return nil, nil })
	if _, err := empty(context.Background(), "c1", "s1"); err == nil {
		t.Fatal("a nil session must be an error")
	}
}

// Parity-exempt: negative regression for the manager-level wiring: with no
// restorer installed, a cold restore must fail instead of claiming a resume.
func TestNewSentinelManagerColdRestoreWithoutRestorerFails(t *testing.T) {
	previous := sentinel.GetSentinelManager()
	manager := NewSentinelManager(t.TempDir(), NewSessionManager())
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(previous)
	})
	if session := manager.ColdRestore(context.Background(), &sentinel.EscalationRecord{ColonyID: "c1", SessionID: "s1"}); session != nil {
		t.Fatalf("cold restore = %#v, want failure without a restorer", session)
	}
}

// Parity-exempt: positive half of the same wiring: a restorer passed at
// construction must be the one ColdRestore uses.
func TestNewSentinelManagerColdRestoreUsesRestorer(t *testing.T) {
	previous := sentinel.GetSentinelManager()
	home := t.TempDir()
	restore := NewSentinelSessionRestorer(hivehome.Home{Root: home}, func(CreateSessionParams) (*Session, error) {
		return NewSession("s1", eventbus.NewEventBus()), nil
	})
	manager := NewSentinelManager(home, NewSessionManager(), restore)
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(previous)
	})
	if session := manager.ColdRestore(context.Background(), &sentinel.EscalationRecord{ColonyID: "c1", SessionID: "s1"}); session == nil {
		t.Fatal("cold restore did not use the restorer passed at construction")
	}
}
