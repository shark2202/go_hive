package session

// Parity: framework/server/session_manager.py::SessionManager.list_cold_sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/aden-hive/hive-go/internal/storage"
)

// Parity: framework/server/session_manager.py::SessionManager.list_cold_sessions
func ListColdSessions(queensDir string) []map[string]any {
	return summarizeSessionsUnder(queensDir, true)
}

// Parity: framework/server/session_manager.py::SessionManager.list_colony_sessions
func ListColonySessions(queensRoot string) []map[string]any {
	return summarizeSessionsUnder(queensRoot, false)
}

// Parity: framework/server/session_manager.py::SessionManager.get_colony_active_session
func ColonyActiveSession(queensRoot string) map[string]any {
	for _, entry := range ListColonySessions(queensRoot) {
		if coldTruthy(entry["has_messages"]) {
			return entry
		}
	}
	return nil
}

func summarizeSessionsUnder(queensDir string, skipColonyFork bool) []map[string]any {
	queenEntries, err := os.ReadDir(queensDir)
	if err != nil {
		return []map[string]any{}
	}
	dirs := make([]string, 0)
	for _, queenEntry := range queenEntries {
		if !queenEntry.IsDir() {
			continue
		}
		sessionsDir := filepath.Join(queensDir, queenEntry.Name(), "sessions")
		if _, err := os.Stat(sessionsDir); err != nil {
			continue
		}
		sessionEntries, err := os.ReadDir(sessionsDir)
		if err != nil {
			return []map[string]any{}
		}
		for _, sessionEntry := range sessionEntries {
			if sessionEntry.IsDir() {
				dirs = append(dirs, filepath.Join(sessionsDir, sessionEntry.Name()))
			}
		}
	}
	results := make([]map[string]any, 0, len(dirs))
	for _, dir := range dirs {
		if entry := summarizeSessionDir(dir, skipColonyFork); entry != nil {
			results = append(results, entry)
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		return coldString(results[i]["session_id"]) > coldString(results[j]["session_id"])
	})
	return results
}

// Parity: framework/server/session_manager.py::SessionManager._summarize_session_dir
func summarizeSessionDir(dir string, skipColonyFork bool) map[string]any {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	var createdAt any = float64(info.ModTime().UnixNano()) / 1e9

	var meta map[string]any
	if data, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	var agentName, agentPath any
	if meta != nil {
		agentName = meta["agent_name"]
		agentPath = meta["agent_path"]
		if value, ok := meta["created_at"]; ok && coldTruthy(value) {
			createdAt = value
		}
	}

	if skipColonyFork && coldTruthy(meta["colony_fork"]) {
		return nil
	}

	lastActiveAt := 0.0
	if number, ok := coldNumber(createdAt); ok {
		lastActiveAt = number
	}

	convsDir := filepath.Join(dir, "conversations")
	convsExists := isDir(convsDir)
	var summary *storage.SessionSummary
	if convsExists {
		if storage.IsStale(dir) {
			summary = storage.RebuildSummary(dir)
		} else {
			summary = storage.ReadSummary(dir)
		}
	}

	messageCount := 0
	var lastMessage any
	if summary != nil {
		messageCount = summary.MessageCount
		if summary.LastMessage != nil {
			lastMessage = *summary.LastMessage
		}
		if summary.LastActiveAt > lastActiveAt {
			lastActiveAt = summary.LastActiveAt
		}
	}

	var queenID any
	if filepath.Base(filepath.Dir(dir)) == "sessions" {
		queenID = filepath.Base(filepath.Dir(filepath.Dir(dir)))
	}

	var forkedFrom any
	if meta != nil {
		if fork, ok := meta["forked_from"].(map[string]any); ok && coldTruthy(fork) {
			forkedFrom = fork["session_id"]
		}
	}

	var title, channel any
	if meta != nil {
		if value, ok := meta["title"]; ok && coldTruthy(value) {
			title = value
		}
		if value, ok := meta["channel"]; ok && coldTruthy(value) {
			channel = value
		}
	}

	return map[string]any{
		"session_id":     filepath.Base(dir),
		"title":          title,
		"channel":        channel,
		"forked_from":    forkedFrom,
		"cold":           true,
		"live":           false,
		"has_messages":   convsExists && messageCount > 0,
		"created_at":     createdAt,
		"last_active_at": lastActiveAt,
		"agent_name":     agentName,
		"agent_path":     agentPath,
		"last_message":   lastMessage,
		"message_count":  messageCount,
		"queen_id":       queenID,
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func coldTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func coldNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func coldString(value any) string {
	text, _ := value.(string)
	return text
}
