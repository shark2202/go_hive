package cli

// Searchable-tools manifest names for a chat queen session.
//
// The Python reference announces its searchable tier in the searchable-tools
// <system-reminder>: the per-queen allowlist (the saved tools.json list, else
// the role-default tool categories) minus the always-enabled set. For
// queen_growth that is exactly chart_render + web_scrape, which is why the
// reference's first-turn manifest lists two names.
//
// The Go lazy layer is wider on purpose: everything the build can execute is
// loadable through search_tools, so the deferred layer keeps all its flat
// names. The manifest, however, has to read like the reference's — the filter
// below narrows the ANNOUNCEMENT only. No tool route is dropped, and
// search_tools still loads the wider layer by select: or keywords.
//
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_searchable_tools

import (
	"sort"
	"strings"

	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// SearchableManifestNames returns the per-queen allowlist that bounds the
// searchable-tools manifest. filtered=false means "no filter": the queen has
// no allowlist at all (the shared default queen, or one the role table does
// not know with no catalog to resolve against), so upstream announces the
// whole pool and so does Go.
//
// The list is resolved exactly like session boot: a saved tools.json sidecar
// wins, else the queen's role-default categories expand against the live MCP
// catalog. An empty allowlist is returned as an empty (non-nil) slice and
// means "announce nothing".
func (c *sessionToolCatalogue) SearchableManifestNames(queenName string) (names []string, filtered bool) {
	if c == nil {
		return nil, false
	}
	queenID := strings.TrimSpace(queenName)
	if queenID == "" || queenID == "default" {
		// The shared default queen carries no role default: upstream leaves
		// enabled_mcp_tools unset, which is allow-all.
		return nil, false
	}
	resolved := hiveserver.QueenToolsAllowlist(c.home, queenID, c.registry.GetFullMCPCatalog())
	if resolved == nil {
		return nil, false
	}
	return resolved, true
}

// CatalogueMCPToolNamesAll returns the union of MCP-origin tool names the
// catalogue's registry recorded — the in-process harness groups plus any
// connected MCP server — upstream's QueenPhaseState.mcp_tool_names_all. It is
// the universe the per-queen allowlist gates and the live-tools diagnostic
// counts. Sorted so downstream iteration is deterministic.
func (c *sessionToolCatalogue) CatalogueMCPToolNamesAll() []string {
	if c == nil || c.registry == nil {
		return nil
	}
	names := map[string]struct{}{}
	for _, entries := range c.registry.GetFullMCPCatalog() {
		for _, entry := range entries {
			if name, _ := entry["name"].(string); name != "" {
				names[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Registry is the catalogue's tool registry. The live-tools diagnostic and the
// Tool Library route read its MCP bookkeeping, upstream
// Session._queen_tool_registry.
func (c *sessionToolCatalogue) Registry() *tools.ToolRegistry {
	if c == nil {
		return nil
	}
	return c.registry
}
