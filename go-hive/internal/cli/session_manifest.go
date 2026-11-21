package cli

// sessionManifestNames announces the reference's per-queen allowlist
// (allowed − eager) in the searchable-tools reminder, not the whole Go
// deferred layer. Loadability is untouched: search_tools still loads every
// deferred definition.
//
// Parity: core/framework/tools/tool_tiers.py::get_searchable_tools
func sessionManifestNames(tools *sessionToolCatalogue, queenScope string) ([]string, bool) {
	return tools.SearchableManifestNames(queenScope)
}
