//go:build e2e

package e2e

// Real-model concept-flow dual run (Go half). Every scenario drives the real
// `hive serve` binary against a temp HIVE_HOME whose configuration points at
// the recording proxy in e2e/recordproxy, which forwards to the real upstream.
// The Python half (go_hive/scripts/concept_flows_real_python.py) runs the same
// scenarios against `uv run hive serve` and writes the same evidence shape;
// go_hive/scripts/concept_flows_real_compare.py diffs the two.
//
// Opt-in: HIVE_REAL_LLM_E2E=1, HIVE_REAL_LLM_UPSTREAM, HIVE_REAL_LLM_KEY_ENV
// (default BROADTOKEN_API_KEY). Evidence root: HIVE_CONCEPT_OUT.
//
// Run:
//
//	HIVE_REAL_LLM_E2E=1 \
//	HIVE_REAL_LLM_UPSTREAM=https://token.tokenbroad.com/v1 \
//	HIVE_CONCEPT_OUT=/tmp/concept-out \
//	go test -tags e2e ./e2e -run TestRealLLMConceptFlows -v -count=1 -timeout 60m

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/e2e/recordproxy"

	_ "modernc.org/sqlite"
)

const (
	conceptColonyID        = "cf_worker"
	conceptProbeWorker     = "cf-probe-worker-model"
	conceptProbeQueen      = "cf-probe-queen-model"
	conceptAskUserQuestion = "Which colour should I use?"
)

func conceptWorkerDefinition(prompt string) map[string]any {
	return map[string]any{
		"name":          "cf_worker",
		"description":   "Concept-flow E2E worker",
		"system_prompt": prompt,
		"tools":         []string{},
		"goal":          map[string]any{"description": "Concept-flow E2E work"},
	}
}

// conceptSubtest scaffolds the shared plumbing for one scenario and returns
// its evidence directory, temp home and started serve.
func conceptSubtest(t *testing.T, env conceptEnv, binary, scenario, variant string, configExtra func(proxyURL string) map[string]any, colonyWorker map[string]any) (string, string, *conceptServe, *recordproxy.Server) {
	t.Helper()
	outDir := filepath.Join(env.outRoot, "go", scenario+"-"+variant)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home: %v", err)
	}
	proxyURL, proxy := conceptProxy(t, env, filepath.Join(outDir, "model-requests.jsonl"))
	var extra map[string]any
	if configExtra != nil {
		extra = configExtra(proxyURL)
	}
	conceptWriteConfig(t, home, proxyURL, env.model, extra)
	if colonyWorker != nil {
		conceptWriteColony(t, home, conceptColonyID, "queen_growth", colonyWorker)
	}
	serve := conceptStartServe(t, binary, home)
	return outDir, home, serve, proxy
}

// Parity-exempt: integration test against a live model; both halves are
// cross-implementation probes, and the Python reference is driven by the
// sibling script rather than a scripted gateway.
func TestRealLLMConceptFlows(t *testing.T) {
	env := conceptLoadEnv(t)
	binary := conceptBuildBinary(t)

	t.Run("worker_dispatch", func(t *testing.T) { conceptWorkerDispatch(t, env, binary) })
	t.Run("worker_llm_probe", func(t *testing.T) { conceptWorkerLLMProbe(t, env, binary) })
	t.Run("escalation_chain", func(t *testing.T) { conceptEscalationChain(t, env, binary) })
	t.Run("ask_user_park", func(t *testing.T) { conceptAskUserPark(t, env, binary) })
	t.Run("queen_llm_probe", func(t *testing.T) { conceptQueenLLMProbe(t, env, binary) })
	t.Run("resume_after_restart", func(t *testing.T) { conceptResumeAfterRestart(t, env, binary) })
}

// conceptBaseEvidence seeds the fields every scenario shares.
func conceptBaseEvidence(side, scenario, variant string) map[string]any {
	return map[string]any{
		"side":     side,
		"scenario": scenario,
		"variant":  variant,
	}
}

func conceptLiveView(live map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"queen_id", "queen_name", "colony_id", "queen_phase", "has_worker"} {
		if value, present := live[key]; present {
			out[key] = value
		}
	}
	return out
}

func conceptEventTypeCounts(events []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, event := range events {
		eventType, _ := event["type"].(string)
		counts[eventType]++
	}
	return counts
}

func conceptFindSessionDir(home, sessionID string) string {
	patterns := []string{
		filepath.Join(home, "queens", "*", "sessions", sessionID),
		filepath.Join(home, "colonies", "*", "queens", "*", "sessions", sessionID),
	}
	for _, pattern := range patterns {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// conceptSessionEvidence captures a session's transcript, events and snapshot.
func conceptSessionEvidence(t *testing.T, serve *conceptServe, home, sessionID string) map[string]any {
	t.Helper()
	dir := conceptFindSessionDir(home, sessionID)
	evidence := map[string]any{"dir_found": dir != ""}
	if dir == "" {
		return evidence
	}
	evidence["parts"] = conceptPartsView(conceptParts(t, dir))
	events := conceptReadEvents(dir)
	evidence["events"] = events
	evidence["event_types"] = conceptEventTypeCounts(events)
	if serve != nil {
		evidence["snapshot"] = conceptSnapshotView(serve.snapshot(sessionID))
	}
	return evidence
}

// conceptWorkerEvidence captures every worker directory under the colony.
func conceptWorkerEvidence(t *testing.T, colonyPath string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, dir := range conceptFindWorkers(colonyPath) {
		entry := map[string]any{"dir": filepath.Base(dir)}
		if parts := conceptParts(t, dir); parts != nil {
			entry["actions"] = conceptWorkerActions(parts)
			if report := conceptWorkerReport(parts); report != nil {
				entry["report"] = report
			}
			entry["part_roles"] = conceptPartRoles(parts)
		}
		for _, name := range []string{"meta.json", "state.json", "result.json"} {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				entry[strings.TrimSuffix(name, ".json")] = conceptTrimValue(decoded, 0)
			}
		}
		out = append(out, entry)
	}
	return out
}

func conceptPartRoles(parts []map[string]any) []string {
	roles := make([]string, 0, len(parts))
	for _, part := range parts {
		role, _ := part["role"].(string)
		roles = append(roles, role)
	}
	return roles
}

// conceptTrackerDump reads every tracker table's name and row count (plus the
// full registry table) from <colony>/tracker/tracker.db.
func conceptTrackerDump(t *testing.T, colonyPath string) map[string]any {
	t.Helper()
	dbPath := filepath.Join(colonyPath, "tracker", "tracker.db")
	out := map[string]any{"db": dbPath, "exists": false}
	if _, err := os.Stat(dbPath); err != nil {
		return out
	}
	out["exists"] = true
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}
	_ = rows.Close()
	counts := map[string]any{}
	for _, name := range tables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count); err == nil {
			counts[name] = count
		}
	}
	out["tables"] = counts
	// The writable-table registry is the diffable state, not just counts.
	if registry, err := conceptTableRows(db, "SELECT * FROM _tracker_registry ORDER BY table_name"); err == nil {
		out["registry"] = registry
	}
	return out
}

func conceptTableRows(db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			continue
		}
		entry := map[string]any{}
		for i, column := range columns {
			switch typed := values[i].(type) {
			case []byte:
				entry[column] = string(typed)
			default:
				entry[column] = typed
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// ---- Scenario 1: worker dispatch -------------------------------------------

func conceptWorkerDispatch(t *testing.T, env conceptEnv, binary string) {
	worker := conceptWorkerDefinition("You are a worker agent. Call the get_current_time tool exactly once, then call report_to_parent with status='success' and a summary containing the returned time. If get_current_time is not in your toolset, get the current time another way (for example the terminal) and report that. Do not call any other tool.")
	outDir, home, serve, proxy := conceptSubtest(t, env, binary, "worker_dispatch", "main", nil, worker)

	sessionID, _ := serve.createSession(map[string]any{"colony_id": conceptColonyID})
	serve.chat(sessionID, "Use the run_worker tool to dispatch exactly one worker with the task: get the current time (call get_current_time if you have it) and then report it back with report_to_parent, status success. After the worker reports, reply with a one-line summary of its report.")

	colonyPath := filepath.Join(home, "colonies", conceptColonyID)
	deadline := 4 * time.Minute
	conceptWait(t, deadline, "worker directory", func() bool { return len(conceptFindWorkers(colonyPath)) > 0 })
	conceptWait(t, deadline, "worker report_to_parent", func() bool {
		for _, dir := range conceptFindWorkers(colonyPath) {
			for _, part := range conceptParts(t, dir) {
				for _, action := range conceptWorkerActions([]map[string]any{part}) {
					if action == "report_to_parent" {
						return true
					}
				}
			}
		}
		return false
	})
	conceptWait(t, time.Minute, "worker report to reach the lead transcript", func() bool {
		for _, dir := range conceptFindWorkers(colonyPath) {
			if conceptWorkerReport(conceptParts(t, dir)) != nil {
				return true
			}
		}
		// The lead side: session transcript or the colony's own conversation.
		for _, candidate := range []string{conceptFindSessionDir(home, sessionID), colonyPath} {
			if candidate == "" {
				continue
			}
			for _, part := range conceptParts(t, candidate) {
				if content, _ := part["content"].(string); strings.Contains(content, "[WORKER_REPORT]") {
					return true
				}
			}
		}
		return false
	})
	conceptWaitIdle(t, serve, sessionID, 2*time.Minute)

	evidence := conceptBaseEvidence("go", "worker_dispatch", "main")
	evidence["session_id"] = sessionID
	evidence["session_live"] = conceptLiveView(serve.sessionJSON(sessionID))
	evidence["session"] = conceptSessionEvidence(t, serve, home, sessionID)
	evidence["colony_queen"] = map[string]any{"parts": conceptPartsView(conceptParts(t, colonyPath))}
	evidence["workers"] = conceptWorkerEvidence(t, colonyPath)
	evidence["colony_files"] = conceptColonyFiles(colonyPath)
	evidence["tracker"] = conceptTrackerDump(t, colonyPath)
	evidence["model_requests"] = conceptModelRequests(proxy)
	checks := map[string]any{
		"worker_spawned":       len(conceptFindWorkers(colonyPath)) > 0,
		"worker_reported":      false,
		"worker_report_status": "",
		"queen_dispatched":     false,
		"all_models_main":      true,
	}
	var workerToolSets []string
	for _, record := range proxy.Requests() {
		if record.Model != env.model {
			checks["all_models_main"] = false
		}
		if hasTool(record.Tools, "run_worker") {
			if len(record.ToolCalls) > 0 && strings.HasPrefix(record.ToolCalls[0], "run_worker") {
				checks["queen_dispatched"] = true
			}
		}
		if hasTool(record.Tools, "report_to_parent") && !hasTool(record.Tools, "run_worker") {
			workerToolSets = append(workerToolSets, strings.Join(record.Tools, ","))
		}
	}
	for _, worker := range evidence["workers"].([]map[string]any) {
		if report, ok := worker["report"].(map[string]any); ok {
			checks["worker_reported"] = true
			checks["worker_report_status"], _ = report["status"].(string)
		}
	}
	if len(workerToolSets) > 0 {
		checks["worker_tools"] = strings.Split(workerToolSets[0], ",")
	}
	evidence["checks"] = checks
	conceptWriteEvidence(t, outDir, evidence)

	if checks["worker_spawned"] != true || checks["worker_reported"] != true {
		t.Errorf("worker dispatch did not close: %v", checks)
	}
	if checks["queen_dispatched"] != true {
		t.Errorf("queen never called run_worker: %v", checks)
	}
}

func hasTool(tools []string, name string) bool {
	for _, tool := range tools {
		if tool == name {
			return true
		}
	}
	return false
}

// ---- Scenario 1b: worker_llm probe -----------------------------------------

func conceptWorkerLLMProbe(t *testing.T, env conceptEnv, binary string) {
	worker := conceptWorkerDefinition("You are a worker agent. Call the get_current_time tool exactly once, then call report_to_parent with status='success' and a summary containing the returned time.")
	extra := func(proxyURL string) map[string]any {
		return map[string]any{
			"worker_llm": map[string]any{"provider": "openai", "model": conceptProbeWorker, "api_base": proxyURL, "api_key_env_var": conceptProxyKeyEnv},
		}
	}
	outDir, home, serve, proxy := conceptSubtest(t, env, binary, "worker_llm_probe", "probe", extra, worker)

	sessionID, _ := serve.createSession(map[string]any{"colony_id": conceptColonyID})
	serve.chat(sessionID, "Use the run_worker tool to dispatch exactly one worker with the task: call get_current_time once and report with report_to_parent.")

	probeRecord := conceptWaitForModel(t, proxy, 4*time.Minute, "a worker request carrying the probe model", func(record recordproxy.Record) bool {
		return record.Model == conceptProbeWorker && hasTool(record.Tools, "report_to_parent")
	})
	// The probe model does not exist upstream, so the worker turn fails; give
	// the failure a moment to settle on disk before snapshotting state.
	time.Sleep(8 * time.Second)

	evidence := conceptBaseEvidence("go", "worker_llm_probe", "probe")
	evidence["session_id"] = sessionID
	evidence["session_live"] = conceptLiveView(serve.sessionJSON(sessionID))
	evidence["session"] = conceptSessionEvidence(t, serve, home, sessionID)
	colonyPath := filepath.Join(home, "colonies", conceptColonyID)
	evidence["workers"] = conceptWorkerEvidence(t, colonyPath)
	evidence["tracker"] = conceptTrackerDump(t, colonyPath)
	evidence["model_requests"] = conceptModelRequests(proxy)
	checks := map[string]any{
		"worker_llm_consumed":      true,
		"probe_request_status":     probeRecord.Status,
		"probe_request_advertised": probeRecord.Tools,
		"main_slot_still_used":     false,
	}
	for _, record := range proxy.Requests() {
		if record.Model == env.model {
			checks["main_slot_still_used"] = true
		}
	}
	for _, worker := range evidence["workers"].([]map[string]any) {
		if state, ok := worker["state"].(map[string]any); ok {
			checks["worker_state_status"], _ = state["status"].(string)
		}
	}
	evidence["checks"] = checks
	conceptWriteEvidence(t, outDir, evidence)

	if checks["worker_llm_consumed"] != true {
		t.Errorf("worker_llm was not consumed: %v", checks)
	}
	if probeRecord.Status == http.StatusOK {
		t.Errorf("probe model unexpectedly answered 200; probe is not selective enough")
	}
}

// ---- Scenario 5: queen_llm probe -------------------------------------------

func conceptQueenLLMProbe(t *testing.T, env conceptEnv, binary string) {
	extra := func(proxyURL string) map[string]any {
		return map[string]any{
			"queen_llm": map[string]any{
				"queen_growth": map[string]any{"provider": "openai", "model": conceptProbeQueen, "api_base": proxyURL, "api_key_env_var": conceptProxyKeyEnv},
			},
		}
	}
	outDir, home, serve, proxy := conceptSubtest(t, env, binary, "queen_llm_probe", "probe", extra, nil)

	sessionID, created := serve.createSession(map[string]any{"queen_name": "queen_growth"})
	serve.chat(sessionID, "Reply with the single word: hello.")

	probeRecord := conceptWaitForModel(t, proxy, 3*time.Minute, "a queen request carrying the probe model", func(record recordproxy.Record) bool {
		return record.Model == conceptProbeQueen
	})
	time.Sleep(8 * time.Second)

	evidence := conceptBaseEvidence("go", "queen_llm_probe", "probe")
	evidence["session_id"] = sessionID
	evidence["session_live"] = conceptLiveView(serve.sessionJSON(sessionID))
	evidence["create_response_queen_id"] = created["queen_id"]
	evidence["session"] = conceptSessionEvidence(t, serve, home, sessionID)
	evidence["model_requests"] = conceptModelRequests(proxy)
	evidence["checks"] = map[string]any{
		"queen_llm_consumed":   true,
		"probe_request_status": probeRecord.Status,
		"queen_id":             created["queen_id"],
	}
	conceptWriteEvidence(t, outDir, evidence)
}

// ---- Scenario 2: escalation chain ------------------------------------------

func conceptEscalationChain(t *testing.T, env conceptEnv, binary string) {
	// Real workers can outrun the queen's reply: once the doom-loop notice
	// lands, a text-only turn counts toward the worker's stall auto-fail and a
	// slow queen loses the worker. Two attempts (fresh session each) make the
	// chain observable without scripting the model; both attempts are recorded.
	const attempts = 2
	var all []map[string]any
	full := false
	for attempt := 1; attempt <= attempts && !full; attempt++ {
		marks, evidence := conceptEscalationAttempt(t, env, binary, attempt)
		all = append(all, evidence)
		if marks["escalation_handoff_to_lead"] == true && marks["list_worker_questions"] == true &&
			marks["reply_to_worker"] == true && marks["worker_resumed_reported"] == true {
			full = true
		}
	}
	outDir := filepath.Join(env.outRoot, "go", "escalation_chain-main")
	combined := conceptBaseEvidence("go", "escalation_chain", "main")
	combined["attempts"] = all
	combined["full_chain_observed"] = full
	// Promote the winning (or last) attempt's projections so the comparison
	// sees the same top-level shape as every other scenario.
	best := all[0]
	bestScore := -1
	for _, attempt := range all {
		checks := attempt["checks"].(map[string]any)
		score := 0
		for _, key := range []string{"escalation_handoff_to_lead", "list_worker_questions", "reply_to_worker", "worker_resumed_reported"} {
			if checks[key] == true {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			best = attempt
		}
	}
	for _, key := range []string{"session_id", "session_live", "session", "colony_queen", "workers", "tracker", "model_requests"} {
		if value, present := best[key]; present {
			combined[key] = value
		}
	}
	combined["checks"] = best["checks"]
	conceptWriteEvidence(t, outDir, combined)
	if !full {
		t.Errorf("no real-model attempt produced the full escalation chain; attempts: %v", all)
	}
}

func conceptEscalationAttempt(t *testing.T, env conceptEnv, binary string, attempt int) (map[string]any, map[string]any) {
	worker := conceptWorkerDefinition("You are a worker agent under test. The test harness REQUIRES this exact sequence: (1) call the task_list tool with an empty object three times in a row, with identical (empty) arguments; (2) then wait for a [QUEEN_REPLY] message — do NOT call report_to_parent yet and do NOT produce text-only turns; while waiting, call task_list once per turn with a distinct harmless filter, e.g. {\"filter\":\"wait-1\"}, {\"filter\":\"wait-2\"}, so your turn is never text-only; (3) as soon as a [QUEEN_REPLY] message arrives, call report_to_parent with status='success' and summary='recovered after escalation'. Do not call any other tool.")
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home: %v", err)
	}
	outDir := filepath.Join(env.outRoot, "go", "escalation_chain-main")
	proxyURL, proxy := conceptProxy(t, env, filepath.Join(outDir, fmt.Sprintf("model-requests-attempt%d.jsonl", attempt)))
	conceptWriteConfig(t, home, proxyURL, env.model, nil)
	colonyPath := conceptWriteColony(t, home, conceptColonyID, "queen_growth", worker)
	serve := conceptStartServe(t, binary, home)
	defer serve.stop()

	sessionID, _ := serve.createSession(map[string]any{"colony_id": conceptColonyID})
	serve.chat(sessionID, "Use run_worker to dispatch exactly one worker with the task: call task_list three times in a row with empty arguments, then wait for guidance. When you receive a [WORKER_ESCALATION] handoff from that worker, you MUST first call list_worker_questions, then call reply_to_worker with guidance telling it to stop repeating and to call report_to_parent now with status success. Then summarize the outcome for me.")

	// Wait for the chain to settle: a post-reply worker report, or a terminal
	// worker state (the text-only auto-fail) with the inbox reply already sent.
	start := time.Now()
	deadline := start.Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		if conceptWorkerReported(colonyPath) {
			break
		}
		if conceptWorkerTerminal(colonyPath) && time.Since(start) > 45*time.Second {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)

	records := proxy.Requests()
	marks := map[string]any{
		"worker_repeated_identical":  false,
		"escalation_handoff_to_lead": false,
		"list_worker_questions":      false,
		"reply_to_worker":            false,
		"queen_reply_consumed":       false,
		"worker_resumed_reported":    false,
	}
	repeats := 0
	for _, record := range records {
		for _, call := range record.ToolCalls {
			if strings.HasPrefix(call, "task_list ") {
				repeats++
			}
			if strings.HasPrefix(call, "list_worker_questions") {
				marks["list_worker_questions"] = true
			}
			if strings.HasPrefix(call, "reply_to_worker") {
				marks["reply_to_worker"] = true
			}
			if strings.HasPrefix(call, "report_to_parent") {
				marks["worker_resumed_reported"] = true
			}
		}
		if strings.Contains(record.Body, "[QUEEN_REPLY]") {
			marks["queen_reply_consumed"] = true
		}
	}
	handoffParts := append(conceptParts(t, conceptFindSessionDir(home, sessionID)), conceptParts(t, colonyPath)...)
	for _, part := range handoffParts {
		if content, _ := part["content"].(string); strings.Contains(content, "[External event] [WORKER_ESCALATION]") {
			marks["escalation_handoff_to_lead"] = true
		}
	}
	marks["identical_task_list_calls"] = repeats
	if repeats >= 3 {
		marks["worker_repeated_identical"] = true
	}

	evidence := conceptBaseEvidence("go", "escalation_chain", fmt.Sprintf("attempt-%d", attempt))
	evidence["session_id"] = sessionID
	evidence["session_live"] = conceptLiveView(serve.sessionJSON(sessionID))
	evidence["session"] = conceptSessionEvidence(t, serve, home, sessionID)
	evidence["colony_queen"] = map[string]any{"parts": conceptPartsView(conceptParts(t, colonyPath))}
	evidence["workers"] = conceptWorkerEvidence(t, colonyPath)
	evidence["tracker"] = conceptTrackerDump(t, colonyPath)
	evidence["model_requests"] = conceptModelRequests(proxy)
	evidence["checks"] = marks
	return marks, evidence
}

func conceptWorkerReported(colonyPath string) bool {
	for _, dir := range conceptFindWorkers(colonyPath) {
		parts, err := readConversationParts(dir)
		if err != nil {
			continue
		}
		if conceptWorkerReport(parts) != nil {
			return true
		}
	}
	return false
}

func conceptWorkerTerminal(colonyPath string) bool {
	for _, dir := range conceptFindWorkers(colonyPath) {
		raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			continue
		}
		var state map[string]any
		if json.Unmarshal(raw, &state) != nil {
			continue
		}
		status, _ := state["status"].(string)
		if status == "failed" || status == "success" || status == "completed" || status == "stopped" {
			return true
		}
	}
	return false
}

// ---- Scenario 3: ask_user park ---------------------------------------------

func conceptAskUserPark(t *testing.T, env conceptEnv, binary string) {
	outDir, home, serve, proxy := conceptSubtest(t, env, binary, "ask_user_park", "main", nil, nil)

	sessionID, _ := serve.createSession(map[string]any{})
	serve.chat(sessionID, "Call the ask_user tool right now with exactly one question: '"+conceptAskUserQuestion+"' Then wait for my answer. After I answer, reply with exactly: COLOUR=<my answer>")

	conceptWait(t, 3*time.Minute, "ask_user park event", func() bool {
		dir := conceptFindSessionDir(home, sessionID)
		if dir == "" {
			return false
		}
		for _, event := range conceptReadEvents(dir) {
			eventType, _ := event["type"].(string)
			if eventType != "client_input_requested" {
				continue
			}
			data, _ := event["data"].(map[string]any)
			reason, _ := data["park_reason"].(string)
			questions, _ := data["questions"].([]any)
			if reason == "ask_user" || len(questions) > 0 {
				return true
			}
		}
		return false
	})
	dir := conceptFindSessionDir(home, sessionID)
	parkSnapshot := serve.snapshot(sessionID)

	serve.chat(sessionID, "blue")
	conceptWait(t, 3*time.Minute, "assistant reply after the answer", func() bool {
		for _, part := range conceptParts(t, dir) {
			if role, _ := part["role"].(string); role != "assistant" {
				continue
			}
			content, _ := part["content"].(string)
			if strings.Contains(strings.ToLower(content), "blue") {
				return true
			}
		}
		return false
	})
	conceptWaitIdle(t, serve, sessionID, time.Minute)

	evidence := conceptBaseEvidence("go", "ask_user_park", "main")
	evidence["session_id"] = sessionID
	evidence["session_live"] = conceptLiveView(serve.sessionJSON(sessionID))
	evidence["session"] = conceptSessionEvidence(t, serve, home, sessionID)
	evidence["park_snapshot"] = conceptSnapshotView(parkSnapshot)
	evidence["snapshot_after_answer"] = conceptSnapshotView(serve.snapshot(sessionID))
	evidence["model_requests"] = conceptModelRequests(proxy)

	checks := map[string]any{"park_event": false, "ask_user_tool_call": false, "resumed_after_answer": false}
	for _, event := range evidence["session"].(map[string]any)["events"].([]map[string]any) {
		if eventType, _ := event["type"].(string); eventType == "client_input_requested" {
			checks["park_event"] = true
		}
	}
	for _, record := range proxy.Requests() {
		for _, call := range record.ToolCalls {
			if strings.HasPrefix(call, "ask_user") {
				checks["ask_user_tool_call"] = true
			}
		}
	}
	for _, part := range evidence["session"].(map[string]any)["parts"].([]map[string]any) {
		if content, _ := part["content"].(string); strings.Contains(strings.ToLower(content), "blue") {
			if role, _ := part["role"].(string); role == "assistant" {
				checks["resumed_after_answer"] = true
			}
		}
	}
	evidence["checks"] = checks
	conceptWriteEvidence(t, outDir, evidence)

	for key, value := range checks {
		if value != true {
			t.Errorf("ask_user/park missing %s: %v", key, checks)
		}
	}
}

// ---- Scenario 4: resume after restart --------------------------------------

// conceptResumeAfterRestart forks a DM session into a colony, dispatches a
// worker, restarts `hive serve --colony`, and checks the colony's recorded
// session (queen_session_id) resumes live with its worker/tracker state intact.
func conceptResumeAfterRestart(t *testing.T, env conceptEnv, binary string) {
	outDir, home, serve, proxy := conceptSubtest(t, env, binary, "resume_after_restart", "main", nil, nil)

	dmID, _ := serve.createSession(map[string]any{"queen_name": "queen_growth"})
	serve.chat(dmID, "We are about to fork this conversation into a colony. Reply with OK.")
	conceptWaitIdle(t, serve, dmID, 2*time.Minute)

	status, raw := credDoJSON(t, serve.client, http.MethodPost, serve.base+"/api/sessions", map[string]any{
		"colony_id":         "cf_fork",
		"source_session_id": dmID,
		"initial_prompt":    "Use run_worker to dispatch exactly one worker with the task: get the current time (terminal date is fine) and report it back with report_to_parent. After the worker reports, reply with a one-line summary.",
	})
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("fork create = %d %s; logs:\n%s", status, raw, serve.logs.String())
	}
	var forked map[string]any
	_ = json.Unmarshal(raw, &forked)
	forkedID, _ := forked["session_id"].(string)
	if forkedID == "" {
		t.Fatalf("fork create returned no session_id: %s", raw)
	}
	colonyPath := filepath.Join(home, "colonies", "cf_fork")
	conceptWait(t, 4*time.Minute, "forked colony worker report", func() bool {
		for _, dir := range conceptFindWorkers(colonyPath) {
			if conceptWorkerReport(conceptParts(t, dir)) != nil {
				return true
			}
		}
		return false
	})
	conceptWaitIdle(t, serve, forkedID, 2*time.Minute)

	metadataRaw, _ := os.ReadFile(filepath.Join(colonyPath, "metadata.json"))
	var metadata map[string]any
	_ = json.Unmarshal(metadataRaw, &metadata)
	recordedID, _ := metadata["queen_session_id"].(string)
	before := map[string]any{
		"forked_session_id":               forkedID,
		"metadata_queen_session_id":       recordedID,
		"session_parts":                   len(conceptParts(t, conceptFindSessionDir(home, forkedID))),
		"worker_dirs":                     len(conceptFindWorkers(colonyPath)),
		"tracker":                         conceptTrackerDump(t, colonyPath),
		"model_requests_before_restart_n": len(proxy.Requests()),
	}
	serve.stop()

	restarted := conceptStartServePreloadedColony(t, binary, home, colonyPath)
	time.Sleep(2 * time.Second)
	listStatus, listRaw := credDoJSON(t, restarted.client, http.MethodGet, restarted.base+"/api/sessions", nil)
	var listBody map[string]any
	_ = json.Unmarshal(listRaw, &listBody)
	liveIDs := []string{}
	if sessions, ok := listBody["sessions"].([]any); ok {
		for _, entry := range sessions {
			session, _ := entry.(map[string]any)
			if id, _ := session["session_id"].(string); id != "" {
				liveIDs = append(liveIDs, id)
			}
		}
	}
	getStatus, getRaw := credDoJSON(t, restarted.client, http.MethodGet, restarted.base+"/api/sessions/"+forkedID, nil)
	chatStatus, chatRaw := credDoJSON(t, restarted.client, http.MethodPost, restarted.base+"/api/sessions/"+forkedID+"/chat", map[string]any{"message": "In one line: what did the worker report?"})
	resumedReply := false
	if chatStatus == http.StatusOK {
		conceptWait(t, 3*time.Minute, "resumed fork session reply", func() bool {
			for _, part := range conceptParts(t, conceptFindSessionDir(home, forkedID)) {
				if role, _ := part["role"].(string); role != "assistant" {
					continue
				}
				if content, _ := part["content"].(string); strings.TrimSpace(content) != "" {
					resumedReply = true
					return true
				}
			}
			return false
		})
	}
	conceptWaitIdle(t, restarted, forkedID, time.Minute)

	evidence := conceptBaseEvidence("go", "resume_after_restart", "main")
	evidence["session_id"] = forkedID
	evidence["dm_session_id"] = dmID
	evidence["before_restart"] = before
	evidence["after_restart"] = map[string]any{
		"list_status":      listStatus,
		"live_session_ids": liveIDs,
		"get_status":       getStatus,
		"get_body":         conceptCap(string(getRaw), 1200),
		"chat_status":      chatStatus,
		"chat_body":        conceptCap(string(chatRaw), 600),
		"session_parts":    len(conceptParts(t, conceptFindSessionDir(home, forkedID))),
		"worker_dirs":      len(conceptFindWorkers(colonyPath)),
		"tracker":          conceptTrackerDump(t, colonyPath),
	}
	checks := map[string]any{
		"metadata_records_session":       recordedID == forkedID,
		"session_live_after_restart":     containsStringValue(liveIDs, forkedID),
		"session_readable_after_restart": getStatus == http.StatusOK,
		"resumed_reply":                  resumedReply,
		"worker_dirs_intact":             len(conceptFindWorkers(colonyPath)) > 0,
		"tracker_intact":                 before["tracker"].(map[string]any)["exists"] == true,
	}
	evidence["checks"] = checks
	conceptWriteEvidence(t, outDir, evidence)

	for key, value := range checks {
		if value != true {
			t.Errorf("resume check %s = %v (evidence %s)", key, value, outDir)
		}
	}
}

func containsStringValue(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// conceptStartServePreloadedColony starts `hive serve --colony <path>`.
func conceptStartServePreloadedColony(t *testing.T, binary, home, colonyPath string) *conceptServe {
	t.Helper()
	port := credFreePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	cmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port), "--colony", colonyPath)
	cmd.Dir = t.TempDir()
	cmd.Env = append(conceptChildEnv(home), "CF_PROXY_KEY="+conceptProxyKey)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hive serve --colony: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	serve := &conceptServe{t: t, home: home, base: base, client: &http.Client{Timeout: 120 * time.Second}, cmd: cmd, logs: logs, exited: exited}
	t.Cleanup(serve.stop)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("hive serve --colony exited before /api/health; logs:\n%s", logs.String())
		default:
		}
		resp, err := serve.client.Get(base + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return serve
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hive serve --colony did not answer /api/health; logs:\n%s", logs.String())
	return nil
}

// conceptSortStrings keeps evidence stable across runs.
func conceptSortStrings(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
}
