package server

// Parity: framework/server/session_manager.py

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

type QueenNode interface {
	InjectEvent(text string, isClientInput bool) error
}

type QueenExecutor struct {
	NodeRegistry map[string]QueenNode
}

func (e *QueenExecutor) QueenNode() QueenNode {
	if e == nil {
		return nil
	}
	node, ok := e.NodeRegistry["queen"]
	if !ok {
		return nil
	}
	return node
}

type ColonyRuntime interface {
	StreamID() string
	SubscribeToEvents(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) (string, error)
	InjectInput(workerID, content string) error
}

// Parity: framework/server/session_manager.py::Session (handoff fields)
type Session struct {
	ID         string
	EventBus   *eventbus.EventBus
	LLM        any
	LoadedAt   float64
	QueenTask  any
	Colony     ColonyRuntime
	ColonyID   string
	WorkerPath string
	Binding    any
	PhaseState any
	// QueenToolRegistry mirrors Session._queen_tool_registry upstream; the
	// live-tools diagnostic reads an admission-gate snapshot off it when the
	// concrete value implements the snapshotter interface.
	QueenToolRegistry any

	EntryNode string

	// Chat is the per-session queen runtime (agent loop + conversation +
	// model). Nil when the session was created without a usable LLM, in which
	// case POST /chat reports 503 instead of pretending to deliver.
	Chat *ChatRuntime

	// QueenLoopFresh reports that AddSession wrote this segment's session-start
	// block, i.e. the queen loop has never run here. A fresh create starts the
	// opening iteration through the chat runtime; a resumed segment does not.
	QueenLoopFresh bool

	// LiveSkills is the session's reloadable skills catalog — the queen's
	// SkillsManager, whose prompt the skills reminder announces each turn. The
	// /api/skills reload fan-out rebuilds it so a skill that landed after this
	// session started reaches the queen's next turn instead of the catalog the
	// session loaded at creation. Nil when the assembly layer attached none.
	LiveSkills skills.ReloadableSkillsManager

	ColonySpawned   bool
	SpawnedColonyID string

	// SupersededBy is the successor session id once this session has been
	// forked away from; /chat locks it. ForkKickoffPending marks a freshly
	// forked session whose synthetic kickoff turn must not fork again — the
	// first genuine user message clears it. Mirrors the Session dataclass
	// fields in framework/server/session_manager.py.
	SupersededBy       string
	ForkKickoffPending bool

	QueenResumeFrom string
	QueenDir        string
	QueenName       string

	QueenExecutor *QueenExecutor

	// WorkerHandoffSub is the live worker-escalation subscription id (the
	// session's escalation inbox). WorkerHandoffRuntime is the runtime whose
	// bus carries it: the colony prefers a colony-scoped subscription, so the
	// id must be unsubscribed there, not on the session bus.
	//
	// Parity: framework/server/session_manager.py::Session.worker_handoff_sub
	WorkerHandoffSub     string
	WorkerHandoffRuntime ColonyRuntime

	PendingEscalations map[string]map[string]any
	// pendingOrder is the arrival order of PendingEscalations keys. Python
	// dicts are insertion-ordered and list_worker_questions returns the inbox
	// in that order; a Go map has none, so the order is kept explicitly.
	pendingOrder []string

	pivotMu            sync.Mutex
	pendingColonyPivot any

	MemoryReflectionSubs []string

	// TitleTrigger names the session's auto-title subscription (the worker
	// model writes the title after the first finished turn). It is released
	// when the session stops, matching upstream's use of the reflection-sub
	// list for the same trigger.
	TitleTrigger *TitleTrigger

	SSEClientCount int

	AvailableTriggerDefs  map[string]triggers.TriggerDefinition
	AvailableTriggerOrder []string
	ActiveTriggers        map[string]struct{}
	NextFireTimes         map[string]float64
	FireStats             map[string]triggers.TriggerFireStat
	TriggerClock          SessionTriggerClock

	ActiveTimerTasks  map[string]*TriggerTimer
	ActiveWebhookSubs map[string]string
	WebhookServer     TriggerWebhookServer

	pendingMu sync.Mutex
	triggerMu sync.Mutex
	// channelMu guards channel, the chat channel this session belongs to
	// (upstream Session.channel): written by the channel that claims the
	// session (queen mail), read per reminder render.
	channelMu sync.RWMutex
	channel   string
	// closedFlag marks a session torn down by StopSession. The reload fan-out
	// snapshots live sessions before reloading them, so a session that closed
	// in between must refuse the reload instead of rebuilding a catalog
	// nothing reads.
	closedFlag atomic.Bool
}

// SetChannel marks the session as a channel's, now and after a restart
// (upstream ChannelHost._claim: session.channel = self.name plus the meta.json
// merge). Trims the name; an empty name clears the claim.
//
// Parity: framework/server/channels.py::ChannelHost._claim
func (s *Session) SetChannel(name string) {
	if s == nil {
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	s.channel = strings.TrimSpace(name)
}

// Channel is the chat channel this session belongs to, "" for the dashboard
// (upstream Session.channel). The chat-app gate reads it at each reminder, so
// a claim made while the session is live reaches the next turn.
func (s *Session) Channel() string {
	if s == nil {
		return ""
	}
	s.channelMu.RLock()
	defer s.channelMu.RUnlock()
	return s.channel
}

type SessionTriggerClock interface {
	NowMonotonic() float64
	NowWall() float64
}

func (s *Session) NowMonotonic() float64 {
	if s.TriggerClock != nil {
		return s.TriggerClock.NowMonotonic()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (s *Session) NowWall() float64 {
	if s.TriggerClock != nil {
		return s.TriggerClock.NowWall()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (s *Session) AvailableTriggers() []triggers.TriggerDefinition {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	out := make([]triggers.TriggerDefinition, 0, len(s.AvailableTriggerOrder))
	for _, id := range s.AvailableTriggerOrder {
		if def, ok := s.AvailableTriggerDefs[id]; ok {
			out = append(out, def)
		}
	}
	return out
}

func (s *Session) ActiveTriggerIDs() map[string]struct{} {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	out := make(map[string]struct{}, len(s.ActiveTriggers))
	for id := range s.ActiveTriggers {
		out[id] = struct{}{}
	}
	return out
}

func (s *Session) TriggerNextFire() map[string]float64 {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	out := make(map[string]float64, len(s.NextFireTimes))
	for id, at := range s.NextFireTimes {
		out[id] = at
	}
	return out
}

func (s *Session) TriggerFireStats() map[string]triggers.TriggerFireStat {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	out := make(map[string]triggers.TriggerFireStat, len(s.FireStats))
	for id, stat := range s.FireStats {
		out[id] = stat
	}
	return out
}

func (s *Session) PutTrigger(def triggers.TriggerDefinition) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.AvailableTriggerDefs == nil {
		s.AvailableTriggerDefs = map[string]triggers.TriggerDefinition{}
	}
	if _, exists := s.AvailableTriggerDefs[def.ID]; !exists {
		s.AvailableTriggerOrder = append(s.AvailableTriggerOrder, def.ID)
	}
	s.AvailableTriggerDefs[def.ID] = def
}

func (s *Session) ActivateTrigger(id string) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.ActiveTriggers == nil {
		s.ActiveTriggers = map[string]struct{}{}
	}
	s.ActiveTriggers[id] = struct{}{}
}

func (s *Session) DeactivateTrigger(id string) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	delete(s.ActiveTriggers, id)
}

func (s *Session) SetTriggerNextFire(id string, monotonic float64) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.NextFireTimes == nil {
		s.NextFireTimes = map[string]float64{}
	}
	s.NextFireTimes[id] = monotonic
}

func (s *Session) RecordTriggerFire(id string, stat triggers.TriggerFireStat) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.FireStats == nil {
		s.FireStats = map[string]triggers.TriggerFireStat{}
	}
	s.FireStats[id] = stat
}

func (s *Session) LookupTrigger(id string) (triggers.TriggerDefinition, bool) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	def, ok := s.AvailableTriggerDefs[id]
	return def, ok
}

func (s *Session) RemoveTrigger(id string) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	delete(s.AvailableTriggerDefs, id)
	for index, existing := range s.AvailableTriggerOrder {
		if existing == id {
			s.AvailableTriggerOrder = append(s.AvailableTriggerOrder[:index], s.AvailableTriggerOrder[index+1:]...)
			break
		}
	}
}

func (s *Session) IsTriggerActive(id string) bool {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	_, ok := s.ActiveTriggers[id]
	return ok
}

func (s *Session) PopTriggerNextFire(id string) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	delete(s.NextFireTimes, id)
}

func (s *Session) BumpTriggerFire(id string, lastFiredAt int64) triggers.TriggerFireStat {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.FireStats == nil {
		s.FireStats = map[string]triggers.TriggerFireStat{}
	}
	stat := s.FireStats[id]
	stat.FireCount++
	stamp := lastFiredAt
	stat.LastFiredAt = &stamp
	s.FireStats[id] = stat
	return stat
}

func (s *Session) SetActiveTimer(id string, timer *TriggerTimer) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.ActiveTimerTasks == nil {
		s.ActiveTimerTasks = map[string]*TriggerTimer{}
	}
	s.ActiveTimerTasks[id] = timer
}

func (s *Session) PopActiveTimer(id string) *TriggerTimer {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	timer, ok := s.ActiveTimerTasks[id]
	if !ok {
		return nil
	}
	delete(s.ActiveTimerTasks, id)
	return timer
}

func (s *Session) SetActiveWebhookSub(id, subID string) {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	if s.ActiveWebhookSubs == nil {
		s.ActiveWebhookSubs = map[string]string{}
	}
	s.ActiveWebhookSubs[id] = subID
}

func (s *Session) PopActiveWebhookSub(id string) string {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	subID, ok := s.ActiveWebhookSubs[id]
	if !ok {
		return ""
	}
	delete(s.ActiveWebhookSubs, id)
	return subID
}

// StopAllTriggers cancels every timer and webhook route the session owns and
// drops its trigger state. Session teardown and colony unload call it so a
// trigger cannot fire into a runtime that is already gone.
func (s *Session) StopAllTriggers() {
	if s == nil {
		return
	}
	s.triggerMu.Lock()
	timers := s.ActiveTimerTasks
	s.ActiveTimerTasks = nil
	subs := s.ActiveWebhookSubs
	s.ActiveWebhookSubs = nil
	server := s.WebhookServer
	s.WebhookServer = nil
	s.ActiveTriggers = nil
	s.NextFireTimes = nil
	s.triggerMu.Unlock()

	for _, timer := range timers {
		timer.Stop()
	}
	if s.EventBus != nil {
		for _, subID := range subs {
			s.EventBus.Unsubscribe(subID)
		}
	}
	if stopper, ok := server.(interface{ Stop() error }); ok {
		_ = stopper.Stop()
	}
}

func (s *Session) PendingCount() int {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return len(s.PendingEscalations)
}

func (s *Session) RecordPendingEscalation(requestID string, entry map[string]any) {
	s.pendingMu.Lock()
	if s.PendingEscalations == nil {
		s.PendingEscalations = map[string]map[string]any{}
	}
	if _, exists := s.PendingEscalations[requestID]; !exists {
		s.pendingOrder = append(s.pendingOrder, requestID)
	}
	s.PendingEscalations[requestID] = entry
	s.pendingMu.Unlock()
}

// Parity: framework/server/queen_orchestrator.py::_make_pivot_payload_sink (sets
// session.pending_colony_pivot, which this accessor surface reads and clears;
// framework/server/routes_sessions.py::handle_dismiss_colony_pivot clears it too)
func (s *Session) PendingColonyPivot() any {
	s.pivotMu.Lock()
	defer s.pivotMu.Unlock()
	return s.pendingColonyPivot
}

func (s *Session) HasPendingColonyPivot() bool {
	s.pivotMu.Lock()
	defer s.pivotMu.Unlock()
	return s.pendingColonyPivot != nil
}

func (s *Session) SetPendingColonyPivot(payload any) {
	s.pivotMu.Lock()
	defer s.pivotMu.Unlock()
	s.pendingColonyPivot = payload
}

func (s *Session) ClearPendingColonyPivot() {
	s.pivotMu.Lock()
	defer s.pivotMu.Unlock()
	s.pendingColonyPivot = nil
}

func NewSession(id string, bus *eventbus.EventBus) *Session {
	return &Session{
		ID:                 id,
		EventBus:           bus,
		PendingEscalations: map[string]map[string]any{},
	}
}

// SessionLiveListener is called when a session registers as live (upstream
// SessionManager.add_session_listener). Channel subscriptions use it to
// re-attach after a stopped session is resumed.
//
// Parity: framework/server/session_manager.py::SessionManager.add_session_listener
type SessionLiveListener func(*Session)

// Parity: framework/server/session_manager.py::SessionManager (stop_session)
type SessionManager struct {
	mu           sync.Mutex
	sessions     map[string]*Session
	order        []string
	loading      map[string]struct{}
	listeners    map[uint64]SessionLiveListener
	nextListener uint64
	// queenMail is the process's queen-mail service, the same instance the
	// /api/queen-mail routes use. Mirrors upstream's SessionManager.queen_mail:
	// the chat tools and the deliverer must share one service or daily limits,
	// tokens and return addresses split in two.
	queenMailMu sync.Mutex
	queenMail   *QueenMailAdapter
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: map[string]*Session{},
		loading:  map[string]struct{}{},
	}
}

// AddSessionListener registers fn for each session added afterwards. The
// returned function removes it. Listeners run without the manager lock and
// should return quickly.
//
// Parity: framework/server/session_manager.py::SessionManager.add_session_listener
func (m *SessionManager) AddSessionListener(fn SessionLiveListener) func() {
	if m == nil || fn == nil {
		return func() {}
	}
	m.mu.Lock()
	if m.listeners == nil {
		m.listeners = map[uint64]SessionLiveListener{}
	}
	m.nextListener++
	id := m.nextListener
	m.listeners[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

// SetQueenMail records the server's queen-mail service on the manager.
func (m *SessionManager) SetQueenMail(mail *QueenMailAdapter) {
	if m == nil {
		return
	}
	m.queenMailMu.Lock()
	m.queenMail = mail
	m.queenMailMu.Unlock()
}

// QueenMail is the server's queen-mail service, or nil when this process has
// none (the routes then answer unauthorized, exactly as upstream without
// queen_mail).
func (m *SessionManager) QueenMail() *QueenMailAdapter {
	if m == nil {
		return nil
	}
	m.queenMailMu.Lock()
	defer m.queenMailMu.Unlock()
	return m.queenMail
}

// Parity: framework/server/session_manager.py::SessionManager.is_loading
func (m *SessionManager) IsLoading(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.loading[sessionID]
	return ok
}

func (m *SessionManager) MarkLoading(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loading == nil {
		m.loading = map[string]struct{}{}
	}
	m.loading[sessionID] = struct{}{}
}

func (m *SessionManager) ClearLoading(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.loading, sessionID)
}

func (m *SessionManager) AddSession(session *Session) {
	m.mu.Lock()
	if _, exists := m.sessions[session.ID]; !exists {
		m.order = append(m.order, session.ID)
	}
	m.sessions[session.ID] = session
	m.mu.Unlock()
	// A session created with a colony already attached (the colony fork path)
	// binds its queen before the first turn can call run_worker.
	session.BindChatColony()
	m.notifySessionLive(session)
	// The chat runtime's live tool tier is the phase's tool surface: attach it
	// once per registration so /live_tools and the phase-aware routes read the
	// phase state, as upstream (QueenPhaseState owns the tiers). This wraps the
	// runtime already built for the session — it never constructs a second one.
	session.attachPhaseToolSurface()
	// Arm the loop-level guards once per session: the stall-check subscription
	// and the doom-loop park sink. Both are no-ops without a bus or a home, and
	// startLoopGuards is idempotent for the repeated session lookups below.
	if session.Chat != nil {
		session.Chat.startLoopGuards()
	}
	// A session registering on an empty event segment gets the opening block
	// upstream's queen writes at session start (identity, the session_start
	// reminder, loop start, iteration 0 and the context readout).
	session.QueenLoopFresh = emitSessionStartEvents(session)
}

// notifySessionLive tells listeners a session just went live. The snapshot is
// taken under the lock; callbacks run without it.
func (m *SessionManager) notifySessionLive(session *Session) {
	m.mu.Lock()
	listeners := make([]SessionLiveListener, 0, len(m.listeners))
	for _, listener := range m.listeners {
		listeners = append(listeners, listener)
	}
	m.mu.Unlock()
	for _, listener := range listeners {
		listener(session)
	}
}

// Parity: framework/server/session_manager.py::SessionManager.list_sessions
func (m *SessionManager) ListSessions() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		if session, ok := m.sessions[id]; ok {
			out = append(out, session)
		}
	}
	return out
}

// PhaseSkillsManager exposes the session's reloadable skills catalog to the
// skills reload fan-out (the skills.LiveQueenSession seam). Nil means the
// session has no catalog of its own or is already closed.
func (s *Session) PhaseSkillsManager() skills.ReloadableSkillsManager {
	if s == nil || s.LiveSkills == nil || s.isClosed() {
		return nil
	}
	return sessionLiveSkills{session: s, inner: s.LiveSkills}
}

// markClosed flags the session as torn down. StopSession removes the session
// from the manager first; the flag covers a fan-out that already snapshotted
// it, so a closing session is never reloaded.
func (s *Session) markClosed() {
	if s != nil {
		s.closedFlag.Store(true)
	}
}

func (s *Session) isClosed() bool { return s == nil || s.closedFlag.Load() }

func (m *SessionManager) GetSession(id string) *Session {
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	// load_colony attaches a colony to a session that is already registered;
	// binding on lookup keeps the queen's tool surface and report path in step
	// with the live colony without a separate callback.
	if session != nil {
		session.BindChatColony()
	}
	return session
}

func (m *SessionManager) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// StopColonyRuntime stops a colony host when the concrete runtime exposes
// Stop(). The read-only ColonyRuntime seam used by routes deliberately omits
// lifecycle methods, so both the session teardown and the CLI unload path
// share this single probe instead of duplicating the assertion.
func StopColonyRuntime(runtime ColonyRuntime) {
	if runtime == nil {
		return
	}
	if stopper, ok := runtime.(interface{ Stop() }); ok {
		stopper.Stop()
	}
}

// Parity: framework/server/session_manager.py::SessionManager.stop_session
func (m *SessionManager) StopSession(sessionID string) bool {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
		for index, id := range m.order {
			if id == sessionID {
				m.order = append(m.order[:index], m.order[index+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	// Flag the session before its runtime is torn down: a reload fan-out that
	// snapshotted this session a moment ago must skip it from here on.
	session.markClosed()

	// Stop the session's worker before unwiring its subscriptions so an
	// in-flight turn cannot publish into a session that is being torn down.
	// Triggers go first: a timer that fires mid-teardown would otherwise try
	// to inject into a closed runtime.
	session.StopAllTriggers()
	session.QueenExecutor = nil
	if session.Chat != nil {
		chat := session.Chat
		if err := chat.Close(); err != nil {
			log.Printf("stop session %s: closing chat runtime: %v", sessionID, err)
		}
		// Guards come down after the runtime is quiescent: clearing the park
		// sink while the turn goroutine is still running would be a data race,
		// and Close has already waited for it to exit.
		chat.stopLoopGuards()
		session.Chat = nil
	}
	// A colony host holds an overseer goroutine and worker runtimes; tearing
	// the session down without stopping it leaves them running until process
	// exit. The concrete runtimes expose Stop(); the read-only seam does not,
	// so probe for it instead of widening ColonyRuntime for every caller.
	session.dropWorkerEscalationRouting()
	if session.Colony != nil {
		StopColonyRuntime(session.Colony)
		session.Colony = nil
	}

	if session.EventBus != nil {
		for _, subID := range session.MemoryReflectionSubs {
			session.EventBus.Unsubscribe(subID)
		}
	}
	session.MemoryReflectionSubs = nil
	if session.TitleTrigger != nil {
		// Upstream drops the title trigger's ids at stop and lets a title
		// generation already in flight finish on its own; Release keeps that
		// non-blocking teardown.
		session.TitleTrigger.Release()
		session.TitleTrigger = nil
	}

	return true
}

type workerPathSession struct{ session *Session }

func (w workerPathSession) WorkerPath() string { return w.session.WorkerPath }

func epochSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}
