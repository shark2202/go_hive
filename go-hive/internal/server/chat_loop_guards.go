package server

// Loop-level guards for a chat session.
//
// Two protections existed without a production consumer: the judge-based stall
// check (SubscribeStallCheck) and the deterministic doom-loop park. This file
// arms both when the session manager hands a session out. The stall check stays
// in-band — a reminder injected into the conversation, as upstream; a loop park
// is mirrored onto the session's event bus by publishLoopPark so a stuck queen
// is observable instead of ending in a log line.
//
// Parity: framework/server/stall_check.py::subscribe_stall_check (the call
// site upstream has in queen_orchestrator; the port's session manager is the
// equivalent seam).

import (
	"log"
	"sync"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

// chatLoopGuards is one session's armed guard handles. ChatRuntime carries no
// field for them (its struct belongs to the turn machinery, which this port
// keeps untouched), so the handle is keyed by the runtime here.
type chatLoopGuards struct {
	stall *StallCheckSubscription
}

var (
	chatGuardsMu sync.Mutex
	chatGuards   = map[*ChatRuntime]*chatLoopGuards{}
)

// startLoopGuards subscribes the session's loop-level guards. It is idempotent:
// the session manager calls it every time a session is handed out, and a second
// call must not stack a second stall-check subscription.
func (r *ChatRuntime) startLoopGuards() {
	if r == nil {
		return
	}
	chatGuardsMu.Lock()
	defer chatGuardsMu.Unlock()
	if _, armed := chatGuards[r]; armed {
		return
	}
	guards := &chatLoopGuards{}
	chatGuards[r] = guards
	if r.loop != nil {
		r.loop.ParkSink = r.publishLoopPark
	}
	guards.stall = startStallCheck(r.bus, r.deps.HomeRoot, func() QueenNode { return r }, nil)
}

// stopLoopGuards tears the guards down. The session manager calls it before it
// closes the runtime, so a torn-down session publishes no late reminders.
func (r *ChatRuntime) stopLoopGuards() {
	if r == nil {
		return
	}
	chatGuardsMu.Lock()
	guards := chatGuards[r]
	delete(chatGuards, r)
	chatGuardsMu.Unlock()
	if guards == nil {
		return
	}
	if guards.stall != nil {
		guards.stall.Close()
	}
	if r.loop != nil {
		r.loop.ParkSink = nil
	}
}

// startStallCheck subscribes the judge-based stall check for one session bus.
// The feature flag and the judge slot are read per check (StallJudgeSlot), so
// enabling stall_check later takes effect without rebuilding the session. A
// session without a bus or a resolvable home has nothing to subscribe.
func startStallCheck(bus *eventbus.EventBus, homeRoot string, node func() QueenNode, judges map[string]JudgeProtocol) *StallCheckSubscription {
	if bus == nil || homeRoot == "" || node == nil {
		return nil
	}
	home, err := hivehome.ResolveAt(homeRoot)
	if err != nil {
		log.Printf("stall check: unusable hive home %q: %v", homeRoot, err)
		return nil
	}
	return SubscribeStallCheck(bus, home, node, judges)
}

// publishLoopPark mirrors a turn-loop park onto the session bus. The doom loop
// is the only park the loop raises on its own; it rides the bus's dedicated
// node_tool_doom_loop event so the dashboard (and the event log) show the
// repetition even though the turn ends as a failure.
func (r *ChatRuntime) publishLoopPark(reason agentloop.ParkReason, detail string) {
	if r == nil || r.bus == nil {
		return
	}
	var executionID *string
	if r.agentCtx != nil && r.agentCtx.ExecutionID != "" {
		id := r.agentCtx.ExecutionID
		executionID = &id
	}
	if reason == agentloop.ParkReasonDoomLoop {
		r.bus.EmitToolDoomLoop(chatStreamID, chatNodeID, detail, executionID)
		return
	}
	r.bus.EmitNodeStalled(chatStreamID, chatNodeID, string(reason), executionID)
}
