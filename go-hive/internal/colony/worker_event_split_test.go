package colony

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// TestSplitWorkerEventsResolver pins HIVE_SPLIT_WORKER_EVENTS: with the switch
// on, a parallel worker's per-turn chatter lands in its own
// <worker storage>/events.jsonl; with it off (the default) nothing is
// installed, so the queen's log keeps every worker event.
//
// Parity: core/tests/test_sse_worker_filter.py::test_worker_llm_and_tool_events_are_filtered
func TestSplitWorkerEventsResolver(t *testing.T) {
	storage := t.TempDir()
	bus := eventbus.NewEventBus()
	runtime := &ColonyRuntime{streamID: "colony", storagePath: storage, bus: bus}

	t.Setenv("HIVE_SPLIT_WORKER_EVENTS", "")
	runtime.enableWorkerEventLogs()
	publishWorkerEvent(bus, "worker:w1")
	if _, err := os.Stat(filepath.Join(storage, "workers", "w1", "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl written without the split switch: %v", err)
	}

	t.Setenv("HIVE_SPLIT_WORKER_EVENTS", "true")
	runtime.enableWorkerEventLogs()
	publishWorkerEvent(bus, "worker:w1")
	workerLog := filepath.Join(storage, "workers", "w1", "events.jsonl")
	info, err := os.Stat(workerLog)
	if err != nil {
		t.Fatalf("worker event log missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("worker event log is empty")
	}
}

// TestSplitWorkerEventsAllowList matches upstream's accepted values: only
// 1/true/yes (case-insensitive) enable the split.
//
// Parity: core/tests/test_sse_worker_filter.py::test_worker_llm_and_tool_events_are_filtered
func TestSplitWorkerEventsAllowList(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "Yes"} {
		t.Setenv("HIVE_SPLIT_WORKER_EVENTS", value)
		if !splitWorkerEventsEnabled() {
			t.Fatalf("HIVE_SPLIT_WORKER_EVENTS=%q did not enable the split", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "enabled"} {
		t.Setenv("HIVE_SPLIT_WORKER_EVENTS", value)
		if splitWorkerEventsEnabled() {
			t.Fatalf("HIVE_SPLIT_WORKER_EVENTS=%q enabled the split", value)
		}
	}
}

func publishWorkerEvent(bus *eventbus.EventBus, streamID string) {
	event := eventbus.NewAgentEvent(eventbus.EventToolCallStarted, streamID)
	event.NodeID = eventbus.StrPtr("worker")
	event.ExecutionID = eventbus.StrPtr("exec")
	bus.Publish(event)
}
