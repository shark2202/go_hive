package server

// Parity: framework/server/session_manager.py

import (
	"log"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/triggers"
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

	WorkerHandoffSub string

	PendingEscalations map[string]map[string]any

	pivotMu            sync.Mutex
	pendingColonyPivot any

	MemoryReflectionSubs []string

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

// Parity: framework/server/session_manager.py::SessionManager (stop_session)
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	loading  map[string]struct{}
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: map[string]*Session{},
		loading:  map[string]struct{}{},
	}
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

	// Stop the session's worker before unwiring its subscriptions so an
	// in-flight turn cannot publish into a session that is being torn down.
	if session.Chat != nil {
		if err := session.Chat.Close(); err != nil {
			log.Printf("stop session %s: closing chat runtime: %v", sessionID, err)
		}
		session.Chat = nil
	}
	// A colony host holds an overseer goroutine and worker runtimes; tearing
	// the session down without stopping it leaves them running until process
	// exit. The concrete runtimes expose Stop(); the read-only seam does not,
	// so probe for it instead of widening ColonyRuntime for every caller.
	if session.Colony != nil {
		StopColonyRuntime(session.Colony)
		session.Colony = nil
	}

	if session.WorkerHandoffSub != "" {
		if session.EventBus != nil {
			session.EventBus.Unsubscribe(session.WorkerHandoffSub)
		}
		session.WorkerHandoffSub = ""
	}

	if session.EventBus != nil {
		for _, subID := range session.MemoryReflectionSubs {
			session.EventBus.Unsubscribe(subID)
		}
	}
	session.MemoryReflectionSubs = nil

	return true
}

type workerPathSession struct{ session *Session }

func (w workerPathSession) WorkerPath() string { return w.session.WorkerPath }

func epochSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}
