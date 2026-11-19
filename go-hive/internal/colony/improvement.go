package colony

// Colony improvement signals.
//
// A missed success criterion or a judge RETRY is only useful if something can
// act on it. This file turns both into durable, structured records:
//
//   - <colony>/improvement/signals.jsonl — append-only ledger the improvement
//     loop (reflection, skill authoring, a re-dispatch decision) reads.
//   - a `custom` bus event with kind=improvement_signal — the live detection
//     event, so a dashboard/subscriber sees the miss as it happens.
//   - the worker's terminal report carries the same signals, so the queen's
//     [WORKER_REPORT] shows exactly which criterion fell short.
//
// An empty registered tracker table is the "nothing was recorded" case from
// the concept docs: it produces empty_tracker detection signals rather than
// silently reading as success.
//
// Parity-exempt: upstream Hive documents improvement as judge feedback plus
// queen reflection; it has no structured improvement ledger to port. The
// record/event shape is a Go-side extension that makes the documented signal
// machine-readable.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/tracker"
)

const (
	// improvementDirName is the colony subdirectory that holds the signals.
	improvementDirName = "improvement"
	// improvementSignalsFile is the append-only JSONL ledger.
	improvementSignalsFile = "signals.jsonl"
	// ImprovementSignalEventKind tags the bus detection event; the event type
	// stays the generic custom type so existing subscribers are unaffected.
	ImprovementSignalEventKind = "improvement_signal"
)

// Improvement signal kinds.
const (
	// ImprovementKindUnmetCriterion marks a success criterion that did not
	// pass (or could not be executed).
	ImprovementKindUnmetCriterion = "unmet_criterion"
	// ImprovementKindJudgeRetry marks a judge RETRY/ESCALATE verdict.
	ImprovementKindJudgeRetry = "judge_retry"
	// ImprovementKindEmptyTracker marks a registered tracker table with no rows.
	ImprovementKindEmptyTracker = "empty_tracker"
)

// ImprovementSignal is one actionable "this fell short" record.
type ImprovementSignal struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	ColonyID      string         `json:"colony_id,omitempty"`
	WorkerID      string         `json:"worker_id,omitempty"`
	CriterionID   string         `json:"criterion_id,omitempty"`
	CriterionName string         `json:"criterion,omitempty"`
	Metric        string         `json:"metric,omitempty"`
	Supported     bool           `json:"supported"`
	Summary       string         `json:"summary"`
	Feedback      string         `json:"feedback,omitempty"`
	Iteration     int            `json:"iteration,omitempty"`
	Evidence      map[string]any `json:"evidence,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

// Map renders the signal as the JSON-shaped payload reports and events carry.
func (s ImprovementSignal) Map() map[string]any {
	out := map[string]any{
		"id":         s.ID,
		"kind":       s.Kind,
		"supported":  s.Supported,
		"summary":    s.Summary,
		"created_at": s.CreatedAt,
	}
	if s.ColonyID != "" {
		out["colony_id"] = s.ColonyID
	}
	if s.WorkerID != "" {
		out["worker_id"] = s.WorkerID
	}
	if s.CriterionID != "" {
		out["criterion_id"] = s.CriterionID
	}
	if s.CriterionName != "" {
		out["criterion"] = s.CriterionName
	}
	if s.Metric != "" {
		out["metric"] = s.Metric
	}
	if s.Feedback != "" {
		out["feedback"] = s.Feedback
	}
	if s.Iteration != 0 {
		out["iteration"] = s.Iteration
	}
	if len(s.Evidence) > 0 {
		out["evidence"] = s.Evidence
	}
	return out
}

// improvementSignalsPath is the ledger path; empty when the runtime has no
// storage to write to.
func (r *ColonyRuntime) improvementSignalsPath() string {
	if r == nil || r.storagePath == "" {
		return ""
	}
	return filepath.Join(r.storagePath, improvementDirName, improvementSignalsFile)
}

// RecordImprovementSignal appends one signal to the colony ledger and
// publishes its detection event. The stored copy (with id/created_at filled)
// is returned.
func (r *ColonyRuntime) RecordImprovementSignal(signal ImprovementSignal) ImprovementSignal {
	if r == nil {
		return signal
	}
	if signal.CreatedAt == "" {
		signal.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if signal.ColonyID == "" {
		signal.ColonyID = r.streamID
	}
	signal.ID = fmt.Sprintf("sig_%d_%d", time.Now().UTC().UnixNano(), r.improvementSeq.Add(1))

	if path := r.improvementSignalsPath(); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				if encoded, err := json.Marshal(signal); err == nil {
					_, _ = file.Write(append(encoded, '\n'))
				}
				_ = file.Close()
			}
		}
	}
	r.publishImprovementSignal(signal)
	return signal
}

// publishImprovementSignal emits the live detection event for a signal.
func (r *ColonyRuntime) publishImprovementSignal(signal ImprovementSignal) {
	if r == nil || r.bus == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventCustom, r.streamID)
	event.Data = map[string]any{
		"kind":   ImprovementSignalEventKind,
		"signal": signal.Map(),
	}
	colonyID := r.streamID
	event.ColonyID = &colonyID
	r.bus.Publish(event)
}

// ImprovementSignals reads the colony ledger back in write order. A missing
// ledger is an empty list, not an error: a healthy colony writes none.
func (r *ColonyRuntime) ImprovementSignals() []ImprovementSignal {
	out := []ImprovementSignal{}
	path := r.improvementSignalsPath()
	if path == "" {
		return out
	}
	file, err := os.Open(path)
	if err != nil {
		return out
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var signal ImprovementSignal
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			continue
		}
		out = append(out, signal)
	}
	return out
}

// recordGoalImprovementSignals turns an executed goal evaluation into
// improvement signals: one per unmet/unsupported criterion. It also detects
// empty registered tracker tables, the "no progress recorded" case.
func (r *ColonyRuntime) recordGoalImprovementSignals(worker *Worker, evaluation *agentloop.GoalEvaluation) {
	if r == nil || worker == nil {
		return
	}
	if evaluation != nil {
		for _, criterion := range evaluation.Criteria {
			if criterion.Met {
				continue
			}
			summary := fmt.Sprintf("worker %s missed success criterion %q (%s)", worker.ID, criterion.ID, criterion.Description)
			evidence := map[string]any{
				"metric":  criterion.Metric,
				"weight":  criterion.Weight,
				"score":   evaluation.Score,
				"success": evaluation.Success,
			}
			if criterion.Target != "" {
				evidence["target"] = criterion.Target
			}
			if criterion.Detail != "" {
				evidence["detail"] = criterion.Detail
			}
			signal := ImprovementSignal{
				Kind:          ImprovementKindUnmetCriterion,
				WorkerID:      worker.ID,
				CriterionID:   criterion.ID,
				CriterionName: criterion.Description,
				Metric:        criterion.Metric,
				Supported:     criterion.Supported,
				Summary:       summary,
				Evidence:      evidence,
			}
			worker.appendImprovementSignal(r.RecordImprovementSignal(signal))
		}
	}
	r.detectEmptyTrackerColumns(worker)
}

// recordJudgeImprovementSignal turns a non-accepting judge verdict into a
// signal. RETRY stays in the loop (the critique is injected as feedback) and
// ESCALATE ends the turn; both are misses the improvement loop should see.
func (r *ColonyRuntime) recordJudgeImprovementSignal(worker *Worker, verdict agentloop.JudgeVerdict, iteration int) {
	if r == nil || worker == nil || verdict.Action == agentloop.JudgeAccept {
		return
	}
	summary := fmt.Sprintf("judge returned %s for worker %s", verdict.Action, worker.ID)
	if feedback := strings.TrimSpace(verdict.Feedback); feedback != "" {
		summary += ": " + feedback
	}
	signal := ImprovementSignal{
		Kind:      ImprovementKindJudgeRetry,
		WorkerID:  worker.ID,
		Supported: true,
		Summary:   summary,
		Feedback:  verdict.Feedback,
		Iteration: iteration,
	}
	worker.appendImprovementSignal(r.RecordImprovementSignal(signal))
}

// detectEmptyTrackerColumns records one empty_tracker signal per registered
// writable table that holds no rows. A runtime without a tracker binding (DM
// overhead, tests, a colony that never provisioned one) is a no-op.
func (r *ColonyRuntime) detectEmptyTrackerColumns(worker *Worker) {
	tables := r.emptyTrackerTables()
	if len(tables) == 0 {
		return
	}
	workerID := ""
	if worker != nil {
		workerID = worker.ID
	}
	for _, table := range tables {
		signal := ImprovementSignal{
			Kind:      ImprovementKindEmptyTracker,
			WorkerID:  workerID,
			Supported: true,
			Summary:   fmt.Sprintf("tracker table %q has no rows — no progress was recorded for this work", table),
			Evidence:  map[string]any{"table": table},
		}
		recorded := r.RecordImprovementSignal(signal)
		if worker != nil {
			worker.appendImprovementSignal(recorded)
		}
	}
}

// emptyTrackerTables lists the colony tracker's registered writable tables
// with zero rows. Query failures degrade to "nothing detected" — detection is
// an observability signal, never a reason to fail a finished worker.
func (r *ColonyRuntime) emptyTrackerTables() []string {
	binding := r.TrackerBinding()
	if binding == nil || binding.TrackerDB == "" {
		return nil
	}
	registry, err := tracker.ExecuteSQL(binding.TrackerDB, "SELECT table_name FROM _tracker_registry ORDER BY table_name", 100)
	if err != nil {
		return nil
	}
	tables := trackerColumnStrings(registry, "table_name")
	empty := make([]string, 0, len(tables))
	for _, table := range tables {
		if table == "" {
			continue
		}
		statement := "SELECT COUNT(*) AS row_count FROM " + quoteTrackerIdent(table)
		result, err := tracker.ExecuteSQL(binding.TrackerDB, statement, 1)
		if err != nil {
			continue
		}
		if trackerColumnInt(result, "row_count") == 0 {
			empty = append(empty, table)
		}
	}
	return empty
}

// quoteTrackerIdent quotes a SQLite identifier for the empty-table probe.
func quoteTrackerIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// trackerColumnStrings reads one text column out of a tracker.ExecuteSQL
// result ({"columns": [...], "rows": [[value, ...]]}).
func trackerColumnStrings(result map[string]any, column string) []string {
	index := trackerColumnIndex(result, column)
	if index < 0 {
		return nil
	}
	rows, _ := result["rows"].([]any)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		values, ok := row.([]any)
		if !ok || index >= len(values) {
			continue
		}
		text, _ := values[index].(string)
		out = append(out, text)
	}
	return out
}

// trackerColumnInt reads one integer column out of a tracker.ExecuteSQL
// result. SQLite returns int64 for COUNT(*); other numeric shapes are
// tolerated so a driver change cannot silently read as zero.
func trackerColumnInt(result map[string]any, column string) int64 {
	index := trackerColumnIndex(result, column)
	if index < 0 {
		return 0
	}
	rows, _ := result["rows"].([]any)
	if len(rows) == 0 {
		return 0
	}
	values, ok := rows[0].([]any)
	if !ok || index >= len(values) {
		return 0
	}
	switch value := values[index].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

// trackerColumnIndex resolves a column name to its position in the result.
func trackerColumnIndex(result map[string]any, column string) int {
	columns, _ := result["columns"].([]string)
	for index, name := range columns {
		if name == column {
			return index
		}
	}
	// Some call paths hand the columns back as []any.
	if raw, ok := result["columns"].([]any); ok {
		for index, name := range raw {
			if text, _ := name.(string); text == column {
				return index
			}
		}
	}
	return -1
}

// appendImprovementSignal records one signal on the worker so its terminal
// report can carry it.
func (w *Worker) appendImprovementSignal(signal ImprovementSignal) {
	if w == nil || signal.ID == "" {
		return
	}
	w.mu.Lock()
	w.improvementSignals = append(w.improvementSignals, signal)
	w.mu.Unlock()
}

// ImprovementSignals returns a copy of the worker's recorded signals.
func (w *Worker) ImprovementSignals() []ImprovementSignal {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]ImprovementSignal(nil), w.improvementSignals...)
}

// improvementSignalMaps renders the worker's signals for report payloads.
func (w *Worker) improvementSignalMaps() []any {
	signals := w.ImprovementSignals()
	if len(signals) == 0 {
		return nil
	}
	out := make([]any, 0, len(signals))
	for _, signal := range signals {
		out = append(out, signal.Map())
	}
	return out
}
