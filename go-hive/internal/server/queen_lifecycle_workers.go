package server

// Colony-runtime lifecycle tools: stop_worker, inject_message,
// update_worker_profile and list_browser_profiles.
//
// The first two drive the bound colony runtime; the profile tools read and
// write the colony's worker-profile registry on disk (the same store the
// /api/colonies/{id}/worker-profiles routes use), and list_browser_profiles
// asks the local browser bridge for its connected Chrome profiles.
//
// Parity: framework/tools/queen_lifecycle_tools.py::stop_worker
// Parity: framework/tools/queen_lifecycle_tools.py::inject_message
// Parity: framework/tools/queen_lifecycle_tools.py::update_worker_profile
// Parity: framework/tools/queen_lifecycle_tools.py::list_browser_profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/playbook"
)

// InjectMessage sends one message into the colony's live worker conversations.
//
// Upstream injects into the loaded graph's active node. This port's colony has
// a worker registry instead of a single graph, so the message is delivered to
// every active worker and the delivered worker ids are reported back. The
// status/node_id/content_preview keys keep the upstream shape.
//
// Parity: framework/tools/queen_lifecycle_tools.py::inject_message
func (b *QueenLifecycleBinding) injectMessage(input map[string]any) (string, bool) {
	runtime := sessionColonyRuntime(b.session)
	if runtime == nil {
		return queenLifecycleJSON(map[string]any{"error": "No colony running in this session."}), true
	}
	content := queenInputString(input, "content")
	delivered := []string{}
	failed := []string{}
	for _, worker := range runtime.WorkerList() {
		if worker == nil || worker.IsPersistent() || !worker.IsActive() {
			continue
		}
		if err := runtime.InjectInput(worker.ID, content); err != nil {
			failed = append(failed, worker.ID)
			continue
		}
		delivered = append(delivered, worker.ID)
	}
	if len(delivered) == 0 {
		return queenLifecycleJSON(map[string]any{
			"error": "No active graph node found — graph may be idle.",
		}), true
	}
	sort.Strings(delivered)
	nodeID := ""
	if len(delivered) == 1 {
		nodeID = delivered[0]
	}
	payload := map[string]any{
		"status":          "delivered",
		"node_id":         nodeID,
		"worker_ids":      delivered,
		"delivered_count": len(delivered),
		"content_preview": charPrefix(content, 100),
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		payload["not_delivered"] = failed
	}
	return queenLifecycleJSON(payload), false
}

// StopWorker halts all colony work: it cancels running playbook loops, gives
// live workers a bounded window to file their final report, then runs the
// authoritative stop sweep (queued workers included) and pauses the session's
// timer triggers.
//
// Parity: framework/tools/queen_lifecycle_tools.py::stop_worker
func (b *QueenLifecycleBinding) stopWorker(ctx context.Context, input map[string]any) (string, bool) {
	concrete := sessionColonyRuntime(b.session)
	if concrete == nil {
		return queenLifecycleJSON(map[string]any{"error": "No runtime on this session."}), true
	}
	reason := queenInputString(input, "reason")
	if reason == "" {
		reason = "Stopped by queen"
	}
	grace := defaultStopGraceSeconds
	if value, ok := floatValue(input["grace_seconds"]); ok {
		grace = value
	}
	// Clamp the grace window under the tool-call budget (see the module
	// constants) so this tool always returns cleanly instead of timing out
	// and triggering a retry loop.
	if grace < 0 {
		grace = 0
	}
	if grace > maxStopGraceSeconds {
		grace = maxStopGraceSeconds
	}

	errors := []string{}
	playbooksStopped := playbook.StopPlaybooksForColony(concrete)

	// Snapshot live worker ids BEFORE injecting — the stop mutates the
	// registry, so a stable list is needed for the report.
	liveIDs := []string{}
	queuedIDs := []string{}
	for _, info := range concrete.ListWorkers() {
		worker := concrete.GetWorker(info.WorkerID)
		if worker == nil || worker.IsPersistent() {
			continue
		}
		if info.Status == colony.WorkerQueued {
			queuedIDs = append(queuedIDs, info.WorkerID)
			continue
		}
		if worker.IsActive() {
			liveIDs = append(liveIDs, info.WorkerID)
		}
	}

	reports := []map[string]any{}
	if len(liveIDs) > 0 && grace > 0 {
		stopMessage := fmt.Sprintf(
			"[STOP REQUESTED] %s. Call report_to_parent immediately with your latest progress, "+
				"decisions, and partial findings. You have ~%.0fs before a hard stop — anything not "+
				"reported by then will be lost.", reason, grace)
		for _, workerID := range liveIDs {
			worker := concrete.GetWorker(workerID)
			if worker == nil || !worker.IsActive() {
				continue
			}
			// The worker may have just filed a report on its own; don't nudge
			// a finished worker into emitting a redundant turn.
			if worker.ExplicitReport() != nil {
				continue
			}
			if err := concrete.InjectInput(workerID, stopMessage); err != nil {
				errors = append(errors, fmt.Sprintf("stop-inject %s: %v", workerID, err))
			}
		}
		waitCtx := context.WithoutCancel(context.Background())
		if ctx != nil {
			waitCtx = context.WithoutCancel(ctx)
		}
		if collected, err := concrete.WaitForWorkerReports(waitCtx, liveIDs, time.Duration(grace*float64(time.Second))); err == nil {
			reports = collected
		} else {
			errors = append(errors, "collect reports: "+err.Error())
		}
	}

	// Authoritative hard stop for EVERYTHING non-persistent — queued workers,
	// any live laggard that ignored the grace inject, and anything spawned in
	// the gap. The persistent overseer is spared.
	summary, err := concrete.StopWorkers(false)
	if err != nil {
		errors = append(errors, "stop sweep: "+err.Error())
	}
	if timedOut, ok := summary["timed_out"].([]string); ok && len(timedOut) > 0 {
		sorted := append([]string(nil), timedOut...)
		sort.Strings(sorted)
		errors = append(errors, "force-stopped (unresponsive): "+strings.Join(sorted, ", "))
	}

	workersStopped := len(liveIDs) + len(queuedIDs)
	timersPaused := b.pauseTriggerTimers()
	status := "no_active_workers"
	if workersStopped > 0 || len(playbooksStopped) > 0 {
		status = "stopped"
	}

	var errorValue any
	if len(errors) > 0 {
		errorValue = errors
	}
	return queenLifecycleJSON(map[string]any{
		"status":            status,
		"workers_stopped":   workersStopped,
		"queued_stopped":    len(queuedIDs),
		"playbooks_stopped": playbooksStopped,
		"reports":           reports,
		"timers_paused":     timersPaused,
		"reason":            reason,
		"errors":            errorValue,
	}), false
}

// pauseTriggerTimers cancels the session's active timer triggers (their
// definitions and activation stay in place, so a restart resumes them the way
// upstream's paused legacy timers resume). Reports whether any timer was
// actually cancelled.
//
// Parity: framework/tools/queen_lifecycle_tools.py::stop_worker (pause_timers branch)
func (b *QueenLifecycleBinding) pauseTriggerTimers() bool {
	if b.session == nil {
		return false
	}
	paused := false
	for triggerID := range b.session.ActiveTriggerIDs() {
		def, ok := b.session.LookupTrigger(triggerID)
		if !ok || def.TriggerType != "timer" {
			continue
		}
		if timer := b.session.PopActiveTimer(triggerID); timer != nil {
			timer.Stop()
			b.session.PopTriggerNextFire(triggerID)
			paused = true
		}
	}
	return paused
}

// updateWorkerProfile inserts or updates one worker profile on an existing
// colony, preserving siblings. Only the fields the caller supplied are
// changed; an explicit empty object/list clears the field.
//
// Parity: framework/tools/queen_lifecycle_tools.py::update_worker_profile
func (b *QueenLifecycleBinding) updateWorkerProfile(input map[string]any) (string, bool) {
	colonyID := strings.TrimSpace(queenInputString(input, "colony_id"))
	if !validColonyNameSegment(colonyID) {
		return queenLifecycleJSON(map[string]any{"error": "colony_id must be lowercase alphanumeric with underscores."}), true
	}
	profileName := queenInputString(input, "profile_name")
	if err := validateWorkerProfileName(profileName); err != "" {
		return queenLifecycleJSON(map[string]any{"error": err}), true
	}
	if b.home.Root == "" {
		return queenLifecycleJSON(map[string]any{"error": "Colony '" + colonyID + "' not found"}), true
	}

	merged := WorkerProfile{Name: profileName, Integrations: map[string]string{}}
	for _, existing := range listWorkerProfilesWithHome(b.home, colonyID) {
		if existing.Name != profileName {
			continue
		}
		merged = WorkerProfile{
			Name:            existing.Name,
			Task:            existing.Task,
			SkillName:       existing.SkillName,
			Integrations:    map[string]string{},
			ConcurrencyHint: existing.ConcurrencyHint,
			PromptOverride:  existing.PromptOverride,
			ToolFilter:      append([]any(nil), existing.ToolFilter...),
			BrowserProfile:  existing.BrowserProfile,
		}
		for key, value := range existing.Integrations {
			merged.Integrations[key] = value
		}
		break
	}

	if queenInputPresent(input, "integrations") {
		integrations := map[string]string{}
		for key, raw := range mapDefault(input["integrations"]) {
			name := stringValue(key)
			value := stringValue(raw)
			if name != "" && value != "" {
				integrations[name] = value
			}
		}
		merged.Integrations = integrations
	}
	if queenInputPresent(input, "task") {
		merged.Task = stringValue(input["task"])
	}
	if queenInputPresent(input, "skill_name") {
		merged.SkillName = stringValue(input["skill_name"])
	}
	if queenInputPresent(input, "concurrency_hint") {
		if hint, ok := pythonIntValue(input["concurrency_hint"]); ok && hint > 0 {
			merged.ConcurrencyHint = &hint
		} else {
			merged.ConcurrencyHint = nil
		}
	}
	if queenInputPresent(input, "prompt_override") {
		override := input["prompt_override"]
		if !pyTruthy(override) {
			override = nil
		}
		merged.PromptOverride = override
	}
	if queenInputPresent(input, "tool_filter") {
		merged.ToolFilter = nil
		if filter, ok := queenStringSlice(input["tool_filter"]); ok && len(filter) > 0 {
			merged.ToolFilter = make([]any, 0, len(filter))
			for _, name := range filter {
				merged.ToolFilter = append(merged.ToolFilter, name)
			}
		}
	}
	if queenInputPresent(input, "browser_profile") {
		merged.BrowserProfile = strings.TrimSpace(stringValue(input["browser_profile"]))
	}

	saved, err := upsertWorkerProfile(b.home, colonyID, merged)
	if err != nil {
		return queenLifecycleJSON(map[string]any{"error": err.Error()}), true
	}
	profiles := make([]any, 0, len(saved))
	for _, profile := range saved {
		profiles = append(profiles, profile.ToDict())
	}
	return queenLifecycleJSON(map[string]any{
		"ok":              true,
		"colony_id":       colonyID,
		"profile_name":    profileName,
		"worker_profiles": profiles,
	}), false
}

// listBrowserProfiles lists the Chrome profiles whose Hive extension is
// connected, by asking the local browser bridge's status port for /profiles.
//
// No bridge (or no profiles) yields an explicit empty list — never a
// fabricated one: the payload reports ok=false with an error and hint when the
// bridge is unreachable, and ok=true with a hint when it is reachable but no
// Chrome profile has the extension connected.
//
// Parity: framework/tools/queen_lifecycle_tools.py::list_browser_profiles
func (b *QueenLifecycleBinding) listBrowserProfiles(_ context.Context) string {
	// The bridge serves /profiles on its status port (WS port + 1); the legacy
	// 9230 status endpoint is probed too, as upstream does during migration.
	for _, addr := range queenBrowserBridgeStatusAddrs() {
		raw, err := bridgeHTTPRequest(addr, "GET /profiles HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n", browserProbeTimeout)
		if err != nil {
			continue
		}
		body, ok := bridgeResponseBody(raw)
		if !ok {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var data map[string]any
		if err := decoder.Decode(&data); err != nil || data == nil {
			continue
		}
		profiles := []map[string]any{}
		for _, raw := range queenListValue(data["profiles"]) {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			profiles = append(profiles, map[string]any{
				"label":            entry["label"],
				"is_default":       pyTruthy(entry["is_default"]),
				"starred":          pyTruthy(entry["starred"]),
				"version":          entry["version"],
				"protocol_version": entry["protocol_version"],
			})
		}
		out := map[string]any{"ok": true, "profiles": profiles}
		if len(profiles) == 0 {
			out["hint"] = "No Chrome profiles are connected. In each Chrome profile, install/enable the " +
				"Hive Browser Bridge extension and set its label in the side panel, then retry."
		}
		return queenLifecycleJSON(out)
	}
	return queenLifecycleJSON(map[string]any{
		"ok":       false,
		"error":    "browser bridge not reachable",
		"hint":     "Is the Hive app running? The browser bridge serves /profiles on 127.0.0.1.",
		"profiles": []map[string]any{},
	})
}

// queenBrowserBridgeStatusAddrs is the probe list for list_browser_profiles:
// HIVE_BRIDGE_PORT (default 14829) + 1, then the legacy 9230 status endpoint.
// It is a var so tests can point the probe at a local listener instead of a
// real bridge.
//
// Parity: framework/tools/queen_lifecycle_tools.py::list_browser_profiles
var queenBrowserBridgeStatusAddrs = func() []string {
	port := 14829
	if raw := strings.TrimSpace(os.Getenv("HIVE_BRIDGE_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}
	return []string{
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1)),
		net.JoinHostPort("127.0.0.1", "9230"),
	}
}
