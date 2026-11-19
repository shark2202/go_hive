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
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

// chatToolTier owns one ToolTierState for a chat session and refreshes its
// split when the advertised tool names change.
type chatToolTier struct {
	mu     sync.Mutex
	source func() []llm.ToolDef
	state  *tools.ToolTierState

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

// newChatToolTier returns nil when the process has no MCP loader: without
// searchable tools there is no split to carry, and the chat surface keeps its
// existing flat shape.
func newChatToolTier(root string, source func() []llm.ToolDef) *chatToolTier {
	if source == nil {
		return nil
	}
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
	state := &tools.ToolTierState{
		AlwaysEnabledNames: map[string]struct{}{},
		GateableNames:      map[string]struct{}{},
	}
	if strings.TrimSpace(root) != "" {
		state.PersistPath = filepath.Join(root, "tool_tiers.json")
	}
	return &chatToolTier{source: source, state: state}
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
	pool := make([]tools.Tool, 0, len(defs))
	eager := make(map[string]struct{}, len(defs))
	gateable := make(map[string]struct{})
	names := make([]string, 0, len(defs))
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
		if isMCPTool(def.Name, mcpNames) {
			gateable[def.Name] = struct{}{}
			continue
		}
		eager[def.Name] = struct{}{}
	}
	signature := strings.Join(names, "\n")
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
		registered := make(map[string]struct{}, len(pool))
		for _, tool := range pool {
			registered[tool.Name] = struct{}{}
		}
		c.state.RestoreLoadedTools(c.state.LoadPersistedTools(), registered)
	} else {
		for _, name := range previous {
			if _, ok := gateable[name]; ok {
				c.state.LoadedToolNames = append(c.state.LoadedToolNames, name)
			}
		}
	}
	c.state.Rebuild()
	c.signature = signature
	c.loaded = true
}

// toolDefs is the eager surface the loop advertises for the next model call.
func (c *chatToolTier) toolDefs() []llm.ToolDef {
	state := c.statePtr()
	if state == nil {
		return nil
	}
	current := state.GetCurrentTools()
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

// searchableTools is the reminder's view of the deferred MCP tools.
func (c *chatToolTier) searchableTools() []agentloop.Tool {
	state := c.statePtr()
	if state == nil {
		return nil
	}
	searchable := state.GetSearchableTools()
	out := make([]agentloop.Tool, 0, len(searchable))
	for _, tool := range searchable {
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
// session catalogue advertises. The registry namespaces every MCP tool, so the
// namespaced form is the only one that can collide with a real tool call.
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
			out[tools.NamespacedMCPToolName(tools.MCPToolNamespace, status.Name, name)] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isMCPTool treats the loader's connected set as authoritative, with the
// registry's namespaced form as a fallback for a catalogue assembled without
// the process loader (tests, an embedded host).
func isMCPTool(name string, connected map[string]struct{}) bool {
	if _, ok := connected[name]; ok {
		return true
	}
	return isNamespacedMCPTool(name)
}

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
