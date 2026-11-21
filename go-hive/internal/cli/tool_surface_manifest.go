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
	"strings"

	hiveserver "github.com/aden-hive/hive-go/internal/server"
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
