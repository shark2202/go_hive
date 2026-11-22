package server

// Parity: framework/server/routes_queen_tools.py::register_routes
// Parity: framework/agents/queen/queen_tools_config.py
// Parity: framework/agents/queen/queen_tools_defaults.py

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queen"
	"github.com/aden-hive/hive-go/internal/tools"
	"gopkg.in/yaml.v3"
)

// QueenToolRouteDeps wires the Tool Library routes. The registry and the live
// phase state live behind hooks: the route owns the snapshot shape and the
// sidecar, the hook owns what the running queen actually sees.
type QueenToolRouteDeps struct {
	Home    hivehome.Home
	Manager *SessionManager

	// MCPCatalog returns the per-server MCP catalog ({server: [{name,
	// description, input_schema, provider}]}) the queen's registry knows.
	MCPCatalog func(queenID string, session *Session) map[string][]map[string]any
	// LifecycleTools returns the non-MCP tools in the queen's current phase.
	LifecycleTools func(queenID string, session *Session) []map[string]any
	// SyntheticTools overrides the agent loop's synthetic tool list.
	SyntheticTools func() []map[string]any
	// ConnectedProviders lists OAuth providers with at least one live account.
	ConnectedProviders func() []string
	// CredentialedTools maps MCP tool name -> OAuth provider; used to keep
	// credentialed tools out of implicit defaults.
	CredentialedTools func() map[string]string
	// RefreshSessionTools applies a new allowlist to one live session and
	// reports whether its filter was rebuilt.
	RefreshSessionTools func(session *Session, enabled any) bool
	// PublishToolsConfigChanged broadcasts TOOLS_CONFIG_CHANGED on the
	// global bus so other Tool Library tabs refetch.
	PublishToolsConfigChanged func(scope, scopeID, action string)
	// EmailSendersEnabled gates the email_senders category in the snapshot
	// (framework.config.get_email_senders_enabled).
	EmailSendersEnabled bool

	SchedulePush func(kind, key string)
}

func (deps QueenToolRouteDeps) queensDir() string {
	return deps.Home.QueensDir()
}

func (deps QueenToolRouteDeps) schedulePush(kind, key string) {
	if deps.SchedulePush != nil {
		deps.SchedulePush(kind, key)
	}
}

func (deps QueenToolRouteDeps) connectedProviders() map[string]struct{} {
	out := map[string]struct{}{}
	if deps.ConnectedProviders == nil {
		return out
	}
	for _, provider := range deps.ConnectedProviders() {
		if provider != "" {
			out[provider] = struct{}{}
		}
	}
	return out
}

func (deps QueenToolRouteDeps) credentialedTools() map[string]string {
	if deps.CredentialedTools == nil {
		return map[string]string{}
	}
	return deps.CredentialedTools()
}

func queenSyntheticTools(deps QueenToolRouteDeps) []map[string]any {
	if deps.SyntheticTools != nil {
		return deps.SyntheticTools()
	}
	return []map[string]any{{
		"name":        "ask_user",
		"description": "Pause and ask the user a structured question.",
		"editable":    false,
	}}
}

// queenLiveSessionFor returns a live session owned by the queen, preferring a
// DM (no colony runtime) over a colony overseer.
func queenLiveSessionFor(manager *SessionManager, queenID string) *Session {
	if manager == nil {
		return nil
	}
	var colonyFallback *Session
	for _, session := range manager.ListSessions() {
		if session.QueenName != queenID {
			continue
		}
		if session.Colony == nil && session.ColonyID == "" {
			return session
		}
		colonyFallback = session
	}
	return colonyFallback
}

// normalizeQueenMCPCatalog sorts each server's tool list by name, matching
// _normalize_catalog's stable rendering.
func normalizeQueenMCPCatalog(catalog map[string][]map[string]any) map[string][]map[string]any {
	out := make(map[string][]map[string]any, len(catalog))
	for server, entries := range catalog {
		copied := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			tool := make(map[string]any, len(entry))
			for key, value := range entry {
				tool[key] = value
			}
			copied = append(copied, tool)
		}
		sort.SliceStable(copied, func(i, j int) bool {
			left, _ := copied[i]["name"].(string)
			right, _ := copied[j]["name"].(string)
			return left < right
		})
		out[server] = copied
	}
	return out
}

func queenCatalogNames(catalog map[string][]map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	for _, entries := range catalog {
		for _, entry := range entries {
			if name, _ := entry["name"].(string); name != "" {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

func queenToolsConfigPath(queensDir, queenID string) string {
	return filepath.Join(queensDir, queenID, "tools.json")
}

func queenToolsConfigExists(queensDir, queenID string) bool {
	_, err := os.Stat(queenToolsConfigPath(queensDir, queenID))
	return err == nil
}

func queenAppVersion() string {
	if version := os.Getenv("HIVE_APP_VERSION"); version != "" {
		return version
	}
	return "0.0.0"
}

// writeQueenToolsConfig mirrors update_queen_tools_config: refuse to create a
// sidecar for a queen with no directory, then write atomically.
func writeQueenToolsConfig(queensDir, queenID string, enabled any) error {
	queenDir := filepath.Join(queensDir, queenID)
	if info, err := os.Stat(queenDir); err != nil || !info.IsDir() {
		return &queen.ProfileNotFoundError{ID: queenID}
	}
	payload := map[string]any{
		"enabled_mcp_tools": enabled,
		"updated_at":        time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"),
		"saved_on_version":  queenAppVersion(),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return queenAtomicWrite(queenToolsConfigPath(queensDir, queenID), string(encoded))
}

func deleteQueenToolsConfig(queensDir, queenID string) bool {
	path := queenToolsConfigPath(queensDir, queenID)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		return false
	}
	return true
}

// queenStringList interprets a decoded JSON array of strings; ok is false for
// any other shape (including null).
func queenStringList(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// migrateQueenToolsFromProfile hoists a legacy enabled_mcp_tools field out of
// profile.yaml into the tools.json sidecar.
func migrateQueenToolsFromProfile(queensDir, queenID string) (any, bool) {
	profilePath := filepath.Join(queensDir, queenID, "profile.yaml")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, false
	}
	var parsed any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, false
	}
	profile, ok := parsed.(map[string]any)
	if !ok {
		return nil, false
	}
	raw, present := profile["enabled_mcp_tools"]
	if !present {
		return nil, false
	}
	delete(profile, "enabled_mcp_tools")
	var enabled any
	if raw == nil {
		enabled = nil
	} else if list, ok := queenStringList(raw); ok {
		enabled = list
	} else {
		enabled = nil
	}
	if err := writeQueenToolsConfig(queensDir, queenID, enabled); err != nil {
		return nil, false
	}
	if err := writeQueenProfileYAML(profilePath, profile); err != nil {
		return nil, false
	}
	return enabled, true
}

// resolveQueenDefaultTools mirrors queen_tools_defaults.resolve_queen_default_tools:
// known queens get their role categories expanded against the catalog, with
// credentialed tools dropped; unknown queens get every credential-less tool
// when a catalog is available, and nil (allow-all) otherwise.
func resolveQueenDefaultTools(deps QueenToolRouteDeps, queenID string, catalog map[string][]map[string]any) any {
	credentialed := deps.credentialedTools()
	categories := queen.DefaultToolsFor(queenID)
	if len(categories) == 0 {
		if catalog == nil {
			return nil
		}
		names := []string{}
		seen := map[string]struct{}{}
		for _, entries := range catalog {
			for _, entry := range entries {
				name, _ := entry["name"].(string)
				if name == "" {
					continue
				}
				if _, dup := seen[name]; dup {
					continue
				}
				if _, provider := credentialed[name]; provider {
					continue
				}
				if providerText, _ := entry["provider"].(string); providerText != "" {
					continue
				}
				seen[name] = struct{}{}
				names = append(names, name)
			}
		}
		return names
	}
	names := []string{}
	seen := map[string]struct{}{}
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		if _, provider := credentialed[name]; provider {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, category := range categories {
		for _, name := range tools.ResolveCategoryTools(category, catalog) {
			add(name)
		}
	}
	return names
}

// augmentQueenRoleDefaultWithConnectedOAuth applies the "default-on for OAuth"
// policy: on the role default, tools whose provider is connected join the
// allowlist (intersected with the catalog).
func augmentQueenRoleDefaultWithConnectedOAuth(deps QueenToolRouteDeps, names []string, catalog map[string][]map[string]any) []string {
	if names == nil {
		return nil
	}
	connected := deps.connectedProviders()
	if len(connected) == 0 {
		return names
	}
	catalogNames := queenCatalogNames(catalog)
	existing := map[string]struct{}{}
	for _, name := range names {
		existing[name] = struct{}{}
	}
	additions := []string{}
	for toolName, provider := range deps.credentialedTools() {
		if provider == "" {
			continue
		}
		if _, ok := connected[provider]; !ok {
			continue
		}
		if len(catalogNames) > 0 {
			if _, ok := catalogNames[toolName]; !ok {
				continue
			}
		}
		if _, ok := existing[toolName]; ok {
			continue
		}
		additions = append(additions, toolName)
	}
	if len(additions) == 0 {
		return names
	}
	for _, name := range additions {
		existing[name] = struct{}{}
	}
	merged := make([]string, 0, len(existing))
	for name := range existing {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}

// loadQueenToolsConfig mirrors queen_tools_config.load_queen_tools_config.
// Returns nil for "allow every MCP tool".
func loadQueenToolsConfig(deps QueenToolRouteDeps, queenID string, catalog map[string][]map[string]any) any {
	queensDir := deps.queensDir()
	path := queenToolsConfigPath(queensDir, queenID)
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			return nil
		}
		raw, present := object["enabled_mcp_tools"]
		if !present || raw == nil {
			return nil
		}
		if list, ok := queenStringList(raw); ok {
			// Heal frozen sidecars: grant tools added to a category after
			// this allowlist was last saved. Prefer the deterministic
			// role-default-based grant for known queens; fall back to the
			// baseline-coverage heuristic for custom queens.
			savedOnVersion, _ := object["saved_on_version"].(string)
			if categories := queen.DefaultToolsFor(queenID); len(categories) > 0 {
				return tools.GrantRoleDefaultAdditions(categories, list, savedOnVersion)
			}
			return tools.InferCategoryAdditions(list, savedOnVersion, catalog)
		}
		return nil
	}

	if migrated, ok := migrateQueenToolsFromProfile(queensDir, queenID); ok {
		return migrated
	}
	if queenToolsConfigExists(queensDir, queenID) {
		return nil
	}
	resolved := resolveQueenDefaultTools(deps, queenID, catalog)
	list, ok := resolved.([]string)
	if !ok {
		return nil
	}
	return augmentQueenRoleDefaultWithConnectedOAuth(deps, list, catalog)
}

// QueenToolsAllowlist resolves the queen's per-queen MCP tool allowlist the way
// session boot does upstream: the tools.json sidecar when present, else the
// role-default categories expanded against the MCP catalog (an unknown queen
// gets every credential-less catalog tool). Nil means allow every tool.
//
// The tools routes resolve the same thing through their deps; this entry point
// is for callers that hold only a home and a catalog snapshot — the chat
// loader's searchable-manifest filter. Credentialed-tool dropping and OAuth
// augmentation are the route-layer's additions (they need a live credential
// view); a session-boot caller passes neither, which is what upstream's
// boot-time load_queen_tools_config sees too.
//
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
func QueenToolsAllowlist(home hivehome.Home, queenID string, catalog map[string][]map[string]any) []string {
	deps := QueenToolRouteDeps{Home: home}
	enabled := loadQueenToolsConfig(deps, queenID, catalog)
	names, ok := enabled.([]string)
	if !ok {
		return nil
	}
	return names
}

// renderQueenMCPServers shapes catalog entries for the Tool Library.
func renderQueenMCPServers(deps QueenToolRouteDeps, catalog map[string][]map[string]any, enabled any) []map[string]any {
	var allowed map[string]struct{}
	if list, ok := enabled.([]string); ok {
		allowed = map[string]struct{}{}
		for _, name := range list {
			allowed[name] = struct{}{}
		}
	}
	connected := deps.connectedProviders()
	servers := []string{}
	for server := range catalog {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	out := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		rendered := []map[string]any{}
		for _, entry := range catalog[server] {
			name, _ := entry["name"].(string)
			isEnabled := allowed == nil
			if allowed != nil {
				_, isEnabled = allowed[name]
			}
			var provider any
			providerText, _ := entry["provider"].(string)
			if providerText != "" {
				provider = providerText
			}
			providerConnected := true
			if providerText != "" {
				_, providerConnected = connected[providerText]
			}
			description, _ := entry["description"].(string)
			inputSchema, ok := entry["input_schema"]
			if !ok || inputSchema == nil {
				inputSchema = map[string]any{}
			}
			rendered = append(rendered, map[string]any{
				"name":               name,
				"description":        description,
				"input_schema":       inputSchema,
				"enabled":            isEnabled,
				"provider":           provider,
				"provider_connected": providerConnected,
			})
		}
		out = append(out, map[string]any{"name": server, "tools": rendered})
	}
	return out
}

// renderQueenCategories exposes the role-default category table.
func renderQueenCategories(deps QueenToolRouteDeps, queenID string, catalog map[string][]map[string]any) []map[string]any {
	hidden := map[string]struct{}{}
	if !deps.EmailSendersEnabled && !EmailSendersEnabled(deps.Home) {
		hidden["email_senders"] = struct{}{}
	}
	alwaysEnabled := configuredQueenAlwaysEnabledCategories(deps)
	applied := map[string]struct{}{}
	if categories := queen.DefaultToolsFor(queenID); categories != nil {
		for _, name := range categories {
			applied[name] = struct{}{}
		}
	}
	out := []map[string]any{}
	for _, name := range tools.ListCategoryNames() {
		if _, skip := hidden[name]; skip {
			continue
		}
		members := tools.ResolveCategoryTools(name, catalog)
		if members == nil {
			members = []string{}
		}
		_, inRole := applied[name]
		_, always := alwaysEnabled[name]
		out = append(out, map[string]any{
			"name":            name,
			"tools":           members,
			"in_role_default": inRole,
			"always_enabled":  always,
		})
	}
	return out
}

// configuredQueenAlwaysEnabledCategories mirrors
// queen_tools_defaults.configured_always_enabled_categories: the
// queen_tools.always_enabled_categories override when it is a list of strings,
// else the hardcoded always-enabled set.
func configuredQueenAlwaysEnabledCategories(deps QueenToolRouteDeps) map[string]struct{} {
	if section, ok := HiveConfig(deps.Home)["queen_tools"].(map[string]any); ok {
		if list, ok := section["always_enabled_categories"].([]any); ok {
			out := map[string]struct{}{}
			valid := true
			for _, item := range list {
				text, ok := item.(string)
				if !ok {
					valid = false
					break
				}
				out[text] = struct{}{}
			}
			if valid {
				return out
			}
		}
	}
	return tools.AlwaysEnabledCategories
}

// queenSortLifecycle returns lifecycle entries sorted by name, the way
// _lifecycle_entries renders them.
func queenSortLifecycle(entries []map[string]any) []map[string]any {
	if entries == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		copied := map[string]any{"name": entry["name"], "description": entry["description"], "editable": false}
		out = append(out, copied)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _ := out[i]["name"].(string)
		right, _ := out[j]["name"].(string)
		return left < right
	})
	return out
}

// buildQueenToolsSnapshot builds the GET /api/queen/{id}/tools payload.
func buildQueenToolsSnapshot(deps QueenToolRouteDeps, queenID string) map[string]any {
	session := queenLiveSessionFor(deps.Manager, queenID)
	catalog := map[string][]map[string]any{}
	if deps.MCPCatalog != nil {
		catalog = normalizeQueenMCPCatalog(deps.MCPCatalog(queenID, session))
	}
	stale := len(catalog) == 0

	var lifecycle []map[string]any
	if deps.LifecycleTools != nil {
		lifecycle = queenSortLifecycle(deps.LifecycleTools(queenID, session))
	} else {
		lifecycle = []map[string]any{}
	}

	enabled := loadQueenToolsConfig(deps, queenID, catalog)
	isRoleDefault := !queenToolsConfigExists(deps.queensDir(), queenID)
	connected := deps.connectedProviders()
	connectedList := make([]string, 0, len(connected))
	for provider := range connected {
		connectedList = append(connectedList, provider)
	}
	sort.Strings(connectedList)

	synthetic := queenSyntheticTools(deps)
	categories := renderQueenCategories(deps, queenID, catalog)
	return map[string]any{
		"queen_id":            queenID,
		"enabled_mcp_tools":   enabled,
		"is_role_default":     isRoleDefault,
		"stale":               stale,
		"lifecycle":           lifecycle,
		"synthetic":           synthetic,
		"mcp_servers":         renderQueenMCPServers(deps, catalog, enabled),
		"categories":          categories,
		"connected_providers": connectedList,
	}
}

// refreshQueenToolSessions hot-reloads every live session of the queen and
// reports how many filter rebuilds succeeded.
func refreshQueenToolSessions(deps QueenToolRouteDeps, queenID string, enabled any) int {
	if deps.Manager == nil {
		return 0
	}
	refreshed := 0
	for _, session := range deps.Manager.ListSessions() {
		if session.QueenName != queenID || session.PhaseState == nil {
			continue
		}
		if deps.RefreshSessionTools != nil && deps.RefreshSessionTools(session, enabled) {
			refreshed++
		}
	}
	return refreshed
}

func publishQueenToolsChanged(deps QueenToolRouteDeps, queenID, action string) {
	if deps.PublishToolsConfigChanged != nil {
		deps.PublishToolsConfigChanged("queen", queenID, action)
	}
}

// Parity: framework/server/routes_queen_tools.py::handle_get_tools
func handleQueenGetTools(w http.ResponseWriter, r *http.Request, deps QueenToolRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	if _, err := queen.LoadQueenProfile(deps.queensDir(), queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildQueenToolsSnapshot(deps, queenID))
}

// Parity: framework/server/routes_queen_tools.py::handle_patch_tools
func handleQueenPatchTools(w http.ResponseWriter, r *http.Request, deps QueenToolRouteDeps) {
	queenID := r.PathValue("queen_id")
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be an object with an 'enabled_mcp_tools' field"})
		return
	}
	raw, present := body["enabled_mcp_tools"]
	if !present {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be an object with an 'enabled_mcp_tools' field"})
		return
	}
	var enabled any
	if raw != nil {
		list, ok := queenStringList(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'enabled_mcp_tools' must be null or a list of strings"})
			return
		}
		enabled = list
	}

	queen.EnsureDefaultQueens()
	if _, err := queen.LoadQueenProfile(deps.queensDir(), queenID); err != nil {
		queenProfileError(w, err)
		return
	}

	session := queenLiveSessionFor(deps.Manager, queenID)
	catalog := map[string][]map[string]any{}
	if deps.MCPCatalog != nil {
		catalog = normalizeQueenMCPCatalog(deps.MCPCatalog(queenID, session))
	}
	known := queenCatalogNames(catalog)
	if enabled != nil && len(known) > 0 {
		unknown := []string{}
		for _, name := range enabled.([]string) {
			if _, ok := known[name]; !ok {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Unknown MCP tool name(s)", "unknown": unknown})
			return
		}
	}

	if err := writeQueenToolsConfig(deps.queensDir(), queenID, enabled); err != nil {
		queenProfileError(w, err)
		return
	}
	refreshed := refreshQueenToolSessions(deps, queenID, enabled)
	snapshot := buildQueenToolsSnapshot(deps, queenID)
	snapshot["refreshed_sessions"] = refreshed
	publishQueenToolsChanged(deps, queenID, "update")
	deps.schedulePush("tool_allowlist", "queen:"+queenID)
	writeJSON(w, http.StatusOK, snapshot)
}

// Parity: framework/server/routes_queen_tools.py::handle_delete_tools
func handleQueenDeleteTools(w http.ResponseWriter, r *http.Request, deps QueenToolRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	if _, err := queen.LoadQueenProfile(deps.queensDir(), queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	removed := deleteQueenToolsConfig(deps.queensDir(), queenID)

	session := queenLiveSessionFor(deps.Manager, queenID)
	catalog := map[string][]map[string]any{}
	if deps.MCPCatalog != nil {
		catalog = normalizeQueenMCPCatalog(deps.MCPCatalog(queenID, session))
	}
	newEnabled := loadQueenToolsConfig(deps, queenID, catalog)
	refreshed := refreshQueenToolSessions(deps, queenID, newEnabled)
	snapshot := buildQueenToolsSnapshot(deps, queenID)
	snapshot["removed"] = removed
	snapshot["refreshed_sessions"] = refreshed
	publishQueenToolsChanged(deps, queenID, "reset")
	deps.schedulePush("tool_allowlist", "queen:"+queenID)
	writeJSON(w, http.StatusOK, snapshot)
}

// RegisterQueenToolRoutes mounts the per-queen MCP tool allowlist routes.
//
// Parity: framework/server/routes_queen_tools.py::register_routes
func RegisterQueenToolRoutes(mux *http.ServeMux, deps QueenToolRouteDeps) {
	mux.HandleFunc("GET /api/queen/{queen_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		handleQueenGetTools(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/queen/{queen_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		handleQueenPatchTools(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/queen/{queen_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		handleQueenDeleteTools(w, r, deps)
	})
}
