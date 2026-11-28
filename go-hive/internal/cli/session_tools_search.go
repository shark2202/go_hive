package cli

// Session history search: `search_messages` and `search_timeline`.
//
// The tool definitions, parameter coercion and scope resolution live in
// internal/cli/toolcatalog; this file is the composition-root adapter that
// keeps the catalogue's method names and the shared parameter helpers the
// memory-timeline tool also uses. Scope is resolved from the session
// directory this catalogue was built for (…/queens/<queen>/sessions/<id>),
// never from model input.

import (
	"github.com/shark2202/go_hive/go-hive/internal/cli/toolcatalog"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

// SearchMessagesToolName / SearchTimelineToolName are the upstream tool names
// (tools/src/memory_tools/tool.py, timeline.py).
const (
	SearchMessagesToolName = toolcatalog.SearchMessagesToolName
	SearchTimelineToolName = toolcatalog.SearchTimelineToolName
)

// searchToolDefs returns both definitions in catalogue order.
func searchToolDefs(home hivehome.Home) []llm.ToolDef {
	return toolcatalog.SearchToolDefs(home)
}

// memoryTimelineAvailable mirrors the upstream `memory_timeline` feature flag.
func memoryTimelineAvailable(home hivehome.Home) bool {
	return toolcatalog.MemoryTimelineAvailable(home)
}

func searchStringParam(input map[string]any, key string) string {
	return toolcatalog.SearchStringParam(input, key)
}

func searchIntParam(input map[string]any, key string) int {
	return toolcatalog.SearchIntParam(input, key)
}

// executeSearchTool runs one history-search call. handled is false for any
// other tool name so the caller can keep its dispatch order.
func (c *sessionToolCatalogue) executeSearchTool(name string, input map[string]any) (content string, isError bool, handled bool) {
	return toolcatalog.ExecuteSearchTool(c.home, c.root, name, input)
}

// searchScope resolves the queen this session belongs to; the memory-timeline
// tool reuses it for the same scope rules.
func (c *sessionToolCatalogue) searchScope() (memory.ScopeRef, *memory.ToolError) {
	return toolcatalog.SearchScope(c.home, c.root)
}

// queenScopeFromSessionRoot recovers the queen a chat session belongs to from
// its on-disk tree; the skill tools use the same identity.
func queenScopeFromSessionRoot(home hivehome.Home, root string) string {
	return toolcatalog.QueenScopeFromSessionRoot(home, root)
}
