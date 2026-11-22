package server

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState (tool tier surface)

// Upstream's QueenPhaseState owns the queen's tool tiers: independent_tools,
// mcp_tool_names_all, enabled_mcp_tools, the always-enabled split and the
// get_current_tools()/get_searchable_tools()/unregistered_allowlisted_names()
// views the debug routes read. The Go port keeps that tier inside ChatRuntime
// (chatToolTier / tools.ToolTierState); this adapter attaches it to the
// session's QueenPhaseState so /api/sessions/{id}/live_tools and the phase-
// aware queen routes see one object, as upstream does.
//
// Parity: framework/server/routes_sessions.py::handle_session_live_tools
// Parity: framework/tools/tool_tiers.py::ToolTierState

import (
	"sort"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// phaseStandaloneShims are advertised-but-standalone names with no live tier
// membership: ask_user is appended by the agent loop at dispatch (and the
// live-tools handler reports it as framework_added), while
// collect_result/credentials/sentinel_setup are flat-surface names upstream's
// phase state never carries. Upstream's phase state does not contain them in
// independent_tools (sentinel_setup is appended to the loop's tools[] at
// dispatch, exactly like ask_user), so the phase surface excludes them rather
// than presenting a different callable set than the reference view.
var phaseStandaloneShims = map[string]struct{}{
	"ask_user":       {},
	"collect_result": {},
	"credentials":    {},
	"sentinel_setup": {},
}

// PhaseToolSurface is one session's live tool tier projected through the
// upstream QueenPhaseState surface. It reads the same tools.ToolTierState the
// chat runtime advertises, so a search_tools promotion or an allowlist refresh
// shows up here on the next read.
type PhaseToolSurface struct {
	runtime *ChatRuntime
	// mcpNames is the MCP-origin name universe (harness groups plus connected
	// MCP servers), upstream's QueenPhaseState.mcp_tool_names_all.
	mcpNames map[string]struct{}
	// allowlist mirrors enabled_mcp_tools: isNone reports Python's None
	// (allow every MCP tool) versus an authoritative list.
	allowlist []string
	isNone    bool
}

// newPhaseToolSurface builds the surface over a live chat runtime. Nil when
// the runtime has no catalogue to describe.
func newPhaseToolSurface(runtime *ChatRuntime) *PhaseToolSurface {
	if runtime == nil {
		return nil
	}
	surface := &PhaseToolSurface{runtime: runtime, mcpNames: map[string]struct{}{}, isNone: true}
	if runtime.deps.ManifestFiltered {
		surface.allowlist = append([]string(nil), runtime.deps.ManifestToolNames...)
		surface.isNone = false
	}
	for _, name := range runtime.deps.MCPToolNamesAll {
		if name != "" {
			surface.mcpNames[name] = struct{}{}
		}
	}
	// Connected external MCP servers are part of the same universe upstream
	// collects from the registry's per-server bookkeeping.
	for name := range mcpToolNames() {
		surface.mcpNames[name] = struct{}{}
	}
	return surface
}

// attachPhaseToolSurface wires this session's chat runtime into its phase
// state. Called from the session manager's registration choke point so every
// live session (created, preloaded, resumed, forked) gets the same surface
// without a second runtime.
func (s *Session) attachPhaseToolSurface() {
	if s == nil || s.Chat == nil {
		return
	}
	state, ok := s.PhaseState.(*QueenPhaseState)
	if !ok || state == nil {
		return
	}
	state.SetToolSurface(newPhaseToolSurface(s.Chat))
}

func (p *PhaseToolSurface) tier() *tools.ToolTierState {
	if p == nil || p.runtime == nil || p.runtime.toolTier == nil {
		return nil
	}
	return p.runtime.toolTier.statePtr()
}

// advertisedTools is the pre-tier surface: when the runtime built no tier the
// loop advertises the whole catalogue, so that is the actual callable set.
func (p *PhaseToolSurface) advertisedTools() []tools.Tool {
	if p == nil || p.runtime == nil {
		return nil
	}
	out := make([]tools.Tool, 0, len(p.runtime.deps.Tools))
	for _, def := range p.runtime.deps.Tools {
		out = append(out, tools.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
		})
	}
	return out
}

func (p *PhaseToolSurface) phaseTool(tool tools.Tool) bool {
	if tool.Name == "" {
		return false
	}
	_, shim := phaseStandaloneShims[tool.Name]
	return !shim
}

// GetCurrentTools returns the eager (callable-now) tools of the phase, minus
// the loop's synthetic ask_user (the handler reports that as framework_added)
// and the advertised-without-executor shims.
func (p *PhaseToolSurface) GetCurrentTools() []tools.Tool {
	if p == nil {
		return nil
	}
	source := p.advertisedTools()
	if tier := p.tier(); tier != nil {
		source = tier.GetCurrentTools()
	}
	out := make([]tools.Tool, 0, len(source))
	for _, tool := range source {
		if p.phaseTool(tool) {
			out = append(out, tool)
		}
	}
	return out
}

// allows reports whether an MCP-origin name survives the per-queen allowlist.
// Lifecycle tools never reach this gate upstream, so callers pass MCP names
// only.
func (p *PhaseToolSurface) allows(name string) bool {
	if p == nil || p.isNone {
		return true
	}
	for _, allowed := range p.allowlist {
		if allowed == name {
			return true
		}
	}
	return false
}

// GetSearchableTools is the allowlist-filtered searchable tier: the tools the
// phase may use but that need search_tools. The Go tier keeps a wider
// loadable layer than upstream's announcement; the allowlist filter restores
// the reference view when a queen has one, matching
// QueenPhaseState._filtered_tools_for_current_phase.
func (p *PhaseToolSurface) GetSearchableTools() []tools.Tool {
	if p == nil {
		return nil
	}
	tier := p.tier()
	if tier == nil {
		return nil
	}
	out := make([]tools.Tool, 0, len(tier.GetSearchableTools()))
	for _, tool := range tier.GetSearchableTools() {
		if !p.phaseTool(tool) {
			continue
		}
		if !p.allows(tool.Name) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// UnregisteredAllowlistedNames reports allowlisted names no MCP server
// registered this session. Allow-all mode returns nothing (an unregistered
// name cannot be told from a typo), matching upstream.
func (p *PhaseToolSurface) UnregisteredAllowlistedNames() map[string]struct{} {
	out := map[string]struct{}{}
	if p == nil || p.isNone {
		return out
	}
	for _, name := range p.allowlist {
		if name == "" {
			continue
		}
		if _, registered := p.mcpNames[name]; registered {
			continue
		}
		if _, shim := phaseStandaloneShims[name]; shim {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// MCPToolNamesAll is the MCP-origin name universe the allowlist can gate.
func (p *PhaseToolSurface) MCPToolNamesAll() map[string]struct{} {
	out := map[string]struct{}{}
	if p == nil {
		return out
	}
	for name := range p.mcpNames {
		out[name] = struct{}{}
	}
	return out
}

// EnabledMCPTools exposes the allowlist with Python's None semantics:
// isNone=true means every MCP tool is allowed.
func (p *PhaseToolSurface) EnabledMCPTools() ([]string, bool) {
	if p == nil || p.isNone {
		return nil, true
	}
	return append([]string(nil), p.allowlist...), false
}

// phaseLifecycleTools is the non-MCP part of the phase list: the eager
// lifecycle/synthetic names upstream's independent_names carries.
func (p *PhaseToolSurface) phaseLifecycleTools() []tools.Tool {
	out := []tools.Tool{}
	for _, tool := range p.GetCurrentTools() {
		if _, mcp := p.mcpNames[tool.Name]; mcp {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// mcpToolStubs projects the MCP name universe into tools so the diagnostic can
// count what the allowlist filters, as upstream's independent_tools does.
func (p *PhaseToolSurface) mcpToolStubs() []tools.Tool {
	if p == nil {
		return nil
	}
	byName := map[string]tools.Tool{}
	if tier := p.tier(); tier != nil {
		for _, tool := range tier.Pool {
			byName[tool.Name] = tool
		}
	}
	names := make([]string, 0, len(p.mcpNames))
	for name := range p.mcpNames {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]tools.Tool, 0, len(names))
	for _, name := range names {
		tool, ok := byName[name]
		if !ok {
			tool = tools.Tool{Name: name}
		}
		out = append(out, tool)
	}
	return out
}

// IndependentTools is the phase's membership list: every lifecycle tool the
// phase carries plus every MCP-origin tool (the set the allowlist filters).
func (p *PhaseToolSurface) IndependentTools() []tools.Tool {
	out := append([]tools.Tool{}, p.phaseLifecycleTools()...)
	return append(out, p.mcpToolStubs()...)
}

// FilteredIndependentTools is IndependentTools after the allowlist gate; the
// non-MCP lifecycle tools always pass, as upstream's _passes_allowlist does.
func (p *PhaseToolSurface) FilteredIndependentTools() []tools.Tool {
	out := append([]tools.Tool{}, p.phaseLifecycleTools()...)
	for _, tool := range p.mcpToolStubs() {
		if p.allows(tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}
