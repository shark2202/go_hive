package sentinel

// Sentinel's decision engine — a temporal agentloop reminder source.
//
// For a colony queen whose colony has opted in, this owns the parked-queen
// decision the plain idle-nudge cannot make: when the goal is not done and the
// queen has stalled, choose between
//
//   - nudge — return a Reminder, which breaks the pending-input wait and
//     pushes the queen to keep working autonomously;
//   - escalate — message the human via the manager and return nothing so the
//     queen stays parked until they reply;
//   - nothing — goal complete, a sacred stop, or a human is watching.
//
// A returned reminder wakes the loop; doing I/O and returning nothing keeps it
// parked. When autopilot is enabled for a colony queen this source owns its
// park decisions; the idle-nudge source self-skips that case so the two never
// double-nudge.
//
// Parity: core/framework/sentinel/escalation_source.py

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Report kinds — every Sentinel evaluation produces one:
//
//	blocker   → a genuine block (broken park, or classifier says needs_human).
//	heartbeat → auto-continued for max_nudges cycles; a louder checkpoint.
//	done      → the judge says the goal is COMPLETE; a completion report.
//	progress  → the judge says "keep going": an FYI plus an internal nudge.
const (
	EscalateBlocker   = "blocker"
	EscalateHeartbeat = "heartbeat"
	ReportDone        = "done"
	ReportProgress    = "progress"
)

// skipReasons are parks Sentinel never acts on: explicit user pause, a
// colony-fork suggestion (cannot be answered by a text reply), or a worker
// waiting on its queen.
var skipReasons = map[agentloop.ParkReason]bool{
	agentloop.ParkReasonUserStopped:      true,
	agentloop.ParkReasonColonySuggestion: true,
	agentloop.ParkReasonAwaitingQueen:    true,
}

// EscalationSourceOptions configures an EscalationSource. All callbacks are
// injectable so the decision engine is testable without an LLM, a manager or a
// network.
type EscalationSourceOptions struct {
	Store               *Store
	Tokens              *TokenManager
	ParkContextProvider func() (ParkContext, error)
	OnEscalate          func(payload map[string]any) bool
	HasAttachedUI       func(sessionID string) bool
	ClassifyFn          func(ctx context.Context, park ParkContext, completer llm.Completer) ClassifierVerdict
	Now                 func() time.Time
	LLMProvider         func(agentCtx *agentloop.ReminderAgentContext) llm.Completer
}

// EscalationSource implements agentloop.ReminderSource,
// agentloop.RichReminderSource and agentloop.TemporalSource.
type EscalationSource struct {
	Store               *Store
	Tokens              *TokenManager
	ParkContextProvider func() (ParkContext, error)
	OnEscalate          func(payload map[string]any) bool
	HasAttachedUI       func(sessionID string) bool
	ClassifyFn          func(ctx context.Context, park ParkContext, completer llm.Completer) ClassifierVerdict
	Now                 func() time.Time
	LLMProvider         func(agentCtx *agentloop.ReminderAgentContext) llm.Completer

	mu            sync.Mutex
	held          bool
	nudgeCount    int
	lastEvalAt    time.Time
	lastReportSig string
	gateLogged    bool
}

// NewEscalationSource returns a source with defaults filled in.
func NewEscalationSource(opts EscalationSourceOptions) *EscalationSource {
	source := &EscalationSource{
		Store:               opts.Store,
		Tokens:              opts.Tokens,
		ParkContextProvider: opts.ParkContextProvider,
		OnEscalate:          opts.OnEscalate,
		HasAttachedUI:       opts.HasAttachedUI,
		ClassifyFn:          opts.ClassifyFn,
		Now:                 opts.Now,
		LLMProvider:         opts.LLMProvider,
	}
	if source.Store == nil {
		source.Store = DefaultStore()
	}
	if source.Tokens == nil {
		source.Tokens = DefaultTokens()
	}
	if source.Now == nil {
		source.Now = time.Now
	}
	if source.ClassifyFn == nil {
		source.ClassifyFn = ClassifyPark
	}
	return source
}

// Name is the reminder source name.
func (s *EscalationSource) Name() string { return "sentinel_escalation" }

// AppliesTo is queen/overseer only (workers have IsQueenStream false), and
// only once a colony is bound — a DM queen has no per-colony config and so is
// off by default.
func (s *EscalationSource) AppliesTo(agentCtx *agentloop.ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	isQueen := agentCtx.IsQueenStream
	colonyID := agentCtx.ColonyID
	s.mu.Lock()
	shouldLog := !s.gateLogged
	if shouldLog {
		s.gateLogged = true
	}
	s.mu.Unlock()
	if shouldLog {
		optedIn := false
		if colonyID != "" {
			optedIn = s.store().LoadNotificationsConfig(colonyID).SentinelEnabled
		}
		action := "SKIPS"
		if isQueen && colonyID != "" {
			action = "OWNS"
		}
		sentinelLogf("[sentinel] applies_to: is_queen_stream=%t colony_id=%q opted_in=%t (stream_id=%q) — sentinel %s this stream",
			isQueen, colonyID, optedIn, agentCtx.SessionID, action)
	}
	if !isQueen {
		return false
	}
	return colonyID != ""
}

// Points reports the reminder point this source serves.
func (s *EscalationSource) Points() []agentloop.ReminderPoint {
	return []agentloop.ReminderPoint{agentloop.IDLE_TICK}
}

// TickInterval is coarse on purpose: the budget is enforced in Render against
// idle seconds, and a ~60s cap keeps the shared hub ticker from tightening for
// every colony queen.
func (s *EscalationSource) TickInterval() (float64, bool) {
	budget := s.store().ClassifyAfterSeconds() / 2
	if budget < 5.0 {
		budget = 5.0
	}
	if budget > 60.0 {
		budget = 60.0
	}
	return budget, true
}

// Reset re-arms the source on a real resume.
func (s *EscalationSource) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = false
	s.nudgeCount = 0
	s.lastEvalAt = time.Time{}
	s.lastReportSig = ""
}

func (s *EscalationSource) store() *Store {
	if s.Store == nil {
		return DefaultStore()
	}
	return s.Store
}

func (s *EscalationSource) tokens() *TokenManager {
	if s.Tokens == nil {
		return DefaultTokens()
	}
	return s.Tokens
}

func (s *EscalationSource) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// Render is the plain ReminderSource entry point.
func (s *EscalationSource) Render(rctx agentloop.ReminderContext) (string, bool) {
	reminder, ok := s.RenderReminder(rctx)
	if !ok || reminder == nil {
		return "", false
	}
	return reminder.Body, true
}

// RenderReminder implements agentloop.RichReminderSource.
func (s *EscalationSource) RenderReminder(rctx agentloop.ReminderContext) (*agentloop.Reminder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	signals := rctx.Signals
	if signals == nil || !signals.AwaitingInput || signals.UserStopped {
		return nil, false
	}
	reason := agentloop.ParkReasonUnknown
	if signals.ParkReason != nil {
		reason = *signals.ParkReason
	}
	if skipReasons[reason] {
		return nil, false
	}

	agentCtx := rctx.AgentCtx
	if agentCtx == nil {
		return nil, false
	}
	colonyID := agentCtx.ColonyID
	sessionID := agentCtx.SessionID
	if colonyID == "" || sessionID == "" {
		return nil, false
	}

	cfg := s.store().LoadNotificationsConfig(colonyID)
	if !cfg.SentinelEnabled {
		sentinelLogf("[sentinel] no-escalate (colony=%s): not opted in (sentinel_enabled=false, channel=%s)", colonyID, cfg.Channel)
		return nil, false
	}

	budget := s.store().ClassifyAfterSeconds()
	if cfg.ClassifyAfterSeconds != nil {
		budget = *cfg.ClassifyAfterSeconds
	}
	if signals.IdleSeconds < budget {
		sentinelLogf("[sentinel] waiting (colony=%s): idle %.0fs < budget %.0fs", colonyID, signals.IdleSeconds, budget)
		return nil, false
	}

	// Self-rate-limit: the ticker can poll several times per window before the
	// loop drains what we parked — evaluate at most once per window.
	now := s.now()
	if !s.lastEvalAt.IsZero() && now.Sub(s.lastEvalAt).Seconds() < budget {
		sentinelLogf("[sentinel] rate-limited (colony=%s): last evaluated %.0fs ago (< %.0fs)",
			colonyID, now.Sub(s.lastEvalAt).Seconds(), budget)
		return nil, false
	}
	s.lastEvalAt = now
	sentinelLogf("[sentinel] evaluating (colony=%s idle=%.0fs reason=%s)", colonyID, signals.IdleSeconds, string(reason))

	if s.held {
		sentinelLogf("[sentinel] holding (colony=%s): report already out", colonyID)
		return nil, false
	}

	park, err := s.provideParkContext()
	if err != nil {
		sentinelLogf("sentinel: park-context provider failed: %v", err)
		return nil, false
	}

	// No goal AND no open tasks is the one "nothing to act on" case.
	if park.GoalText() == "" && len(park.OpenTasks) == 0 {
		sentinelLogf("[sentinel] nothing to report (colony=%s): no goal and no open tasks", colonyID)
		return nil, false
	}

	kind, decided := s.decide(reason, park, s.llmFor(agentCtx))
	if !decided {
		sentinelLogf("[sentinel] no-op (colony=%s): classifier unavailable/errored, not nudging a parked queen", colonyID)
		return nil, false
	}
	s.reportLocked(colonyID, sessionID, reason, park, cfg, kind)

	// Only progress keeps the colony moving — wake the queen.
	if kind == ReportProgress {
		s.nudgeCount++
		sentinelLogf("[sentinel] nudge (colony=%s count=%d idle=%.0fs)", colonyID, s.nudgeCount, signals.IdleSeconds)
		return &agentloop.Reminder{
			Body:   s.nudgeBody(park),
			Source: s.Name(),
			Meta: map[string]any{
				"substate":    "sentinel_nudge",
				"nudge_count": s.nudgeCount,
			},
		}, true
	}
	return nil, false
}

func (s *EscalationSource) provideParkContext() (ParkContext, error) {
	if s.ParkContextProvider == nil {
		return ParkContext{}, nil
	}
	return s.ParkContextProvider()
}

func (s *EscalationSource) llmFor(agentCtx *agentloop.ReminderAgentContext) llm.Completer {
	if s.LLMProvider == nil {
		return nil
	}
	return s.LLMProvider(agentCtx)
}

// decide picks this evaluation's report kind. ok=false means "cannot decide
// this cycle" — do not manufacture a nudge or an escalation.
func (s *EscalationSource) decide(reason agentloop.ParkReason, park ParkContext, completer llm.Completer) (string, bool) {
	if reason.IsBroken() || park.HardBlocker {
		return EscalateBlocker, true
	}
	// Auto-continued for max_nudges cycles — stop nudging, surface a louder
	// checkpoint for the human to redirect or ack.
	if s.nudgeCount >= s.store().MaxNudgesBeforeEscalate() {
		return EscalateHeartbeat, true
	}
	// Loop-breaker: a clean turn-end with no open tasks that we have ALREADY
	// nudged once is not producing new work. One classifier-driven nudge is
	// still allowed first, to catch a queen that stalled before recording
	// tasks.
	if reason == agentloop.ParkReasonTurnDone && len(park.OpenTasks) == 0 && s.nudgeCount >= 1 {
		return EscalateHeartbeat, true
	}
	if s.ClassifyFn == nil {
		return "", false
	}
	verdict := s.ClassifyFn(context.Background(), park, completer)
	if verdict.Errored {
		return "", false
	}
	if verdict.NeedsHuman() {
		return EscalateBlocker, true
	}
	if verdict.IsDone() {
		return ReportDone, true
	}
	return ReportProgress, true
}

func (s *EscalationSource) uiAttached(sessionID string) (attached bool) {
	fn := s.HasAttachedUI
	if fn == nil {
		fn = managerHasAttachedUI
	}
	if fn == nil {
		return false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelLogf("sentinel: has_attached_ui failed: %v", recovered)
			attached = false
		}
	}()
	return fn(sessionID)
}

// reportLocked tells the human. Progress is Inbox-feed-only; remote channels
// are suppressed while the user is at the desktop; identical consecutive
// reports collapse. Anything but progress holds the source until resume.
// Callers hold s.mu.
func (s *EscalationSource) reportLocked(colonyID, sessionID string, reason agentloop.ParkReason, park ParkContext, cfg NotificationsConfig, kind string) {
	if kind == ReportProgress && cfg.Channel != ChannelHive {
		return
	}
	if cfg.Channel != ChannelHive && !s.store().EscalateWhenUIAttached() && s.uiAttached(sessionID) {
		sentinelLogf("[sentinel] report suppressed — UI attached (colony=%s)", colonyID)
		return
	}
	summary := questionText(park)
	signature := kind + "\x00" + summary
	if signature == s.lastReportSig {
		return // collapse identical consecutive reports
	}
	s.lastReportSig = signature

	escalationID := "esc_" + NewUUID4Hex()
	payload := map[string]any{
		"escalation_id":     escalationID,
		"colony_id":         colonyID,
		"session_id":        sessionID,
		"correlation_token": s.tokens().MakeToken(escalationID),
		"park_reason":       string(reason),
		"kind":              kind,
		"question_text":     summary,
		"channel":           cfg.Channel,
		"target":            cfg.Target,
		"thread":            cfg.Thread,
	}
	enqueue := s.OnEscalate
	if enqueue == nil {
		enqueue = managerEnqueueEscalation
	}
	accepted := enqueueSafely(enqueue, payload)
	if !accepted {
		sentinelLogf("[sentinel] report not delivered (colony=%s kind=%s)", colonyID, kind)
		return
	}
	// Progress re-evaluates next cycle; every other kind holds until resume.
	if kind != ReportProgress {
		s.held = true
	}
	sentinelLogf("[sentinel] report %s (colony=%s esc=%s)", kind, colonyID, escalationID)
}

// enqueueSafely isolates a panicking callback (the Go analog of Python's
// try/except around the escalation enqueue).
func enqueueSafely(enqueue func(map[string]any) bool, payload map[string]any) (accepted bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelLogf("sentinel: report enqueue raised: %v", recovered)
			accepted = false
		}
	}()
	return enqueue(payload)
}

func questionText(park ParkContext) string {
	if len(park.PendingQuestions) > 0 {
		lines := make([]string, 0, len(park.PendingQuestions))
		for index, question := range park.PendingQuestions {
			if index == 5 {
				break
			}
			value, ok := question["prompt"]
			if !ok {
				value = question
			}
			lines = append(lines, "- "+PyStr(value))
		}
		return strings.Join(lines, "\n")
	}
	text := strings.TrimSpace(park.LastAssistantText)
	if text == "" {
		return "(the queen stalled with no message)"
	}
	return TruncateRunes(text, 1500)
}

func (s *EscalationSource) nudgeBody(park ParkContext) string {
	goal := ""
	if value := park.GoalText(); value != "" {
		goal = "Your goal: " + value + ". "
	}
	tasks := "open work remains"
	if len(park.OpenTasks) > 0 {
		shown := park.OpenTasks
		if len(shown) > 5 {
			shown = shown[:5]
		}
		tasks = strings.Join(shown, ", ")
	}
	workers := ""
	if len(park.RunningWorkers) > 0 {
		count := len(park.RunningWorkers)
		plural := "s"
		verb := "are"
		if count == 1 {
			plural = ""
			verb = "is"
		}
		workers = fmt.Sprintf(" NOTE: %d worker%s from an earlier fan-out %s still running:\n%s\n", count, plural, verb, FormatRunningWorkers(park.RunningWorkers, 8)) +
			"Their results arrive as WORKER_REPORT messages — wait for those rather " +
			"than re-dispatching the same tasks or summarizing the batch early."
	}
	return goal + "You paused, but the goal is not finished — still to do: " + tasks + ". " +
		"Continue now and keep working toward the goal autonomously. Do NOT pause to " +
		"ask permission or for a progress check-in; only stop via ask_user if you " +
		"genuinely cannot proceed without a human decision." + workers
}
