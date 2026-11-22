package server

// Parity: framework/server/routes_mcp.py::register_routes

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

var mcpValidTransports = map[string]bool{
	"stdio": true,
	"http":  true,
	"sse":   true,
	"unix":  true,
}

var mcpValidModes = map[string]bool{
	"tools": true,
	"cli":   true,
}

// Harness groups run in-process; the Tool Library lists them alongside real
// MCP servers. Order matches dict insertion order in harness_tools.py.
var mcpHarnessGroupOrder = []string{
	"terminal-tools",
	"files-tools",
	"chart-tools",
	"memory-tools",
	"hive_tools",
}

// MCPRegistryAPI is the seam onto framework/loader/mcp_registry.py's
// MCPRegistry. Tests substitute an in-memory fake.
type MCPRegistryAPI interface {
	Initialize() error
	ListInstalled() ([]map[string]any, error)
	AddLocal(name string, opts mcpregistry.AddLocalOptions) error
	GetServer(name string) (map[string]any, error)
	Remove(name string) error
	Enable(name string) error
	Disable(name string) error
	SetMode(name, mode string) error
	HealthCheck(name string) (map[string]any, error)
}

type MCPRouteDeps struct {
	Home     hivehome.Home
	Registry func(hivehome.Home) MCPRegistryAPI
	// HarnessGroups overrides the built-in tool group descriptions. Nil uses
	// tools.HarnessGroups.
	HarnessGroups map[string]string
}

func RegisterMCPRoutes(mux *http.ServeMux, deps MCPRouteDeps) {
	if deps.Registry == nil {
		deps.Registry = func(home hivehome.Home) MCPRegistryAPI {
			return mcpregistry.NewAtHome(home)
		}
	}
	if deps.HarnessGroups == nil {
		deps.HarnessGroups = tools.HarnessGroups
	}

	mux.HandleFunc("GET /api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		handleListMCPServers(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		handleAddMCPServer(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/mcp/servers/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleRemoveMCPServer(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/servers/{name}/enable", func(w http.ResponseWriter, r *http.Request) {
		handleEnableMCPServer(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/servers/{name}/disable", func(w http.ResponseWriter, r *http.Request) {
		handleDisableMCPServer(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/servers/{name}/health", func(w http.ResponseWriter, r *http.Request) {
		handleMCPHealth(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/servers/{name}/mode", func(w http.ResponseWriter, r *http.Request) {
		handleSetMCPMode(w, r, deps)
	})
	mux.HandleFunc("POST /api/mcp/reload", func(w http.ResponseWriter, r *http.Request) {
		handleReloadMCP(w, r, deps)
	})
}

func mcpRegistryFor(deps MCPRouteDeps) (MCPRegistryAPI, error) {
	registry := deps.Registry(deps.Home)
	if err := registry.Initialize(); err != nil {
		return nil, err
	}
	return registry, nil
}

// Parity: framework/server/routes_mcp.py::_package_builtin_servers
func mcpBuiltinServers(groups map[string]string) []map[string]any {
	rows := make([]map[string]any, 0, len(mcpHarnessGroupOrder))
	for _, name := range mcpHarnessGroupOrder {
		description, ok := groups[name]
		if !ok {
			continue
		}
		rows = append(rows, map[string]any{
			"name":                 name,
			"source":               "built-in",
			"transport":            "harness",
			"description":          description,
			"enabled":              true,
			"last_health_status":   nil,
			"last_error":           nil,
			"last_health_check_at": nil,
			"tool_count":           nil,
			"removable":            false,
		})
	}
	return rows
}

// Parity: framework/server/routes_mcp.py::_server_to_summary
func mcpServerToSummary(entry map[string]any) map[string]any {
	manifest, _ := entry["manifest"].(map[string]any)
	var toolList []any
	if manifest != nil {
		switch value := manifest["tools"].(type) {
		case []any:
			toolList = value
		case []map[string]any:
			toolList = make([]any, len(value))
			for i, item := range value {
				toolList[i] = item
			}
		}
	}

	description := any("")
	if manifest != nil {
		if value, ok := manifest["description"]; ok && pyTruthy(value) {
			description = value
		}
	}
	mode := any("tools")
	if value, ok := entry["mode"]; ok {
		mode = value
	}
	enabled := any(true)
	if value, ok := entry["enabled"]; ok {
		enabled = value
	}
	var toolCount any
	if toolList != nil {
		toolCount = len(toolList)
	} else {
		toolCount = entry["last_tool_count"]
	}
	return map[string]any{
		"name":                 entry["name"],
		"source":               entry["source"],
		"transport":            entry["transport"],
		"mode":                 mode,
		"description":          description,
		"enabled":              enabled,
		"last_health_status":   entry["last_health_status"],
		"last_error":           entry["last_error"],
		"last_health_check_at": entry["last_health_check_at"],
		"tool_count":           toolCount,
	}
}

// Parity: framework/server/routes_mcp.py::_mcp_error_response
func mcpErrorResponse(w http.ResponseWriter, err error, defaultStatus int) bool {
	var mcErr *mcpregistry.Error
	if !errors.As(err, &mcErr) {
		return false
	}
	writeJSON(w, defaultStatus, map[string]any{
		"error": mcErr.What,
		"code":  mcErr.Code,
		"what":  mcErr.What,
		"why":   mcErr.Why,
		"fix":   mcErr.Fix,
	})
	return true
}

func mcpInternalError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

// Parity: framework/server/routes_mcp.py::handle_list_servers
func handleListMCPServers(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	installed, err := registry.ListInstalled()
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	registryRows := make([]map[string]any, 0, len(installed))
	seen := map[string]bool{}
	for _, entry := range installed {
		summary := mcpServerToSummary(entry)
		registryRows = append(registryRows, summary)
		if name, ok := summary["name"].(string); ok {
			seen[name] = true
		}
	}

	servers := make([]map[string]any, 0, len(registryRows)+len(mcpHarnessGroupOrder))
	for _, row := range mcpBuiltinServers(deps.HarnessGroups) {
		if name, ok := row["name"].(string); ok && seen[name] {
			continue
		}
		servers = append(servers, row)
	}
	servers = append(servers, registryRows...)
	servers = mcpApplyRuntimeStatus(servers)
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// mcpApplyRuntimeStatus merges the serve loader's connection snapshot into the
// registry listing. The registry only knows what is configured on disk; the
// loader knows what actually connected at boot and how many tools it fetched,
// which is what the dashboard needs to show a server as live.
//
// A connected server the registry does not list (for example one declared only
// in configuration.json) is appended so a pinned server is never invisible.
func mcpApplyRuntimeStatus(rows []map[string]any) []map[string]any {
	attacher := tools.ProcessMCP()
	if attacher == nil {
		return rows
	}
	statuses := attacher.Status()
	if len(statuses) == 0 {
		return rows
	}
	byName := make(map[string]tools.MCPServerStatus, len(statuses))
	order := make([]string, 0, len(statuses))
	for _, status := range statuses {
		byName[status.Name] = status
		order = append(order, status.Name)
	}
	listed := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		status, ok := byName[name]
		if !ok {
			continue
		}
		if source, _ := row["source"].(string); source == "built-in" {
			continue
		}
		listed[name] = struct{}{}
		row["connected"] = status.Connected
		if status.Connected {
			row["tool_count"] = status.Tools
			if len(status.ToolNames) > 0 {
				row["tool_names"] = mcpStringAny(status.ToolNames)
			}
			continue
		}
		if status.Error != "" {
			row["last_error"] = status.Error
		}
	}
	for _, name := range order {
		if _, ok := listed[name]; ok {
			continue
		}
		status := byName[name]
		row := map[string]any{
			"name":                 status.Name,
			"source":               "local",
			"transport":            status.Transport,
			"mode":                 "tools",
			"description":          "",
			"enabled":              status.Enabled,
			"connected":            status.Connected,
			"tool_count":           status.Tools,
			"last_health_status":   nil,
			"last_error":           nil,
			"last_health_check_at": nil,
		}
		if !status.Connected && status.Error != "" {
			row["last_error"] = status.Error
		}
		if len(status.ToolNames) > 0 {
			row["tool_names"] = mcpStringAny(status.ToolNames)
		}
		rows = append(rows, row)
	}
	return rows
}

func mcpStringAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// handleReloadMCP re-reads the MCP configuration and reconnects the pool without
// restarting serve. Sessions already running pick the new server set up on their
// next tool call (the registry re-attaches when the loader generation changes).
//
// Parity-exempt: upstream has no manual reload route — its loader reads the MCP
// config once at process start, so a reload there means a restart.
func handleReloadMCP(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	attacher := tools.ProcessMCP()
	if attacher == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"reloaded": false,
			"servers":  []any{},
			"hint":     "MCP is not configured for this server process; add a server under /api/mcp/servers.",
		})
		return
	}
	if err := attacher.Reload(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "reloaded": false})
		return
	}
	statuses := attacher.Status()
	rows := make([]any, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, map[string]any{
			"name":        status.Name,
			"transport":   status.Transport,
			"enabled":     status.Enabled,
			"connected":   status.Connected,
			"tool_count":  status.Tools,
			"tool_names":  mcpStringAny(status.ToolNames),
			"last_error":  mcpNullableError(status.Error),
			"source":      "local",
			"description": "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reloaded": true,
		"servers":  rows,
		"hint":     "Running sessions pick the reloaded servers up on their next tool call.",
	})
}

func mcpNullableError(message string) any {
	if message == "" {
		return nil
	}
	return message
}

// mcpStringList coerces a JSON array into []string the way the Python route's
// passthrough reaches MCPRegistry (values are otherwise stringified).
func mcpStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if direct, ok := value.([]string); ok {
			return direct
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, pyStrOrEmpty(item))
	}
	return out
}

func mcpStringMap(value any) map[string]string {
	object, ok := value.(map[string]any)
	if !ok {
		if direct, ok := value.(map[string]string); ok {
			return direct
		}
		return nil
	}
	out := make(map[string]string, len(object))
	for key, item := range object {
		out[key] = pyStrOrEmpty(item)
	}
	return out
}

// Parity: framework/server/routes_mcp.py::handle_add_server
func handleAddMCPServer(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	value, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := value.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be a JSON object"})
		return
	}

	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'name' is required"})
		return
	}
	transport, _ := body["transport"].(string)
	if !mcpValidTransports[transport] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'transport' must be one of ['http', 'sse', 'stdio', 'unix']"})
		return
	}
	mode := "tools"
	if raw, present := body["mode"]; present {
		mode = pyStrOrEmpty(raw)
	}
	if !mcpValidModes[mode] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'mode' must be one of ['cli', 'tools']"})
		return
	}

	description := ""
	if raw, ok := body["description"]; ok {
		description = pyStrOrEmpty(raw)
	}
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	name = strings.TrimSpace(name)
	err = registry.AddLocal(name, mcpregistry.AddLocalOptions{
		Transport:   transport,
		Command:     pyStrOrEmpty(body["command"]),
		Args:        mcpStringList(body["args"]),
		Env:         mcpStringMap(body["env"]),
		Cwd:         pyStrOrEmpty(body["cwd"]),
		URL:         pyStrOrEmpty(body["url"]),
		Headers:     mcpStringMap(body["headers"]),
		SocketPath:  pyStrOrEmpty(body["socket_path"]),
		Description: description,
		Mode:        mode,
	})
	if err != nil {
		status := http.StatusBadRequest
		var mcErr *mcpregistry.Error
		if errors.As(err, &mcErr) && strings.Contains(mcErr.What, "already exists") {
			status = http.StatusConflict
		}
		if !mcpErrorResponse(w, err, status) {
			log.Printf("MCP add_local failed for %q: %v", name, err)
			mcpInternalError(w, err)
		}
		return
	}

	entry, err := registry.GetServer(name)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	if entry == nil {
		entry = map[string]any{
			"name":        name,
			"source":      "local",
			"transport":   transport,
			"mode":        mode,
			"description": description,
			"enabled":     true,
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"server": mcpServerToSummary(entry),
		"hint":   "Start a new queen session to use this server's tools.",
	})
}

// Parity: framework/server/routes_mcp.py::handle_remove_server
func handleRemoveMCPServer(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	name := r.PathValue("name")
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	existing, err := registry.GetServer(name)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Server '%s' not installed", name)})
		return
	}
	if source, _ := existing["source"].(string); source != "local" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("Server '%s' is a built-in; it cannot be removed from the UI.", name),
		})
		return
	}
	if err := registry.Remove(name); err != nil {
		if !mcpErrorResponse(w, err, http.StatusNotFound) {
			mcpInternalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": name})
}

// Parity: framework/server/routes_mcp.py::handle_set_enabled
func handleSetMCPEnabled(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps, enabled bool) {
	name := r.PathValue("name")
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	if enabled {
		err = registry.Enable(name)
	} else {
		err = registry.Disable(name)
	}
	if err != nil {
		if !mcpErrorResponse(w, err, http.StatusNotFound) {
			mcpInternalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "enabled": enabled})
}

// Parity: framework/server/routes_mcp.py::handle_enable, framework/server/routes_mcp.py::handle_disable
func handleEnableMCPServer(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	handleSetMCPEnabled(w, r, deps, true)
}

func handleDisableMCPServer(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	handleSetMCPEnabled(w, r, deps, false)
}

// Parity: framework/server/routes_mcp.py::handle_set_mode
func handleSetMCPMode(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	name := r.PathValue("name")
	value, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, _ := value.(map[string]any)
	mode := ""
	if body != nil {
		mode = pyStrOrEmpty(body["mode"])
	}
	if !mcpValidModes[mode] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'mode' must be one of ['cli', 'tools']"})
		return
	}
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	if err := registry.SetMode(name, mode); err != nil {
		if !mcpErrorResponse(w, err, http.StatusNotFound) {
			mcpInternalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name,
		"mode": mode,
		"hint": "Start a new queen session to apply it.",
	})
}

// Parity: framework/server/routes_mcp.py::handle_health
func handleMCPHealth(w http.ResponseWriter, r *http.Request, deps MCPRouteDeps) {
	name := r.PathValue("name")
	registry, err := mcpRegistryFor(deps)
	if err != nil {
		mcpInternalError(w, err)
		return
	}
	result, err := registry.HealthCheck(name)
	if err != nil {
		if !mcpErrorResponse(w, err, http.StatusNotFound) {
			log.Printf("MCP health_check failed for %q: %v", name, err)
			mcpInternalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
