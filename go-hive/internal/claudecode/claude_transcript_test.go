package claudecode

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/queenmail"
)

// claudeSessionLines mirrors the session file upstream's dashboard test writes
// (SESSION_LINES in test_claude_code_queens.py): a letter from Ava, a meta
// line, an attachment, speech with a hidden thinking part and a tool call, a
// tool result, a subagent's own turn, and her answer.
var claudeSessionLines = []string{
	`{"type": "user", "timestamp": "2026-10-25T10:00:00Z", "message": {"role": "user", "content": "[Ava(queen_ava)]: Where is the north shore?"}}`,
	`{"type": "user", "isMeta": true, "timestamp": "2026-10-25T10:00:00Z", "message": {"role": "user", "content": "caveat"}}`,
	`{"type": "attachment", "timestamp": "2026-10-25T10:00:00Z"}`,
	`{"type": "assistant", "timestamp": "2026-10-25T10:00:01Z", "message": {"role": "assistant", "content": [` +
		`{"type": "thinking", "thinking": "Maybe the atlas."}, {"type": "text", "text": "Let me look."}, ` +
		`{"type": "tool_use", "name": "Bash", "input": {"command": "ls maps"}}]}}`,
	`{"type": "user", "timestamp": "2026-10-25T10:00:02Z", "message": {"role": "user", "content": [{"type": "tool_result", "content": "north.txt", "is_error": false}]}}`,
	`{"type": "assistant", "isSidechain": true, "timestamp": "2026-10-25T10:00:03Z", "message": {"role": "assistant", "content": [{"type": "text", "text": "subagent chatter"}]}}`,
	`{"type": "assistant", "timestamp": "2026-10-25T10:00:04Z", "message": {"role": "assistant", "content": [{"type": "text", "text": "It is at 61°N."}]}}`,
}

func writeSessionFile(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_dashboard_lists_her_conversations_and_reads_one
func TestReadTranscriptShowsLettersSpeechToolsAndResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeSessionFile(t, path, claudeSessionLines)

	got := ReadTranscript(path)
	want := []TranscriptEntry{
		{At: "2026-10-25T10:00:00Z", Kind: "letter", Text: "[Ava(queen_ava)]: Where is the north shore?"},
		{At: "2026-10-25T10:00:01Z", Kind: "text", Text: "Let me look."},
		{At: "2026-10-25T10:00:01Z", Kind: "tool", Tool: "Bash", Text: `{"command": "ls maps"}`},
		{At: "2026-10-25T10:00:02Z", Kind: "result", Text: "north.txt"},
		{At: "2026-10-25T10:00:04Z", Kind: "text", Text: "It is at 61°N."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries =\n%#v\nwant\n%#v", got, want)
	}
}

// Parity: framework/server/claude_code_queens.py::_session_file
func TestSessionFileFindsHerProjectSession(t *testing.T) {
	home := newMailHome(t)
	store := NewStore(home)
	configDir := filepath.Join(t.TempDir(), "claude-config")
	// Two project folders hold a session of the same id: upstream sorts the
	// paths and takes the first.
	writeSessionFile(t, filepath.Join(configDir, "projects", "b-project", "cc-session-1.jsonl"), []string{`{}`})
	want := filepath.Join(configDir, "projects", "a-project", "cc-session-1.jsonl")
	writeSessionFile(t, want, []string{`{}`})

	queen := queenmail.ClaudeQueen{ID: claude, ConfigDir: configDir}
	if got := store.SessionFile(queen, "cc-session-1"); got != want {
		t.Fatalf("session file = %q, want %q", got, want)
	}
	if got := store.SessionFile(queen, "no-such-session"); got != "" {
		t.Fatalf("unknown session file = %q", got)
	}
	// Without config_dir it is her Hive home folder, as the runner uses it.
	defaultFile := filepath.Join(home.Root, "claude_code", claude, "config", "projects", "p", "cc-session-2.jsonl")
	writeSessionFile(t, defaultFile, []string{`{}`})
	if got := store.SessionFile(queenmail.ClaudeQueen{ID: claude}, "cc-session-2"); got != defaultFile {
		t.Fatalf("default session file = %q, want %q", got, defaultFile)
	}
}

// Parity: framework/server/claude_code_queens.py::_read_transcript
func TestReadTranscriptClipsLongTextAndMarksFailedTools(t *testing.T) {
	longResult := strings.Repeat("x", claudeToolResultChars+5)
	longInput := strings.Repeat("y", claudeToolInputChars+5)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeSessionFile(t, path, []string{
		`{"type": "user", "timestamp": "t1", "message": {"content": [{"type": "tool_result", "content": "` + longResult + `", "is_error": true}]}}`,
		`{"type": "assistant", "timestamp": "t2", "message": {"content": [{"type": "tool_use", "name": "Write", "input": {"content": "` + longInput + `"}}]}}`,
	})

	got := ReadTranscript(path)
	if len(got) != 2 {
		t.Fatalf("entries = %#v", got)
	}
	if !got[0].Error || got[0].Kind != "result" {
		t.Fatalf("result entry = %#v", got[0])
	}
	if utf8.RuneCountInString(got[0].Text) != claudeToolResultChars || !strings.HasSuffix(got[0].Text, "…") {
		t.Fatalf("result text = %q (len %d)", got[0].Text, utf8.RuneCountInString(got[0].Text))
	}
	// The clip applies to the serialized JSON, as Python's _clip does after
	// json.dumps: the first 599 characters, then an ellipsis.
	prefix := `{"content": "`
	if want := prefix + strings.Repeat("y", claudeToolInputChars-len(prefix)-1) + "…"; got[1].Text != want {
		t.Fatalf("tool text = %q, want %q", got[1].Text, want)
	}
	if got[1].Error || got[1].Tool != "Write" {
		t.Fatalf("tool entry = %#v", got[1])
	}
}

// Parity-exempt: 上游没有对缺失/损坏会话文件的直接单测；这里钉住它读成空（路由随后返回 200 + []）。
func TestReadTranscriptOfMissingOrBrokenFileIsEmpty(t *testing.T) {
	if got := ReadTranscript(""); len(got) != 0 {
		t.Fatalf("empty path = %#v", got)
	}
	if got := ReadTranscript(filepath.Join(t.TempDir(), "absent.jsonl")); len(got) != 0 {
		t.Fatalf("absent file = %#v", got)
	}
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	writeSessionFile(t, path, []string{"{not json", `{"type": "user", "timestamp": "t", "message": {"content": "hello"}}`})
	got := ReadTranscript(path)
	if len(got) != 1 || got[0].Kind != "letter" || got[0].Text != "hello" {
		t.Fatalf("broken file = %#v", got)
	}
}

// Parity-exempt: 上游没有对 worker 存活状态的直接单测（dashboard 测试只断言 running=false）；
// 这里用 fake claude 钉住「回合在跑」的会话被标记，跑完又落回。
func TestRunningMarksAConversationWithATurnInFlight(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.Env["FAKE_CLAUDE_SLEEP"] = "1"
	})
	if h.runner.Running("claude_code:queen_claude:nope") {
		t.Fatal("an unknown conversation reports running")
	}
	h.queueResult(t, "Noted.")
	if _, err := h.mail.Send(ava, "session_ava", claude, "Where is the north shore?", nil); err != nil {
		t.Fatal(err)
	}
	conversations := h.store.Conversations(claude)
	if len(conversations) != 1 {
		t.Fatalf("conversations = %#v", conversations)
	}
	ref := conversations[0].Ref
	h.waitStarted(t, 0)
	if !h.runner.Running(ref) {
		t.Fatal("a conversation with a turn running is not marked")
	}
	h.waitReplies(t, 1)
	waitFor(t, "the turn to finish", func() bool { return !h.runner.Running(ref) })
}
