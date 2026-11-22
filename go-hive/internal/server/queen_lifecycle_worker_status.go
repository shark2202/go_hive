package server

// get_worker_status: the queen's monitoring read over the bound colony.
//
// Upstream renders a brief prose summary by default and drills into
// activity/memory/tools/issues/progress/full on request, with a tiered
// cooldown so a polling queen cannot flood the LLM context. This port keeps
// the vocabulary and the sections; the data source is the colony's live worker
// registry plus the session event bus (the same bus the upstream tool reads).
//
// Parity: framework/tools/queen_lifecycle_tools.py::get_worker_status

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// Tiered cooldowns: summary is free, detail has a short cooldown, full keeps
// 30s between dumps.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_COOLDOWN_FULL
// Parity: framework/tools/queen_lifecycle_tools.py::_COOLDOWN_DETAIL
const (
	queenStatusCooldownFull   = 30.0
	queenStatusCooldownDetail = 10.0
)

// queenWorkerPreamble is the cheap status snapshot both the summary and the
// focus renderers build from.
type queenWorkerPreamble struct {
	status   string // not_loaded / idle / running / waiting_for_input
	active   []queenActiveWorker
	finished int
	queued   int
}

type queenActiveWorker struct {
	ID      string
	Status  string
	Elapsed float64
	Task    string
	Goal    string
}

// getWorkerStatus renders one status read. Plain-prose results are returned
// as-is (upstream returns bare strings for "No colony running." and the focus
// sections); JSON results carry the upstream object shapes.
//
// Parity: framework/tools/queen_lifecycle_tools.py::get_worker_status
func (b *QueenLifecycleBinding) getWorkerStatus(input map[string]any) string {
	focus := queenInputString(input, "focus")
	lastN := 20
	if value, ok := pythonIntValue(input["last_n"]); ok && value > 0 {
		lastN = value
	}

	// --- Tiered cooldown ---------------------------------------------------
	tier := "detail"
	cooldown := queenStatusCooldownDetail
	if focus == "full" {
		tier = "full"
		cooldown = queenStatusCooldownFull
	} else if focus == "" {
		tier = "summary"
		cooldown = 0
	}
	now := b.session.NowMonotonic()
	b.statusMu.Lock()
	last, called := b.statusCalls[tier]
	b.statusMu.Unlock()
	if called {
		elapsed := now - last
		if elapsed < cooldown {
			label := focus
			if label == "" {
				label = "summary"
			}
			return queenLifecycleJSON(map[string]any{
				"status": "cooldown",
				"message": fmt.Sprintf(
					"Status '%s' was checked %ds ago. Wait %ds or try a different focus.",
					label, int(elapsed), int(cooldown-elapsed)),
			})
		}
	}
	b.statusMu.Lock()
	b.statusCalls[tier] = now
	b.statusMu.Unlock()

	// --- Runtime check -----------------------------------------------------
	runtime := sessionColonyRuntime(b.session)
	if runtime == nil {
		return "No colony running."
	}

	bus := b.session.EventBus
	preamble := b.buildWorkerPreamble(runtime)

	if focus == "" {
		return b.formatSummary(preamble, queenRedFlagCount(bus))
	}
	if bus == nil {
		return fmt.Sprintf("Worker is %s. EventBus unavailable — only basic status returned.", preamble.status)
	}
	switch focus {
	case "activity":
		return b.formatActivity(bus, preamble, lastN)
	case "memory":
		return queenWorkerMemoryNote()
	case "tools":
		return formatWorkerTools(bus, lastN)
	case "issues":
		return formatWorkerIssues(bus)
	case "progress":
		return b.formatProgress(runtime, bus)
	case "full":
		return b.buildWorkerStatusJSON(runtime, bus, preamble, lastN)
	}
	return fmt.Sprintf("Unknown focus '%s'. Valid options: activity, memory, tools, issues, progress, full.", focus)
}

// buildWorkerPreamble snapshots the colony's non-persistent workers: how many
// are live, how many are queued, how many are terminal, and how long the
// oldest live execution has been running.
func (b *QueenLifecycleBinding) buildWorkerPreamble(runtime *colony.ColonyRuntime) queenWorkerPreamble {
	preamble := queenWorkerPreamble{status: "not_loaded"}
	now := time.Now()
	for _, worker := range runtime.WorkerList() {
		if worker == nil || worker.IsPersistent() {
			continue
		}
		if worker.Status() == colony.WorkerQueued {
			preamble.queued++
			continue
		}
		if !worker.IsActive() {
			preamble.finished++
			continue
		}
		elapsed := 0.0
		if started := worker.StartedAt(); !started.IsZero() {
			elapsed = now.Sub(started).Seconds()
		}
		preamble.active = append(preamble.active, queenActiveWorker{
			ID:      worker.ID,
			Status:  string(worker.Status()),
			Elapsed: elapsed,
			Task:    worker.Task,
			Goal:    worker.Goal,
		})
	}
	sort.Slice(preamble.active, func(i, j int) bool { return preamble.active[i].ID < preamble.active[j].ID })

	pending := b.session.pendingQuestionEntries()
	switch {
	case len(preamble.active) == 0 && preamble.queued == 0 && preamble.finished == 0:
		preamble.status = "not_loaded"
	case len(preamble.active) == 0:
		preamble.status = "idle"
	case len(pending) > 0:
		preamble.status = "waiting_for_input"
	default:
		preamble.status = "running"
	}
	return preamble
}

// formatSummary is the default prose read.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_summary
func (b *QueenLifecycleBinding) formatSummary(preamble queenWorkerPreamble, redFlags int) string {
	switch preamble.status {
	case "not_loaded":
		return "No workers loaded."
	case "idle":
		parts := []string{"Workers are idle. No active executions"}
		if preamble.queued > 0 {
			parts[0] = fmt.Sprintf("Workers are idle. %d queued execution(s) waiting", preamble.queued)
		}
		summary := strings.Join(parts, "") + "."
		if preamble.finished > 0 {
			summary += fmt.Sprintf(" %d finished worker(s) tracked.", preamble.finished)
		}
		return summary
	case "waiting_for_input":
		if pending := b.session.pendingQuestionEntries(); len(pending) > 0 {
			reason := stringValue(pending[0]["reason"])
			if reason != "" {
				return fmt.Sprintf("Worker is waiting for input: %q — call list_worker_questions to answer.", charPrefix(reason, 200))
			}
		}
		return "Worker is waiting for input."
	}

	parts := []string{}
	longest := 0.0
	for _, worker := range preamble.active {
		if worker.Elapsed > longest {
			longest = worker.Elapsed
		}
	}
	if len(preamble.active) == 1 {
		parts = append(parts, fmt.Sprintf("Worker is running (%s)", formatElapsed(longest)))
	} else {
		parts = append(parts, fmt.Sprintf("%d workers are running (longest %s)", len(preamble.active), formatElapsed(longest)))
	}
	if preamble.queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", preamble.queued))
	}
	if redFlags > 0 {
		parts = append(parts, fmt.Sprintf("%d issue type(s) detected — use focus='issues' for details", redFlags))
	} else {
		parts = append(parts, "No issues detected")
	}
	summary := strings.Join(parts, ". ") + "."

	if bus := b.session.EventBus; bus != nil {
		reports := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventSubagentReport), eventbus.WithHistoryLimit(1))
		if len(reports) > 0 {
			latest := reports[0]
			message := charPrefix(stringValue(latest.Data["message"]), 200)
			summary += fmt.Sprintf(" Latest worker report (%s): %s", formatTimeAgo(latest.Timestamp), message)
		}
	}
	return summary
}

// formatActivity describes what the live workers are doing right now.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_activity
func (b *QueenLifecycleBinding) formatActivity(bus *eventbus.EventBus, preamble queenWorkerPreamble, lastN int) string {
	lines := []string{}
	for _, worker := range preamble.active {
		label := worker.ID
		if worker.Goal != "" {
			label += " (" + charPrefix(worker.Goal, 120) + ")"
		} else if worker.Task != "" {
			label += " (" + charPrefix(worker.Task, 120) + ")"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s, %s elapsed", label, worker.Status, formatElapsed(worker.Elapsed)))
	}
	if len(lines) == 0 {
		lines = append(lines, "No active executions.")
	}

	node := ""
	iteration := 0
	if events := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeLoopIteration), eventbus.WithHistoryLimit(1)); len(events) > 0 {
		if events[0].NodeID != nil {
			node = *events[0].NodeID
		}
		if value, ok := pythonIntValue(events[0].Data["iteration"]); ok {
			iteration = value
		}
	}
	if node != "" {
		lines = append(lines, fmt.Sprintf("Current node: %s (iteration %d)", node, iteration))
	}

	if events := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventLLMTextDelta), eventbus.WithHistoryLimit(1)); len(events) > 0 {
		snapshot := stringValue(events[0].Data["snapshot"])
		snippet := strings.TrimSpace(lastRunes(snapshot, 300))
		if snippet != "" {
			lines = append(lines, fmt.Sprintf("Last LLM output: %q", snippet))
		}
	}

	edges := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeRetry), eventbus.WithHistoryLimit(lastN))
	if len(edges) > 0 {
		lines = append(lines, "", "Recent transitions:")
		for _, event := range edges {
			source := stringValue(event.Data["source_node"])
			if source == "" {
				source = "?"
			}
			target := stringValue(event.Data["target_node"])
			if target == "" {
				target = "?"
			}
			condition := stringValue(event.Data["edge_condition"])
			lines = append(lines, fmt.Sprintf("  %s -> %s (%s, %s)", source, target, condition, formatTimeAgo(event.Timestamp)))
		}
	}
	return strings.Join(lines, "\n")
}

// queenWorkerMemoryNote is the memory focus's answer in this port. Workers run
// isolated agent loops with no framework shared buffer, so there is no buffer
// state to inspect — the shared state a queen should read here is the colony's
// tracker database, reached through tracker_query.
//
// Parity-exempt: Go-worker isolation has no shared buffer; the upstream
// _format_memory buffer dump has no counterpart to report, and inventing one
// would mislead the queen.
func queenWorkerMemoryNote() string {
	return "Worker memory inspection is unavailable in this port: workers run isolated agent loops without a framework shared buffer. " +
		"Use tracker_query for shared state, or focus='activity' for live progress."
}

// queenRedFlagCount counts issue categories with cheap limit=1 queries.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_detect_red_flags
func queenRedFlagCount(bus *eventbus.EventBus) int {
	if bus == nil {
		return 0
	}
	count := 0
	for _, eventType := range []eventbus.EventType{
		eventbus.EventNodeStalled,
		eventbus.EventNodeToolDoomLoop,
		eventbus.EventConstraintViolation,
	} {
		if len(bus.GetHistory(eventbus.WithHistoryType(eventType), eventbus.WithHistoryLimit(1))) > 0 {
			count++
		}
	}
	return count
}

// formatElapsed renders seconds as a human-readable duration.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_elapsed
func formatElapsed(seconds float64) string {
	total := int(seconds)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	minutes, remainder := total/60, total%60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainder)
	}
	hours, minutes := minutes/60, minutes%60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

// formatTimeAgo renders a timestamp as relative time ago.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_time_ago
func formatTimeAgo(timestamp time.Time) string {
	delta := time.Since(timestamp).Seconds()
	if delta < 60 {
		return fmt.Sprintf("%ds ago", int(delta))
	}
	if delta < 3600 {
		return fmt.Sprintf("%dm ago", int(delta/60))
	}
	return fmt.Sprintf("%dh ago", int(delta/3600))
}

// lastRunes returns the last n runes of text.
func lastRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}
