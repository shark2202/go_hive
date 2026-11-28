package sentinel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type nodeCall struct {
	content  string
	clientIn bool
}

type fakeNode struct {
	mu       sync.Mutex
	injected []nodeCall
	err      error
}

func (n *fakeNode) InjectEvent(_ context.Context, content string, isClientInput bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.injected = append(n.injected, nodeCall{content: content, clientIn: isClientInput})
	return nil
}

type fakeSession struct {
	node QueenNode
	sse  int
}

func (s *fakeSession) SSEClientCount() int { return s.sse }

func (s *fakeSession) QueenNode() QueenNode { return s.node }

type createCall struct {
	colonyID string
	resume   string
}

type fakeSM struct {
	sessions  map[string]LiveSession
	created   []createCall
	createErr error
}

func (s *fakeSM) GetLiveSession(sessionID string) LiveSession { return s.sessions[sessionID] }

func (s *fakeSM) CreateSession(_ context.Context, colonyID, queenResumeFrom string) (LiveSession, error) {
	s.created = append(s.created, createCall{colonyID: colonyID, resume: queenResumeFrom})
	if s.createErr != nil {
		return nil, s.createErr
	}
	return nil, nil
}

type sendCall struct {
	channel   string
	target    map[string]any
	text      string
	threadRef map[string]any
	meta      map[string]any
}

type fakeNotifier struct {
	result    NotifierResult
	sends     []sendCall
	resolved  []string
	resolveOK bool
}

func (f *fakeNotifier) Send(_ context.Context, channel string, target map[string]any, text string, threadRef map[string]any, meta map[string]any) NotifierResult {
	f.sends = append(f.sends, sendCall{channel: channel, target: target, text: text, threadRef: threadRef, meta: meta})
	return f.result
}

func (f *fakeNotifier) ResolveHiveSession(_ context.Context, sessionID, _ string) bool {
	f.resolved = append(f.resolved, sessionID)
	return f.resolveOK
}

func newTestManager(t *testing.T, store *Store, notifier Notifier, sessions SessionManager) *Manager {
	t.Helper()
	return NewManager(ManagerOptions{
		Store:    store,
		Tokens:   fixedSecretTokens(t, store),
		Notifier: notifier,
		Sessions: sessions,
		Clock:    newFakeClock(),
	})
}

func writeNotificationAllowlist(t *testing.T, store *Store, colony string, allowlist []string) {
	t.Helper()
	values := make([]any, 0, len(allowlist))
	for _, value := range allowlist {
		values = append(values, value)
	}
	writeColonyConfig(t, store, colony, map[string]any{
		"sentinel_enabled": true,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "9"},
		"allowlist":        values,
	})
}

func openRecord(t *testing.T, store *Store, tokens *TokenManager, colony, escalation, session string) *EscalationRecord {
	t.Helper()
	record := &EscalationRecord{
		EscalationID:     escalation,
		ColonyID:         colony,
		SessionID:        session,
		CorrelationToken: tokens.MakeToken(escalation),
		ParkReason:       "ask_user",
		QuestionText:     "continue?",
		Channel:          "telegram",
		ThreadRef:        map[string]any{"message_id": 5},
		Status:           StatusOpen,
		CreatedAt:        "2026-01-02T03:04:05+00:00",
	}
	if err := store.WriteEscalation(record); err != nil {
		t.Fatalf("write record: %v", err)
	}
	return record
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_authorized_reply_resumes_and_resolves
func TestAuthorizedReplyResumesAndResolves(t *testing.T) {
	store := newTestStore(t, nil)
	writeNotificationAllowlist(t, store, "c1", []string{"42"})
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{"s1": &fakeSession{node: node}}})
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}

	text := "yes go ahead " + FormatRef(record.CorrelationToken)
	manager.OnInbound(context.Background(), "telegram", "42", text, map[string]any{"message_id": 5}, "")

	if len(node.injected) != 1 || node.injected[0].content != "yes go ahead" || !node.injected[0].clientIn {
		t.Fatalf("injected = %#v", node.injected)
	}
	loaded, ok := store.LoadEscalation("c1", "esc_1")
	if !ok || loaded.Status != StatusResolved {
		t.Fatalf("record = %#v", loaded)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_unauthorized_sender_ignored
func TestUnauthorizedSenderIgnored(t *testing.T) {
	store := newTestStore(t, nil)
	writeNotificationAllowlist(t, store, "c1", []string{"42"})
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{"s1": &fakeSession{node: node}}})
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}

	manager.OnInbound(context.Background(), "telegram", "999", "hi "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	if len(node.injected) != 0 {
		t.Fatalf("injected = %#v", node.injected)
	}
	loaded, _ := store.LoadEscalation("c1", "esc_1")
	if loaded.Status != StatusOpen {
		t.Fatalf("record status = %q", loaded.Status)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_ambiguous_reply_without_token_ignored
func TestAmbiguousReplyWithoutTokenIgnored(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	openRecord(t, store, tokens, "c1", "esc_1", "s1")
	openRecord(t, store, tokens, "c2", "esc_2", "s2")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{
		"s1": &fakeSession{node: node},
		"s2": &fakeSession{node: node},
	}})
	manager.Tokens = tokens

	manager.OnInbound(context.Background(), "telegram", "42", "just continue", map[string]any{}, "")

	if len(node.injected) != 0 {
		t.Fatalf("two open telegram escalations must not disambiguate: %#v", node.injected)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_fallback_single_open_resolves
func TestFallbackSingleOpenResolves(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	openRecord(t, store, tokens, "c1", "esc_1", "s1")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{"s1": &fakeSession{node: node}}})
	manager.Tokens = tokens

	manager.OnInbound(context.Background(), "telegram", "42", "go", map[string]any{"message_id": 5}, "")

	if len(node.injected) != 1 || node.injected[0].content != "go" {
		t.Fatalf("injected = %#v", node.injected)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_resolved_record_not_reprocessed
func TestResolvedRecordNotReprocessed(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	store.ResolveEscalation("c1", "esc_1", "")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{"s1": &fakeSession{node: node}}})
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}

	manager.OnInbound(context.Background(), "telegram", "42", "go "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	if len(node.injected) != 0 {
		t.Fatalf("injected = %#v", node.injected)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_cold_restore_when_session_not_live
func TestColdRestoreWhenSessionNotLive(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	sessions := &fakeSM{sessions: map[string]LiveSession{}}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, sessions)
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}

	manager.OnInbound(context.Background(), "telegram", "42", "go "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	if len(sessions.created) != 1 || sessions.created[0] != (createCall{colonyID: "c1", resume: "s1"}) {
		t.Fatalf("cold restore = %#v", sessions.created)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_cold_restore_when_session_not_live
//
// The host-supplied restorer is the production path: it must receive the
// record's colony and resume target, override the bare SessionManager, and the
// restored queen must receive the reply.
func TestColdRestoreUsesWiredSessionRestorer(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	sessions := &fakeSM{sessions: map[string]LiveSession{}}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, sessions)
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}
	node := &fakeNode{}
	manager.SetSessionRestorer(func(_ context.Context, colonyID, resumeFrom string) (LiveSession, error) {
		if colonyID != "c1" || resumeFrom != "s1" {
			return nil, errors.New("wrong restore target")
		}
		return &fakeSession{node: node}, nil
	})

	manager.OnInbound(context.Background(), "telegram", "42", "go "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	if len(sessions.created) != 0 {
		t.Fatalf("bare SessionManager.CreateSession should not run when a restorer is wired: %#v", sessions.created)
	}
	if len(node.injected) != 1 || node.injected[0].content != "go" || !node.injected[0].clientIn {
		t.Fatalf("restored queen injections = %#v", node.injected)
	}
	loaded, _ := store.LoadEscalation("c1", "esc_1")
	if loaded.Status != StatusResolved {
		t.Fatalf("record status = %q, want resolved", loaded.Status)
	}
}

// Parity-exempt: cold-restore failure has no Python unit test; this pins that
// an unwired/failed restoration is reported, not mistaken for delivery, and
// leaves the escalation open for a later reply.
func TestColdRestoreFailureLeavesEscalationOpen(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{}})
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}
	manager.SetSessionRestorer(func(context.Context, string, string) (LiveSession, error) {
		return nil, errors.New("provider unavailable")
	})

	manager.OnInbound(context.Background(), "telegram", "42", "go "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	if len(node.injected) != 0 {
		t.Fatalf("unexpected injection: %#v", node.injected)
	}
	loaded, _ := store.LoadEscalation("c1", "esc_1")
	if loaded.Status != StatusOpen {
		t.Fatalf("record status = %q, want open", loaded.Status)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_has_attached_ui
func TestHasAttachedUI(t *testing.T) {
	store := newTestStore(t, nil)
	node := &fakeNode{}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{
		"s1": &fakeSession{node: node, sse: 2},
		"s2": &fakeSession{node: node, sse: 0},
	}})
	if !manager.HasAttachedUI("s1") {
		t.Fatal("s1 has a subscribed client")
	}
	if manager.HasAttachedUI("s2") {
		t.Fatal("s2 has no subscribed client")
	}
	if manager.HasAttachedUI("missing") {
		t.Fatal("missing session cannot have a UI")
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_enqueue_escalation_accepts
func TestEnqueueEscalationAccepts(t *testing.T) {
	store := newTestStore(t, nil)
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})
	if !manager.EnqueueEscalation(map[string]any{"escalation_id": "e"}) {
		t.Fatal("enqueue should be accepted")
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_format_message_blocker_vs_heartbeat_and_mrkdwn
func TestFormatMessageBlockerVsHeartbeatAndMrkdwn(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})
	base := map[string]any{
		"colony_id":         "c1",
		"correlation_token": tokens.MakeToken("esc_1"),
		"question_text":     "Hit **5000** engagers?",
	}
	blocker := manager.FormatMessage(withKind(base, "blocker"))
	if !strings.Contains(blocker, "needs you") {
		t.Fatalf("blocker = %q", blocker)
	}
	if !strings.Contains(blocker, "*5000*") || strings.Contains(blocker, "**5000**") {
		t.Fatalf("markdown not converted: %q", blocker)
	}
	heartbeat := manager.FormatMessage(withKind(base, "heartbeat"))
	if !strings.Contains(heartbeat, "still working") || !strings.Contains(strings.ToLower(heartbeat), "redirect") {
		t.Fatalf("heartbeat = %q", heartbeat)
	}
	if strings.Contains(heartbeat, "needs you") {
		t.Fatalf("heartbeat must not read as a blocker: %q", heartbeat)
	}
}

func withKind(payload map[string]any, kind string) map[string]any {
	copied := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		copied[key] = value
	}
	copied["kind"] = kind
	return copied
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_on_local_resume_closes_open
func TestOnLocalResumeClosesOpen(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	openRecord(t, store, tokens, "c1", "esc_1", "s1")
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})

	manager.OnLocalResume(context.Background(), "s1")

	loaded, _ := store.LoadEscalation("c1", "esc_1")
	if loaded.Status != StatusResolved {
		t.Fatalf("status = %q", loaded.Status)
	}
}

// Parity: core/framework/sentinel/manager.py, core/tests/test_sentinel_manager.py::test_report_supersedes_prior_open_for_session
func TestReportSupersedesPriorOpenForSession(t *testing.T) {
	store := newTestStore(t, nil)
	writeColonyConfig(t, store, "c1", map[string]any{
		"sentinel_enabled": true,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "1"},
		"allowlist":        []any{},
	})
	tokens := fixedSecretTokens(t, store)
	openRecord(t, store, tokens, "c1", "esc_old", "s1") // e.g. stale progress
	notifier := &fakeNotifier{result: NotifierResult{OK: true, MessageID: "m1"}}
	manager := newTestManager(t, store, notifier, &fakeSM{})
	manager.Tokens = tokens

	manager.HandleEscalation(context.Background(), map[string]any{
		"escalation_id":     "esc_new",
		"colony_id":         "c1",
		"session_id":        "s1",
		"correlation_token": tokens.MakeToken("esc_new"),
		"kind":              "progress",
		"question_text":     "working on Y",
		"channel":           "telegram",
		"target":            map[string]any{"chat_id": "1"},
		"thread":            map[string]any{},
	})

	old, _ := store.LoadEscalation("c1", "esc_old")
	if old.Status != StatusResolved {
		t.Fatalf("old status = %q", old.Status)
	}
	fresh, _ := store.LoadEscalation("c1", "esc_new")
	if fresh.Status != StatusOpen {
		t.Fatalf("new status = %q", fresh.Status)
	}
	// A different session is untouched.
	openRecord(t, store, tokens, "c1", "esc_other", "s2")
	other, _ := store.LoadEscalation("c1", "esc_other")
	if other.Status != StatusOpen {
		t.Fatalf("other status = %q", other.Status)
	}
}

// Parity-exempt: manager.py::SentinelManager.start has no Python test; this
// covers the disk-to-token-index rebuild that survives a restart.
func TestStartRebuildsTokenIndex(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})
	manager.Tokens = tokens

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer manager.Stop()
	manager.mu.Lock()
	ref, ok := manager.byToken[record.CorrelationToken]
	manager.mu.Unlock()
	if !ok || ref.colonyID != "c1" || ref.escalationID != "esc_1" {
		t.Fatalf("token index = %#v, ok=%t", ref, ok)
	}
}

// Parity-exempt: manager.py::SentinelManager._reply_anchor has no Python test.
func TestReplyAnchorPerChannel(t *testing.T) {
	store := newTestStore(t, nil)
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})
	anchor := manager.ReplyAnchor("telegram", "77")
	if number, ok := anchor["message_id"].(int64); !ok || number != 77 {
		t.Fatalf("telegram anchor = %#v", anchor)
	}
	if got := manager.ReplyAnchor("hive", "row-1"); len(got) != 0 {
		t.Fatalf("hive anchor = %#v", got)
	}
	slack := manager.ReplyAnchor("slack", "123.45")
	if slack["ts"] != "123.45" {
		t.Fatalf("slack anchor = %#v", slack)
	}
}

// Parity-exempt: manager.py::SentinelManager._thread_matches has no Python test.
func TestThreadMatchesByMessageIDOrTS(t *testing.T) {
	telegram := &EscalationRecord{ThreadRef: map[string]any{"message_id": 5}}
	if !ThreadMatches(telegram, map[string]any{"message_id": "5"}) {
		t.Fatal("string/int message ids should match")
	}
	slack := &EscalationRecord{ThreadRef: map[string]any{"ts": "100.1"}}
	if !ThreadMatches(slack, map[string]any{"ts": "100.1"}) {
		t.Fatal("ts should match")
	}
	if ThreadMatches(telegram, map[string]any{"ts": "100.1"}) {
		t.Fatal("different anchor kinds must not match")
	}
}

// Parity-exempt: manager.py has no async drain test; this exercises the
// enqueue -> drain -> persist -> deliver path end to end.
func TestEnqueuedEscalationIsDeliveredAndPersisted(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	notifier := &fakeNotifier{result: NotifierResult{OK: true, MessageID: "m1"}}
	manager := newTestManager(t, store, notifier, &fakeSM{})
	manager.Tokens = tokens

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer manager.Stop()
	if !manager.EnqueueEscalation(map[string]any{
		"escalation_id":     "esc_drain",
		"colony_id":         "c1",
		"session_id":        "s1",
		"correlation_token": tokens.MakeToken("esc_drain"),
		"kind":              "blocker",
		"question_text":     "need a decision",
		"channel":           "telegram",
		"target":            map[string]any{"chat_id": "1"},
		"thread":            map[string]any{},
	}) {
		t.Fatal("enqueue rejected")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := store.LoadEscalation("c1", "esc_drain"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("escalation was not drained and persisted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(notifier.sends) != 1 || notifier.sends[0].channel != "telegram" {
		t.Fatalf("sends = %#v", notifier.sends)
	}
}

// Parity-exempt: manager.py has no test for a failing queen injection; the
// escalation must stay open so the user can retry the reply.
func TestFailedResumeKeepsEscalationOpen(t *testing.T) {
	store := newTestStore(t, nil)
	tokens := fixedSecretTokens(t, store)
	record := openRecord(t, store, tokens, "c1", "esc_1", "s1")
	node := &fakeNode{err: errors.New("loop gone")}
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{sessions: map[string]LiveSession{"s1": &fakeSession{node: node}}})
	manager.Tokens = tokens
	manager.byToken[record.CorrelationToken] = tokenRef{colonyID: "c1", escalationID: "esc_1"}

	manager.OnInbound(context.Background(), "telegram", "42", "go "+FormatRef(record.CorrelationToken), map[string]any{}, "")

	loaded, _ := store.LoadEscalation("c1", "esc_1")
	if loaded.Status != StatusOpen {
		t.Fatalf("status = %q, want open", loaded.Status)
	}
}

// Parity-exempt: manager.py has no recent-chat test.
func TestRecentChatExpires(t *testing.T) {
	store := newTestStore(t, nil)
	manager := newTestManager(t, store, &fakeNotifier{result: NotifierResult{OK: true}}, &fakeSM{})
	manager.RecordRecentChat("telegram", map[string]any{"chat_id": "9"})
	if got := manager.RecentChat("telegram"); got == nil || got["chat_id"] != "9" {
		t.Fatalf("recent chat = %#v", got)
	}
	manager.Clock.(*fakeClock).Advance(2 * time.Hour)
	if got := manager.RecentChat("telegram"); got != nil {
		t.Fatalf("stale chat should expire: %#v", got)
	}
}
