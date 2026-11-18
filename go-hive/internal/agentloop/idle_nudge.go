package agentloop

// Parity: framework/agent_loop/idle_nudge.py.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

var idleNudgeLogf = log.Printf

// Parity: IdleNudgeSource.__init__ keyword arguments.
type IdleNudgeConfig struct {
	BudgetSeconds         float64
	MaxNudges             int
	AwaitingBudgetSeconds float64
	BrokenBudgetSeconds   float64
}

// Parity: framework/agent_loop/idle_nudge.py IdleNudgeSource.
type IdleNudgeSource struct {
	mu          sync.Mutex
	config      IdleNudgeConfig
	nudges      map[string]int
	lastEvalAt  time.Time
	lastGateSig string
}

// Parity: IdleNudgeSource.__init__.
func NewIdleNudgeSource(cfg IdleNudgeConfig) *IdleNudgeSource {
	return &IdleNudgeSource{config: cfg, nudges: map[string]int{}}
}

// Parity: IdleNudgeSource.name.
func (s *IdleNudgeSource) Name() string { return "idle_nudge" }

// Parity: IdleNudgeSource.points.
func (s *IdleNudgeSource) Points() []ReminderPoint { return []ReminderPoint{IDLE_TICK} }

// Parity: ReminderSource.applies_to default.
func (s *IdleNudgeSource) AppliesTo(*ReminderAgentContext) bool { return true }

// Parity: IdleNudgeSource.reset.
func (s *IdleNudgeSource) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nudges = map[string]int{}
	s.lastEvalAt = time.Time{}
	s.lastGateSig = ""
}

// Parity: IdleNudgeSource._enabled.
func (s *IdleNudgeSource) enabled() bool {
	return s.config.MaxNudges > 0 &&
		(s.config.BudgetSeconds > 0 || s.config.AwaitingBudgetSeconds > 0 || s.config.BrokenBudgetSeconds > 0)
}

// Parity: IdleNudgeSource._cap_for.
func (s *IdleNudgeSource) capFor(substate string, reason *ParkReason) int {
	if substate == "parked_no_question" {
		return 1
	}
	if substate == "parked_open_tasks" && reason != nil && *reason == ParkReasonTurnDone {
		return 1
	}
	return s.config.MaxNudges
}

// Parity: IdleNudgeSource.tick_interval.
func (s *IdleNudgeSource) TickInterval() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled() {
		return 0, false
	}
	smallest := 0.0
	for _, budget := range []float64{s.config.BudgetSeconds, s.config.AwaitingBudgetSeconds, s.config.BrokenBudgetSeconds} {
		if budget > 0 && (smallest == 0 || budget < smallest) {
			smallest = budget
		}
	}
	interval := smallest / 2
	if interval < 0.01 {
		interval = 0.01
	}
	if interval > 5.0 {
		interval = 5.0
	}
	return interval, true
}

// Parity: IdleNudgeSource._gate.
func (s *IdleNudgeSource) gate(signature, msg string, args ...any) {
	if signature == s.lastGateSig {
		return
	}
	s.lastGateSig = signature
	idleNudgeLogf(msg, args...)
}

// Parity: IdleNudgeSource.render (bare-string branch).
func (s *IdleNudgeSource) Render(rctx ReminderContext) (string, bool) {
	reminder, ok := s.RenderReminder(rctx)
	if !ok {
		return "", false
	}
	return reminder.Body, true
}

// Parity: IdleNudgeSource.render.
func (s *IdleNudgeSource) RenderReminder(rctx ReminderContext) (*Reminder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled() {
		return nil, false
	}
	signals := rctx.Signals
	if signals == nil {
		return nil, false
	}
	if sentinelAutopilotActive(rctx.AgentCtx) {
		return nil, false
	}
	if signals.UserStopped {
		if signals.AwaitingInput {
			s.gate("user_stopped",
				"[idle_nudge] gate=user_stopped — explicit Stop, suppressed (idle=%.0fs)",
				signals.IdleSeconds)
		}
		return nil, false
	}

	reason := signals.ParkReason
	var substate string
	var budget float64
	if signals.AwaitingInput {
		if reason == nil {
			unknown := ParkReasonUnknown
			reason = &unknown
		}
		if reason.IsExternalWait() {
			s.gate("external_wait:"+string(*reason),
				"[idle_nudge] gate=external_wait — parked awaiting another party (%s), suppressed (idle=%.0fs)",
				string(*reason), signals.IdleSeconds)
			return nil, false
		}
		if reason.IsSilentPark() {
			s.gate("silent_park:"+string(*reason),
				"[idle_nudge] gate=silent_park — clean stopping point (%s), suppressed (idle=%.0fs)",
				string(*reason), signals.IdleSeconds)
			return nil, false
		}
		if reason.IsBroken() {
			if s.config.BrokenBudgetSeconds <= 0 {
				s.gate("no_broken_budget:"+string(*reason),
					"[idle_nudge] gate=no_broken_budget — broken park (%s) but the broken budget is disabled (idle=%.0fs)",
					string(*reason), signals.IdleSeconds)
				return nil, false
			}
			substate = "parked_broken"
			budget = s.config.BrokenBudgetSeconds
		} else {
			if s.config.AwaitingBudgetSeconds <= 0 {
				s.gate("no_awaiting_budget",
					"[idle_nudge] gate=no_awaiting_budget — questionless park but the awaiting budget is disabled (idle=%.0fs)",
					signals.IdleSeconds)
				return nil, false
			}
			substate = "parked_no_question"
			budget = s.config.AwaitingBudgetSeconds
		}
	} else if !signals.StreamActive {
		substate = "between_turns"
		budget = s.config.BudgetSeconds
	} else if !signals.FirstEventSeen {
		substate = "slow_ttft"
		budget = s.config.BudgetSeconds
	} else {
		return nil, false
	}
	if budget <= 0 || signals.IdleSeconds < budget {
		return nil, false
	}
	now := time.Now()
	if !s.lastEvalAt.IsZero() && now.Sub(s.lastEvalAt) < secondsDuration(budget) {
		return nil, false
	}
	s.lastEvalAt = now

	var openTasks []IdleTask
	if substate == "parked_no_question" {
		openTasks = openTasksFor(rctx.AgentCtx)
		if len(openTasks) > 0 {
			substate = "parked_open_tasks"
		} else if reason != nil && *reason == ParkReasonTurnDone {
			s.gate("turn_done_idle",
				"[idle_nudge] gate=turn_done_idle — turn finished cleanly with no open tasks, nothing to nudge (idle=%.0fs)",
				signals.IdleSeconds)
			return nil, false
		}
	}

	cap := s.capFor(substate, reason)
	count := s.nudges[substate]
	if count >= cap {
		if signals.AwaitingInput {
			s.gate("cap_reached:"+substate,
				"[idle_nudge] gate=cap_reached — substate=%s count=%d/%d, re-arms on the next user message",
				substate, count, cap)
		}
		return nil, false
	}
	count++
	s.nudges[substate] = count
	s.lastGateSig = ""

	goal := ""
	if substate == "parked_open_tasks" {
		goal = sessionGoalFor(rctx.AgentCtx)
	}
	body := idleNudgeBody(substate, signals.IdleSeconds, openTasks, signals.ParkReason, goal)
	idleNudgeLogf("[idle_nudge] producing nudge (substate=%s, count=%d/%d, idle=%.0fs)",
		substate, count, cap, signals.IdleSeconds)
	return &Reminder{
		Body:   body,
		Source: s.Name(),
		Meta: map[string]any{
			"idle_seconds": signals.IdleSeconds,
			"substate":     substate,
			"nudge_count":  count,
			"cap":          cap,
		},
	}, true
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

// Parity: framework/agent_loop/idle_nudge.py _open_tasks.
func openTasksFor(agentCtx *ReminderAgentContext) (tasks []IdleTask) {
	if agentCtx == nil || agentCtx.SessionID == "" || agentCtx.OpenTasksProvider == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			tasks = nil
		}
	}()
	records := agentCtx.OpenTasksProvider()
	open := make([]IdleTask, 0, len(records))
	for _, record := range records {
		if record.Status == "completed" || record.Status == "archived" {
			continue
		}
		open = append(open, record)
	}
	return open
}

// Parity: framework/agent_loop/idle_nudge.py _session_goal.
func sessionGoalFor(agentCtx *ReminderAgentContext) (goal string) {
	if agentCtx == nil || agentCtx.SessionID == "" || agentCtx.SessionGoalProvider == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			goal = ""
		}
	}()
	return agentCtx.SessionGoalProvider()
}

// Parity: framework/agent_loop/idle_nudge.py _sentinel_autopilot_active.
func sentinelAutopilotActive(agentCtx *ReminderAgentContext) (active bool) {
	if agentCtx == nil || !agentCtx.IsQueenStream || agentCtx.ColonyID == "" {
		return false
	}
	if agentCtx.SentinelAutopilotProvider == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			active = false
		}
	}()
	return agentCtx.SentinelAutopilotProvider()
}

// Parity: framework/agent_loop/idle_nudge.py _name_tasks.
func nameTasks(tasks []IdleTask, limit int) string {
	if limit <= 0 {
		limit = 5
	}
	shown := tasks
	if len(shown) > limit {
		shown = shown[:limit]
	}
	parts := make([]string, 0, len(shown))
	for _, task := range shown {
		subject := strings.TrimSpace(task.Subject)
		id := task.ID
		if id == "" {
			id = "?"
		}
		if subject != "" {
			parts = append(parts, fmt.Sprintf("#%s %q", id, subject))
		} else {
			parts = append(parts, "#"+id)
		}
	}
	joined := strings.Join(parts, ", ")
	if rest := len(tasks) - len(shown); rest > 0 {
		return fmt.Sprintf("%s (+%d more)", joined, rest)
	}
	return joined
}

// Parity: framework/agent_loop/idle_nudge.py IdleNudgeSource._body.
func idleNudgeBody(substate string, idleSeconds float64, openTasks []IdleTask, parkReason *ParkReason, goal string) string {
	if substate == "parked_broken" {
		reason := ParkReasonUnknown
		if parkReason != nil {
			reason = *parkReason
		}
		if reason == ParkReasonDoomLoop {
			return fmt.Sprintf(
				"The agent loop parked %.0fs ago after a tool "+
					"doom-loop — the same call was issued repeatedly with "+
					"identical arguments, so the breaker blocked it. Do NOT "+
					"repeat that call; that is what tripped the breaker. Recover "+
					"with a DIFFERENT approach (different arguments, a different "+
					"tool, or a text-only step), or stop and report what you "+
					"already have. Do NOT block on the user unless you genuinely "+
					"need an answer to proceed.", idleSeconds)
		}
		return fmt.Sprintf(
			"The agent loop parked %.0fs ago after a failure "+
				"(%s) — this is not a normal wait for the user. "+
				"Recover now: re-attempt the work that failed, or take the next "+
				"concrete step from the conversation above. Do NOT block on the "+
				"user unless you genuinely need an answer to proceed.", idleSeconds, string(reason))
	}
	if substate == "parked_open_tasks" {
		goalPrefix := ""
		if goal != "" {
			goalPrefix = fmt.Sprintf("Your goal: %s. ", goal)
		}
		return fmt.Sprintf(
			"%sYou have been parked awaiting user input for "+
				"%.0fs with no pending question, and your task "+
				"list still has open work: %s. Resume "+
				"now and finish those open tasks. Only block on the user via "+
				"ask_user if a task genuinely needs their answer.", goalPrefix, idleSeconds, nameTasks(openTasks, 5))
	}
	if substate == "parked_no_question" {
		return fmt.Sprintf(
			"You have been parked awaiting user input for %.0fs, "+
				"but you asked no question — there is nothing for the user to "+
				"answer. Continue now: take the next concrete step on your work. "+
				"Only block on the user via a proper question tool (ask_user) if "+
				"you genuinely need an answer to proceed.", idleSeconds)
	}
	return fmt.Sprintf(
		"The session has been idle for %.0fs (%s) with "+
			"no progress and no pending user input. Pick the next concrete step "+
			"from the conversation above and take it. Do NOT ask the user a "+
			"question unless absolutely necessary — prefer acting on "+
			"already-available information.", idleSeconds, substate)
}
