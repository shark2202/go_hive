package colony

// The overseer's queen-grade tool surface: the colony's own persistent lead
// runs with the same dispatch/state tools a chat Queen gets, gated by the
// colony that owns it (the overseer only exists inside a colony runtime, and
// every tool resolves that runtime's tracker binding / worker spawner).
//
// Upstream hands start_overseer the queen's tool catalogue (`queen_tools`);
// this port composes the subset the colony runtime can execute itself:
// run_worker (fan-out), the queen-role tracker table (shared ledger), and the
// four session task tools (the overseer's own plan).
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
// Parity: framework/tasks/tools/register.py::register_task_tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tasks"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// overseerTaskSessionID is the session scope the overseer's task list lives
// under. The colony directory is the storage root, so each colony owns its own
// plan file (<colony>/_misc/overseer/tasks.json) and the colony stays one
// portable directory.
//
// Parity-exempt: upstream's overseer shares the queen session's task list;
// this port gives the colony-rooted overseer its own scope because a
// `serve --colony` session is a synthetic id with no task directory of its own.
const overseerTaskSessionID = "overseer"

// overseerTaskAgentID is the owner task_update stamps on in_progress tasks;
// the overseer is the colony's queen.
const overseerTaskAgentID = "queen"

// overseerTaskSet binds the four session task tools to the overseer's scope.
// The executors read the process-wide tasks execution context, so calls are
// serialised here the same way the chat catalogue serialises its own.
type overseerTaskSet struct {
	mu        sync.Mutex
	sessionID string
	ordered   []tasks.Tool
	byName    map[string]tasks.Tool
}

func newOverseerTaskSet(storagePath string) *overseerTaskSet {
	store := tasks.NewTaskStore(storagePath)
	built := tasks.BuildSessionTools(store, nil)
	set := &overseerTaskSet{
		sessionID: overseerTaskSessionID,
		ordered:   built,
		byName:    make(map[string]tasks.Tool, len(built)),
	}
	for _, tool := range built {
		set.byName[tool.Name] = tool
	}
	return set
}

func (s *overseerTaskSet) Defs() []llm.ToolDef {
	if s == nil {
		return nil
	}
	defs := make([]llm.ToolDef, 0, len(s.ordered))
	for _, tool := range s.ordered {
		defs = append(defs, llm.ToolDef{
			Name:            tool.Name,
			Description:     tool.Description,
			Parameters:      tool.Parameters,
			ConcurrencySafe: tool.ConcurrencySafe,
		})
	}
	return defs
}

func (s *overseerTaskSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.byName[name]
	return ok
}

func (s *overseerTaskSet) Execute(name string, input map[string]any) (string, bool, error) {
	tool, ok := s.byName[name]
	if !ok {
		return "", true, fmt.Errorf("unknown task tool %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	restore := tasks.SetExecContext(tasks.ExecContext{
		AgentID:   overseerTaskAgentID,
		SessionID: s.sessionID,
	})
	defer restore()
	return tools.PythonJSONDumps(tool.Execute(input)), false, nil
}

// overseerToolDefs is the tool surface the persistent overseer advertises: the
// colony's configured tools plus run_worker, the queen tracker table, and the
// session task tools. run_worker stays colony-gated by construction — this
// surface is only reachable through a colony runtime's overseer.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (r *ColonyRuntime) overseerToolDefs() []llm.ToolDef {
	defs := appendMissingToolDefs(r.applyToolAllowlist(r.tools), trackerToolDefs("queen")...)
	defs = appendMissingToolDefs(defs, RunWorkerToolDef())
	defs = appendMissingToolDefs(defs, r.overseerTasks.Defs()...)
	// The overseer is the colony's client-facing lead: when it needs a
	// decision it asks in the same conversation the session front end
	// renders, so it advertises the same synthetic question tool a chat queen
	// gets. Its answer arrives as an injected client input.
	defs = appendMissingToolDefs(defs, agentloop.BuildAskUserTool())
	return defs
}

// overseerTrackerRegistry binds the queen-role tracker tools to the colony's
// own ledger. The binding is resolved per call: Start provisions it, and a
// runtime whose storage is not a colony directory simply has none (the tools
// return ErrNoBinding rather than lying).
func (r *ColonyRuntime) overseerTrackerRegistry() *tracker.Registry {
	if r == nil {
		return nil
	}
	registry := tracker.NewRegistry(func() (*tracker.Binding, error) {
		return r.TrackerBinding(), nil
	})
	if err := tracker.RegisterTrackerTools(registry, "queen"); err != nil {
		return nil
	}
	return registry
}

// executeOverseerTool runs one overseer-only tool. ok is false for names this
// surface does not own, letting the caller fall through to the shared colony
// executor.
func (r *ColonyRuntime) executeOverseerTool(registry *tracker.Registry, req agentloop.ToolCallRequest) (agentloop.ToolResult, bool) {
	if req.Name == RunWorkerToolName {
		content, isError := RunWorker(r.spawnContext(), r, req.Input)
		return agentloop.ToolResult{Content: content, IsError: isError}, true
	}
	if req.Name == "ask_user" {
		return r.askOverseerUser(req.Input), true
	}
	if r.overseerTasks.Has(req.Name) {
		content, isError, err := r.overseerTasks.Execute(req.Name, req.Input)
		if err != nil {
			return agentloop.ToolResult{Content: err.Error(), IsError: true}, true
		}
		return agentloop.ToolResult{Content: content, IsError: isError}, true
	}
	if registry != nil {
		return r.executeTrackerTool(registry, req)
	}
	return agentloop.ToolResult{}, false
}

// spawnContext is the context run_worker dispatches under: the runtime's armed
// context when started, so a colony stop cancels the fan-out, otherwise the
// background context.
func (r *ColonyRuntime) spawnContext() context.Context {
	if r == nil {
		return context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}
