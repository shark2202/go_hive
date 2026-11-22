package server

// Trigger lifecycle tools: set_trigger / list_triggers / remove_trigger.
//
// The session already owns the trigger store (definitions on
// Session.AvailableTriggerDefs, activation on Session.ActiveTriggers, the
// timer/webhook runtime in trigger_runtime.go). These tools are the agent's
// entry point onto that same store — they never invent a second one, so a
// trigger activated here is the trigger the REST routes and the timer loop see.
//
// Parity: framework/tools/queen_lifecycle_tools.py::set_trigger
// Parity: framework/tools/queen_lifecycle_tools.py::remove_trigger
// Parity: framework/tools/queen_lifecycle_tools.py::list_triggers

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

// validWebhookMethods is upstream's valid_methods set for webhook triggers.
var validWebhookMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {}, "OPTIONS": {},
}

// setTrigger activates a trigger, creating it when the caller supplied a type
// and config. Activation validates the config, starts the timer/webhook
// runtime, marks the definition enabled and persists both the session state and
// the agent's triggers.json.
//
// Parity: framework/tools/queen_lifecycle_tools.py::set_trigger
func (b *QueenLifecycleBinding) setTrigger(input map[string]any) (string, bool) {
	triggerID := queenInputString(input, "trigger_id")
	if b.session.IsTriggerActive(triggerID) {
		return queenLifecycleJSON(map[string]any{"error": "Trigger '" + triggerID + "' is already active."}), true
	}

	triggerType := queenInputString(input, "trigger_type")
	triggerConfig := mapDefault(input["trigger_config"])
	task := queenInputString(input, "task")

	def, found := b.session.LookupTrigger(triggerID)
	if !found {
		if triggerType != "" && len(triggerConfig) > 0 {
			def = triggers.TriggerDefinition{
				ID:            triggerID,
				TriggerType:   triggerType,
				TriggerConfig: copyTriggerConfig(triggerConfig),
			}
			b.session.PutTrigger(def)
		} else {
			return queenLifecycleJSON(map[string]any{
				"error": "Trigger '" + triggerID + "' not found. Provide trigger_type and trigger_config to create a custom trigger.",
			}), true
		}
	}

	if task != "" {
		def.Task = task
	}
	if def.Task == "" {
		return queenLifecycleJSON(map[string]any{
			"error": "Trigger '" + triggerID + "' has no task configured. Set a task describing what the worker should do when this trigger fires.",
		}), true
	}

	tType := def.TriggerType
	if triggerType != "" {
		tType = triggerType
		def.TriggerType = tType
	}
	tConfig := def.TriggerConfig
	if len(triggerConfig) > 0 {
		tConfig = copyTriggerConfig(triggerConfig)
		def.TriggerConfig = tConfig
	}
	b.session.PutTrigger(def)

	if tType == "webhook" {
		path := strings.TrimSpace(stringValue(tConfig["path"]))
		if path == "" || !strings.HasPrefix(path, "/") {
			return queenLifecycleJSON(map[string]any{
				"error": "Webhook trigger requires 'path' starting with '/' in trigger_config (e.g. '/hooks/github').",
			}), true
		}
		methods := triggerMethodList(tConfig)
		invalid := make([]string, 0)
		for _, method := range methods {
			upper := strings.ToUpper(method)
			if _, ok := validWebhookMethods[upper]; !ok {
				invalid = append(invalid, upper)
			}
		}
		if len(invalid) > 0 {
			sort.Strings(invalid)
			valid := make([]string, 0, len(validWebhookMethods))
			for method := range validWebhookMethods {
				valid = append(valid, method)
			}
			sort.Strings(valid)
			return queenLifecycleJSON(map[string]any{
				"error": fmt.Sprintf("Invalid HTTP methods: %v. Valid: %v", invalid, valid),
			}), true
		}

		if err := StartTriggerWebhook(b.session, triggerID, &def); err != nil {
			return queenLifecycleJSON(map[string]any{"error": "Failed to start webhook trigger: " + err.Error()}), true
		}
		def.Enabled = true
		b.session.PutTrigger(def)
		b.session.ActivateTrigger(triggerID)
		PersistActiveTriggers(b.session, b.session.ID)
		_ = saveTriggerToAgent(b.session, triggerID, &def)
		b.publishTriggerActivated(triggerID, tType, tConfig, def)
		port := 8090
		if value, ok := floatValue(tConfig["port"]); ok {
			port = int(value)
		}
		return queenLifecycleJSON(map[string]any{
			"status":       "activated",
			"trigger_id":   triggerID,
			"trigger_type": tType,
			"webhook_url":  fmt.Sprintf("http://127.0.0.1:%d%s", port, path),
		}), false
	}

	if tType != "timer" {
		return queenLifecycleJSON(map[string]any{"error": "Unsupported trigger type: " + tType}), true
	}

	if cronExpr, ok := tConfig["cron"].(string); ok && cronExpr != "" {
		if !triggers.IsValidCron(cronExpr) {
			return queenLifecycleJSON(map[string]any{"error": "Invalid cron expression: " + cronExpr}), true
		}
	} else if interval, present := tConfig["interval_minutes"]; present && pyTruthy(interval) {
		minutes, ok := floatValue(interval)
		if !ok || minutes <= 0 {
			return queenLifecycleJSON(map[string]any{
				"error": fmt.Sprintf("interval_minutes must be > 0, got %v", interval),
			}), true
		}
	} else {
		return queenLifecycleJSON(map[string]any{
			"error": "Timer trigger needs 'cron' or 'interval_minutes' in trigger_config.",
		}), true
	}

	if err := StartTriggerTimer(b.session, triggerID, &def); err != nil {
		return queenLifecycleJSON(map[string]any{"error": "Failed to start trigger timer: " + err.Error()}), true
	}
	def.Enabled = true
	b.session.PutTrigger(def)
	b.session.ActivateTrigger(triggerID)

	PersistActiveTriggers(b.session, b.session.ID)
	_ = saveTriggerToAgent(b.session, triggerID, &def)
	b.publishTriggerActivated(triggerID, tType, tConfig, def)

	return queenLifecycleJSON(map[string]any{
		"status":         "activated",
		"trigger_id":     triggerID,
		"trigger_type":   tType,
		"trigger_config": tConfig,
	}), false
}

// removeTrigger deactivates an active trigger: it cancels the timer, drops the
// webhook subscription, clears the active flag and removes the definition from
// the agent's triggers.json (the trigger stays in the session catalog for
// re-activation).
//
// Parity: framework/tools/queen_lifecycle_tools.py::remove_trigger
func (b *QueenLifecycleBinding) removeTrigger(input map[string]any) (string, bool) {
	triggerID := queenInputString(input, "trigger_id")
	if !b.session.IsTriggerActive(triggerID) {
		return queenLifecycleJSON(map[string]any{"error": "Trigger '" + triggerID + "' is not active."}), true
	}

	if timer := b.session.PopActiveTimer(triggerID); timer != nil {
		timer.Stop()
	}
	b.session.PopTriggerNextFire(triggerID)
	if subID := b.session.PopActiveWebhookSub(triggerID); subID != "" && b.session.EventBus != nil {
		b.session.EventBus.Unsubscribe(subID)
	}
	b.session.DeactivateTrigger(triggerID)

	name := triggerID
	if def, ok := b.session.LookupTrigger(triggerID); ok {
		def.Enabled = false
		b.session.PutTrigger(def)
		name = triggerName(def)
	}

	PersistActiveTriggers(b.session, b.session.ID)
	_ = triggers.RemoveTriggerFromAgent(workerPathSession{b.session}, triggerID)

	if b.session.EventBus != nil {
		event := eventbus.NewAgentEvent(eventbus.EventTriggerDeactivated, "queen")
		event.Data = map[string]any{"trigger_id": triggerID, "name": name}
		b.session.EventBus.Publish(event)
	}

	return queenLifecycleJSON(map[string]any{"status": "deactivated", "trigger_id": triggerID}), false
}

// listTriggers returns every trigger the session knows, with its live
// activation state.
//
// Parity: framework/tools/queen_lifecycle_tools.py::list_triggers
func (b *QueenLifecycleBinding) listTriggers() string {
	defs := b.session.AvailableTriggers()
	rows := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		config := copyTriggerConfig(def.TriggerConfig)
		rows = append(rows, map[string]any{
			"id":             def.ID,
			"trigger_type":   def.TriggerType,
			"trigger_config": config,
			"description":    def.Description,
			"task":           def.Task,
			// The active set is the authoritative "will fire" state in this
			// port: definitions survive deactivation, so def.enabled alone
			// would report a re-loadable trigger as live.
			"enabled": b.session.IsTriggerActive(def.ID),
		})
	}
	return queenLifecycleJSON(map[string]any{"triggers": rows})
}

// publishTriggerActivated announces the activation on the session bus, using
// the same payload shape the REST activation route publishes.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_publish_trigger_activated
func (b *QueenLifecycleBinding) publishTriggerActivated(triggerID, triggerType string, config map[string]any, def triggers.TriggerDefinition) {
	if b.session.EventBus == nil {
		return
	}
	configOut := nextFireConfigOut(b.session, triggerID, config)
	event := eventbus.NewAgentEvent(eventbus.EventTriggerActivated, "queen")
	payload := map[string]any{
		"trigger_id":     triggerID,
		"trigger_type":   triggerType,
		"trigger_config": configOut,
		"name":           triggerName(def),
	}
	if entryNode := triggerEntryNode(b.session); entryNode != nil {
		payload["entry_node"] = entryNode
	}
	event.Data = payload
	b.session.EventBus.Publish(event)
}

// triggerMethodList reads a webhook trigger's methods, defaulting to POST.
func triggerMethodList(config map[string]any) []string {
	raw, present := config["methods"]
	if !present {
		return []string{"POST"}
	}
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, stringValue(item))
		}
		return out
	case string:
		return []string{typed}
	default:
		return []string{"POST"}
	}
}
