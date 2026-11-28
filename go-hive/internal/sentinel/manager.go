package sentinel

// Sentinel manager — outbound delivery, inbound routing, and resume.
//
// One process-global instance, started by the server and reachable from the
// in-loop EscalationSource. It drains escalation payloads (enqueued
// non-blockingly from the loop), writes the on-disk record and delivers the
// message; runs the inbound listeners and routes a reply back to the parked
// queen; and answers "is a desktop UI attached?" for the escalate gate.
//
// Disk is the source of truth: on Start it rebuilds the in-memory token index
// from open records so a reply after a restart still resolves.
//
// Parity: core/framework/sentinel/manager.py

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// QueenNode is the parked queen's event sink.
type QueenNode interface {
	InjectEvent(ctx context.Context, content string, isClientInput bool) error
}

// LiveSession is the manager's view of a running session.
type LiveSession interface {
	SSEClientCount() int
	QueenNode() QueenNode
}

// SessionManager resolves live sessions and cold-restores them.
type SessionManager interface {
	GetLiveSession(sessionID string) LiveSession
	CreateSession(ctx context.Context, colonyID, queenResumeFrom string) (LiveSession, error)
}

// SessionRestorer recreates a session for a reply that outlived it. The host
// owns the loader, provider and tool catalogue a cold session needs, so it can
// supply a real factory where SessionManager.CreateSession is unsupported.
//
// Parity: framework/sentinel/manager.py::SentinelManager._cold_restore
type SessionRestorer func(ctx context.Context, colonyID, resumeFrom string) (LiveSession, error)

// HiveSessionResolver closes a stale Hive Inbox escalation out of band.
type HiveSessionResolver interface {
	ResolveHiveSession(ctx context.Context, sessionID, resolvedBy string) bool
}

// ManagerOptions configures a Manager. Zero values get production defaults.
type ManagerOptions struct {
	Store           *Store
	Tokens          *TokenManager
	Runtime         *RuntimeIdentity
	Notifier        Notifier
	Sessions        SessionManager
	SessionRestorer SessionRestorer
	Clock           Clock
	HTTP            HTTPDoer
	ListenerFactory func(name string, mgr *Manager) Listener
}

type tokenRef struct {
	colonyID     string
	escalationID string
}

type listenerEntry struct {
	running bool
}

// Manager is Sentinel's process-global orchestrator.
type Manager struct {
	Store    *Store
	Tokens   *TokenManager
	Runtime  *RuntimeIdentity
	Notifier Notifier
	Sessions SessionManager
	Clock    Clock
	HTTP     HTTPDoer
	// ListenerFactory builds an inbound listener by channel name. Nil uses
	// DefaultListenerFactory.
	ListenerFactory func(name string, mgr *Manager) Listener

	refreshMu   sync.Mutex
	mu          sync.Mutex
	queue       []map[string]any
	wake        chan struct{}
	byToken     map[string]tokenRef
	listeners   map[string]*listenerEntry
	recentChats map[string]map[string]any

	lifecycleMu sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	running     bool

	restoreMu       sync.RWMutex
	sessionRestorer SessionRestorer
}

// NewManager returns a manager with production defaults.
func NewManager(opts ManagerOptions) *Manager {
	manager := &Manager{
		Store:           opts.Store,
		Tokens:          opts.Tokens,
		Runtime:         opts.Runtime,
		Notifier:        opts.Notifier,
		Sessions:        opts.Sessions,
		sessionRestorer: opts.SessionRestorer,
		Clock:           opts.Clock,
		HTTP:            opts.HTTP,
		ListenerFactory: opts.ListenerFactory,
		wake:            make(chan struct{}, 1),
		byToken:         map[string]tokenRef{},
		listeners:       map[string]*listenerEntry{},
		recentChats:     map[string]map[string]any{},
	}
	if manager.Store == nil {
		manager.Store = DefaultStore()
	}
	if manager.Tokens == nil {
		manager.Tokens = DefaultTokens()
	}
	if manager.Runtime == nil {
		manager.Runtime = DefaultRuntimeIdentity()
	}
	if manager.Clock == nil {
		manager.Clock = RealClock{}
	}
	if manager.HTTP == nil {
		manager.HTTP = defaultHTTPDoer()
	}
	if manager.Notifier == nil {
		manager.Notifier = NewHTTPNotifier(manager.HTTP)
	}
	return manager
}

var (
	managerMu       sync.RWMutex
	managerInstance *Manager
)

// GetSentinelManager returns the process-global manager, or nil.
func GetSentinelManager() *Manager {
	managerMu.RLock()
	defer managerMu.RUnlock()
	return managerInstance
}

// SetSentinelManager installs (or clears) the process-global manager.
func SetSentinelManager(manager *Manager) {
	managerMu.Lock()
	defer managerMu.Unlock()
	managerInstance = manager
}

func managerEnqueueEscalation(payload map[string]any) bool {
	manager := GetSentinelManager()
	if manager == nil {
		return false
	}
	return manager.EnqueueEscalation(payload)
}

func managerHasAttachedUI(sessionID string) bool {
	manager := GetSentinelManager()
	if manager == nil {
		return false
	}
	return manager.HasAttachedUI(sessionID)
}

// ----- lifecycle ----------------------------------------------------------

// Start rebuilds the token index from disk, starts the delivery drain, and
// starts whichever inbound listeners already have a token.
func (m *Manager) Start(ctx context.Context) error {
	m.lifecycleMu.Lock()
	if m.running {
		m.lifecycleMu.Unlock()
		return nil
	}
	child, cancel := context.WithCancel(ctx)
	m.ctx = child
	m.cancel = cancel
	m.running = true

	open := m.Store.ListAllOpen()
	m.mu.Lock()
	for _, record := range open {
		if record.CorrelationToken != "" {
			m.byToken[record.CorrelationToken] = tokenRef{colonyID: record.ColonyID, escalationID: record.EscalationID}
		}
	}
	m.mu.Unlock()
	sentinelLogf("[sentinel] manager starting (%d open escalations)", len(open))

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.drainEscalations(child)
	}()
	// Release the lifecycle lock before starting listeners: ensureListener
	// reads the lifecycle context under the same mutex.
	m.lifecycleMu.Unlock()
	m.RefreshListeners()
	return nil
}

// Stop cancels the drain and every listener and waits for them to exit.
func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	cancel := m.cancel
	wasRunning := m.running
	m.running = false
	m.cancel = nil
	m.ctx = nil
	m.lifecycleMu.Unlock()
	if !wasRunning {
		return
	}
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	m.listeners = map[string]*listenerEntry{}
	m.mu.Unlock()
}

// RefreshListeners (re)starts inbound listeners for channels that now have a
// token. Idempotent — a listener already running for a channel is left alone.
func (m *Manager) RefreshListeners() {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	provider, ok := m.Notifier.(ChannelTokenProvider)
	if !ok {
		return
	}
	factory := m.ListenerFactory
	if factory == nil {
		factory = DefaultListenerFactory
	}
	m.ensureListener("telegram", provider.TelegramToken() != "", factory)
	m.ensureListener("slack", provider.SlackAppToken() != "", factory)
	// Hive Inbox needs no per-channel token — just a signed-in account.
	m.ensureListener("hive", provider.HiveConnected(), factory)
}

func (m *Manager) ensureListener(name string, hasToken bool, factory func(string, *Manager) Listener) {
	m.mu.Lock()
	entry := m.listeners[name]
	if entry == nil {
		entry = &listenerEntry{}
		m.listeners[name] = entry
	}
	start := hasToken && !entry.running
	if start {
		entry.running = true
	}
	m.mu.Unlock()
	if !start {
		return
	}

	m.lifecycleMu.Lock()
	if m.ctx == nil || !m.running {
		m.lifecycleMu.Unlock()
		m.mu.Lock()
		entry.running = false
		m.mu.Unlock()
		return
	}
	ctx := m.ctx
	// Reserve the goroutine while holding the lifecycle lock so Stop's
	// wg.Wait can never race a late Add.
	m.wg.Add(1)
	m.lifecycleMu.Unlock()

	listener := factory(name, m)
	if listener == nil {
		m.mu.Lock()
		entry.running = false
		m.mu.Unlock()
		m.wg.Done()
		return
	}
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			entry.running = false
			m.mu.Unlock()
		}()
		if err := listener.Run(ctx); err != nil && ctx.Err() == nil {
			sentinelLogf("[sentinel] %s listener stopped: %v", name, err)
		}
	}()
	sentinelLogf("[sentinel] %s listener started", name)
}

// IsListening reports whether an inbound listener for channel is running. When
// true that listener owns the update stream, so detect-chat must read
// RecentChat rather than polling the API itself.
func (m *Manager) IsListening(channel string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.listeners[channel]
	return entry != nil && entry.running
}

// RecordRecentChat stashes the latest inbound chat seen on channel, stamped
// with the injectable clock.
func (m *Manager) RecordRecentChat(channel string, info map[string]any) {
	if info == nil {
		info = map[string]any{}
	}
	stamped := make(map[string]any, len(info)+1)
	for key, value := range info {
		stamped[key] = value
	}
	stamped["at"] = float64(m.Clock.Now().UnixNano()) / 1e9
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recentChats[channel] = stamped
}

// RecentChat returns the most recent inbound chat on channel, or nil when
// there is none within maxAgeSeconds (default one hour) so a stale record
// cannot be mistaken for a fresh "Start".
func (m *Manager) RecentChat(channel string, maxAge ...float64) map[string]any {
	maxAgeSeconds := 3600.0
	if len(maxAge) > 0 {
		maxAgeSeconds = maxAge[0]
	}
	m.mu.Lock()
	record := m.recentChats[channel]
	m.mu.Unlock()
	if record == nil {
		return nil
	}
	at, _ := ToFloat(record["at"])
	if maxAgeSeconds > 0 && float64(m.Clock.Now().UnixNano())/1e9-at > maxAgeSeconds {
		return nil
	}
	return record
}

// ----- in-loop hooks ------------------------------------------------------

// EnqueueEscalation is the non-blocking hand-off from the EscalationSource.
// The queue is unbounded, matching Python's default asyncio.Queue, so the
// result is always acceptance.
func (m *Manager) EnqueueEscalation(payload map[string]any) bool {
	m.mu.Lock()
	m.queue = append(m.queue, payload)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return true
}

// HasAttachedUI reports whether any desktop client is subscribed to session.
func (m *Manager) HasAttachedUI(sessionID string) bool {
	session := m.getLiveSession(sessionID)
	return session != nil && session.SSEClientCount() > 0
}

// OnLocalResume closes any open escalation for a session that was answered
// in-app, on the runtime side AND (cross-channel) the backend Hive Inbox row.
func (m *Manager) OnLocalResume(ctx context.Context, sessionID string) {
	hadHive := false
	for _, record := range m.Store.ListAllOpen() {
		if record.SessionID != sessionID {
			continue
		}
		if record.Channel == ChannelHive {
			hadHive = true
		}
		m.Store.ResolveEscalation(record.ColonyID, record.EscalationID, "local")
		m.mu.Lock()
		delete(m.byToken, record.CorrelationToken)
		m.mu.Unlock()
	}
	if !hadHive {
		return
	}
	if resolver, ok := m.Notifier.(HiveSessionResolver); ok {
		resolver.ResolveHiveSession(ctx, sessionID, "local")
	}
}

func (m *Manager) dequeue() (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return nil, false
	}
	payload := m.queue[0]
	m.queue = m.queue[1:]
	return payload, true
}

func (m *Manager) drainEscalations(ctx context.Context) {
	for {
		payload, ok := m.dequeue()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		m.handleEscalationSafely(ctx, payload)
	}
}

func (m *Manager) handleEscalationSafely(ctx context.Context, payload map[string]any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelLogf("[sentinel] escalation delivery failed: %v", recovered)
		}
	}()
	m.HandleEscalation(ctx, payload)
}

// ----- outbound delivery --------------------------------------------------

// HandleEscalation writes the record, indexes its token, and delivers the
// message through the notifier.
func (m *Manager) HandleEscalation(ctx context.Context, payload map[string]any) {
	channel, _ := payload["channel"].(string)
	target, _ := payload["target"].(map[string]any)
	if channel == "" || len(target) == 0 {
		sentinelLogf("[sentinel] escalation has no channel/target; dropping (colony=%v)", payload["colony_id"])
		return
	}
	colonyID := PyStrOr(payload["colony_id"], "")
	sessionID := PyStrOr(payload["session_id"], "")
	if colonyID == "" || sessionID == "" {
		sentinelLogf("[sentinel] escalation missing colony/session id; dropping")
		return
	}
	thread, _ := payload["thread"].(map[string]any)
	sendUnder := m.ThreadRefFor(channel, thread)

	var text string
	var meta map[string]any
	if channel == ChannelHive {
		text, _ = payload["question_text"].(string)
		if text == "" {
			text = "(the queen stalled with no message)"
		}
		kind := "blocker"
		if value, ok := payload["kind"]; ok && value != nil && PyStr(value) != "" {
			kind = PyStr(value)
		}
		meta = map[string]any{
			"runtime_id":        m.Runtime.ID(),
			"runtime_kind":      m.Runtime.Kind(),
			"session_id":        payload["session_id"],
			"colony_id":         payload["colony_id"],
			"kind":              kind,
			"title":             m.HiveTitle(payload),
			"correlation_token": payload["correlation_token"],
			"deep_link":         nil,
		}
	} else {
		text = m.FormatMessage(payload)
	}

	result := m.Notifier.Send(ctx, channel, target, text, sendUnder, meta)

	anchor := map[string]any{}
	if result.OK {
		anchor = m.ReplyAnchor(channel, result.MessageID)
	}
	record := &EscalationRecord{
		EscalationID:     PyStrOr(payload["escalation_id"], ""),
		ColonyID:         colonyID,
		SessionID:        sessionID,
		CorrelationToken: PyStrOr(payload["correlation_token"], ""),
		ParkReason:       PyStrOr(payload["park_reason"], ""),
		QuestionText:     PyStrOr(payload["question_text"], ""),
		Channel:          channel,
		ThreadRef:        anchor,
		Status:           StatusOpen,
		CreatedAt:        m.Store.nowISO(),
	}
	// One open Sentinel item per session: the latest report supersedes any
	// prior open one (e.g. stale progress), so the inbox is a live status
	// rather than a pile of "open" rows.
	for _, prev := range m.Store.ListOpen(colonyID) {
		if prev.SessionID == record.SessionID && prev.EscalationID != record.EscalationID {
			m.Store.ResolveEscalation(prev.ColonyID, prev.EscalationID, "superseded")
			m.mu.Lock()
			delete(m.byToken, prev.CorrelationToken)
			m.mu.Unlock()
		}
	}
	if err := m.Store.WriteEscalation(record); err != nil {
		sentinelLogf("[sentinel] could not persist escalation %s: %v", record.EscalationID, err)
	}
	m.mu.Lock()
	m.byToken[record.CorrelationToken] = tokenRef{colonyID: record.ColonyID, escalationID: record.EscalationID}
	m.mu.Unlock()

	if !result.OK {
		sentinelLogf("[sentinel] message not delivered (colony=%s): %s", colonyID, result.Error)
		return
	}
	// First escalation for this colony becomes the thread root for grouping.
	if len(thread) == 0 && len(anchor) > 0 {
		_ = m.Store.UpdateNotificationsThread(colonyID, anchor)
	}
}

var boldPattern = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)

// ToMrkdwn converts UI markdown **bold** to Slack/Telegram mrkdwn *bold*.
func ToMrkdwn(text string) string {
	return boldPattern.ReplaceAllString(text, "*$1*")
}

// FormatMessage renders the one-blob message Telegram/Slack receive. The
// (ref:) token is intentionally not shown: replies are matched by thread plus
// the single-open-per-colony invariant.
func (m *Manager) FormatMessage(payload map[string]any) string {
	colony := PyStr(payload["colony_id"])
	detail := "(no detail)"
	if value, ok := payload["question_text"].(string); ok && value != "" {
		detail = ToMrkdwn(value)
	}
	kind, _ := payload["kind"].(string)
	switch kind {
	case "done":
		return fmt.Sprintf("✅ Your colony %q finished.\n\n%s\n\nReply to send it back out, or ignore.", colony, detail)
	case "progress":
		return fmt.Sprintf("🐝 Your colony %q — progress update:\n\n%s\n\nReply to redirect it, or ignore to let it keep going.", colony, detail)
	case "heartbeat":
		return fmt.Sprintf("🐝 Your colony %q is still working.\n\nIt's been running on its own for a while — latest checkpoint:\n\n%s\n\nReply to redirect it, or ignore to let it keep going.", colony, detail)
	default:
		return fmt.Sprintf("🐝 Your colony %q needs you.\n\n%s\n\nReply to this message to answer and resume the colony.", colony, detail)
	}
}

// HiveTitle is the short headline for the Hive Inbox row (body carries the
// detail).
func (m *Manager) HiveTitle(payload map[string]any) string {
	colony := PyStr(payload["colony_id"])
	kind, _ := payload["kind"].(string)
	switch kind {
	case "done":
		return fmt.Sprintf("Colony %q finished", colony)
	case "progress":
		return fmt.Sprintf("Colony %q — progress", colony)
	case "heartbeat":
		return fmt.Sprintf("Colony %q is still working", colony)
	default:
		return fmt.Sprintf("Colony %q needs you", colony)
	}
}

// ThreadRefFor is how to post under the colony's existing thread root, or nil.
func (m *Manager) ThreadRefFor(channel string, thread map[string]any) map[string]any {
	if channel == ChannelTelegram {
		if value, ok := thread["message_id"]; ok && PyTruthy(value) {
			return map[string]any{"message_id": value}
		}
	}
	if channel == ChannelSlack {
		if value, ok := thread["ts"]; ok && PyTruthy(value) {
			return map[string]any{"ts": value}
		}
	}
	return nil
}

// ReplyAnchor is the anchor stored so a reply threads/matches back.
func (m *Manager) ReplyAnchor(channel, messageID string) map[string]any {
	if messageID == "" {
		return map[string]any{}
	}
	switch channel {
	case ChannelTelegram:
		if number, err := strconv.ParseInt(messageID, 10, 64); err == nil {
			return map[string]any{"message_id": number}
		}
		return map[string]any{"message_id": messageID}
	case ChannelHive:
		// Hive replies resolve by correlation token, not a thread anchor.
		return map[string]any{}
	default:
		return map[string]any{"ts": messageID}
	}
}

// ----- inbound routing + resume -------------------------------------------

// OnInbound routes one inbound reply to its escalation and resumes the queen.
func (m *Manager) OnInbound(ctx context.Context, channel, senderID, text string, threadRef map[string]any, source string) {
	record := m.ResolveRecord(channel, text, threadRef)
	if record == nil || record.Status != StatusOpen {
		return
	}
	cfg := m.Store.LoadNotificationsConfig(record.ColonyID)
	if len(cfg.Allowlist) > 0 {
		authorized := false
		for _, allowed := range cfg.Allowlist {
			if allowed == senderID {
				authorized = true
				break
			}
		}
		if !authorized {
			sentinelLogf("[sentinel] reply from unauthorized sender %s (colony=%s)", senderID, record.ColonyID)
			return
		}
	}
	reply := StripRef(text)
	if m.Resume(ctx, record, reply) {
		m.Store.ResolveEscalation(record.ColonyID, record.EscalationID, senderID)
		m.mu.Lock()
		delete(m.byToken, record.CorrelationToken)
		m.mu.Unlock()
		sentinelLogf("[sentinel] resumed colony=%s from reply", record.ColonyID)
	}
}

// ResolveRecord finds the escalation a reply belongs to: primary the (ref: …)
// token, fallback a unique open escalation on the channel.
func (m *Manager) ResolveRecord(channel, text string, threadRef map[string]any) *EscalationRecord {
	if token, ok := ExtractToken(text); ok {
		m.mu.Lock()
		ref, found := m.byToken[token]
		m.mu.Unlock()
		if found {
			if record, ok := m.Store.LoadEscalation(ref.colonyID, ref.escalationID); ok {
				if m.Tokens.VerifyToken(token, record.EscalationID) {
					return record
				}
			}
		}
	}
	candidates := make([]*EscalationRecord, 0)
	for _, record := range m.Store.ListAllOpen() {
		if record.Channel == channel {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	if threadRef != nil {
		matched := make([]*EscalationRecord, 0)
		for _, record := range candidates {
			if ThreadMatches(record, threadRef) {
				matched = append(matched, record)
			}
		}
		if len(matched) == 1 {
			return matched[0]
		}
	}
	return nil
}

// ThreadMatches reports whether a thread reference points at a record anchor.
func ThreadMatches(record *EscalationRecord, threadRef map[string]any) bool {
	anchor := record.ThreadRef
	if anchor == nil {
		anchor = map[string]any{}
	}
	if want, ok := threadRef["message_id"]; ok {
		if have, ok := anchor["message_id"]; ok {
			return PyStr(want) == PyStr(have)
		}
	}
	if want, ok := threadRef["ts"]; ok {
		if have, ok := anchor["ts"]; ok {
			return PyStr(want) == PyStr(have)
		}
	}
	return false
}

// Resume injects the reply into the live queen, cold-restoring if needed.
func (m *Manager) Resume(ctx context.Context, record *EscalationRecord, reply string) bool {
	if m.Sessions == nil {
		sentinelLogf("[sentinel] could not reach session %s to resume (no session manager)", record.SessionID)
		return false
	}
	session := m.getLiveSession(record.SessionID)
	if session == nil {
		session = m.ColdRestore(ctx, record)
	}
	if session == nil {
		sentinelLogf("[sentinel] could not reach session %s to resume", record.SessionID)
		return false
	}
	node := session.QueenNode()
	if node == nil {
		sentinelLogf("[sentinel] no queen node for session %s", record.SessionID)
		return false
	}
	if err := node.InjectEvent(ctx, reply, true); err != nil {
		sentinelLogf("[sentinel] inject_event failed for session %s: %v", record.SessionID, err)
		return false
	}
	return true
}

// SetSessionRestorer installs the host's cold-restore factory. Wiring it is
// what lets a reply resume a session that is no longer in the live table;
// without it ColdRestore falls back to SessionManager.CreateSession.
func (m *Manager) SetSessionRestorer(restore SessionRestorer) {
	if m == nil {
		return
	}
	m.restoreMu.Lock()
	m.sessionRestorer = restore
	m.restoreMu.Unlock()
}

func (m *Manager) currentSessionRestorer() SessionRestorer {
	m.restoreMu.RLock()
	defer m.restoreMu.RUnlock()
	return m.sessionRestorer
}

// ColdRestore recreates a session from a previous one.
func (m *Manager) ColdRestore(ctx context.Context, record *EscalationRecord) LiveSession {
	if record == nil {
		return nil
	}
	if restore := m.currentSessionRestorer(); restore != nil {
		session, err := restore(ctx, record.ColonyID, record.SessionID)
		if err != nil {
			sentinelLogf("[sentinel] cold-restore failed for %s: %v", record.SessionID, err)
			return nil
		}
		return session
	}
	if m.Sessions == nil {
		return nil
	}
	session, err := m.Sessions.CreateSession(ctx, record.ColonyID, record.SessionID)
	if err != nil {
		sentinelLogf("[sentinel] cold-restore failed for %s: %v", record.SessionID, err)
		return nil
	}
	return session
}

func (m *Manager) getLiveSession(sessionID string) LiveSession {
	if m.Sessions == nil {
		return nil
	}
	return m.Sessions.GetLiveSession(sessionID)
}

// ----- default listener wiring -------------------------------------------

// DefaultListenerFactory builds the production inbound listeners.
func DefaultListenerFactory(name string, manager *Manager) Listener {
	provider, _ := manager.Notifier.(ChannelTokenProvider)
	switch name {
	case "telegram":
		if provider == nil {
			return nil
		}
		// 30s server-side long-poll + client margin.
		return NewTelegramListener(manager, &http.Client{Timeout: 45 * time.Second}, manager.Clock, provider.TelegramToken, "")
	case "slack":
		if provider == nil {
			return nil
		}
		return NewSlackSocketListener(manager, &http.Client{Timeout: 30 * time.Second}, manager.Clock, provider.SlackAppToken, "", NewSocketDialer())
	case "hive":
		hiveProvider, ok := manager.Notifier.(interface {
			HiveBackendBase() string
			HiveJWT() string
		})
		if !ok {
			return nil
		}
		// The SSE stream is idle between replies; it must not carry an overall
		// client timeout.
		return NewHiveInboxListener(manager, &http.Client{}, manager.Clock, func() HiveStreamConfig {
			return HiveStreamConfig{
				BaseURL:     hiveProvider.HiveBackendBase(),
				JWT:         hiveProvider.HiveJWT(),
				RuntimeID:   manager.Runtime.ID(),
				RuntimeKind: manager.Runtime.Kind(),
			}
		})
	default:
		return nil
	}
}
