package tasks

// Parity: framework/tasks/events.py

import (
	"log"
	"sync"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

var (
	eventBusMu      sync.RWMutex
	defaultEventBus *eventbus.EventBus
	busResolver     func(sessionID string) *eventbus.EventBus
)

func SetDefaultEventBus(bus *eventbus.EventBus) {
	eventBusMu.Lock()
	defaultEventBus = bus
	eventBusMu.Unlock()
}

func SetBusResolver(resolver func(sessionID string) *eventbus.EventBus) {
	eventBusMu.Lock()
	busResolver = resolver
	eventBusMu.Unlock()
}

func getEventBus(explicit *eventbus.EventBus, sessionID string) *eventbus.EventBus {
	if explicit != nil {
		return explicit
	}
	eventBusMu.RLock()
	resolver := busResolver
	fallback := defaultEventBus
	eventBusMu.RUnlock()
	if sessionID != "" && resolver != nil {
		if resolved := resolveBusSafely(resolver, sessionID); resolved != nil {
			return resolved
		}
	}
	return fallback
}

func resolveBusSafely(resolver func(string) *eventbus.EventBus, sessionID string) (bus *eventbus.EventBus) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("task bus resolver failed for %s: %v", sessionID, recovered)
			bus = nil
		}
	}()
	return resolver(sessionID)
}

func SerializeRecord(rec *TaskRecord) map[string]any {
	if rec == nil {
		return nil
	}
	metadata := rec.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	blocks := rec.Blocks
	if blocks == nil {
		blocks = []int{}
	}
	blockedBy := rec.BlockedBy
	if blockedBy == nil {
		blockedBy = []int{}
	}
	activeForm := any(nil)
	if rec.ActiveForm != nil {
		activeForm = *rec.ActiveForm
	}
	owner := any(nil)
	if rec.Owner != nil {
		owner = *rec.Owner
	}
	return map[string]any{
		"id":          rec.ID,
		"subject":     rec.Subject,
		"description": rec.Description,
		"active_form": activeForm,
		"owner":       owner,
		"status":      string(rec.Status),
		"blocks":      append([]int(nil), blocks...),
		"blocked_by":  append([]int(nil), blockedBy...),
		"metadata":    CloneMap(metadata),
		"created_at":  rec.CreatedAt,
		"updated_at":  rec.UpdatedAt,
	}
}

func emitEngine(explicit *eventbus.EventBus, sessionID string) *eventbus.EventBus {
	return getEventBus(explicit, sessionID)
}

func publishSafely(bus *eventbus.EventBus, event *eventbus.AgentEvent) {
	if bus == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("task event publish failed: %v", recovered)
		}
	}()
	bus.Publish(event)
}

func EmitTaskCreated(sessionID string, record *TaskRecord, streamID string, bus *eventbus.EventBus) {
	target := emitEngine(bus, sessionID)
	if target == nil {
		return
	}
	if streamID == "" {
		streamID = "primary"
	}
	event := eventbus.NewAgentEvent(eventbus.EventTaskCreated, streamID)
	event.Data = map[string]any{
		"session_id": sessionID,
		"task":       SerializeRecord(record),
	}
	publishSafely(target, event)
}

func EmitTaskUpdated(sessionID string, record *TaskRecord, fields []string, streamID string, bus *eventbus.EventBus) {
	target := emitEngine(bus, sessionID)
	if target == nil || len(fields) == 0 {
		return
	}
	if streamID == "" {
		streamID = "primary"
	}
	event := eventbus.NewAgentEvent(eventbus.EventTaskUpdated, streamID)
	event.Data = map[string]any{
		"session_id": sessionID,
		"task_id":    record.ID,
		"after":      SerializeRecord(record),
		"fields":     append([]string(nil), fields...),
	}
	publishSafely(target, event)
}

func EmitTaskDeleted(sessionID string, taskID int, cascade []int, streamID string, bus *eventbus.EventBus) {
	target := emitEngine(bus, sessionID)
	if target == nil {
		return
	}
	if streamID == "" {
		streamID = "primary"
	}
	if cascade == nil {
		cascade = []int{}
	}
	event := eventbus.NewAgentEvent(eventbus.EventTaskDeleted, streamID)
	event.Data = map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"cascade":    append([]int(nil), cascade...),
	}
	publishSafely(target, event)
}
