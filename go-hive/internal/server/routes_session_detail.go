package server

// Parity: framework/server/routes_sessions.py::handle_get_live_session
// Parity: framework/server/routes_sessions.py::handle_stop_session
// Parity: framework/server/routes_sessions.py::handle_session_stats
// Parity: framework/server/routes_sessions.py::handle_list_triggers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/triggers"
)

type colonyStatsProvider interface {
	GetStats() map[string]any
}

// Parity: framework/server/session_manager.py::SessionManager.get_cold_session_info
func ColdSessionInfo(home hivehome.Home, sessionID string) map[string]any {
	queenDir, err := FindQueenSessionDir(home, sessionID)
	if err != nil {
		return nil
	}
	convsDir := filepath.Join(queenDir, "conversations")
	if !pathExists(convsDir) {
		return nil
	}

	hasMessages := false
	if flatParts := filepath.Join(convsDir, "parts"); pathExists(flatParts) && hasJSONPart(flatParts) {
		hasMessages = true
	} else if entries, err := os.ReadDir(convsDir); err == nil {
		for _, entry := range entries {
			child := filepath.Join(convsDir, entry.Name())
			if !entryIsDir(child) || entry.Name() == "parts" {
				continue
			}
			partsDir := filepath.Join(child, "parts")
			if pathExists(partsDir) && hasJSONPart(partsDir) {
				hasMessages = true
				break
			}
		}
	}

	var createdAt any = 0.0
	if info, err := os.Stat(queenDir); err == nil {
		createdAt = float64(info.ModTime().UnixNano()) / 1e9
	}

	var agentName any
	var agentPath any
	colonySpawned := false
	var spawnedColonyID any
	metaPath := filepath.Join(queenDir, "meta.json")
	if pathExists(metaPath) {
		if data, err := os.ReadFile(metaPath); err == nil {
			var meta map[string]any
			if json.Unmarshal(data, &meta) == nil {
				agentName = meta["agent_name"]
				agentPath = meta["agent_path"]
				if value, present := meta["created_at"]; present && pyTruthy(value) {
					createdAt = value
				}
				colonySpawned = pyTruthy(meta["colony_spawned"])
				if spawned, ok := meta["spawned_colony_id"].(string); ok {
					spawnedColonyID = spawned
				}
			}
		}
	}

	return map[string]any{
		"session_id":        sessionID,
		"cold":              true,
		"live":              false,
		"has_messages":      hasMessages,
		"created_at":        createdAt,
		"agent_name":        agentName,
		"agent_path":        agentPath,
		"colony_spawned":    colonySpawned,
		"spawned_colony_id": spawnedColonyID,
	}
}

func hasJSONPart(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			return true
		}
	}
	return false
}

func lookupSession(deps SessionRouteDeps, sessionID string) *Session {
	if deps.Manager == nil {
		return nil
	}
	return deps.Manager.GetSession(sessionID)
}

func sessionNotFound(w http.ResponseWriter, sessionID string) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": "Session '" + sessionID + "' not found",
	})
}

func handleGetLiveSession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		if deps.Manager != nil && deps.Manager.IsLoading(sessionID) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"session_id": sessionID,
				"loading":    true,
			})
			return
		}
		if cold := ColdSessionInfo(deps.Home, sessionID); cold != nil {
			writeJSON(w, http.StatusOK, cold)
			return
		}
		sessionNotFound(w, sessionID)
		return
	}

	data := sessionToLiveDict(session)
	data["entry_points"] = buildEntryPoints(session)
	writeJSON(w, http.StatusOK, data)
}

func buildEntryPoints(session *Session) []map[string]any {
	defs := session.AvailableTriggers()
	nextFire := session.TriggerNextFire()
	stats := session.TriggerFireStats()
	nowMono := session.NowMonotonic()
	nowWall := session.NowWall()

	out := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		config := def.TriggerConfig
		if config == nil {
			config = map[string]any{}
		}
		name := def.Description
		if name == "" {
			name = def.ID
		}
		entry := map[string]any{
			"id":             def.ID,
			"name":           name,
			"entry_node":     "",
			"trigger_type":   def.TriggerType,
			"trigger_config": config,
			"task":           def.Task,
		}
		if mono, ok := nextFire[def.ID]; ok {
			remaining := mono - nowMono
			if remaining < 0 {
				remaining = 0
			}
			entry["next_fire_in"] = remaining
			entry["next_fire_at"] = int64((nowWall + remaining) * 1000)
		}
		if stat, ok := stats[def.ID]; ok {
			entry["fire_count"] = stat.FireCount
			if stat.LastFiredAt != nil {
				entry["last_fired_at"] = *stat.LastFiredAt
			}
		}
		out = append(out, entry)
	}
	return out
}

func handleStopSession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	if deps.Manager == nil || !deps.Manager.StopSession(sessionID) {
		sessionNotFound(w, sessionID)
		return
	}
	if deps.OnSessionStopped != nil {
		deps.OnSessionStopped(sessionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"stopped":    true,
	})
}

func handleSessionStats(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	// Upstream answers colony.get_stats() verbatim. A session whose colony has
	// not spawned still owes the client the same field set, so the session
	// baseline is rendered first and colony values overlay it field-by-field.
	var colonyStats map[string]any
	if session.Colony != nil {
		if provider, ok := session.Colony.(colonyStatsProvider); ok {
			colonyStats = provider.GetStats()
		}
	}
	writeJSON(w, http.StatusOK, buildSessionStats(session, colonyStats))
}

func handleListTriggers(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"triggers": triggers.BuildTriggerView(session),
	})
}
