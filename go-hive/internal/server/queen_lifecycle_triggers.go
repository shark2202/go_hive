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
	"math"
	"sort"
	"strconv"
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
		methods, invalid := triggerMethodList(tConfig)
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

		// Python derives the listening port with int(config["port"]) before
		// reporting the URL; reject a non-numeric value instead of silently
		// binding 8090, and hand the validated number to the runtime so the
		// listener and the reported URL cannot disagree.
		port := 8090
		if rawPort, present := tConfig["port"]; present {
			parsed, ok := triggerPortValue(rawPort)
			if !ok {
				return queenLifecycleJSON(map[string]any{
					"error": fmt.Sprintf("Webhook trigger 'port' must be a number between 1 and 65535, got %v", rawPort),
				}), true
			}
			port = parsed
			tConfig["port"] = port
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
		// Upstream requires isinstance(interval, (int, float)); a numeric
		// string is not a number there, so numericValue (not floatValue)
		// keeps "5" rejected exactly as Python rejects it.
		minutes, numeric := numericValue(interval)
		if !numeric || minutes <= 0 {
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

// triggerMethodList reads a webhook trigger's methods. Upstream defaults to
// ["POST"] and then iterates whatever it got (`[m.upper() for m in methods]`),
// so a bare string is iterated character by character and a non-string entry
// raises. This port rejects every shape that is not a list of strings instead
// of coercing it: the returned invalid entries carry the offending values (a
// bare string expands to its characters, exactly as Python would iterate it,
// and an empty string has no characters to report) so the caller reports them
// through Python's "Invalid HTTP methods" error.
//
// Parity: framework/tools/queen_lifecycle_tools.py::set_trigger
func triggerMethodList(config map[string]any) ([]string, []string) {
	raw, present := config["methods"]
	if !present {
		return []string{"POST"}, nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed, nil
	case []any:
		methods := make([]string, 0, len(typed))
		var invalid []string
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				invalid = append(invalid, methodValueRepr(item))
				continue
			}
			methods = append(methods, text)
		}
		return methods, invalid
	case string:
		if typed == "" {
			return nil, []string{`""`}
		}
		invalid := make([]string, 0, len(typed))
		for _, char := range typed {
			invalid = append(invalid, strings.ToUpper(string(char)))
		}
		return nil, invalid
	default:
		return nil, []string{methodValueRepr(raw)}
	}
}

// methodValueRepr renders a non-string methods entry the way Python would
// print the offending value in the invalid-method error.
func methodValueRepr(value any) string {
	if value == nil {
		return "None"
	}
	return fmt.Sprint(value)
}

// triggerPortValue parses a webhook trigger's "port" the way upstream's
// int(t_config.get("port", 8090)) does: numbers and numeric strings are
// accepted, floats truncate toward zero, and anything else is rejected
// instead of silently falling back to 8090. The result must still be a
// bindable TCP port, so the caller reports a bad value before starting the
// listener and hands the parsed number to the runtime.
//
// Parity: framework/tools/queen_lifecycle_tools.py::set_trigger
func triggerPortValue(value any) (int, bool) {
	if number, ok := numericValue(value); ok {
		if math.IsNaN(number) || number < 1 || number > 65535 {
			return 0, false
		}
		// Python's int() truncates toward zero; the range check above keeps
		// the conversion inside int bounds.
		return int(number), true
	}
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	// Python's int() accepts surrounding whitespace and single underscores
	// between digits ("80_92" == 8092).
	trimmed := strings.TrimSpace(text)
	sign := ""
	if trimmed != "" && (trimmed[0] == '+' || trimmed[0] == '-') {
		sign, trimmed = trimmed[:1], trimmed[1:]
	}
	if trimmed == "" || strings.HasPrefix(trimmed, "_") || strings.HasSuffix(trimmed, "_") || strings.Contains(trimmed, "__") {
		return 0, false
	}
	digits := strings.ReplaceAll(trimmed, "_", "")
	for _, char := range digits {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(sign + digits)
	if err != nil || parsed < 1 || parsed > 65535 {
		return 0, false
	}
	return parsed, true
}
