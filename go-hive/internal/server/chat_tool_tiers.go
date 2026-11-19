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
	if c.loaded && signature == c.signature {
		return
	}
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
