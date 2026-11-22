package host

// Session-facing runtime seams.
//
// The server declares its optional runtime surfaces as duck-typed interfaces on
// Session.Colony (LiveWorkerRuntime, ColonyWorkerReadSurface, the stop cascade,
// worker-LLM access, profile bindings, event subscription). Host is the object
// the assembly layer actually holds, so it forwards each one to the colony
// runtime instead of forcing the caller to reach through Runtime().
//
// Parity: framework/server/session_manager.py (session.colony accessor)

import (
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// StreamID is the colony stream identifier the session layer adopts as its
// ColonyRuntime identity.
func (h *Host) StreamID() string {
	return h.runtime.StreamID()
}

// ActiveWorkerCount is the live worker gauge the session detail route reads.
func (h *Host) ActiveWorkerCount() int {
	return h.runtime.ActiveWorkerCount()
}

// GetStats exposes the runtime gauges without the host-level run counts Stats
// merges in.
func (h *Host) GetStats() map[string]any {
	return h.runtime.GetStats()
}

// GetWorker returns the live worker object (janitor safety context).
func (h *Host) GetWorker(id string) *colony.Worker {
	return h.runtime.GetWorker(id)
}

// WorkerToolTiering is the live-tools debug view.
func (h *Host) WorkerToolTiering() []colony.WorkerToolTiering {
	return h.runtime.WorkerToolTiering()
}

// StopWorker stops a single worker; false means unknown or already terminal.
func (h *Host) StopWorker(id string) bool {
	return h.runtime.StopWorker(id)
}

// StopWorkers is the whole-colony stop cascade used by cancel-queen.
func (h *Host) StopWorkers(blockDispatch bool) (map[string]any, error) {
	return h.runtime.StopWorkers(blockDispatch)
}

// ListWorkers returns the colony worker registry rows.
func (h *Host) ListWorkers() []colony.WorkerInfo {
	return h.runtime.ListWorkers()
}

// Worker returns one registry row.
func (h *Host) Worker(workerID string) (colony.WorkerInfo, bool) {
	return h.runtime.Worker(workerID)
}

// LiveWorkers returns the live workers stream/stop rows.
func (h *Host) LiveWorkers() []colony.LiveWorkerState {
	return h.runtime.LiveWorkers()
}

// LiveWorker returns one live worker row.
func (h *Host) LiveWorker(workerID string) (colony.LiveWorkerState, bool) {
	return h.runtime.LiveWorker(workerID)
}

// StopLiveWorker stops one worker on the live-workers route.
func (h *Host) StopLiveWorker(workerID string) (map[string]any, error) {
	return h.runtime.StopLiveWorker(workerID)
}

// StopAllLiveWorkers stops every worker on the stop-all route.
func (h *Host) StopAllLiveWorkers() map[string]any {
	return h.runtime.StopAllLiveWorkers()
}

// StoragePath is the runtime's worker-directory root.
func (h *Host) StoragePath() string {
	return h.runtime.StoragePath()
}

// Skills reports the colony's skill catalog (false = unavailable).
func (h *Host) Skills() ([]*skills.ParsedSkill, bool) {
	return h.runtime.Skills()
}

// ReloadSkills rebuilds the colony's catalog after a skill changed on disk; the
// session manager's reload fan-out reaches a preloaded colony through this seam.
func (h *Host) ReloadSkills() (map[string]any, error) {
	return h.runtime.ReloadSkills()
}

// Tools reports the colony's default tool set plus provider attribution.
func (h *Host) Tools() ([]colony.ColonyTool, map[string]string) {
	return h.runtime.Tools()
}

// WorkerLLM exposes the worker completer for in-place model reconfiguration.
func (h *Host) WorkerLLM() any {
	return h.runtime.WorkerLLM()
}

// LLM exposes the colony's own completer (overseer transport) for the
// queen-model reconfigure path.
func (h *Host) LLM() any {
	return h.runtime.LLM()
}

// BoundWorkersForProfile lists workers dispatched from a worker profile.
func (h *Host) BoundWorkersForProfile(profileName string) []string {
	return h.runtime.BoundWorkersForProfile(profileName)
}

// SubscribeToEvents wires a handler onto the colony bus.
func (h *Host) SubscribeToEvents(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) (string, error) {
	return h.runtime.SubscribeToEvents(eventTypes, handler, opts...)
}

// EventBus is the colony bus this host publishes on. Session runtimes compare
// it against their own bus before mirroring events.
func (h *Host) EventBus() *eventbus.EventBus {
	return h.bus
}

// InjectInput queues a client message for a running worker.
func (h *Host) InjectInput(workerID, content string) error {
	return h.runtime.InjectInput(workerID, content)
}

// BlockDispatch gates worker admission.
func (h *Host) BlockDispatch() { h.runtime.BlockDispatch() }

// UnblockDispatch re-opens worker admission.
func (h *Host) UnblockDispatch() { h.runtime.UnblockDispatch() }

// DispatchBlocked reports the admission gate state.
func (h *Host) DispatchBlocked() bool { return h.runtime.DispatchBlocked() }
