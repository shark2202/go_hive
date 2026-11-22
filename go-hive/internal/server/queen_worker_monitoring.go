package server

// get_worker_health_summary: the queen's read-only health snapshot over the
// colony's worker run records.
//
// Upstream registers this tool from framework/tools/worker_monitoring_tools.py
// only while a colony is bound (queen_orchestrator.py: `if session.colony_id`)
// and lists it in routes_execution.py's `_QUEEN_LIFECYCLE_EXTRAS` so a forked
// worker never inherits it. The model-visible contract (name / description /
// parameters) is the Python Tool(...) declaration verbatim.
//
// Data source difference (environment, not contract): upstream reads the
// worker storage's `sessions/<session_id>/{state.json,logs/tool_logs.jsonl}`.
// The Go colony runtime keeps one directory per spawned worker at
// `<colony>/workers/<worker_id>/` with `state.json`, the persisted transcript
// (`conversations/parts/*.json`) and the run index copy at `<colony>/runs/`.
// The tool therefore resolves "sessions" from `workers/` in this port (and
// still understands an upstream-shaped `sessions/` tree, including
// `logs/tool_logs.jsonl` when one exists), while the returned JSON object is
// the upstream shape field for field.
//
// Parity: framework/tools/worker_monitoring_tools.py::get_worker_health_summary
// Parity: framework/server/routes_execution.py::_QUEEN_LIFECYCLE_EXTRAS

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	// QueenWorkerHealthSummaryToolName is the queen-only monitoring tool.
	QueenWorkerHealthSummaryToolName = "get_worker_health_summary"
	// defaultHealthLastNSteps is upstream's _DEFAULT_LAST_N_STEPS.
	defaultHealthLastNSteps = 40
)

// QueenWorkerHealthSummaryToolDef returns the tool definition, byte-identical
// to Python's Tool(name=..., description=..., parameters=...) declaration.
//
// Parity: framework/tools/worker_monitoring_tools.py::register_worker_monitoring_tools
func QueenWorkerHealthSummaryToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: QueenWorkerHealthSummaryToolName,
		Description: "Read the worker agent's execution logs and return a compact health snapshot. " +
			"Returns worker_agent_id and worker_graph_id (use these for ticket identity fields), " +
			"recent verdicts, step count, time since last step, and " +
			"a snippet of the most recent LLM output. " +
			"session_id is optional — omit it to auto-discover the most recent active session.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "The worker's active session ID. Omit or pass 'auto' to auto-discover the most recent session.",
				},
				"last_n_steps": map[string]any{
					"type":        "integer",
					"description": "How many recent log steps to include (default 40)",
				},
			},
			"required": []any{},
		},
	}
}

// queenWorkerHealthPayload is the upstream JSON object in upstream's insertion
// order (json.dumps of a dict preserves it); Go maps would sort the keys.
type queenWorkerHealthPayload struct {
	WorkerAgentID        string   `json:"worker_agent_id"`
	WorkerGraphID        string   `json:"worker_graph_id"`
	SessionID            string   `json:"session_id"`
	SessionStatus        string   `json:"session_status"`
	TotalSteps           int      `json:"total_steps"`
	RecentVerdicts       []string `json:"recent_verdicts"`
	StepsSinceLastAccept int      `json:"steps_since_last_accept"`
	LastStepTimeISO      *string  `json:"last_step_time_iso"`
	StallMinutes         *float64 `json:"stall_minutes"`
	EvidenceSnippet      string   `json:"evidence_snippet"`
}

// getWorkerHealthSummary renders one health snapshot.
//
// Parity: framework/tools/worker_monitoring_tools.py::get_worker_health_summary
func (b *QueenLifecycleBinding) getWorkerHealthSummary(input map[string]any) string {
	if b == nil || b.session == nil {
		return queenLifecycleJSON(map[string]any{"error": "No runtime on this session."})
	}
	storagePath := strings.TrimSpace(b.session.WorkerPath)
	if storagePath == "" {
		return queenLifecycleJSON(map[string]any{"error": "No colony running."})
	}
	storagePath = filepath.Clean(storagePath)

	sessionID := queenInputString(input, "session_id")
	lastN := defaultHealthLastNSteps
	if value, ok := pythonIntValue(input["last_n_steps"]); ok {
		lastN = value
	}

	// Auto-discover the most recent active session if not specified.
	if sessionID == "" || sessionID == "auto" {
		resolved, ok := b.discoverWorkerSession(storagePath)
		if !ok {
			return queenLifecycleJSON(map[string]any{"error": "No sessions found — worker has not started yet"})
		}
		sessionID = resolved
	}

	sessionDir := workerSessionDir(storagePath, sessionID)
	statePath := filepath.Join(sessionDir, "state.json")

	// Read session status; unknown covers missing/unreadable state.
	sessionStatus := "unknown"
	if state := readJSONMap(statePath); state != nil {
		if status := stringValue(state["status"]); status != "" {
			sessionStatus = status
		}
	}

	// Read the step log. The upstream layout carries logs/tool_logs.jsonl;
	// the Go colony layout persists the transcript instead.
	toolLogsPath := filepath.Join(sessionDir, "logs", "tool_logs.jsonl")
	steps, err := readToolLogSteps(toolLogsPath)
	if err != nil {
		return queenLifecycleJSON(map[string]any{"error": "Could not read tool logs: " + err.Error()})
	}
	var lastStepTime *time.Time
	if steps != nil && len(steps) > 0 {
		if info, statErr := os.Stat(toolLogsPath); statErr == nil {
			mtime := info.ModTime()
			lastStepTime = &mtime
		}
	}
	if steps == nil {
		// Go colony layout: count the worker's persisted LLM turns and take
		// the newest assistant text as the evidence snippet.
		steps, lastStepTime = readTranscriptSteps(filepath.Join(sessionDir, "conversations", "parts"))
	}

	recent := recentSteps(steps, lastN)
	recentVerdicts := []string{}
	for _, step := range recent {
		if verdict := stringValue(step["verdict"]); verdict != "" {
			recentVerdicts = append(recentVerdicts, verdict)
		}
	}
	stepsSinceLastAccept := 0
	for index := len(recentVerdicts) - 1; index >= 0; index-- {
		if recentVerdicts[index] == "ACCEPT" {
			break
		}
		stepsSinceLastAccept++
	}

	var lastStepISO *string
	var stallMinutes *float64
	if lastStepTime != nil {
		formatted := pythonUTCIso(*lastStepTime)
		lastStepISO = &formatted
		elapsed := time.Since(*lastStepTime).Minutes()
		if elapsed >= 1.0 {
			rounded := roundTo1(elapsed)
			stallMinutes = &rounded
		}
	}

	evidence := ""
	for index := len(recent) - 1; index >= 0; index-- {
		if text := stringValue(recent[index]["llm_text"]); text != "" {
			evidence = charPrefix(text, 500)
			break
		}
	}

	agentName := filepath.Base(storagePath)
	return queenLifecycleJSON(queenWorkerHealthPayload{
		WorkerAgentID:        agentName,
		WorkerGraphID:        agentName,
		SessionID:            sessionID,
		SessionStatus:        sessionStatus,
		TotalSteps:           len(steps),
		RecentVerdicts:       recentVerdicts,
		StepsSinceLastAccept: stepsSinceLastAccept,
		LastStepTimeISO:      lastStepISO,
		StallMinutes:         stallMinutes,
		EvidenceSnippet:      evidence,
	})
}

// workerSessionRoots lists the directories a worker's session records can live
// under. Go colony runs keep `<colony>/workers/<id>/`; an upstream-shaped
// storage root keeps `<storage>/sessions/<id>/`.
func workerSessionRoots(storagePath string) []string {
	return []string{
		filepath.Join(storagePath, "workers"),
		filepath.Join(storagePath, "sessions"),
	}
}

// workerSessionDir resolves one session id against the first existing root;
// discovery already chose the root that owns the id.
func workerSessionDir(storagePath, sessionID string) string {
	for _, root := range workerSessionRoots(storagePath) {
		dir := filepath.Join(root, sessionID)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return filepath.Join(storagePath, "workers", sessionID)
}

// discoverWorkerSession picks the session the tool inspects when the caller
// omitted session_id: the queen's own session id when that directory exists
// (upstream's default_session_id), otherwise the most recent active session by
// status priority then mtime.
//
// Parity: framework/tools/worker_monitoring_tools.py::get_worker_health_summary
// (auto-discovery block)
func (b *QueenLifecycleBinding) discoverWorkerSession(storagePath string) (string, bool) {
	roots := workerSessionRoots(storagePath)
	// Prefer the queen's own session id over mtime discovery, exactly as the
	// registration-time default_session_id does upstream.
	if defaultID := strings.TrimSpace(b.session.ID); defaultID != "" {
		for _, root := range roots {
			if info, err := os.Stat(filepath.Join(root, defaultID)); err == nil && info.IsDir() {
				return defaultID, true
			}
		}
	}

	type candidate struct {
		id       string
		priority int
		mtime    time.Time
	}
	candidates := []candidate{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			statePath := filepath.Join(root, entry.Name(), "state.json")
			info, statErr := os.Stat(statePath)
			if statErr != nil {
				continue
			}
			status := ""
			if state := readJSONMap(statePath); state != nil {
				status = strings.ToLower(strings.TrimSpace(stringValue(state["status"])))
			}
			priority := 1
			switch status {
			case "in_progress", "running", "queued":
				// queued is the Go colony's pre-start active state.
				priority = 0
			case "":
				priority = 2
			}
			candidates = append(candidates, candidate{id: entry.Name(), priority: priority, mtime: info.ModTime()})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	return candidates[0].id, true
}

// readToolLogSteps loads an upstream-shaped tool_logs.jsonl. A missing file
// returns (nil, nil): the caller then falls back to the transcript layout.
func readToolLogSteps(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	steps := []map[string]any{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		step := map[string]any{}
		if json.Unmarshal([]byte(line), &step) != nil {
			// Partial writes from crashes are skipped, as upstream does.
			continue
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// readTranscriptSteps counts the worker's persisted assistant turns and returns
// the synthetic step rows the health summary reads (llm_text only). The newest
// assistant part's mtime is the "last step time" when no tool_logs.jsonl
// exists.
func readTranscriptSteps(partsDir string) ([]map[string]any, *time.Time) {
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		return []map[string]any{}, nil
	}
	type part struct {
		seq     int
		role    string
		content string
		mtime   time.Time
	}
	parts := []part{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(partsDir, entry.Name())
		record := readJSONMap(path)
		if record == nil {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		seq := 0
		if value, ok := pythonIntValue(record["seq"]); ok {
			seq = value
		}
		parts = append(parts, part{
			seq:     seq,
			role:    stringValue(record["role"]),
			content: stringValue(record["content"]),
			mtime:   info.ModTime(),
		})
	}
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].seq < parts[j].seq })
	steps := []map[string]any{}
	var lastStep *time.Time
	for _, entry := range parts {
		if entry.role != "assistant" {
			continue
		}
		steps = append(steps, map[string]any{"llm_text": entry.content})
		mtime := entry.mtime
		lastStep = &mtime
	}
	return steps, lastStep
}

// recentSteps applies upstream's `steps[-last_n_steps:]` window, including its
// behaviour for a zero or negative last_n_steps (0 → every step).
func recentSteps(steps []map[string]any, lastN int) []map[string]any {
	if len(steps) == 0 || lastN == 0 {
		return steps
	}
	if len(steps) <= lastN {
		return steps
	}
	start := len(steps) - lastN
	if lastN < 0 {
		start = -lastN
	}
	if start > len(steps) {
		start = len(steps)
	}
	return steps[start:]
}

// readJSONMap reads one JSON object; nil means missing or unreadable.
func readJSONMap(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	record := map[string]any{}
	if json.Unmarshal(data, &record) != nil {
		return nil
	}
	return record
}

// pythonUTCIso renders a timestamp the way Python's
// `datetime.fromtimestamp(mtime, UTC).isoformat()` does: microsecond precision,
// omitted when zero, with a +00:00 offset.
func pythonUTCIso(t time.Time) string {
	utc := t.UTC()
	base := utc.Format("2006-01-02T15:04:05")
	if micros := utc.Nanosecond() / 1000; micros != 0 {
		return base + "." + padSix(micros) + "+00:00"
	}
	return base + "+00:00"
}

// padSix zero-pads a microsecond value to six digits.
func padSix(value int) string {
	text := "00000" + itoa(value)
	return text[len(text)-6:]
}

// itoa is a small non-negative int formatter (avoids pulling fmt into the hot
// path for one call).
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
