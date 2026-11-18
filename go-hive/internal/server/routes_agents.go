package server

// Parity: framework/server/routes_sessions.py::handle_discover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"unicode/utf16"

	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

type AgentRouteDeps struct {
	Manager *SessionManager
	Home    hivehome.Home
	Roots   *agents.AllowedRoots
}

// Parity: framework/server/routes_sessions.py::register_routes
// Parity: framework/server/routes_credentials.py::register_routes
func RegisterAgentRoutes(mux *http.ServeMux, deps AgentRouteDeps) {
	mux.HandleFunc("GET /api/discover", func(w http.ResponseWriter, r *http.Request) {
		handleDiscoverAgents(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/agents", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteAgent(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/agents/metadata", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateAgentMetadata(w, r, deps)
	})
	mux.HandleFunc("POST /api/credentials/check-agent", func(w http.ResponseWriter, r *http.Request) {
		handleCheckAgent(w, r, deps)
	})
}

func (deps AgentRouteDeps) allowedRoots() *agents.AllowedRoots {
	if deps.Roots != nil {
		return deps.Roots
	}
	return agents.NewAllowedRoots(deps.Home.ColoniesDir(), repositoryRoot(), filepath.Join(deps.Home.Root, "agents"))
}

// Parity: framework/server/app.py::_REPO_ROOT
func repositoryRoot() string {
	here := packageSourceDir()
	if here == "" {
		return ""
	}
	return filepath.Join(here, "..", "..", "..", "..")
}

// Parity: framework/server/routes_sessions.py::handle_discover
func handleDiscoverAgents(w http.ResponseWriter, r *http.Request, deps AgentRouteDeps) {
	loaded := map[string]struct{}{}
	if deps.Manager != nil {
		for _, session := range deps.Manager.ListSessions() {
			if session.WorkerPath != "" {
				loaded[session.WorkerPath] = struct{}{}
			}
		}
	}
	groups := agents.DiscoverAgents(deps.Home.ColoniesDir())
	result := map[string]any{}
	for category, entries := range groups {
		items := make([]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, discoverEntryDict(entry, loaded))
		}
		result[category] = items
	}
	writeJSON(w, http.StatusOK, result)
}

func discoverEntryDict(entry agents.AgentEntry, loaded map[string]struct{}) map[string]any {
	workers := make([]any, 0, len(entry.Workers))
	for _, worker := range entry.Workers {
		workers = append(workers, worker.ToDict())
	}
	var lastActive any
	if entry.LastActive != nil {
		lastActive = *entry.LastActive
	}
	_, isLoaded := loaded[entry.Path]
	return map[string]any{
		"path":          entry.Path,
		"name":          entry.Name,
		"description":   entry.Description,
		"category":      entry.Category,
		"session_count": entry.SessionCount,
		"run_count":     entry.RunCount,
		"node_count":    entry.NodeCount,
		"tool_count":    entry.ToolCount,
		"tags":          entry.Tags,
		"last_active":   lastActive,
		"created_at":    entry.CreatedAt,
		"icon":          entry.Icon,
		"is_loaded":     isLoaded,
		"workers":       workers,
	}
}

// Parity: framework/server/routes_sessions.py::handle_delete_agent
func handleDeleteAgent(w http.ResponseWriter, r *http.Request, deps AgentRouteDeps) {
	raw, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	agentPath := body["agent_path"]
	purge := pythonTruthy(body["purge"])
	if !pythonTruthy(agentPath) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	resolved, err := deps.allowedRoots().Validate(pathString)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Upstream compares against the unresolved HIVE_HOME/agents path, which
	// misses the guard when the home path crosses a symlink. Resolving both
	// sides keeps framework agents protected on such setups.
	hiveAgentsDir, resolveErr := agents.ResolvePath(filepath.Join(deps.Home.Root, "agents"))
	if resolveErr == nil && agents.IsRelativeTo(resolved, hiveAgentsDir) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Cannot delete framework agents"})
		return
	}

	if deps.Manager != nil {
		for _, session := range deps.Manager.ListSessions() {
			if session.WorkerPath != "" && session.WorkerPath == resolved {
				deps.Manager.StopSession(session.ID)
			}
		}
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": purge})
		return
	}

	if purge {
		if err := os.RemoveAll(resolved); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to delete agent directory: %v", err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": true})
		return
	}

	metadataPath := filepath.Join(resolved, "metadata.json")
	metadata := map[string]any{}
	if data, readErr := os.ReadFile(metadataPath); readErr == nil {
		parsed, parseErr := decodeJSONBytes(data)
		if parseErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if dict, isDict := parsed.(map[string]any); isDict {
			metadata = dict
		}
	} else if !os.IsNotExist(readErr) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to mark agent deleted: %v", readErr)})
		return
	}
	metadata["deleted"] = true
	payload, marshalErr := marshalPythonIndent(metadata, true)
	if marshalErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if err := os.WriteFile(metadataPath, payload, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to mark agent deleted: %v", err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": false})
}

// Parity: framework/server/routes_sessions.py::handle_update_colony_metadata
func handleUpdateAgentMetadata(w http.ResponseWriter, r *http.Request, deps AgentRouteDeps) {
	raw, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	agentPath := body["agent_path"]
	if !pythonTruthy(agentPath) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	resolved, err := deps.allowedRoots().Validate(pathString)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	metadataPath := filepath.Join(resolved, "metadata.json")
	var metadata any = map[string]any{}
	if data, readErr := os.ReadFile(metadataPath); readErr == nil {
		if parsed, parseErr := decodeJSONBytes(data); parseErr == nil {
			metadata = parsed
		}
	}

	if icon, present := body["icon"]; present {
		dict, isDict := metadata.(map[string]any)
		if !isDict {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		dict["icon"] = icon
	}

	payload, marshalErr := marshalPythonIndent(metadata, false)
	if marshalErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if err := os.WriteFile(metadataPath, payload, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Parity: framework/server/routes_credentials.py::handle_check_agent
func handleCheckAgent(w http.ResponseWriter, r *http.Request, deps AgentRouteDeps) {
	raw, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	agentPath := body["agent_path"]
	if !pythonTruthy(agentPath) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if _, err := deps.allowedRoots().Validate(pathString); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal server error while checking credentials"})
}

func marshalPythonIndent(value any, ensureASCII bool) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if !ensureASCII {
		return data, nil
	}
	return escapeNonASCIIBytes(data), nil
}

func escapeNonASCIIBytes(data []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(data) {
		if r < 0x80 {
			out.WriteByte(byte(r))
			continue
		}
		if r <= 0xFFFF {
			fmt.Fprintf(&out, "\\u%04x", r)
			continue
		}
		high, low := utf16.EncodeRune(r)
		fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
	}
	return out.Bytes()
}
