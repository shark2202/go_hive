package server

// Parity: framework/server/routes_colony_tools.py::handle_get_tools
// Parity: framework/server/routes_colony_tools.py::handle_patch_tools
// Parity: framework/server/routes_colony_tools.py::handle_list_colonies

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const colonyToolNote = "Changes apply to the next worker spawn. Running workers keep their booted tool list."

type ColonyToolEntry struct {
	Name        string
	Description string
	InputSchema map[string]any
	Provider    string
}

// ColonyToolsDeps carries the seams the tool-library routes read. Manager is
// used to refresh live colony runtimes; MCPCatalog is the registry snapshot
// (nil means no registry is available yet, which the route reports as stale).
type ColonyToolsDeps struct {
	Home        hivehome.Home
	Manager     *SessionManager
	MCPCatalog  func() map[string][]map[string]any
	Lifecycle   func(colonyID string) []map[string]any
	Providers   func() map[string]string
	Connected   func() []string
	Publish     func(colonyID string)
	AppVersion  func() string
	Clock       func() time.Time
	colonyGuard func(colonyID string) bool
}

func RegisterColonyToolRoutes(mux *http.ServeMux, deps ColonyToolsDeps) {
	mux.HandleFunc("GET /api/colonies/tools-index", func(w http.ResponseWriter, r *http.Request) {
		handleListColoniesToolsIndex(w, r, deps)
	})
	mux.HandleFunc("GET /api/colony/{colony_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		handleGetColonyTools(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/colony/{colony_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		handlePatchColonyTools(w, r, deps)
	})
}

func (d ColonyToolsDeps) colonyMetadataPath(colonyID string) string {
	return filepath.Join(d.Home.ColoniesDir(), colonyID, "metadata.json")
}

func (d ColonyToolsDeps) colonyExists(colonyID string) bool {
	if validateColonyName(colonyID) != "" {
		return false
	}
	if d.colonyGuard != nil {
		return d.colonyGuard(colonyID)
	}
	info, err := os.Stat(d.colonyMetadataPath(colonyID))
	return err == nil && !info.IsDir()
}

func (d ColonyToolsDeps) appVersion() string {
	if d.AppVersion != nil {
		return d.AppVersion()
	}
	if value := os.Getenv("HIVE_APP_VERSION"); value != "" {
		return value
	}
	return "0.0.0"
}

func (d ColonyToolsDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

func (d ColonyToolsDeps) connectedProviders() []string {
	if d.Connected == nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, name := range d.Connected() {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (d ColonyToolsDeps) syntheticEntries() []map[string]any {
	return []map[string]any{{
		"name":        "ask_user",
		"description": "Pause and ask the user a structured question.",
		"editable":    false,
	}}
}

func (d ColonyToolsDeps) catalog(colonyID string) map[string][]ColonyToolEntry {
	out := map[string][]ColonyToolEntry{}
	appendCatalog := func(raw map[string][]map[string]any) {
		for server, entries := range raw {
			converted := make([]ColonyToolEntry, 0, len(entries))
			for _, entry := range entries {
				name, _ := entry["name"].(string)
				description, _ := entry["description"].(string)
				schema, _ := entry["input_schema"].(map[string]any)
				if schema == nil {
					schema = map[string]any{}
				}
				provider, _ := entry["provider"].(string)
				converted = append(converted, ColonyToolEntry{
					Name:        name,
					Description: description,
					InputSchema: schema,
					Provider:    provider,
				})
			}
			sort.SliceStable(converted, func(i, j int) bool { return converted[i].Name < converted[j].Name })
			out[server] = converted
		}
	}

	if d.MCPCatalog != nil {
		appendCatalog(d.MCPCatalog())
		return out
	}
	if d.Manager != nil {
		for _, session := range d.Manager.ListSessions() {
			if session == nil || session.ColonyID != colonyID {
				continue
			}
			if source, ok := session.QueenToolRegistry.(interface {
				GetFullMCPCatalog() map[string][]map[string]any
			}); ok {
				appendCatalog(source.GetFullMCPCatalog())
				return out
			}
		}
	}
	return out
}

func (d ColonyToolsDeps) toolProviders() map[string]string {
	if d.Providers == nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for name, provider := range d.Providers() {
		if name != "" && provider != "" {
			out[name] = provider
		}
	}
	return out
}

func (d ColonyToolsDeps) lifecycle(colonyID string) []map[string]any {
	if d.Lifecycle != nil {
		entries := d.Lifecycle(colonyID)
		if entries == nil {
			return []map[string]any{}
		}
		return entries
	}
	if d.Manager != nil {
		for _, session := range d.Manager.ListSessions() {
			if session == nil || session.ColonyID != colonyID {
				continue
			}
			if source, ok := session.QueenToolRegistry.(interface {
				LifecycleToolEntries() []map[string]any
			}); ok {
				entries := source.LifecycleToolEntries()
				if entries == nil {
					return []map[string]any{}
				}
				return entries
			}
		}
	}
	return []map[string]any{}
}

func renderColonyToolServers(catalog map[string][]ColonyToolEntry, enabled []string, connected []string) []map[string]any {
	var allowed map[string]struct{}
	if enabled != nil {
		allowed = map[string]struct{}{}
		for _, name := range enabled {
			allowed[name] = struct{}{}
		}
	}
	providerConnected := map[string]struct{}{}
	for _, name := range connected {
		providerConnected[name] = struct{}{}
	}

	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools := make([]map[string]any, 0, len(catalog[name]))
		for _, entry := range catalog[name] {
			provider := entry.Provider
			var providerValue any
			if provider != "" {
				providerValue = provider
			}
			enabledTool := true
			if allowed != nil {
				_, enabledTool = allowed[entry.Name]
			}
			connectedTool := true
			if provider != "" {
				_, connectedTool = providerConnected[provider]
			}
			tools = append(tools, map[string]any{
				"name":               entry.Name,
				"description":        entry.Description,
				"input_schema":       entry.InputSchema,
				"enabled":            enabledTool,
				"provider":           providerValue,
				"provider_connected": connectedTool,
			})
		}
		servers = append(servers, map[string]any{"name": name, "tools": tools})
	}
	return servers
}

func (d ColonyToolsDeps) toolsConfigPath(colonyID string) string {
	return filepath.Join(d.Home.ColoniesDir(), colonyID, "tools.json")
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeJSONBytes(data)
	if err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return object, nil
}

func stringList(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
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

// loadColonyToolsConfig mirrors framework/host/colony_tools_config.py: the
// tools.json sidecar wins, and a legacy metadata.json field is migrated on
// first read.
func loadColonyToolsConfig(home hivehome.Home, colonyID string) []string {
	sidecar := filepath.Join(home.ColoniesDir(), colonyID, "tools.json")
	if _, err := os.Stat(sidecar); err == nil {
		data, readErr := readJSONObject(sidecar)
		if readErr != nil {
			return nil
		}
		raw, present := data["enabled_mcp_tools"]
		if !present || raw == nil {
			return nil
		}
		values, ok := stringList(raw)
		if !ok {
			return nil
		}
		return values
	}
	return migrateColonyToolsConfig(home, colonyID)
}

func migrateColonyToolsConfig(home hivehome.Home, colonyID string) []string {
	metadataPath := filepath.Join(home.ColoniesDir(), colonyID, "metadata.json")
	data, err := readJSONObject(metadataPath)
	if err != nil {
		return nil
	}
	raw, present := data["enabled_mcp_tools"]
	if !present {
		return nil
	}
	delete(data, "enabled_mcp_tools")
	var enabled []string
	if raw != nil {
		if values, ok := stringList(raw); ok {
			enabled = values
		}
	}
	_ = writeColonyToolsSidecar(home, colonyID, enabled)
	_ = writeJSONObjectAtomic(metadataPath, data, ".metadata.")
	return enabled
}

func writeColonyToolsSidecar(home hivehome.Home, colonyID string, enabled []string) error {
	payload := map[string]any{
		"enabled_mcp_tools": enabled,
		"updated_at":        time.Now().UTC().Format(time.RFC3339Nano),
		"saved_on_version":  appVersionFromEnv(),
	}
	return writeJSONObjectAtomic(filepath.Join(home.ColoniesDir(), colonyID, "tools.json"), payload, ".tools.")
}

func appVersionFromEnv() string {
	if value := os.Getenv("HIVE_APP_VERSION"); value != "" {
		return value
	}
	return "0.0.0"
}

func writeJSONObjectAtomic(path string, data map[string]any, prefix string) error {
	rendered, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, prefix+"*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(rendered); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func updateColonyToolsConfig(home hivehome.Home, colonyID string, enabled []string) error {
	colonyDir := filepath.Join(home.ColoniesDir(), colonyID)
	if info, err := os.Stat(colonyDir); err != nil || !info.IsDir() {
		return os.ErrNotExist
	}
	return writeColonyToolsSidecar(home, colonyID, enabled)
}

func handleListColoniesToolsIndex(w http.ResponseWriter, r *http.Request, deps ColonyToolsDeps) {
	entries, err := os.ReadDir(deps.Home.ColoniesDir())
	colonies := []map[string]any{}
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, statErr := os.Stat(filepath.Join(deps.Home.ColoniesDir(), entry.Name(), "metadata.json")); statErr != nil {
				continue
			}
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			meta, _ := readJSONObject(filepath.Join(deps.Home.ColoniesDir(), name, "metadata.json"))
			allowlist := loadColonyToolsConfig(deps.Home, name)
			var enabledCount any
			if allowlist != nil {
				enabledCount = len(allowlist)
			}
			colonies = append(colonies, map[string]any{
				"name":          name,
				"queen_name":    meta["queen_name"],
				"created_at":    meta["created_at"],
				"has_allowlist": allowlist != nil,
				"enabled_count": enabledCount,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"colonies": colonies})
}

func handleGetColonyTools(w http.ResponseWriter, r *http.Request, deps ColonyToolsDeps) {
	colonyID := r.PathValue("colony_id")
	if !deps.colonyExists(colonyID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Colony '" + colonyID + "' not found"})
		return
	}

	enabled := loadColonyToolsConfig(deps.Home, colonyID)
	catalog := deps.catalog(colonyID)
	providers := deps.toolProviders()
	for server, entries := range catalog {
		for index := range entries {
			if entries[index].Provider == "" {
				entries[index].Provider = providers[entries[index].Name]
			}
		}
		catalog[server] = entries
	}
	connected := deps.connectedProviders()

	var enabledValue any
	if enabled != nil {
		enabledValue = enabled
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"colony_id":           colonyID,
		"enabled_mcp_tools":   enabledValue,
		"stale":               len(catalog) == 0,
		"lifecycle":           deps.lifecycle(colonyID),
		"synthetic":           deps.syntheticEntries(),
		"mcp_servers":         renderColonyToolServers(catalog, enabled, connected),
		"connected_providers": connected,
	})
}

func handlePatchColonyTools(w http.ResponseWriter, r *http.Request, deps ColonyToolsDeps) {
	colonyID := r.PathValue("colony_id")
	if !deps.colonyExists(colonyID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Colony '" + colonyID + "' not found"})
		return
	}

	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	raw, present := body["enabled_mcp_tools"]
	if !present {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Body must be an object with an 'enabled_mcp_tools' field",
		})
		return
	}

	var enabled []string
	if raw != nil {
		values, valid := stringList(raw)
		if !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "'enabled_mcp_tools' must be null or a list of strings",
			})
			return
		}
		enabled = values
	}

	catalog := deps.catalog(colonyID)
	known := map[string]struct{}{}
	for _, entries := range catalog {
		for _, entry := range entries {
			if entry.Name != "" {
				known[entry.Name] = struct{}{}
			}
		}
	}
	if enabled != nil && len(known) > 0 {
		unknown := []string{}
		for _, name := range enabled {
			if _, ok := known[name]; !ok {
				unknown = append(unknown, name)
			}
		}
		sort.Strings(unknown)
		if len(unknown) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "Unknown MCP tool name(s)",
				"unknown": unknown,
			})
			return
		}
	}

	if err := updateColonyToolsConfig(deps.Home, colonyID, enabled); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Colony '" + colonyID + "' not found"})
		return
	}

	refreshed := 0
	if deps.Manager != nil {
		for _, session := range deps.Manager.ListSessions() {
			if session == nil || session.ColonyID != colonyID || session.Colony == nil {
				continue
			}
			if setter, ok := session.Colony.(interface {
				SetToolAllowlist([]string, map[string]struct{})
			}); ok {
				setter.SetToolAllowlist(enabled, nil)
				refreshed++
			}
		}
	}
	if deps.Publish != nil {
		deps.Publish(colonyID)
	}

	var enabledValue any
	if enabled != nil {
		enabledValue = enabled
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"colony_id":          colonyID,
		"enabled_mcp_tools":  enabledValue,
		"refreshed_runtimes": refreshed,
		"note":               colonyToolNote,
	})
}
