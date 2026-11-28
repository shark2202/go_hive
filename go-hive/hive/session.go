package hive

import (
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/session"
)

// Session is the read view of one queen session stored under
// <HIVE_HOME>/queens/<queen_id>/sessions/<session_id>.
//
// Parity: framework/server/session_manager.py SessionManager._summarize_session_dir
type Session struct {
	ID           string
	QueenID      string
	Title        string
	Channel      string
	ForkedFrom   string
	HasMessages  bool
	MessageCount int
	LastMessage  string
	CreatedAt    float64
	LastActiveAt float64
}

// ListSessions lists every cold queen session, newest session id first.
//
// Parity: framework/server/session_manager.py::SessionManager.list_cold_sessions
func ListSessions(home Home) []Session {
	if home.Root == "" {
		return nil
	}
	return sessionsFromMaps(session.ListColdSessions(home.QueensDir()))
}

// ListColonySessions lists every cold colony session.
//
// Parity: framework/server/session_manager.py::SessionManager.list_colony_sessions
func ListColonySessions(home Home) []Session {
	if home.Root == "" {
		return nil
	}
	return sessionsFromMaps(session.ListColonySessions(home.QueensDir()))
}

// ActiveColonySession returns the newest colony session that has messages.
//
// Parity: framework/server/session_manager.py::SessionManager.get_colony_active_session
func ActiveColonySession(home Home) (Session, bool) {
	if home.Root == "" {
		return Session{}, false
	}
	entry := session.ColonyActiveSession(home.QueensDir())
	if entry == nil {
		return Session{}, false
	}
	return sessionFromMap(entry), true
}

// SessionDir is one session's on-disk directory.
//
// Parity: framework/server/session_manager.py SessionManager session paths
func SessionDir(home Home, queenID, sessionID string) string {
	if home.Root == "" || queenID == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(home.QueenSessionsDir(queenID), sessionID)
}

func sessionsFromMaps(entries []map[string]any) []Session {
	out := make([]Session, 0, len(entries))
	for _, entry := range entries {
		out = append(out, sessionFromMap(entry))
	}
	return out
}

func sessionFromMap(entry map[string]any) Session {
	return Session{
		ID:           stringValue(entry["session_id"]),
		QueenID:      stringValue(entry["queen_id"]),
		Title:        stringValue(entry["title"]),
		Channel:      stringValue(entry["channel"]),
		ForkedFrom:   stringValue(entry["forked_from"]),
		HasMessages:  boolValue(entry["has_messages"]),
		MessageCount: intValue(entry["message_count"]),
		LastMessage:  stringValue(entry["last_message"]),
		CreatedAt:    floatValue(entry["created_at"]),
		LastActiveAt: floatValue(entry["last_active_at"]),
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}
