package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func claudeCodeTestMux(deps ClaudeCodeQueenRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterClaudeCodeQueenRoutes(mux, deps)
	return mux
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_dashboard_lists_her_conversations_and_reads_one
func TestTheDashboardListsHerConversationsAndReadsOne(t *testing.T) {
	const queenID = "queen_claude"
	const sessionID = "cc-session-1"
	conversations := []map[string]any{{
		"session_id": sessionID,
		"peer":       "queen_ava",
		"peer_name":  "Ava",
		"running":    false,
		"letters":    1,
		"last_at":    "2026-10-25T10:00:04Z",
	}}
	entries := []map[string]any{
		{"kind": "letter", "text": "[Ava(queen_ava)]: Where is the north shore?"},
		{"kind": "text", "text": "Let me look."},
		{"kind": "tool", "tool": "Bash", "text": "{\"command\": \"ls maps\"}"},
		{"kind": "result", "text": "north.txt"},
		{"kind": "text", "text": "It is at 61°N."},
	}
	deps := ClaudeCodeQueenRouteDeps{
		Enabled: true,
		Queens: func() []ClaudeCodeQueenView {
			return []ClaudeCodeQueenView{{
				ID:             queenID,
				Name:           "Claude",
				Title:          "Engineer",
				PermissionMode: "default",
				Auth:           "api_key",
				Model:          "claude-x",
			}}
		},
		Conversations: func(id string) ([]map[string]any, error) {
			if id != queenID {
				return nil, nil
			}
			return conversations, nil
		},
		Transcript: func(id, session string) ([]map[string]any, bool) {
			if id == queenID && session == sessionID {
				return entries, true
			}
			return nil, false
		},
	}
	mux := claudeCodeTestMux(deps)

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var listBody map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	queens, _ := listBody["queens"].([]any)
	if len(queens) != 1 {
		t.Fatalf("queens = %#v", listBody["queens"])
	}
	queen := queens[0].(map[string]any)
	if queen["id"] != queenID || queen["name"] != "Claude" || queen["permission_mode"] != "default" || queen["auth"] != "api_key" {
		t.Fatalf("queen = %#v", queen)
	}
	if !reflect.DeepEqual(queen["conversations"], []any{
		map[string]any{
			"session_id": sessionID,
			"peer":       "queen_ava",
			"peer_name":  "Ava",
			"running":    false,
			"letters":    float64(1),
			"last_at":    "2026-10-25T10:00:04Z",
		},
	}) {
		t.Fatalf("conversations = %#v", queen["conversations"])
	}

	transcript := httptest.NewRecorder()
	mux.ServeHTTP(transcript, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens/"+queenID+"/conversations/"+sessionID, nil))
	if transcript.Code != http.StatusOK {
		t.Fatalf("transcript status = %d, body = %s", transcript.Code, transcript.Body.String())
	}
	var transcriptBody map[string]any
	if err := json.Unmarshal(transcript.Body.Bytes(), &transcriptBody); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	got, _ := transcriptBody["entries"].([]any)
	if len(got) != 5 {
		t.Fatalf("entries = %#v", transcriptBody["entries"])
	}
	first := got[0].(map[string]any)
	if first["kind"] != "letter" || first["text"] != "[Ava(queen_ava)]: Where is the north shore?" {
		t.Fatalf("first entry = %#v", first)
	}

	unknown := httptest.NewRecorder()
	mux.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens/"+queenID+"/conversations/no-such-session", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.Code)
	}
}

// Parity-exempt: the disabled branch has no Python unit test; pins the empty list when queen mail is off.
func TestClaudeCodeQueensListIsEmptyWhenMailIsOff(t *testing.T) {
	for _, deps := range []ClaudeCodeQueenRouteDeps{
		{Enabled: false, Queens: func() []ClaudeCodeQueenView { return []ClaudeCodeQueenView{{ID: "x"}} }},
		{Enabled: true},
	} {
		recorder := httptest.NewRecorder()
		claudeCodeTestMux(deps).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if queens, ok := body["queens"].([]any); !ok || len(queens) != 0 {
			t.Fatalf("body = %#v", body)
		}
	}
}
