package colony

// Colony runtime seams the session layer uses to mirror events and to feed the
// persistent overseer. They keep the concrete runtime's internal wiring private
// while giving the server one narrow, testable surface.

import (
	"errors"
	"fmt"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

// EventBus exposes the colony's event bus so a session bound to this runtime
// can tell whether the two already share one bus (and skip a duplicate mirror).
func (r *ColonyRuntime) EventBus() *eventbus.EventBus {
	if r == nil {
		return nil
	}
	return r.bus
}

// ToolAllowlistSnapshot returns the MCP allowlist the runtime gates tools with:
// enabled names, the full MCP name set, and whether gating is active. Callers
// must not mutate the returned maps.
func (r *ColonyRuntime) ToolAllowlistSnapshot() (enabled map[string]struct{}, all map[string]struct{}, gated bool) {
	if r == nil {
		return nil, nil, false
	}
	r.toolsMu.RLock()
	defer r.toolsMu.RUnlock()
	if r.enabledMCPTools != nil {
		enabled = make(map[string]struct{}, len(r.enabledMCPTools))
		for _, name := range r.enabledMCPTools {
			enabled[name] = struct{}{}
		}
		gated = true
	}
	if len(r.mcpToolNamesAll) > 0 {
		all = make(map[string]struct{}, len(r.mcpToolNamesAll))
		for name := range r.mcpToolNamesAll {
			all[name] = struct{}{}
		}
	}
	return enabled, all, gated
}

// OverseerID returns the persistent overseer's worker id, or "" when the colony
// has no started overseer.
func (r *ColonyRuntime) OverseerID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overseer == nil {
		return ""
	}
	return r.overseer.ID
}

// InjectOverseer delivers content to the persistent overseer as an injected
// user turn. It is the colony-side input path the session layer uses for
// triggers and worker reports; a colony without a live overseer returns an
// error instead of silently dropping the input.
func (r *ColonyRuntime) InjectOverseer(content string) error {
	if r == nil {
		return errors.New("colony: nil colony runtime")
	}
	r.mu.Lock()
	overseer := r.overseer
	r.mu.Unlock()
	if overseer == nil {
		return errors.New("colony: no overseer is running")
	}
	if !overseer.Inject(content) {
		return fmt.Errorf("colony: overseer %q is not accepting input", overseer.ID)
	}
	return nil
}

// publishStarted announces that a queued worker actually entered its run. The
// terminal paths already publish execution_completed / execution_failed, so
// this closes the lifecycle with the same existing event family (no new
// telemetry shape). The session bridge mirrors it onto the session bus.
func (r *ColonyRuntime) publishStarted(worker *Worker) {
	if r == nil || r.bus == nil || worker == nil {
		return
	}
	executionID := ""
	if worker.Context != nil {
		executionID = worker.Context.ExecutionID
	}
	event := eventbus.NewAgentEvent(eventbus.EventExecutionStarted, worker.StreamID)
	event.ExecutionID = eventbus.StrPtr(executionID)
	event.Data = map[string]any{
		"worker_id": worker.ID,
		"task":      worker.Task,
	}
	if worker.BatchID != "" {
		event.Data["batch_id"] = worker.BatchID
		event.Data["batch_index"] = worker.BatchIndex
		event.Data["batch_size"] = worker.BatchSize
	}
	colonyID := r.streamID
	event.ColonyID = &colonyID
	r.bus.Publish(event)
}
