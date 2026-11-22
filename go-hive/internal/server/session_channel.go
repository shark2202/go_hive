package server

// The persisted chat channel of a session: meta.json["channel"], the field a
// channel host merges when it claims a session (upstream _merge_meta) and the
// queen orchestrator restores onto session.channel when the session is resumed.
//
// Parity: framework/server/queen_orchestrator.py::create_queen (the
// ``session.channel = _persisted["channel"]`` restore)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ReadSessionChannel reads one session directory's persisted channel, ""
// when absent or unreadable. A resumed channel session (a queen-mail thread,
// say) rebuilds its live Session.channel from this, so the chat-app gate sees
// the same answer before and after a restart.
func ReadSessionChannel(sessionDir string) string {
	dir := strings.TrimSpace(sessionDir)
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return ""
	}
	meta := map[string]any{}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	name, _ := meta["channel"].(string)
	return strings.TrimSpace(name)
}
