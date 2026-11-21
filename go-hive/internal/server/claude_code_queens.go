package server

// Parity: framework/server/routes_claude_code_queens.py::handle_list
//
// The dashboard's read-only view of the queens run by Claude Code
// (queen_mail.claude_code). It is built from the same queen-mail service the
// queen routes use: the configured queens come from its configuration, and
// each one's conversations, letters and transcripts from her own record
// (HIVE_HOME/claude_code/<id>/conversations.json, internal/claudecode) and
// from Claude Code's session files.

import (
	"sort"

	"github.com/aden-hive/hive-go/internal/claudecode"
	"github.com/aden-hive/hive-go/internal/queenmail"
)

// NewClaudeCodeQueenRouteDeps builds the dashboard's view of the Claude Code
// queens from the process's queen-mail service, its store of conversations
// and its runner. A nil service leaves the list empty and every transcript a
// 404, as upstream's `mail is None` does; with queen mail merely switched off
// the list is empty but a configured queen's transcript still reads, which is
// what upstream's two handlers do.
func NewClaudeCodeQueenRouteDeps(mail *queenmail.Mail, store *claudecode.Store, runner *claudecode.Runner) ClaudeCodeQueenRouteDeps {
	if mail == nil || store == nil {
		return ClaudeCodeQueenRouteDeps{}
	}
	config := mail.Config()
	queens := sortedClaudeQueens(config.ClaudeCode)
	return ClaudeCodeQueenRouteDeps{
		Enabled: config.Enabled,
		Queens: func() []ClaudeCodeQueenView {
			views := make([]ClaudeCodeQueenView, 0, len(queens))
			for _, queen := range queens {
				views = append(views, ClaudeCodeQueenView{
					ID:             queen.ID,
					Name:           queen.Name,
					Title:          queen.Title,
					PermissionMode: queen.PermissionMode,
					Auth:           queen.Auth,
					Model:          queen.Model,
				})
			}
			return views
		},
		Conversations: func(queenID string) ([]map[string]any, error) {
			queen, configured := config.Claude(queenID)
			if !configured {
				// Upstream's conversations() answers [] for a queen no longer
				// configured; she is in the list only while she is.
				return []map[string]any{}, nil
			}
			return claudeConversations(mail, store, runner, queen), nil
		},
		Transcript: func(queenID, sessionID string) ([]map[string]any, bool) {
			queen, configured := config.Claude(queenID)
			if !configured {
				return nil, false
			}
			if _, known := store.Find(queenmail.ConversationRef(queenID, sessionID)); !known {
				return nil, false
			}
			path := store.SessionFile(queen, sessionID)
			return claudeTranscriptEntries(claudecode.ReadTranscript(path)), true
		},
	}
}

// sortedClaudeQueens is the configured queens in id order. Upstream keeps the
// configuration's own order; Go's configuration parse keeps them in a map, so
// the list is sorted to stay the same from one request to the next.
func sortedClaudeQueens(configured map[string]queenmail.ClaudeQueen) []queenmail.ClaudeQueen {
	ids := make([]string, 0, len(configured))
	for id := range configured {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	queens := make([]queenmail.ClaudeQueen, 0, len(ids))
	for _, id := range ids {
		queens = append(queens, configured[id])
	}
	return queens
}

// claudeConversations lists one queen's conversations, newest activity first:
// who each is with, whether a turn of hers is running in it, how many letters
// it holds and when the last thing in it happened.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens.conversations
func claudeConversations(mail *queenmail.Mail, store *claudecode.Store, runner *claudecode.Runner, queen queenmail.ClaudeQueen) []map[string]any {
	type activity struct {
		conversation *claudecode.Conversation
		entries      []claudecode.TranscriptEntry
		lastAt       string
	}
	found := make([]activity, 0)
	for _, conversation := range store.Conversations(queen.ID) {
		entries := claudecode.ReadTranscript(store.SessionFile(queen, conversation.SessionID))
		item := activity{conversation: conversation, entries: entries}
		if len(entries) > 0 {
			item.lastAt = entries[len(entries)-1].At
		}
		found = append(found, item)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].lastAt > found[j].lastAt })
	out := make([]map[string]any, 0, len(found))
	for _, item := range found {
		letters := 0
		for _, entry := range item.entries {
			if entry.Kind == "letter" {
				letters++
			}
		}
		// null when the conversation has no transcript yet, as upstream's
		// `... if read[session_id] else None`.
		var lastAt any
		if len(item.entries) > 0 {
			lastAt = item.lastAt
		}
		out = append(out, map[string]any{
			"session_id": item.conversation.SessionID,
			"peer":       item.conversation.Peer,
			"peer_name":  mail.NameOf(item.conversation.Peer),
			"running":    runner.Running(item.conversation.Ref),
			"letters":    letters,
			"last_at":    lastAt,
		})
	}
	return out
}

// claudeTranscriptEntries renders a read transcript as the route's JSON
// entries: every entry has at/kind/text, a tool call names its tool, and a
// tool result says whether it failed.
func claudeTranscriptEntries(entries []claudecode.TranscriptEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		row := map[string]any{"at": entry.At, "kind": entry.Kind, "text": entry.Text}
		switch entry.Kind {
		case "tool":
			row["tool"] = entry.Tool
		case "result":
			row["error"] = entry.Error
		}
		out = append(out, row)
	}
	return out
}
