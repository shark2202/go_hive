package server

// Sentinel's escalation source is a reminder source like any other: upstream
// constructs it inside AgentLoop.__init__ and registers it on that same
// `_reminder_hub`, after the idle nudge and before the stream-stall source.
// A source registered on another hub would never be consulted by the loop
// that fires the lifecycle points, and one registered out of slot would
// change how a combined <system-reminder> block reads.
//
// The other half of inject_event's contract is the re-arm: a real user
// message resets the escalation source before notifying the manager, so a
// queen that escalated once nudges again after the human answers instead of
// staying held for the rest of the session.
//
// Parity: framework/agent_loop/agent_loop.py::__init__ (EscalationSource
// registration and source order)
// Parity: framework/agent_loop/agent_loop.py::inject_event (is_client_input:
// `self._escalation_source.reset()` then `_notify_sentinel_local_resume`)

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
)

func sourceIndex(names []string, want string) int {
	for index, name := range names {
		if name == want {
			return index
		}
	}
	return -1
}

// Parity: framework/agent_loop/agent_loop.py::__init__ (source order)
func TestSentinelSourceRegistersOnSessionHubInUpstreamSlot(t *testing.T) {
	runtime, _, _ := newSentinelWiringRuntime(t, "colony_sentinel_slot", &failingSentinelNotifier{})

	// One hub owns every source: the loop's, mirrored to the REST surface.
	if runtime.hub == nil || runtime.hub != runtime.loop.Hub {
		t.Fatal("chat runtime did not publish the loop's own reminder hub")
	}
	if liveSessionReminderHub(runtime.sessionID) != runtime.hub {
		t.Fatal("session hub mirror does not point at the loop hub")
	}
	names := runtime.hub.ActiveSourceNames()
	idle := sourceIndex(names, "idle_nudge")
	sentinelSlot := sourceIndex(names, "sentinel_escalation")
	streamStall := sourceIndex(names, "stream_stall")
	if idle < 0 || sentinelSlot < 0 || streamStall < 0 {
		t.Fatalf("hub sources = %v, want idle_nudge / sentinel_escalation / stream_stall", names)
	}
	if sentinelSlot != idle+1 {
		t.Fatalf("sentinel_escalation at %d, want the slot right after idle_nudge (%d): %v",
			sentinelSlot, idle, names)
	}
	if streamStall != sentinelSlot+1 {
		t.Fatalf("stream_stall at %d, want the slot right after sentinel_escalation (%d): %v",
			streamStall, sentinelSlot, names)
	}
	// The source is bound (consulted), not merely registered: the IDLE_TICK
	// collection reaches it.
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictContinue)
	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder == nil {
		t.Fatal("bound hub did not consult the sentinel source")
	}
}

// Parity: framework/agent_loop/agent_loop.py::inject_event (is_client_input
// resets the escalation source)
func TestClientInputRearmsSentinelSource(t *testing.T) {
	runtime, _, _ := newSentinelWiringRuntime(t, "colony_sentinel_rearm", &failingSentinelNotifier{})

	// Escalate once: the report goes out and the source holds every later
	// evaluation (upstream's per-park de-dup).
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictNeedsHuman)
	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder != nil {
		t.Fatal("an escalation must not also nudge")
	}
	runtime.sentinelSource.ClassifyFn = classifyAs(sentinel.VerdictContinue)
	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder != nil {
		t.Fatal("a held source must stay quiet without a resume")
	}

	// A real user message is that resume: inject_event re-arms the source and
	// closes the open escalation.
	if err := runtime.Deliver("go ahead, keep working"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonTurnDone); err != nil {
		t.Fatalf("%v", err)
	}
	if reminder := sentinelReminder(collectIdleReminders(runtime)); reminder == nil {
		t.Fatal("client input did not re-arm the sentinel source; it stayed held after the reply")
	}
}
