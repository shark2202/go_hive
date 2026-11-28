package agentloop

// Reminder ordering parity: the hub delivers sources in upstream's registration
// order, not the order this port's hosts happen to register them in.

import (
	"testing"
)

// Parity: framework/agent_loop/agent_loop.py::__init__ (memory_recall and
// memory_index register before turn_context; searchable_tools, skills_catalog
// and the tracker snapshots after it)
func TestReminderHubCollectsInUpstreamOrder(t *testing.T) {
	names := []string{"turn_context", "tracker_snapshot", "searchable_tools", "memory_index", "memory_recall", "custom_extra"}
	hub := NewReminderHub()
	for _, name := range names {
		hub.Register(namedReminderSource{name: name, point: SESSION_START})
	}

	items := hub.Collect(SESSION_START, &ReminderAgentContext{}, nil, "")
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Source)
	}

	want := []string{"memory_recall", "memory_index", "turn_context", "searchable_tools", "tracker_snapshot", "custom_extra"}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sources = %v, want %v", got, want)
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::__init__ (the canonical order the
// hub sorts by is upstream's registration order)
func TestReminderSourceOrderCoversEveryUpstreamSource(t *testing.T) {
	upstream := []string{
		"tasks", "colony_parallel", "active_workers", "past_conversations",
		"memory_recall", "memory_index", "turn_context", "searchable_tools",
		"skills_catalog", "tracker_snapshot", "worker_tracker_snapshot",
		"colony_worker_snapshot", "idle_nudge", "sentinel_escalation", "stream_stall",
	}
	if len(reminderSourceOrder) != len(upstream) {
		t.Fatalf("canonical order has %d entries, want %d", len(reminderSourceOrder), len(upstream))
	}
	for i, name := range upstream {
		if reminderSourceOrder[i] != name {
			t.Fatalf("canonical order[%d] = %q, want %q", i, reminderSourceOrder[i], name)
		}
	}
	// The two names the source structs actually report must match the table,
	// or the sort would silently treat them as unknown.
	if got := (&MemoryRecallReminderSource{}).Name(); got != "memory_recall" {
		t.Fatalf("MemoryRecallReminderSource name = %q", got)
	}
	if got := (&TurnContextReminderSource{}).Name(); got != "turn_context" {
		t.Fatalf("TurnContextReminderSource name = %q", got)
	}
}

// namedReminderSource is a reminder source with a fixed name and one body, used
// to inspect the hub's delivery order.
type namedReminderSource struct {
	name  string
	point ReminderPoint
}

func (s namedReminderSource) Name() string                         { return s.name }
func (s namedReminderSource) Points() []ReminderPoint              { return []ReminderPoint{s.point} }
func (s namedReminderSource) AppliesTo(*ReminderAgentContext) bool { return true }
func (s namedReminderSource) Render(ReminderContext) (string, bool) {
	return "body from " + s.name, true
}
