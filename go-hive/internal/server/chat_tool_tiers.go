package server

// Queen-side tool tier for a chat session.
//
// Upstream's queen phase carries a tool tier: non-MCP tools are eager, while
// MCP tools start searchable (schemas not advertised) until search_tools loads
// them. The chat runtime previously advertised every tool, so the
// searchable-tools reminder had nothing to announce. This adapter builds the
// same split from the session catalogue and the process MCP loader, and hands
// the tier to the agent loop so search_tools can promote tools in place.
//
// The tier is rebuilt when the advertised surface changes (a colony bind can
// filter MCP tools through its allowlist). Loaded names persist in the session
// directory, so a resumed session keeps the tools it already paid to load.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_searchable_tools
// Parity: framework/tools/tool_tiers.py::ToolTierState

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// chatManifestFilter narrows what the searchable-tools reminder announces
// without narrowing the loadable tier.
//
// Upstream announces exactly the per-queen allowlist minus the eager set, so
// the reference's first-turn manifest lists two names (chart_render,
// web_scrape). The Go deferred layer is wider on purpose — everything the
// build can execute stays loadable through search_tools — so the manifest
// announces the Python-equivalent subset while the tier keeps every deferred
// definition. Filtered=false is the allow-all fall-through: announce
// everything, which is what an unknown queen (or a session with no allowlist)
// gets upstream too.
type chatManifestFilter struct {
	Names    []string
	Filtered bool
}

// includes reports whether one deferred tool may be announced.
func (f chatManifestFilter) includes(name string) bool {
	if !f.Filtered {
		return true
	}
	for _, allowed := range f.Names {
		if allowed == name {
			return true
		}
	}
	return false
}

// chatToolTier owns one ToolTierState for a chat session and refreshes its
// split when the advertised tool names change.
type chatToolTier struct {
	mu     sync.Mutex
	source func() []llm.ToolDef
	state  *tools.ToolTierState
	// manifest bounds the searchable-tools reminder to the Python reference's
	// per-queen allowlist; the tier's own searchable set stays complete so
	// search_tools can still load every deferred tool.
	manifest chatManifestFilter

	// Flat lazy layer (Python Tool Library parity). alwaysOn is the ordered
	// eager surface of an independent session; deferred are the loadable flat
	// definitions outside it. Empty both = the legacy MCP-only split.
	alwaysOn    []string
	deferred    []llm.ToolDef
	colonyBound func() bool

	signature string
	loaded    bool
	// allowlist is the per-queen MCP allowlist (nil = allow every MCP tool;
	// a non-nil empty list allows none), the same gate
	// QueenPhaseState.enabled_mcp_tools applies upstream.
	allowlist []string
	// dirty forces the next refresh to rebuild even when the catalogue is
	// unchanged, which is what an allowlist update needs.
	dirty bool
}

// newChatToolTier returns nil when the process has neither a flat lazy layer
// nor any MCP tools: without searchable tools there is no split to carry, and
// the chat surface keeps its pre-tier shape.
//
// alwaysOn/deferred come from the catalogue's LazySurfaceDeps (Python's
// first-request order + the loadable remainder). colonyBound flips the eager
// set to the colony-phase surface, matching upstream's phase lists: a bound
// colony advertises the whole default surface again.
func newChatToolTier(root string, source func() []llm.ToolDef, alwaysOn []string, deferred []llm.ToolDef, manifest chatManifestFilter, colonyBound func() bool) *chatToolTier {
	if source == nil {
		return nil
	}
	lazy := len(alwaysOn) > 0 || len(deferred) > 0
	if !lazy {
		mcpNames := mcpToolNames()
		hasMCP := len(mcpNames) > 0
		if !hasMCP {
			for _, def := range source() {
				if isNamespacedMCPTool(def.Name) {
					hasMCP = true
					break
				}
			}
		}
		if !hasMCP {
			return nil
		}
	}
	state := &tools.ToolTierState{
		AlwaysEnabledNames: map[string]struct{}{},
		GateableNames:      map[string]struct{}{},
	}
	if strings.TrimSpace(root) != "" {
		state.PersistPath = filepath.Join(root, "tool_tiers.json")
	}
	return &chatToolTier{
		source:      source,
		state:       state,
		manifest:    manifest,
		alwaysOn:    append([]string(nil), alwaysOn...),
		deferred:    append([]llm.ToolDef(nil), deferred...),
		colonyBound: colonyBound,
	}
}

// state returns the live tier, rebuilding it when the catalogue changed.
func (c *chatToolTier) statePtr() *tools.ToolTierState {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked()
	return c.state
}

// setAllowlist applies the per-queen MCP allowlist and rebuilds the split in
// place, so the next turn advertises the refreshed surface. It reports whether
// a live tier was updated; a session with no MCP tools has nothing to gate.
func (c *chatToolTier) setAllowlist(names []string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if names == nil {
		c.allowlist = nil
	} else {
		c.allowlist = append([]string{}, names...)
	}
	if c.state == nil {
		return false
	}
	c.dirty = true
	c.refreshLocked()
	return true
}

// allowsMCPTool reports whether the allowlist admits one advertised tool. The
// allowlist carries the registry's tool names (as the Tool Library shows them);
// a namespaced MCP name (mcp__server__tool) matches on its trailing segment.
func (c *chatToolTier) allowsMCPTool(name string) bool {
	if c.allowlist == nil {
		return true
	}
	for _, allowed := range c.allowlist {
		if allowed == name {
			return true
		}
		if allowed != "" && strings.HasSuffix(name, "__"+allowed) {
			return true
		}
	}
	return false
}

func (c *chatToolTier) refreshLocked() {
	if c == nil || c.state == nil {
		return
	}
	defs := c.source()
	mcpNames := mcpToolNames()
	lazy := len(c.alwaysOn) > 0 || len(c.deferred) > 0
	bound := lazy && c.colonyBound != nil && c.colonyBound()
	alwaysOn := make(map[string]struct{}, len(c.alwaysOn))
	for _, name := range c.alwaysOn {
		alwaysOn[name] = struct{}{}
	}
	pool := make([]tools.Tool, 0, len(defs)+len(c.deferred))
	eager := make(map[string]struct{}, len(defs))
	gateable := make(map[string]struct{})
	names := make([]string, 0, len(defs)+len(c.deferred))
	registered := make(map[string]struct{}, len(defs)+len(c.deferred))
	for _, def := range defs {
		if def.Name == "" {
			continue
		}
		if isMCPTool(def.Name, mcpNames) && !c.allowsMCPTool(def.Name) {
			// The queen's tool-library allowlist excludes this MCP tool: it is
			// neither eager nor searchable this turn.
			continue
		}
		pool = append(pool, tools.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
		})
		names = append(names, def.Name)
		registered[def.Name] = struct{}{}
		if isMCPTool(def.Name, mcpNames) {
			gateable[def.Name] = struct{}{}
			continue
		}
		if !lazy || bound {
			// Pre-lazy behavior (and the colony phase, where upstream's phase
			// list advertises the whole default surface): every non-MCP tool is
			// eager.
			eager[def.Name] = struct{}{}
			continue
		}
		if _, on := alwaysOn[def.Name]; on {
			eager[def.Name] = struct{}{}
			continue
		}
		gateable[def.Name] = struct{}{}
	}
	if lazy {
		// Deferred flat layer: loadable by search_tools, never in the first
		// request unless the catalogue marked it always-on.
		for _, def := range c.deferred {
			if def.Name == "" {
				continue
			}
			if _, dup := registered[def.Name]; dup {
				continue
			}
			pool = append(pool, tools.Tool{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			})
			names = append(names, def.Name)
			registered[def.Name] = struct{}{}
			if _, on := alwaysOn[def.Name]; on {
				eager[def.Name] = struct{}{}
				continue
			}
			gateable[def.Name] = struct{}{}
		}
	}
	signature := strings.Join(names, "\n")
	if bound {
		signature = "colony\n" + signature
	}
	if c.loaded && signature == c.signature && !c.dirty {
		return
	}
	c.dirty = false
	previous := append([]string(nil), c.state.LoadedToolNames...)
	c.state.Pool = pool
	c.state.AlwaysEnabledNames = eager
	c.state.GateableNames = gateable
	c.state.LoadedToolNames = nil
	if !c.loaded {
		c.state.RestoreLoadedTools(c.state.LoadPersistedTools(), registered)
	} else {
		for _, name := range previous {
			if _, ok := registered[name]; ok {
				c.state.LoadedToolNames = append(c.state.LoadedToolNames, name)
			}
		}
	}
	c.state.Rebuild()
	c.signature = signature
	c.loaded = true
}

// toolDefs is the eager surface the loop advertises for the next model call.
// The always-on names keep the captured Python first-request order; tools
// loaded later (or made eager by the colony phase) follow in pool order, so
// the first request is order-identical to the reference and promotions append
// behind the stable prefix.
func (c *chatToolTier) toolDefs() []llm.ToolDef {
	state := c.statePtr()
	if state == nil {
		return nil
	}
	current := state.GetCurrentTools()
	if len(c.alwaysOn) > 0 {
		rank := make(map[string]int, len(c.alwaysOn))
		for index, name := range c.alwaysOn {
			rank[name] = index
		}
		current = append([]tools.Tool(nil), current...)
		sort.SliceStable(current, func(i, j int) bool {
			left, leftOn := rank[current[i].Name]
			right, rightOn := rank[current[j].Name]
			if leftOn && rightOn {
				return left < right
			}
			return leftOn && !rightOn
		})
	}
	out := make([]llm.ToolDef, 0, len(current))
	for _, tool := range current {
		out = append(out, llm.ToolDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return out
}

// searchableTools is the reminder's view of the deferred tools. It announces
// the Python-equivalent subset when the session carries a per-queen allowlist,
// so the first-turn manifest reads like the reference's; the tier itself keeps
// every deferred definition, so search_tools still loads and promotes the rest.
func (c *chatToolTier) searchableTools() []agentloop.Tool {
	state := c.statePtr()
	if state == nil {
		return nil
	}
	searchable := state.GetSearchableTools()
	out := make([]agentloop.Tool, 0, len(searchable))
	for _, tool := range searchable {
		if !c.manifest.includes(tool.Name) {
			continue
		}
		out = append(out, agentloop.Tool{Name: tool.Name, Description: tool.Description})
	}
	return out
}

// loadedToolNames lets the reminder tell a loaded departure (silent) from a
// genuinely removed tool (announced).
func (c *chatToolTier) loadedToolNames() []string {
	state := c.statePtr()
	if state == nil {
		return nil
	}
	return append([]string(nil), state.LoadedToolNames...)
}

// mcpToolNames maps the process loader's connected servers onto the names the
// session catalogue advertises. The registry registers the server's bare tool
// names by default (Python parity); only an explicitly configured namespace
// prefixes them, so the mapping has to use the loader's own namespace.
func mcpToolNames() map[string]struct{} {
	attacher := tools.ProcessMCP()
	if attacher == nil {
		return nil
	}
	out := map[string]struct{}{}
	for _, status := range attacher.Status() {
		if !status.Connected {
			continue
		}
		for _, name := range status.ToolNames {
			out[tools.MCPRegistryToolName(status.Namespace, status.Name, name)] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isMCPTool treats the loader's connected set as authoritative, with the
// registry's prefixed form as a fallback for a catalogue assembled without the
// process loader (tests, an embedded host).
func isMCPTool(name string, connected map[string]struct{}) bool {
	if _, ok := connected[name]; ok {
		return true
	}
	return isNamespacedMCPTool(name)
}

// isNamespacedMCPTool recognises the optional prefix form. Bare MCP names are
// recognised through the loader's connected set instead; a catalogue assembled
// without the loader cannot tell a bare remote name from a builtin.
func isNamespacedMCPTool(name string) bool {
	prefix := tools.MCPToolNamespace + "__"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := strings.TrimPrefix(name, prefix)
	parts := strings.SplitN(rest, "__", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// ApplyQueenMCPToolAllowlist applies the per-queen MCP allowlist to this
// session's live tool tier. enabled mirrors the PATCH body: nil allows every
// MCP tool, a string list is authoritative (empty means none). False means the
// session has no tiered MCP surface, so nothing was rebuilt.
func (r *ChatRuntime) ApplyQueenMCPToolAllowlist(enabled any) bool {
	if r == nil {
		return false
	}
	names, ok := parseMCPToolAllowlist(enabled)
	if !ok {
		return false
	}
	tier := r.toolTier
	if tier == nil {
		return false
	}
	return tier.setAllowlist(names)
}

// RefreshQueenToolAllowlist is the QueenToolRouteDeps.RefreshSessionTools hook:
// it applies a new allowlist to one live session and reports whether that
// session's advertised surface was actually rebuilt.
func RefreshQueenToolAllowlist(session *Session, enabled any) bool {
	if session == nil || session.Chat == nil {
		return false
	}
	return session.Chat.ApplyQueenMCPToolAllowlist(enabled)
}

// parseMCPToolAllowlist normalises the PATCH body value: nil is allow-all,
// []string/[]any of strings is the authoritative list.
func parseMCPToolAllowlist(enabled any) ([]string, bool) {
	switch value := enabled.(type) {
	case nil:
		return nil, true
	case []string:
		return value, true
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, name)
		}
		return out, true
	}
	return nil, false
}
