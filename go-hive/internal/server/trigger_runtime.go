package server

// Parity: framework/tools/queen_lifecycle_tools.py::_save_trigger_to_agent
// Parity: framework/tools/queen_lifecycle_tools.py::_persist_active_triggers
// Parity: framework/tools/queen_lifecycle_tools.py::_emit_trigger_fired
// Parity: framework/tools/queen_lifecycle_tools.py::_next_due_from
// Parity: framework/tools/queen_lifecycle_tools.py::resolve_missed
// Parity: framework/tools/queen_lifecycle_tools.py::_start_trigger_timer
// Parity: framework/tools/queen_lifecycle_tools.py::_start_trigger_webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/triggers"
)

type TriggerInjector interface {
	InjectTrigger(event agentloop.TriggerEvent) error
}

var ErrTriggerInjectorUnsupported = errors.New("queen node does not support trigger injection")

func injectTrigger(node QueenNode, event agentloop.TriggerEvent) error {
	if node == nil {
		return errors.New("queen node not ready")
	}
	injector, ok := node.(TriggerInjector)
	if !ok {
		return ErrTriggerInjectorUnsupported
	}
	return injector.InjectTrigger(event)
}

func queenNode(session *Session) QueenNode {
	if session == nil || session.QueenExecutor == nil {
		return nil
	}
	return session.QueenExecutor.QueenNode()
}

type TriggerTimer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *TriggerTimer) Stop() {
	if t == nil {
		return
	}
	t.cancel()
	<-t.done
}

type TriggerWebhookServer interface {
	AddRoute(sourceID, path string, methods []string)
	Start() error
	Running() bool
}

var NewTriggerWebhookServer = newWebhookServer

var ErrNoWebhookServer = errors.New("webhook server is not available in this build")

func slugifyTriggerID(name string, existing map[string]struct{}) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('-')
	}
	base := b.String()
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	base = strings.Trim(base, "-")
	if runes := []rune(base); len(runes) > 40 {
		base = string(runes[:40])
	}
	if base == "" {
		base = "schedule"
	}
	candidate := "user-" + base
	if _, taken := existing[candidate]; !taken {
		return candidate
	}
	for i := 2; ; i++ {
		next := fmt.Sprintf("%s-%d", candidate, i)
		if _, taken := existing[next]; !taken {
			return next
		}
	}
}

func NextDueFrom(def *triggers.TriggerDefinition, anchor time.Time) *string {
	if def == nil {
		return nil
	}
	cfg := def.TriggerConfig
	if cronValue, present := cfg["cron"]; present && pyTruthy(cronValue) {
		cronExpr, ok := cronValue.(string)
		if !ok {
			return nil
		}
		next, ok := triggers.NextCronMatch(cronExpr, anchor)
		if !ok {
			return nil
		}
		formatted := triggers.FormatISO(next)
		return &formatted
	}
	if intervalValue, present := cfg["interval_minutes"]; present && pyTruthy(intervalValue) {
		minutes, ok := floatValue(intervalValue)
		if !ok {
			return nil
		}
		formatted := triggers.FormatISO(anchor.Add(time.Duration(minutes * float64(time.Minute))))
		return &formatted
	}
	return nil
}

// Parity: framework/tools/queen_lifecycle_tools.py::_emit_trigger_fired
func EmitTriggerFired(session *Session, triggerID, triggerType string) {
	if session == nil {
		return
	}
	nowWall := session.NowWall()
	lastFiredAt := int64(nowWall * 1000)
	stat := session.BumpTriggerFire(triggerID, lastFiredAt)

	if session.EventBus == nil {
		return
	}

	taskStr := ""
	nameStr := triggerID
	if def, ok := session.LookupTrigger(triggerID); ok {
		taskStr = def.Task
		if def.Description != "" {
			nameStr = def.Description
		}
	}

	data := map[string]any{
		"trigger_id":    triggerID,
		"trigger_type":  triggerType,
		"name":          nameStr,
		"task":          taskStr,
		"last_fired_at": lastFiredAt,
		"fire_count":    stat.FireCount,
	}
	if mono, ok := session.TriggerNextFire()[triggerID]; ok {
		remaining := math.Max(0, mono-session.NowMonotonic())
		data["next_fire_in"] = remaining
		data["next_fire_at"] = int64((nowWall + remaining) * 1000)
	}

	event := eventbus.NewAgentEvent(eventbus.EventTriggerFired, "queen")
	event.Data = data
	session.EventBus.Publish(event)
}

// Parity: framework/tools/queen_lifecycle_tools.py::_persist_active_triggers
func PersistActiveTriggers(session *Session, sessionID string) {
	_ = sessionID
	if session == nil || session.WorkerPath == "" {
		return
	}
	entries := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(entries) == 0 {
		return
	}
	active := session.ActiveTriggerIDs()
	for _, entry := range entries {
		tid, _ := entry["id"].(string)
		_, enabled := active[tid]
		entry["enabled"] = enabled
		if def, ok := session.LookupTrigger(tid); ok && def.Task != "" {
			entry["task"] = def.Task
		}
	}
	if err := triggers.WriteAgentTriggersJSON(session.WorkerPath, entries); err != nil {
		log.Printf("Failed to persist active triggers to %s: %v", session.WorkerPath, err)
	}
}

func saveTriggerToAgent(session *Session, triggerID string, def *triggers.TriggerDefinition) error {
	if session == nil {
		return nil
	}
	if err := triggers.SaveTriggerToAgent(workerPathSession{session}, triggerID, def); err != nil {
		log.Printf("Failed to save trigger '%s' to %s/triggers.json: %v", triggerID, session.WorkerPath, err)
		return err
	}
	return nil
}

// RestoreSessionTriggers rebuilds a session's trigger runtime from the
// colony's triggers.json. Every persisted definition is hydrated; the ones
// flagged enabled get their timer (or webhook route) started again, so a
// trigger survives a server restart without the user re-activating it. Missed
// timer ticks are stamped instead of replayed — a colony must not auto-start
// work just because the process was down. Returns (loaded, started).
//
// Parity: framework/server/session_manager.py::load_colony
func RestoreSessionTriggers(session *Session) (int, int) {
	if session == nil || session.WorkerPath == "" {
		return 0, 0
	}
	entries := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(entries) == 0 {
		return 0, 0
	}

	loaded := 0
	pending := make([]triggers.TriggerDefinition, 0, len(entries))
	for _, entry := range entries {
		triggerID := stringValue(entry["id"])
		triggerType := stringValue(entry["trigger_type"])
		if triggerID == "" || (triggerType != "timer" && triggerType != "webhook") {
			continue
		}
		// A missing "enabled" key means the trigger was registered without an
		// explicit choice; default to true (every persisted trigger runs unless
		// someone flipped the advanced toggle).
		enabled := true
		if raw, present := entry["enabled"]; present {
			enabled = pyTruthy(raw)
		}
		def := triggers.TriggerDefinition{
			ID:            triggerID,
			TriggerType:   triggerType,
			TriggerConfig: mapDefault(entry["trigger_config"]),
			Description:   stringValue(entry["name"]),
			Task:          stringValue(entry["task"]),
			Enabled:       enabled,
		}
		if def.Description == "" {
			def.Description = triggerID
		}
		if last, ok := entry["last_fired_at"].(string); ok && last != "" {
			def.LastFiredAt = &last
		}
		if next, ok := entry["next_due_at"].(string); ok && next != "" {
			def.NextDueAt = &next
		}
		session.PutTrigger(def)
		loaded++
		if enabled {
			pending = append(pending, def)
		}
	}

	// Silently skip missed ticks: stamping last_fired_at = now keeps the gap
	// from resurfacing on the next reopen (same as upstream load_colony).
	if missed := triggers.ComputeMissed(entries, time.Now().UTC()); len(missed) > 0 {
		stamp := triggers.FormatISO(time.Now().UTC())
		for _, item := range missed {
			def, ok := session.LookupTrigger(item.TriggerID)
			if !ok {
				continue
			}
			def.LastFiredAt = &stamp
			session.PutTrigger(def)
			_ = saveTriggerToAgent(session, item.TriggerID, &def)
		}
		log.Printf("Skipped %d missed trigger(s) on load for %s", len(missed), session.WorkerPath)
	}

	started := 0
	for index := range pending {
		def := pending[index]
		if session.IsTriggerActive(def.ID) {
			continue
		}
		var err error
		switch def.TriggerType {
		case "timer":
			err = StartTriggerTimer(session, def.ID, &def)
		case "webhook":
			err = StartTriggerWebhook(session, def.ID, &def)
		}
		if err != nil {
			log.Printf("Failed to auto-start trigger '%s' from %s: %v", def.ID, session.WorkerPath, err)
			continue
		}
		session.ActivateTrigger(def.ID)
		started++
	}

	if loaded > 0 && session.ID != "" {
		PersistActiveTriggers(session, session.ID)
	}
	return loaded, started
}

// formatTriggerTurn renders a trigger event as the user message the queen
// reads. Multiple triggers merge into one batch upstream; this runtime queues
// one turn per event, so the block is the single-trigger shape.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func formatTriggerTurn(event agentloop.TriggerEvent, stamp string) string {
	task := stringValue(event.Payload["task"])
	taskLine := ""
	if strings.TrimSpace(task) != "" {
		taskLine = "\nTask: " + task
	}
	payload := "{}"
	if event.Payload != nil {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(event.Payload); err == nil {
			payload = strings.TrimSuffix(buffer.String(), "\n")
		}
	}
	return "[" + stamp + "]\n" +
		fmt.Sprintf("[TRIGGER: %s/%s]", event.TriggerType, event.SourceID) +
		taskLine + "\n" + payload
}

var missedTriggerDecisions = map[string]struct{}{
	"fire_latest": {},
	"skip":        {},
	"reschedule":  {},
}

func pythonDecisionStr(value any) string {
	if value == nil {
		return "None"
	}
	return pyStr(value)
}

// Parity: framework/tools/queen_lifecycle_tools.py::resolve_missed
func ResolveMissed(session *Session, decisions map[string]any) (map[string]string, error) {
	results := map[string]string{}
	now := time.Now().UTC()

	for tid, raw := range decisions {
		switch raw.(type) {
		case []any:
			return nil, errors.New("unhashable type: 'list'")
		case map[string]any:
			return nil, errors.New("unhashable type: 'dict'")
		}
		decision, isString := raw.(string)
		if !isString {
			results[tid] = "invalid_decision:" + pythonDecisionStr(raw)
			continue
		}
		if _, ok := missedTriggerDecisions[decision]; !ok {
			results[tid] = "invalid_decision:" + decision
			continue
		}
		def, ok := session.LookupTrigger(tid)
		if !ok {
			results[tid] = "unknown_trigger"
			continue
		}

		def.NextDueAt = NextDueFrom(&def, now)
		stamp := triggers.FormatISO(now)
		def.LastFiredAt = &stamp
		session.PutTrigger(def)
		_ = saveTriggerToAgent(session, tid, &def)

		if decision == "fire_latest" {
			if err := injectCatchUp(session, tid, def); err != nil {
				return nil, err
			}
			EmitTriggerFired(session, tid, def.TriggerType)
			results[tid] = "fired"
		} else if decision == "skip" {
			results[tid] = "skipped"
		} else {
			results[tid] = "rescheduled"
		}
	}
	return results, nil
}

func injectCatchUp(session *Session, triggerID string, def triggers.TriggerDefinition) error {
	node := queenNode(session)
	if node == nil {
		return nil
	}
	config := def.TriggerConfig
	if config == nil {
		config = map[string]any{}
	}
	event := agentloop.NewTriggerEvent(def.TriggerType, triggerID)
	event.Payload = map[string]any{
		"task":           def.Task,
		"trigger_config": config,
		"catch_up":       true,
	}
	return injectTrigger(node, event)
}

// Parity: framework/tools/queen_lifecycle_tools.py::_start_trigger_timer
func StartTriggerTimer(session *Session, triggerID string, def *triggers.TriggerDefinition) error {
	if session == nil || def == nil {
		return errors.New("trigger definition missing")
	}
	cronExpr, _ := def.TriggerConfig["cron"].(string)

	intervalValue, hasIntervalValue := def.TriggerConfig["interval_minutes"]
	var interval float64
	if pyTruthy(intervalValue) {
		minutes, ok := floatValue(intervalValue)
		if !ok {
			return fmt.Errorf("could not convert interval_minutes to float: %v", intervalValue)
		}
		interval = minutes
	}

	var firstDelay float64
	if cronExpr != "" {
		now := time.Now().UTC()
		if first, ok := triggers.NextCronMatch(cronExpr, now); ok {
			firstDelay = math.Max(0, first.Sub(now).Seconds())
		} else {
			firstDelay = 60.0
		}
	} else if hasIntervalValue && interval != 0 {
		firstDelay = interval * 60
	} else {
		firstDelay = 60.0
	}
	session.SetTriggerNextFire(triggerID, session.NowMonotonic()+firstDelay)

	ctx, cancel := context.WithCancel(context.Background())
	timer := &TriggerTimer{cancel: cancel, done: make(chan struct{})}
	initial := *def
	go runTimerLoop(ctx, timer, session, triggerID, cronExpr, interval, hasIntervalValue, initial)
	session.SetActiveTimer(triggerID, timer)
	return nil
}

func runTimerLoop(
	ctx context.Context,
	timer *TriggerTimer,
	session *Session,
	triggerID, cronExpr string,
	interval float64,
	hasInterval bool,
	def triggers.TriggerDefinition,
) {
	defer close(timer.done)

	cursor := time.Now().UTC()
	for {
		var sleepFor time.Duration
		if cronExpr != "" {
			next, ok := triggers.NextCronMatch(cronExpr, cursor)
			if !ok {
				log.Printf("Timer trigger '%s' has no next cron match for %q", triggerID, cronExpr)
				sleepFor = time.Minute
			} else {
				cursor = next
				sleepFor = time.Duration(math.Max(0, next.Sub(time.Now().UTC()).Seconds()) * float64(time.Second))
			}
		} else {
			seconds := 60.0
			if hasInterval && interval != 0 {
				seconds = interval * 60
			}
			sleepFor = time.Duration(seconds * float64(time.Second))
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepFor):
		}

		if cronExpr != "" {
			now := time.Now().UTC()
			nextDelay := 60.0
			if peek, ok := triggers.NextCronMatch(cronExpr, now); ok {
				nextDelay = math.Max(0, peek.Sub(now).Seconds())
			}
			session.SetTriggerNextFire(triggerID, session.NowMonotonic()+nextDelay)
		} else if hasInterval && interval != 0 {
			session.SetTriggerNextFire(triggerID, session.NowMonotonic()+interval*60)
		} else {
			session.SetTriggerNextFire(triggerID, session.NowMonotonic()+60)
		}

		if session.ColonyID == "" {
			continue
		}
		node := queenNode(session)
		if node == nil {
			continue
		}
		if current, ok := session.LookupTrigger(triggerID); ok {
			def = current
		}

		event := agentloop.NewTriggerEvent("timer", triggerID)
		event.Payload = map[string]any{
			"task":           def.Task,
			"trigger_config": def.TriggerConfig,
		}
		if err := injectTrigger(node, event); err != nil {
			log.Printf("Timer trigger '%s' tick failed: %v", triggerID, err)
			continue
		}
		EmitTriggerFired(session, triggerID, "timer")

		fireDT := time.Now().UTC()
		stamp := triggers.FormatISO(fireDT)
		def.LastFiredAt = &stamp
		if cronExpr != "" {
			if next, ok := triggers.NextCronMatch(cronExpr, fireDT); ok {
				nextStamp := triggers.FormatISO(next)
				def.NextDueAt = &nextStamp
			} else {
				def.NextDueAt = nil
			}
		} else if hasInterval && interval != 0 {
			nextStamp := triggers.FormatISO(fireDT.Add(time.Duration(interval * float64(time.Minute))))
			def.NextDueAt = &nextStamp
		}
		session.PutTrigger(def)
		_ = saveTriggerToAgent(session, triggerID, &def)
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::_start_trigger_webhook
func StartTriggerWebhook(session *Session, triggerID string, def *triggers.TriggerDefinition) error {
	if session == nil || def == nil {
		return errors.New("trigger definition missing")
	}
	if session.EventBus == nil {
		return errors.New("session has no event bus")
	}

	path, _ := def.TriggerConfig["path"].(string)
	methods := webhookMethods(def.TriggerConfig)

	handler := func(event *eventbus.AgentEvent) {
		handleTriggerWebhook(session, triggerID, def, path, methods, event)
	}
	subID := session.EventBus.Subscribe(
		[]eventbus.EventType{eventbus.EventWebhookReceived},
		handler,
		eventbus.WithFilterStream(triggerID),
	)
	session.SetActiveWebhookSub(triggerID, subID)

	if session.WebhookServer == nil {
		if NewTriggerWebhookServer == nil {
			return ErrNoWebhookServer
		}
		port := 8090
		if portValue, ok := floatValue(def.TriggerConfig["port"]); ok {
			port = int(portValue)
		}
		session.WebhookServer = NewTriggerWebhookServer(session.EventBus, "127.0.0.1", port)
	}

	server := session.WebhookServer
	server.AddRoute(triggerID, path, methods)
	if !server.Running() {
		if err := server.Start(); err != nil {
			return err
		}
	}
	return nil
}

func handleTriggerWebhook(
	session *Session,
	triggerID string,
	def *triggers.TriggerDefinition,
	path string,
	methods []string,
	event *eventbus.AgentEvent,
) {
	var data map[string]any
	if event != nil {
		data = event.Data
	}
	if stringValue(data["path"]) != path {
		return
	}
	if !containsFold(methods, stringValue(data["method"])) {
		return
	}
	if session.ColonyID == "" {
		return
	}
	node := queenNode(session)
	if node == nil {
		return
	}

	triggerEvent := agentloop.NewTriggerEvent("webhook", triggerID)
	triggerEvent.Payload = map[string]any{
		"task":         def.Task,
		"path":         data["path"],
		"method":       data["method"],
		"headers":      mapDefault(data["headers"]),
		"payload":      mapDefault(data["payload"]),
		"query_params": mapDefault(data["query_params"]),
	}
	if err := injectTrigger(node, triggerEvent); err != nil {
		log.Printf("Webhook trigger '%s' injection failed: %v", triggerID, err)
		return
	}
	EmitTriggerFired(session, triggerID, "webhook")
}

func webhookMethods(config map[string]any) []string {
	raw, ok := config["methods"]
	if !ok {
		return []string{"POST"}
	}
	list, ok := raw.([]any)
	if !ok {
		if typed, isStrings := raw.([]string); isStrings {
			out := make([]string, len(typed))
			for i, method := range typed {
				out[i] = strings.ToUpper(method)
			}
			return out
		}
		return []string{"POST"}
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		out = append(out, strings.ToUpper(stringValue(entry)))
	}
	return out
}

func containsFold(methods []string, method string) bool {
	target := strings.ToUpper(method)
	for _, candidate := range methods {
		if strings.ToUpper(candidate) == target {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func mapDefault(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if typed != nil {
			return typed
		}
	case map[string]string:
		if typed != nil {
			out := make(map[string]any, len(typed))
			for key, entry := range typed {
				out[key] = entry
			}
			return out
		}
	}
	return map[string]any{}
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func floatValue(value any) (float64, bool) {
	if v, ok := numericValue(value); ok {
		return v, true
	}
	if s, ok := value.(string); ok {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}
