package agentloop

// Tracker-state snapshot reminder for the queen's chat session.
//
// The queen is the only agent that designs a colony's tracker, so this source
// is queen-only and self-skips while no colony binding exists. It reads the
// bound tracker.db (table name + row count) and nudges the queen to persist
// progress before context pruning drops in-memory state. With no table yet it
// fires the "set one up" suggestion instead — the empty case is the message.
//
// Timing mirrors the upstream source: the first fire waits a few tool turns,
// then the source cools down between reminders (shorter while the tracker is
// empty, longer once a table exists). Using any tracker_* tool resets the
// cooldown, because that agent is already working the tracker.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_snapshot_tables
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_render_queen_body

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::QUEEN_FIRST_FIRE_TURNS
var TrackerQueenFirstFireTurns = envInt("HIVE_TRACKER_QUEEN_FIRST_FIRE", 3)

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::QUEEN_COOLDOWN_NO_TABLE
var TrackerQueenCooldownNoTable = envInt("HIVE_TRACKER_QUEEN_COOLDOWN_NO_TABLE", 10)

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::QUEEN_COOLDOWN_WITH_TABLE
var TrackerQueenCooldownWithTable = envInt("HIVE_TRACKER_QUEEN_COOLDOWN_WITH_TABLE", 20)

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WORKER_CADENCE_TOOL_CALLS
var TrackerWorkerCadenceToolCalls = envInt("HIVE_TRACKER_WORKER_TOOL_CALLS", 20)

const (
	// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_MAX_TABLES
	trackerSnapshotMaxTables = 10
	// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_QUERY_BUDGET_SECONDS
	trackerSnapshotQueryBudget = 50 * time.Millisecond
)

// TrackerSnapshotConfig overrides the source's cadence (tests and hosts that
// want a tighter loop than the env defaults).
type TrackerSnapshotConfig struct {
	FirstFireTurns    int
	CooldownNoTable   int
	CooldownWithTable int
}

func defaultTrackerSnapshotConfig() TrackerSnapshotConfig {
	return TrackerSnapshotConfig{
		FirstFireTurns:    TrackerQueenFirstFireTurns,
		CooldownNoTable:   TrackerQueenCooldownNoTable,
		CooldownWithTable: TrackerQueenCooldownWithTable,
	}
}

// TrackerSnapshotReminderSource is the queen-only, in-colony tracker nudge.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource
type TrackerSnapshotReminderSource struct {
	firstFireTurns    int
	cooldownNoTable   int
	cooldownWithTable int

	toolTurns              int
	turnsSinceLastReminder int
	hasFired               bool
}

// NewTrackerSnapshotReminderSource builds the source with the env defaults.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.__init__
func NewTrackerSnapshotReminderSource() *TrackerSnapshotReminderSource {
	return NewTrackerSnapshotReminderSourceWithConfig(defaultTrackerSnapshotConfig())
}

// NewTrackerSnapshotReminderSourceWithConfig builds the source with explicit
// cadence values. Zero or negative values keep the upstream defaults.
func NewTrackerSnapshotReminderSourceWithConfig(cfg TrackerSnapshotConfig) *TrackerSnapshotReminderSource {
	defaults := defaultTrackerSnapshotConfig()
	if cfg.FirstFireTurns <= 0 {
		cfg.FirstFireTurns = defaults.FirstFireTurns
	}
	if cfg.CooldownNoTable <= 0 {
		cfg.CooldownNoTable = defaults.CooldownNoTable
	}
	if cfg.CooldownWithTable <= 0 {
		cfg.CooldownWithTable = defaults.CooldownWithTable
	}
	return &TrackerSnapshotReminderSource{
		firstFireTurns:    cfg.FirstFireTurns,
		cooldownNoTable:   cfg.CooldownNoTable,
		cooldownWithTable: cfg.CooldownWithTable,
	}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.name
func (s *TrackerSnapshotReminderSource) Name() string { return "tracker_snapshot" }

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.points
func (s *TrackerSnapshotReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{POST_TOOL_USE}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.applies_to
func (s *TrackerSnapshotReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil || !agentCtx.IsQueenStream {
		return false
	}
	// The provider is wired once, but a queen forks into a colony mid-session:
	// presence of a binding is rechecked per render in Render.
	return agentCtx.ColonyBindingProvider != nil
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.observe_turn
func (s *TrackerSnapshotReminderSource) ObserveTurn(toolNames []string) {
	if usedTrackerTool(toolNames) {
		s.turnsSinceLastReminder = 0
		return
	}
	s.turnsSinceLastReminder++
	if len(toolNames) > 0 {
		s.toolTurns++
	}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.render
func (s *TrackerSnapshotReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != POST_TOOL_USE {
		return "", false
	}
	if !s.hasFired {
		if s.toolTurns < s.firstFireTurns {
			return "", false
		}
	} else if s.turnsSinceLastReminder < s.cooldownNoTable {
		return "", false
	}
	binding := resolveTrackerBinding(rctx.AgentCtx)
	if binding == nil || strings.TrimSpace(binding.TrackerDB) == "" {
		return "", false
	}
	rows := snapshotTrackerTables(binding.TrackerDB)
	if s.hasFired {
		cooldown := s.cooldownNoTable
		if len(rows) > 0 {
			cooldown = s.cooldownWithTable
		}
		if s.turnsSinceLastReminder < cooldown {
			return "", false
		}
	}
	s.hasFired = true
	s.turnsSinceLastReminder = 0
	return renderTrackerQueenBody(rows), true
}

// WorkerTrackerSnapshotReminderSource is the worker-only, in-colony tracker
// nudge. Workers browse constantly, so there is no browser gate; instead the
// source fires once every cadence of tool calls (counted independently of the
// shared tool-budget checkpoint). Using a tracker tool resets the counter, and
// an empty tracker produces no reminder — workers fill the queen's tables, they
// do not create them.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource
type WorkerTrackerSnapshotReminderSource struct {
	cadenceToolCalls           int
	toolCallsSinceLastReminder int
}

// NewWorkerTrackerSnapshotReminderSource builds the source with the upstream
// default cadence (HIVE_TRACKER_WORKER_TOOL_CALLS, 20 tool calls).
func NewWorkerTrackerSnapshotReminderSource() *WorkerTrackerSnapshotReminderSource {
	return NewWorkerTrackerSnapshotReminderSourceWithCadence(TrackerWorkerCadenceToolCalls)
}

// NewWorkerTrackerSnapshotReminderSourceWithCadence builds the source with an
// explicit tool-call cadence. Zero or negative keeps the upstream default.
func NewWorkerTrackerSnapshotReminderSourceWithCadence(cadence int) *WorkerTrackerSnapshotReminderSource {
	if cadence <= 0 {
		cadence = TrackerWorkerCadenceToolCalls
	}
	return &WorkerTrackerSnapshotReminderSource{cadenceToolCalls: cadence}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource.name
func (s *WorkerTrackerSnapshotReminderSource) Name() string { return "worker_tracker_snapshot" }

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource.points
func (s *WorkerTrackerSnapshotReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{POST_TOOL_USE}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource.applies_to
func (s *WorkerTrackerSnapshotReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil || agentCtx.IsQueenStream {
		return false
	}
	return agentCtx.ColonyBindingProvider != nil
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource.observe_turn
func (s *WorkerTrackerSnapshotReminderSource) ObserveTurn(toolNames []string) {
	if usedTrackerTool(toolNames) {
		s.toolCallsSinceLastReminder = 0
		return
	}
	s.toolCallsSinceLastReminder += len(toolNames)
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::WorkerTrackerSnapshotReminderSource.render
func (s *WorkerTrackerSnapshotReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != POST_TOOL_USE {
		return "", false
	}
	if s.toolCallsSinceLastReminder < s.cadenceToolCalls {
		return "", false
	}
	binding := resolveTrackerBinding(rctx.AgentCtx)
	if binding == nil || strings.TrimSpace(binding.TrackerDB) == "" {
		return "", false
	}
	rows := snapshotTrackerTables(binding.TrackerDB)
	if len(rows) == 0 {
		// Don't consume the cadence until there is something to show: the
		// worker upserts into the queen's tables, it doesn't create them.
		return "", false
	}
	s.toolCallsSinceLastReminder = 0
	return renderTrackerWorkerBody(rows), true
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_render_worker_body
func renderTrackerWorkerBody(rows []trackerTableCount) string {
	lines := []string{"Tracker state right now:"}
	for _, row := range rows {
		lines = append(lines, "  - "+row.name+": "+formatRowCount(row.count)+" row"+pluralSuffix(row.count))
	}
	lines = append(lines, "")
	lines = append(lines,
		"Before you continue, persist your progress so far via tracker_upsert. "+
			"Older tool results that are not in the tracker can be lost. "+
			"If you get stuck and can't finish the job, it is okay to report early and explain why you failed.")
	return strings.Join(lines, "\n")
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_used_tracker_tool
func usedTrackerTool(toolNames []string) bool {
	for _, name := range toolNames {
		if strings.HasPrefix(name, "tracker_") {
			return true
		}
	}
	return false
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_resolve_binding
func resolveTrackerBinding(agentCtx *ReminderAgentContext) (binding *ColonyBinding) {
	if agentCtx == nil || agentCtx.ColonyBindingProvider == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			binding = nil
		}
	}()
	return agentCtx.ColonyBindingProvider()
}

type trackerTableCount struct {
	name  string
	count int
}

// snapshotTrackerTables returns [(table_name, row_count)] for non-framework
// tables, bounded by table count and wall-clock time. Any sqlite error yields
// no rows: a stale or busy DB must never break a reminder.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_snapshot_tables
func snapshotTrackerTables(dbPath string) []trackerTableCount {
	if strings.TrimSpace(dbPath) == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil
	}
	defer db.Close()
	_ = db.Ping()
	started := time.Now()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE '\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		return nil
	}
	defer rows.Close()
	names := make([]string, 0, trackerSnapshotMaxTables)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	out := make([]trackerTableCount, 0, len(names))
	for index, name := range names {
		if index >= trackerSnapshotMaxTables {
			break
		}
		if time.Since(started) > trackerSnapshotQueryBudget {
			break
		}
		if !validTrackerTableName(name) {
			continue
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + name + `"`).Scan(&count); err != nil {
			continue
		}
		out = append(out, trackerTableCount{name: name, count: count})
	}
	return out
}

func validTrackerTableName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return strings.Trim(name, "_") != ""
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_render_queen_body
func renderTrackerQueenBody(rows []trackerTableCount) string {
	if len(rows) == 0 {
		return "It looks like there's no tracker table yet. If this is turning " +
			"into multi-step work, it might help to set one up so progress " +
			"sticks around — older tool results that aren't saved to the " +
			"tracker can be dropped when the context is trimmed."
	}
	lines := []string{"Tracker state right now:"}
	for _, row := range rows {
		lines = append(lines, "  - "+row.name+": "+formatRowCount(row.count)+" row"+pluralSuffix(row.count))
	}
	lines = append(lines, "")
	lines = append(lines,
		"If anything you've gathered since isn't in the tracker yet, it "+
			"might be worth saving — older tool results that aren't in the "+
			"tracker can be dropped when the context is trimmed.")
	return strings.Join(lines, "\n")
}

func formatRowCount(count int) string {
	text := strconv.Itoa(count)
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	var out strings.Builder
	for index, digit := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	if negative {
		return "-" + out.String()
	}
	return out.String()
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
