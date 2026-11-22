package agentloop

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

// envInt resolves an integer env override with the upstream helper's exact
// accept/settle rule (Python int() semantics, ValueError -> default).
func envInt(name string, def int) int { return pyenv.Int(name, def) }

var (
	ColonyParallelCooldownTurns = envInt("HIVE_COLONY_PARALLEL_COOLDOWN", 4)
	ColonyParallelMaxPerSession = envInt("HIVE_COLONY_PARALLEL_MAX", 4)
)

const (
	workToolPrefix = "browser_"
	fanoutTool     = "run_playbook"
)

var workToolNames = map[string]bool{"web_scrape": true}

var trackerWriteTools = map[string]bool{"tracker_upsert": true, "tracker_sql": true}

var colonyPhaseMarkerTools = map[string]bool{"run_playbook": true, "run_worker": true}

const colonyParallelNudgeBody = "You've completed a pilot unit and recorded it in the tracker. " +
	"Think if the protocol works. It it works, don't keep doing rows inline. " +
	"Factor it into a skill (write_skill), " +
	"write a playbook, and call run_playbook to converge the " +
	"rest of the table"

type ColonyParallelNudgeSource struct {
	cooldownTurns int
	maxPerSession int

	turnsSinceLastReminder int
	remindersSent          int
	didPilotWork           bool
	fannedOut              bool
	wroteTrackerThisTurn   bool
}

func NewColonyParallelNudgeSource() *ColonyParallelNudgeSource {
	return NewColonyParallelNudgeSourceWithPolicy(ColonyParallelCooldownTurns, ColonyParallelMaxPerSession)
}

func NewColonyParallelNudgeSourceWithPolicy(cooldownTurns, maxPerSession int) *ColonyParallelNudgeSource {
	return &ColonyParallelNudgeSource{
		cooldownTurns:          cooldownTurns,
		maxPerSession:          maxPerSession,
		turnsSinceLastReminder: 1_000_000,
	}
}

func (s *ColonyParallelNudgeSource) Name() string { return "colony_parallel" }

func (s *ColonyParallelNudgeSource) Points() []ReminderPoint {
	return []ReminderPoint{POST_TOOL_USE}
}

func (s *ColonyParallelNudgeSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	return agentCtx != nil && agentCtx.IsQueenStream
}

func (s *ColonyParallelNudgeSource) ObserveTurn(toolNames []string) {
	s.turnsSinceLastReminder++
	s.wroteTrackerThisTurn = false
	for _, name := range toolNames {
		if isWorkTool(name) {
			s.didPilotWork = true
		}
		if name == fanoutTool {
			s.fannedOut = true
		}
		if trackerWriteTools[name] {
			s.wroteTrackerThisTurn = true
		}
	}
}

func isWorkTool(name string) bool {
	return strings.HasPrefix(name, workToolPrefix) || workToolNames[name]
}

func (s *ColonyParallelNudgeSource) Render(rctx ReminderContext) (string, bool) {
	if !s.didPilotWork || !s.wroteTrackerThisTurn {
		return "", false
	}
	if s.fannedOut {
		return "", false
	}
	if s.remindersSent >= s.maxPerSession {
		return "", false
	}
	if s.turnsSinceLastReminder < s.cooldownTurns {
		return "", false
	}
	if !isColonyPhase(rctx.AgentCtx) {
		return "", false
	}
	s.turnsSinceLastReminder = 0
	s.remindersSent++
	return colonyParallelNudgeBody, true
}

func isColonyPhase(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	tools := agentCtx.AvailableTools
	if agentCtx.DynamicToolsProvider != nil {
		func() {
			defer func() { _ = recover() }()
			if live := agentCtx.DynamicToolsProvider(); live != nil {
				tools = live
			}
		}()
	}
	for _, tool := range tools {
		if colonyPhaseMarkerTools[tool.Name] {
			return true
		}
	}
	return false
}
