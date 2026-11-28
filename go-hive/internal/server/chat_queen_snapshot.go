package server

// The live queen loop's forkable surface: the tool names and loop-config knobs
// a sibling-colony fork inherits into the new colony's worker.json.
//
// The Python reference reads them off the source queen's live context and loop
// (`queen_ctx.available_tools`, `queen_loop._config`) inside
// `_create_sibling_colony_from_colony`. The Go chat runtime owns the same two
// values (`agentCtx.AvailableTools`, `loop.Config`) but they are unexported, so
// this is the one read-only seam the colony loader needs.

// QueenForkSurface is the inherited part of a live queen: what the new
// colony's worker config is built from.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
type QueenForkSurface struct {
	// ToolNames is the queen's available-tool list, in its live order,
	// before the queen-only lifecycle strip.
	ToolNames []string
	// HasLoop reports whether the runtime carried a loop config; false means
	// the port falls back to the default worker loop config, as upstream does
	// when `queen_loop._config` is None.
	HasLoop bool
	// The four knobs build_loop_config_dict propagates from the queen loop.
	MaxIterations      int
	ToolCallBudget     int
	MaxContextTokens   int
	MaxToolResultChars int
}

// ForkSurfaceSnapshot returns the queen surface a colony fork inherits. A
// runtime without a built loop (no provider credentials) reports no tools and
// no loop config: upstream's `queen_ctx` is None on that path too, so the new
// colony keeps the default worker loop config.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
func (r *ChatRuntime) ForkSurfaceSnapshot() QueenForkSurface {
	snapshot := QueenForkSurface{}
	if r == nil {
		return snapshot
	}
	defs := r.tools
	if r.agentCtx != nil && r.agentCtx.AvailableTools != nil {
		defs = r.agentCtx.AvailableTools
	}
	if len(defs) > 0 {
		names := make([]string, 0, len(defs))
		for _, def := range defs {
			if def.Name != "" {
				names = append(names, def.Name)
			}
		}
		snapshot.ToolNames = names
	}
	if r.loop != nil {
		snapshot.HasLoop = true
		snapshot.MaxIterations = r.loop.Config.MaxIterations
		snapshot.ToolCallBudget = r.loop.Config.ToolCallBudget
		snapshot.MaxContextTokens = r.loop.Config.MaxContextTokens
		snapshot.MaxToolResultChars = r.loop.Config.MaxToolResultChars
	}
	return snapshot
}
