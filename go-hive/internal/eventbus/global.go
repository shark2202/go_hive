package eventbus

// Parity: framework/host/event_bus.py (get_global_event_bus / publish_global)

import (
	"log"
	"sync"
)

var (
	globalBusMu    sync.Mutex
	globalEventBus *EventBus
)

func GetGlobalEventBus() *EventBus {
	globalBusMu.Lock()
	defer globalBusMu.Unlock()
	if globalEventBus == nil {
		globalEventBus = NewEventBus(WithMaxHistory(200))
	}
	return globalEventBus
}

func PublishGlobal(event *AgentEvent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			eventType := "?"
			if event != nil {
				eventType = string(event.Type)
			}
			log.Printf("Global event publish failed for type=%s: %v", eventType, recovered)
		}
	}()
	GetGlobalEventBus().Publish(event)
}
