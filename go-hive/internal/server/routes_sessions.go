package server

// Parity: framework/server/routes_sessions.py::handle_list_live_sessions

import (
	"math"
	"net/http"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/runtimehealth"
)

type SessionRouteDeps struct {
	LiveSessions      func() []*Session
	Manager           *SessionManager
	Home              hivehome.Home
	EventsReadPage    func(path string, limit int, beforeOffset *int64) ([]map[string]any, int64, int, error)
	EventsReadTimeout time.Duration
	Reveal            func(path string) error
	LivePollInterval  time.Duration
	RuntimeNetwork    func() runtimehealth.Snapshot
	Colony            ColonyLifecycle

	// CreateSession backs POST /api/sessions for the standard branch. The
	// hook owns the orchestration (colony load, queen bootstrap, resume);
	// the route owns body validation and error-to-status mapping.
	CreateSession func(CreateSessionParams) (*Session, error)
	// CreateColonyFromSource backs the fork-into-new-colony branch when both
	// colony_id and source_session_id are supplied.
	CreateColonyFromSource func(CreateColonyFromSourceParams) (*Session, error)
	// VacateSoftDeleted parks a soft-deleted colony squatting on a name
	// before the fork path reuses it.
	VacateSoftDeleted func(colonyID string) error
	// ConnectedProviders feeds the live-tools panel's provider list.
	ConnectedProviders func() []string

	// OnSessionStopped releases per-session side state (reminder hub and
	// queue) after the manager stopped the session. Nil is a no-op.
	OnSessionStopped func(sessionID string)
}

func RegisterSessionRoutes(mux *http.ServeMux, deps SessionRouteDeps) {
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		sessions := []map[string]any{}
		for _, session := range liveSessions(deps) {
			sessions = append(sessions, sessionToLiveDict(session))
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		handleCreateSession(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/live", func(w http.ResponseWriter, r *http.Request) {
		handleLiveSessionsStream(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/history", func(w http.ResponseWriter, r *http.Request) {
		handleSessionHistory(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/sessions/{first}/{second}", func(w http.ResponseWriter, r *http.Request) {
		first := r.PathValue("first")
		second := r.PathValue("second")
		switch {
		case first == "history":
			r.SetPathValue("session_id", second)
			handleDeleteHistorySession(w, r, deps)
		case second == "colony":
			r.SetPathValue("session_id", first)
			handleUnloadColony(w, r, deps)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /api/sessions/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		handleGetLiveSession(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/sessions/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		handleStopSession(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/live_tools", func(w http.ResponseWriter, r *http.Request) {
		handleSessionLiveTools(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/stats", func(w http.ResponseWriter, r *http.Request) {
		handleSessionStats(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		handleGetSessionSnapshot(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/attachment/{filename}", func(w http.ResponseWriter, r *http.Request) {
		handleSessionAttachment(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/upload-attachment", func(w http.ResponseWriter, r *http.Request) {
		handleUploadAttachment(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/triggers", func(w http.ResponseWriter, r *http.Request) {
		handleListTriggers(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/triggers", func(w http.ResponseWriter, r *http.Request) {
		handleCreateTrigger(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/sessions/{session_id}/triggers/{trigger_id}", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateTrigger(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/triggers/{trigger_id}/run", func(w http.ResponseWriter, r *http.Request) {
		handleRunTrigger(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/triggers/{trigger_id}/activate", func(w http.ResponseWriter, r *http.Request) {
		handleActivateTrigger(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/triggers/{trigger_id}/deactivate", func(w http.ResponseWriter, r *http.Request) {
		handleDeactivateTrigger(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/colony", func(w http.ResponseWriter, r *http.Request) {
		handleLoadColony(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/dismiss-colony-pivot", func(w http.ResponseWriter, r *http.Request) {
		handleDismissColonyPivot(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/colony/resolve_missed", func(w http.ResponseWriter, r *http.Request) {
		handleResolveMissedTriggers(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/events/history", func(w http.ResponseWriter, r *http.Request) {
		handleSessionEventsHistory(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/sessions/{session_id}/title", func(w http.ResponseWriter, r *http.Request) {
		handleSetSessionTitle(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/fork", func(w http.ResponseWriter, r *http.Request) {
		handleForkSession(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/reveal", func(w http.ResponseWriter, r *http.Request) {
		handleRevealSession(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/report-bundle", func(w http.ResponseWriter, r *http.Request) {
		handleReportSessionBundle(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/{colony_id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		handleListColonySessions(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/{colony_id}/active-session", func(w http.ResponseWriter, r *http.Request) {
		handleActiveColonySession(w, r, deps)
	})
}

func liveSessions(deps SessionRouteDeps) []*Session {
	if deps.Manager != nil {
		return deps.Manager.ListSessions()
	}
	if deps.LiveSessions != nil {
		return deps.LiveSessions()
	}
	return nil
}

type SessionPhaseState interface {
	Phase() string
	QueenID() string
	QueenProfile() map[string]any
}

type sessionModelNamer interface {
	ModelName() string
}

func sessionToLiveDict(session *Session) map[string]any {
	hasWorker := session.ColonyID != ""
	var colonyID any
	if hasWorker {
		colonyID = session.ColonyID
	} else {
		colonyID = nil
	}

	queenModel := ""
	if namer, ok := session.LLM.(sessionModelNamer); ok && namer != nil {
		queenModel = namer.ModelName()
	}
	queenSupportsImages := true
	if queenModel != "" {
		queenSupportsImages = llm.SupportsImageToolResults(queenModel)
	}

	phaseName := "planning"
	if hasWorker {
		phaseName = "staging"
	}
	var queenID any
	var queenName any
	if phase, ok := session.PhaseState.(SessionPhaseState); ok && phase != nil {
		phaseName = phase.Phase()
		if id := phase.QueenID(); id != "" {
			queenID = id
		}
		if profile := phase.QueenProfile(); profile != nil {
			if name, present := profile["name"]; present {
				queenName = name
			}
		}
	}

	var spawnedColonyID any
	if session.SpawnedColonyID != "" {
		spawnedColonyID = session.SpawnedColonyID
	}

	return map[string]any{
		"session_id":            session.ID,
		"colony_id":             colonyID,
		"has_worker":            hasWorker,
		"agent_path":            session.WorkerPath,
		"description":           "",
		"goal":                  "",
		"node_count":            0,
		"loaded_at":             session.LoadedAt,
		"uptime_seconds":        round1(float64(time.Now().UnixNano())/1e9 - session.LoadedAt),
		"queen_phase":           phaseName,
		"queen_supports_images": queenSupportsImages,
		"queen_id":              queenID,
		"queen_name":            queenName,
		"colony_spawned":        session.ColonySpawned,
		"spawned_colony_id":     spawnedColonyID,
	}
}

func round1(value float64) float64 {
	return math.RoundToEven(value*10) / 10
}
