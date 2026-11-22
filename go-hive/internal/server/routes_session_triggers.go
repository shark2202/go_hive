package server

// Parity: framework/server/routes_sessions.py::_slugify_trigger_id
// Parity: framework/server/routes_sessions.py::handle_create_trigger
// Parity: framework/server/routes_sessions.py::handle_update_trigger_task
// Parity: framework/server/routes_sessions.py::handle_run_trigger
// Parity: framework/server/routes_sessions.py::handle_resolve_missed_triggers
// Parity: framework/server/routes_sessions.py::handle_activate_trigger
// Parity: framework/server/routes_sessions.py::handle_deactivate_trigger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

func decodeTriggerBody(r *http.Request) (map[string]any, error) {
	var data []byte
	if r.Body != nil {
		var err error
		data, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("body is not a JSON object")
	}
	return body, nil
}

func copyTriggerConfig(config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = value
	}
	return out
}

func triggerEntryNode(session *Session) any {
	if session == nil || session.EntryNode == "" {
		return nil
	}
	return session.EntryNode
}

func triggerName(def triggers.TriggerDefinition) string {
	if def.Description != "" {
		return def.Description
	}
	return def.ID
}

func triggerNotFound(w http.ResponseWriter, triggerID string) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": "Trigger '" + triggerID + "' not found",
	})
}

func nextFireConfigOut(session *Session, triggerID string, config map[string]any) map[string]any {
	out := copyTriggerConfig(config)
	if mono, ok := session.TriggerNextFire()[triggerID]; ok {
		remaining := math.Max(0, mono-session.NowMonotonic())
		out["next_fire_in"] = remaining
		out["next_fire_at"] = int64((session.NowWall() + remaining) * 1000)
	}
	return out
}

func handleCreateTrigger(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	body, err := decodeTriggerBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'name' is required"})
		return
	}
	task, _ := body["task"].(string)
	if strings.TrimSpace(task) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'task' is required"})
		return
	}

	triggerType := "timer"
	if value, present := body["trigger_type"]; present {
		triggerType, _ = value.(string)
	}
	if triggerType != "timer" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Only 'timer' schedules can be created here.",
		})
		return
	}

	rawConfig, ok := body["trigger_config"].(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'trigger_config' must be an object"})
		return
	}

	cronRaw := rawConfig["cron"]
	intervalRaw := rawConfig["interval_minutes"]
	cronStr := ""
	if cronRaw != nil {
		typed, isString := cronRaw.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "'trigger_config.cron' must be a string",
			})
			return
		}
		cronStr = typed
	}

	var triggerConfig map[string]any
	if cronStr != "" {
		if !triggers.IsValidCron(cronStr) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "Invalid cron expression: " + cronStr,
			})
			return
		}
		triggerConfig = map[string]any{"cron": cronStr}
	} else if intervalRaw == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Timer trigger needs 'cron' or 'interval_minutes' in trigger_config.",
		})
		return
	} else if minutes, numeric := numericValue(intervalRaw); !numeric || minutes <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "'trigger_config.interval_minutes' must be > 0",
		})
		return
	} else {
		triggerConfig = map[string]any{"interval_minutes": intervalRaw}
	}

	existing := map[string]struct{}{}
	for _, def := range session.AvailableTriggers() {
		existing[def.ID] = struct{}{}
	}
	triggerID := slugifyTriggerID(name, existing)

	def := triggers.TriggerDefinition{
		ID:            triggerID,
		TriggerType:   "timer",
		TriggerConfig: triggerConfig,
		Description:   strings.TrimSpace(name),
		Task:          strings.TrimSpace(task),
	}
	session.PutTrigger(def)

	if err := StartTriggerTimer(session, triggerID, &def); err != nil {
		session.RemoveTrigger(triggerID)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to start trigger timer: " + err.Error(),
		})
		return
	}

	def.Enabled = true
	session.PutTrigger(def)
	session.ActivateTrigger(triggerID)
	PersistActiveTriggers(session, sessionID)
	_ = saveTriggerToAgent(session, triggerID, &def)

	if session.EventBus != nil {
		configOut := nextFireConfigOut(session, triggerID, def.TriggerConfig)
		event := eventbus.NewAgentEvent(eventbus.EventTriggerActivated, "queen")
		event.Data = map[string]any{
			"trigger_id":     triggerID,
			"trigger_type":   "timer",
			"trigger_config": configOut,
			"name":           triggerName(def),
			"entry_node":     triggerEntryNode(session),
		}
		session.EventBus.Publish(event)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"trigger_id":     triggerID,
		"name":           def.Description,
		"trigger_type":   "timer",
		"trigger_config": def.TriggerConfig,
	})
}

func validateMergedTimerConfig(session *Session, merged map[string]any) string {
	_ = session
	cronRaw := merged["cron"]
	cronStr := ""
	if cronRaw != nil {
		typed, isString := cronRaw.(string)
		if !isString {
			return "'trigger_config.cron' must be a string"
		}
		cronStr = typed
	}
	intervalRaw := merged["interval_minutes"]
	if cronStr != "" {
		if !triggers.IsValidCron(cronStr) {
			return "Invalid cron expression: " + cronStr
		}
		delete(merged, "interval_minutes")
		return ""
	}
	if intervalRaw == nil {
		return "Timer trigger needs 'cron' or 'interval_minutes' in trigger_config."
	}
	if minutes, numeric := numericValue(intervalRaw); !numeric || minutes <= 0 {
		return "'trigger_config.interval_minutes' must be > 0"
	}
	return ""
}

func handleUpdateTrigger(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	triggerID := r.PathValue("trigger_id")
	def, ok := session.LookupTrigger(triggerID)
	if !ok {
		triggerNotFound(w, triggerID)
		return
	}

	body, err := decodeTriggerBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	hasTask := false
	hasConfig := false

	if taskRaw, present := body["task"]; present {
		task, isString := taskRaw.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'task' must be a string"})
			return
		}
		def.Task = task
		session.PutTrigger(def)
		hasTask = true
	}

	if configRaw, present := body["trigger_config"]; present && configRaw != nil {
		update, isObject := configRaw.(map[string]any)
		if !isObject {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "'trigger_config' must be an object",
			})
			return
		}
		merged := copyTriggerConfig(def.TriggerConfig)
		for key, value := range update {
			merged[key] = value
		}
		if def.TriggerType == "timer" {
			if message := validateMergedTimerConfig(session, merged); message != "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": message})
				return
			}
		}
		def.TriggerConfig = merged
		session.PutTrigger(def)
		hasConfig = true
	}

	if !hasTask && !hasConfig {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Provide at least one of 'task' or 'trigger_config'",
		})
		return
	}

	if hasConfig && session.IsTriggerActive(triggerID) {
		if timer := session.PopActiveTimer(triggerID); timer != nil {
			timer.Stop()
		}
		session.PopTriggerNextFire(triggerID)
		if subID := session.PopActiveWebhookSub(triggerID); subID != "" && session.EventBus != nil {
			session.EventBus.Unsubscribe(subID)
		}
		switch def.TriggerType {
		case "timer":
			_ = StartTriggerTimer(session, triggerID, &def)
		case "webhook":
			_ = StartTriggerWebhook(session, triggerID, &def)
		}
	}

	if session.IsTriggerActive(triggerID) {
		PersistActiveTriggers(session, sessionID)
	}
	_ = saveTriggerToAgent(session, triggerID, &def)

	if session.EventBus != nil {
		event := eventbus.NewAgentEvent(eventbus.EventTriggerUpdated, "queen")
		event.Data = map[string]any{
			"trigger_id":     triggerID,
			"task":           def.Task,
			"trigger_config": def.TriggerConfig,
			"trigger_type":   def.TriggerType,
			"name":           triggerName(def),
			"entry_node":     triggerEntryNode(session),
		}
		session.EventBus.Publish(event)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"trigger_id":     triggerID,
		"task":           def.Task,
		"trigger_config": def.TriggerConfig,
	})
}

func handleRunTrigger(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	triggerID := r.PathValue("trigger_id")
	def, ok := session.LookupTrigger(triggerID)
	if !ok {
		triggerNotFound(w, triggerID)
		return
	}

	if session.ColonyID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Colony not loaded"})
		return
	}
	node := queenNode(session)
	if node == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Queen not ready"})
		return
	}

	event := agentloop.NewTriggerEvent(def.TriggerType, triggerID)
	event.Payload = map[string]any{
		"task":           def.Task,
		"trigger_config": def.TriggerConfig,
		"forced":         true,
	}
	if err := injectTrigger(node, event); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to fire trigger: " + err.Error(),
		})
		return
	}

	EmitTriggerFired(session, triggerID, def.TriggerType)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "fired",
		"trigger_id": triggerID,
	})
}

func handleResolveMissedTriggers(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	if session.ColonyID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Colony not loaded"})
		return
	}

	body, err := decodeTriggerBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	decisions := map[string]any{}
	if raw, present := body["decisions"]; present && pyTruthy(raw) {
		typed, isObject := raw.(map[string]any)
		if !isObject {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "decisions must be an object {trigger_id: decision}",
			})
			return
		}
		decisions = typed
	}

	results, err := ResolveMissed(session, decisions)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to resolve missed triggers: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func handleActivateTrigger(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	triggerID := r.PathValue("trigger_id")
	def, ok := session.LookupTrigger(triggerID)
	if !ok {
		triggerNotFound(w, triggerID)
		return
	}

	if session.IsTriggerActive(triggerID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "already_active",
			"trigger_id": triggerID,
		})
		return
	}

	switch def.TriggerType {
	case "timer":
		if err := StartTriggerTimer(session, triggerID, &def); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to start trigger: " + err.Error(),
			})
			return
		}
	case "webhook":
		if err := StartTriggerWebhook(session, triggerID, &def); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to start trigger: " + err.Error(),
			})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Unsupported trigger type: " + def.TriggerType,
		})
		return
	}

	def.Enabled = true
	session.PutTrigger(def)
	session.ActivateTrigger(triggerID)
	PersistActiveTriggers(session, sessionID)

	if session.EventBus != nil {
		configOut := nextFireConfigOut(session, triggerID, def.TriggerConfig)
		if stat, present := session.TriggerFireStats()[triggerID]; present {
			configOut["fire_count"] = stat.FireCount
			if stat.LastFiredAt != nil {
				configOut["last_fired_at"] = *stat.LastFiredAt
			}
		}
		event := eventbus.NewAgentEvent(eventbus.EventTriggerActivated, "queen")
		event.Data = map[string]any{
			"trigger_id":     triggerID,
			"trigger_type":   def.TriggerType,
			"trigger_config": configOut,
			"name":           triggerName(def),
		}
		session.EventBus.Publish(event)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "activated",
		"trigger_id": triggerID,
	})
}

func handleDeactivateTrigger(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	triggerID := r.PathValue("trigger_id")
	if !session.IsTriggerActive(triggerID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "already_inactive",
			"trigger_id": triggerID,
		})
		return
	}

	if timer := session.PopActiveTimer(triggerID); timer != nil {
		timer.Stop()
	}
	session.PopTriggerNextFire(triggerID)
	if subID := session.PopActiveWebhookSub(triggerID); subID != "" && session.EventBus != nil {
		session.EventBus.Unsubscribe(subID)
	}
	session.DeactivateTrigger(triggerID)

	name := triggerID
	if def, ok := session.LookupTrigger(triggerID); ok {
		def.Enabled = false
		session.PutTrigger(def)
		name = triggerName(def)
	}

	PersistActiveTriggers(session, sessionID)

	if session.EventBus != nil {
		event := eventbus.NewAgentEvent(eventbus.EventTriggerDeactivated, "queen")
		event.Data = map[string]any{
			"trigger_id": triggerID,
			"name":       name,
		}
		session.EventBus.Publish(event)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "deactivated",
		"trigger_id": triggerID,
	})
}
