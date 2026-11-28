// Package routesagents serves the agent discovery and metadata routes:
// GET /api/discover, DELETE /api/agents, PATCH /api/agents/metadata and
// POST /api/credentials/check-agent. It depends on routeutil and the Deps
// callback seams, never on the parent server package.
package routesagents

// Parity: framework/server/routes_sessions.py::handle_discover
// Parity: framework/server/routes_sessions.py::handle_delete_agent
// Parity: framework/server/routes_sessions.py::handle_update_colony_metadata
// Parity: framework/server/routes_credentials.py::handle_check_agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf16"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/server/routeutil"
)

// SessionRef is the live-session projection the agent routes need: the
// discovery route marks a worker path as loaded and the delete route stops
// sessions whose worker lives in the deleted directory. Keeping the seam at
// this shape lets the routes avoid importing the server package's
// SessionManager/Session types.
type SessionRef struct {
	ID         string
	WorkerPath string
}

// Deps carries the seams the agent routes need. Home and Roots drive path
// validation; Sessions/StopSession replace the server package's
// SessionManager, and CheckAgentCredentials replaces the credential
// validation that lives with the credential routes.
type Deps struct {
	Home  hivehome.Home
	Roots *agents.AllowedRoots
	// Sessions returns the currently loaded sessions. Nil means no session
	// is loaded (the discovery route then reports every agent unloaded).
	Sessions func() []SessionRef
	// StopSession stops one session before its worker directory is deleted.
	// Nil means there is no session runtime to stop.
	StopSession func(id string)
	// CheckAgentCredentials validates an already path-checked agent
	// directory and returns the check-agent response body. The server
	// package wires its credential implementation here.
	CheckAgentCredentials func(path string, verify bool) (map[string]any, error)
}

// RegisterRoutes installs the four agent routes on mux.
// Parity: framework/server/routes_sessions.py::register_routes
// Parity: framework/server/routes_credentials.py::register_routes
func RegisterRoutes(mux *http.ServeMux, deps Deps) {
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

func (deps Deps) allowedRoots() *agents.AllowedRoots {
	if deps.Roots != nil {
		return deps.Roots
	}
	return agents.NewAllowedRoots(deps.Home.ColoniesDir(), repositoryRoot(), filepath.Join(deps.Home.Root, "agents"))
}

// repositoryRoot resolves the checkout root the same way the server
// package's repositoryRoot does. This file used to live in internal/server,
// one directory up, so the extraction adds one more hop to keep the value
// byte-identical.
//
// Parity: framework/server/app.py::_REPO_ROOT
func repositoryRoot() string {
	here := packageSourceDir()
	if here == "" {
		return ""
	}
	return filepath.Join(here, "..", "..", "..", "..", "..")
}

func packageSourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}

// Parity: framework/server/routes_sessions.py::handle_discover
func handleDiscoverAgents(w http.ResponseWriter, r *http.Request, deps Deps) {
	loaded := map[string]struct{}{}
	if deps.Sessions != nil {
		for _, session := range deps.Sessions() {
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
	routeutil.WriteJSON(w, http.StatusOK, result)
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
func handleDeleteAgent(w http.ResponseWriter, r *http.Request, deps Deps) {
	raw, err := routeutil.DecodeJSONValue(r)
	if err != nil {
		routeutil.WriteBodyFailure(w, err, http.StatusInternalServerError, routeutil.InternalServerError)
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	agentPath := body["agent_path"]
	purge := routeutil.PyTruthy(body["purge"])
	if !routeutil.PyTruthy(agentPath) {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	resolved, err := deps.allowedRoots().Validate(pathString)
	if err != nil {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Upstream compares against the unresolved HIVE_HOME/agents path, which
	// misses the guard when the home path crosses a symlink. Resolving both
	// sides keeps framework agents protected on such setups.
	hiveAgentsDir, resolveErr := agents.ResolvePath(filepath.Join(deps.Home.Root, "agents"))
	if resolveErr == nil && agents.IsRelativeTo(resolved, hiveAgentsDir) {
		routeutil.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "Cannot delete framework agents"})
		return
	}

	if deps.Sessions != nil {
		for _, session := range deps.Sessions() {
			if session.WorkerPath != "" && session.WorkerPath == resolved {
				if deps.StopSession != nil {
					deps.StopSession(session.ID)
				}
			}
		}
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		routeutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": purge})
		return
	}

	if purge {
		if err := os.RemoveAll(resolved); err != nil {
			routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to delete agent directory: %v", err)})
			return
		}
		routeutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": true})
		return
	}

	metadataPath := filepath.Join(resolved, "metadata.json")
	metadata := map[string]any{}
	if data, readErr := os.ReadFile(metadataPath); readErr == nil {
		parsed, parseErr := routeutil.DecodeJSONBytes(data)
		if parseErr != nil {
			routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
			return
		}
		if dict, isDict := parsed.(map[string]any); isDict {
			metadata = dict
		}
	} else if !os.IsNotExist(readErr) {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to mark agent deleted: %v", readErr)})
		return
	}
	metadata["deleted"] = true
	payload, marshalErr := marshalPythonIndent(metadata, true)
	if marshalErr != nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	if err := os.WriteFile(metadataPath, payload, 0o644); err != nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("Failed to mark agent deleted: %v", err)})
		return
	}
	routeutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": resolved, "purged": false})
}

// Parity: framework/server/routes_sessions.py::handle_update_colony_metadata
func handleUpdateAgentMetadata(w http.ResponseWriter, r *http.Request, deps Deps) {
	raw, err := routeutil.DecodeJSONValue(r)
	if err != nil {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	agentPath := body["agent_path"]
	if !routeutil.PyTruthy(agentPath) {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	resolved, err := deps.allowedRoots().Validate(pathString)
	if err != nil {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	metadataPath := filepath.Join(resolved, "metadata.json")
	var metadata any = map[string]any{}
	if data, readErr := os.ReadFile(metadataPath); readErr == nil {
		if parsed, parseErr := routeutil.DecodeJSONBytes(data); parseErr == nil {
			metadata = parsed
		}
	}

	if icon, present := body["icon"]; present {
		dict, isDict := metadata.(map[string]any)
		if !isDict {
			routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
			return
		}
		dict["icon"] = icon
	}

	payload, marshalErr := marshalPythonIndent(metadata, false)
	if marshalErr != nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	if err := os.WriteFile(metadataPath, payload, 0o644); err != nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	routeutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Parity: framework/server/routes_credentials.py::handle_check_agent
func handleCheckAgent(w http.ResponseWriter, r *http.Request, deps Deps) {
	raw, err := routeutil.DecodeJSONValue(r)
	if err != nil {
		routeutil.WriteBodyFailure(w, err, http.StatusInternalServerError, routeutil.InternalServerError)
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	agentPath := body["agent_path"]
	if !routeutil.PyTruthy(agentPath) {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_path is required"})
		return
	}
	pathString, ok := agentPath.(string)
	if !ok {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": routeutil.InternalServerError})
		return
	}
	resolved, err := deps.allowedRoots().Validate(pathString)
	if err != nil {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Body: {"agent_path": "...", "verify": true} — verify defaults on, and any
	// truthy JSON value keeps it on (Python truthiness, as upstream).
	verify := true
	if value, present := body["verify"]; present {
		verify = routeutil.PyTruthy(value)
	}

	if deps.CheckAgentCredentials == nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal server error while checking credentials"})
		return
	}
	response, checkErr := deps.CheckAgentCredentials(resolved, verify)
	if checkErr != nil {
		routeutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal server error while checking credentials"})
		return
	}
	routeutil.WriteJSON(w, http.StatusOK, response)
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
