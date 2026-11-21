package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queenmail"
)

// The fake Claude Code script stands in for the real binary: it records what
// each run was given (arguments, prompt, cwd, environment, MCP config) and
// prints the next queued result object as --output-format json does.
const fakeClaudeScript = `#!/bin/sh
# Stands in for Claude Code: records the run, then prints the next queued result.
DIR="${FAKE_CLAUDE_RECORD:?}"
mkdir -p "$DIR"
n=0
while [ "$n" -lt 1000 ]; do
	if mkdir "$DIR/run-$n" 2>/dev/null; then RUN="$DIR/run-$n"; break; fi
	n=$((n+1))
done
printf '%s\n' "$@" > "$RUN/args"
cat > "$RUN/stdin"
pwd > "$RUN/cwd"
env > "$RUN/env"
prev=""
for a in "$@"; do
	if [ "$prev" = "--mcp-config" ]; then
		cp "$a" "$RUN/mcp.json" 2>/dev/null
		(stat -f '%Lp' "$a" 2>/dev/null || stat -c '%a' "$a" 2>/dev/null) > "$RUN/mcp.perm"
	fi
	prev="$a"
done
: > "$RUN/started"
if [ -n "$FAKE_CLAUDE_COUNTER" ]; then
	c=0
	while [ "$c" -lt 100 ]; do
		if mkdir "$FAKE_CLAUDE_COUNTER/live-$c" 2>/dev/null; then break; fi
		c=$((c+1))
	done
	count=$(ls -d "$FAKE_CLAUDE_COUNTER"/live-* 2>/dev/null | wc -l | tr -d ' ')
	printf '%s\n' "$count" >> "$FAKE_CLAUDE_COUNTER/counts"
fi
if [ -n "$FAKE_CLAUDE_CHILD_MARKER" ]; then
	( sleep 1; : > "$FAKE_CLAUDE_CHILD_MARKER" ) &
fi
sleep "${FAKE_CLAUDE_SLEEP:-0}"
if [ -n "$FAKE_CLAUDE_COUNTER" ]; then rmdir "$FAKE_CLAUDE_COUNTER/live-$c" 2>/dev/null; fi
REPLY=""
for f in "$DIR"/replies/*; do
	[ -f "$f" ] || continue
	if mv "$f" "$f.consumed" 2>/dev/null; then REPLY="$f"; break; fi
done
if [ -n "$REPLY" ]; then
	case "$REPLY" in
		*.lost) echo "No conversation found with session ID: gone" >&2; rm -f "$REPLY.consumed"; exit 1 ;;
		*.taken) echo "Error: Session ID taken is already in use." >&2; rm -f "$REPLY.consumed"; exit 1 ;;
		*) cat "$REPLY.consumed"; rm -f "$REPLY.consumed"; exit 0 ;;
	esac
fi
printf '{"type":"result","is_error":false,"result":"Noted.","num_turns":1}\n'
`

// fakeRun is one recorded execution of the fake Claude Code.
type fakeRun struct {
	dir   string
	args  []string
	stdin string
	cwd   string
	env   map[string]string
	mcp   map[string]any
	perm  string
}

// claudeHarness wires a queen-mail service with a runner that executes the
// fake Claude Code, in a fresh Hive home.
type claudeHarness struct {
	home       hivehome.Home
	mail       *queenmail.Mail
	store      *Store
	runner     *Runner
	deliverer  *fakeDeliverer
	recordDir  string
	repliesDir string
	replySeq   int
}

func writeFakeClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(path, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// newClaudeHarness builds the service, its runner and the recording fake. tune
// may adjust the section and her entry before the service is built.
func newClaudeHarness(t *testing.T, tune func(*queenmail.Config, *queenmail.ClaudeQueen)) *claudeHarness {
	t.Helper()
	home := newMailHome(t)
	recordDir := t.TempDir()
	repliesDir := filepath.Join(recordDir, "replies")
	if err := os.MkdirAll(repliesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	queen := config.ClaudeCode[claude]
	queen.Command = []string{writeFakeClaude(t)}
	queen.APIKey = "sk-ant-test-key"
	queen.Env = map[string]string{"FAKE_CLAUDE_RECORD": recordDir}
	if tune != nil {
		tune(&config, &queen)
	}
	if queen.Env == nil {
		queen.Env = map[string]string{}
	}
	if _, ok := queen.Env["FAKE_CLAUDE_RECORD"]; !ok {
		queen.Env["FAKE_CLAUDE_RECORD"] = recordDir
	}
	config.ClaudeCode[claude] = queen
	deliverer := &fakeDeliverer{}
	mail, store := newQueenMailService(t, home, config, deliverer)
	mail.SetBaseURL("http://127.0.0.1:9")
	runner := NewRunner(mail, store)
	mail.SetClaudeRunner(runner)
	t.Cleanup(runner.Close)
	return &claudeHarness{
		home:       home,
		mail:       mail,
		store:      store,
		runner:     runner,
		deliverer:  deliverer,
		recordDir:  recordDir,
		repliesDir: repliesDir,
	}
}

// queueResult queues one successful result object.
func (h *claudeHarness) queueResult(t *testing.T, text string) {
	t.Helper()
	h.queueJSON(t, map[string]any{"type": "result", "is_error": false, "result": text, "num_turns": 1})
}

// queueError queues one result object that reports an error.
func (h *claudeHarness) queueError(t *testing.T, text string) {
	t.Helper()
	h.queueJSON(t, map[string]any{"type": "result", "is_error": true, "result": text, "num_turns": 1})
}

// queueMarker queues the "lost"/"taken" session-error replies the real CLI
// prints when the session record is wrong.
func (h *claudeHarness) queueMarker(t *testing.T, marker string) {
	t.Helper()
	h.replySeq++
	path := filepath.Join(h.repliesDir, fmt.Sprintf("%03d.%s", h.replySeq, marker))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *claudeHarness) queueJSON(t *testing.T, value any) {
	t.Helper()
	h.replySeq++
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.repliesDir, fmt.Sprintf("%03d.json", h.replySeq))
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runs lists what the fake Claude Code was given, in run order.
func (h *claudeHarness) runs(t *testing.T) []fakeRun {
	t.Helper()
	entries, err := os.ReadDir(h.recordDir)
	if err != nil {
		t.Fatal(err)
	}
	type indexedRun struct {
		index int
		run   fakeRun
	}
	var indexed []indexedRun
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "run-") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(name, "run-"))
		if err != nil {
			continue
		}
		indexed = append(indexed, indexedRun{index: index, run: readFakeRun(t, filepath.Join(h.recordDir, name))})
	}
	sort.Slice(indexed, func(i, j int) bool { return indexed[i].index < indexed[j].index })
	out := make([]fakeRun, 0, len(indexed))
	for _, item := range indexed {
		out = append(out, item.run)
	}
	return out
}

func readFakeRun(t *testing.T, dir string) fakeRun {
	t.Helper()
	run := fakeRun{dir: dir, env: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(dir, "args")); err == nil {
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line != "" {
				run.args = append(run.args, line)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "stdin")); err == nil {
		run.stdin = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cwd")); err == nil {
		run.cwd = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "env")); err == nil {
		run.env = parseEnvLines(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "mcp.json")); err == nil {
		_ = json.Unmarshal(data, &run.mcp)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "mcp.perm")); err == nil {
		run.perm = strings.TrimSpace(string(data))
	}
	return run
}

func parseEnvLines(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key != "" {
			out[key] = value
		}
	}
	return out
}

// waitRuns waits until the fake has recorded count runs.
func (h *claudeHarness) waitRuns(t *testing.T, count int) []fakeRun {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d runs", count), func() bool { return len(h.runs(t)) >= count })
	return h.runs(t)
}

// waitReplies waits until the writer has been told count things.
func (h *claudeHarness) waitReplies(t *testing.T, count int) []string {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d replies", count), func() bool {
		h.deliverer.mu.Lock()
		defer h.deliverer.mu.Unlock()
		return len(h.deliverer.replies) >= count
	})
	h.deliverer.mu.Lock()
	defer h.deliverer.mu.Unlock()
	return append([]string(nil), h.deliverer.replies...)
}

// waitStarted waits until the fake has begun its index-th run.
func (h *claudeHarness) waitStarted(t *testing.T, index int) {
	t.Helper()
	path := filepath.Join(h.recordDir, fmt.Sprintf("run-%d", index), "started")
	waitFor(t, "run "+strconv.Itoa(index)+" to start", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// resolvedPath resolves the symlinks macOS puts in temp paths, so a shell's
// pwd and the Go path compare equal.
func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// lastLine is the last non-empty line of text.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) != "" {
			return lines[index]
		}
	}
	return ""
}

// bracketedLines are the lines that carry a "[Name(id)]:" prefix.
func bracketedLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[") {
			out = append(out, line)
		}
	}
	return out
}

// argValue is the argument following flag, or "".
func argValue(args []string, flag string) string {
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_letter_to_her_runs_a_turn_and_her_answer_goes_back
func TestALetterToHerRunsATurnAndHerAnswerGoesBack(t *testing.T) {
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "The north shore is at 61°N.")

	sent, err := h.mail.Send(ava, "session_ava", claude, "Where is the north shore?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if replies := h.waitReplies(t, 1); replies[0] != "The north shore is at 61°N." {
		t.Fatalf("replies = %#v", replies)
	}

	runs := h.runs(t)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	if argValue(runs[0].args, "--session-id") == "" {
		t.Fatalf("args = %#v", runs[0].args)
	}
	if got := lastLine(runs[0].stdin); got != "[Ava(queen_ava)]: Where is the north shore?" {
		t.Fatalf("prompt tail = %q", got)
	}
	audit := h.mail.Store().ReadAudit()
	if len(audit) != 2 {
		t.Fatalf("audit = %#v", audit)
	}
	if sent["sent"] != audit[0].ID || sent["to"] != claude || sent["depth"] != 1 {
		t.Fatalf("sent = %#v", sent)
	}
	reply := audit[1]
	if reply.Kind != queenmail.KindReply || reply.From != claude || reply.To != ava || reply.Depth != 2 {
		t.Fatalf("reply audit = %#v", reply)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_her_conversation_with_a_writer_is_resumed_next_time
func TestHerConversationWithAWriterIsResumedNextTime(t *testing.T) {
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "One.")
	h.queueResult(t, "Two.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "One", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)
	if _, err := h.mail.Send(ava, "session_ava", claude, "Two", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 2)

	runs := h.runs(t)
	if len(runs) != 2 {
		t.Fatalf("runs = %d", len(runs))
	}
	sessionID := argValue(runs[0].args, "--session-id")
	if sessionID == "" || argValue(runs[1].args, "--resume") != sessionID {
		t.Fatalf("first = %#v, second = %#v", runs[0].args, runs[1].args)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_letters_that_arrive_during_a_turn_go_in_together
func TestLettersThatArriveDuringATurnGoInTogether(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.Env["FAKE_CLAUDE_SLEEP"] = "0.6"
	})
	if _, err := h.mail.Send(ava, "session_ava", claude, "One", nil); err != nil {
		t.Fatal(err)
	}
	h.waitStarted(t, 0)
	if _, err := h.mail.Send(ava, "session_ava", claude, "Two", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mail.Send(ava, "session_ava", claude, "Three", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 2)

	prompts := make([][]string, 0, 2)
	for _, run := range h.runs(t) {
		prompts = append(prompts, bracketedLines(run.stdin))
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %#v", prompts)
	}
	if got := strings.Join(prompts[0], "|"); got != "[Ava(queen_ava)]: One" {
		t.Fatalf("first prompt = %#v", prompts[0])
	}
	if got := strings.Join(prompts[1], "|"); got != "[Ava(queen_ava)]: Two|[Ava(queen_ava)]: Three" {
		t.Fatalf("second prompt = %#v", prompts[1])
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_she_writes_to_a_hive_queen_and_her_answer_comes_back_to_the_conversation
func TestSheWritesToAHiveQueenAndHerAnswerComesBackToTheConversation(t *testing.T) {
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "Let me ask Ivy.")
	h.queueResult(t, "Ivy says the east shore is unmapped.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Is the east shore mapped?", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)
	conversation := h.mail.Store().ReadAudit()[0].ToSession

	sent, err := h.mail.SendFromConversation(conversation, ivy, "Have you mapped the east shore?")
	if err != nil {
		t.Fatal(err)
	}
	replyHop := sent["sent"].(string)
	if err := h.mail.DeliverReply(conversation, ivy, claude, "session_ivy", "Not yet.", &queenmail.Cause{Hop: replyHop, Depth: 2}); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 2)
	h.waitRuns(t, 2)

	runs := h.runs(t)
	if got := lastLine(runs[1].stdin); got != "[Ivy(queen_ivy)]: Not yet." {
		t.Fatalf("second prompt tail = %q", got)
	}
	if replies := h.waitReplies(t, 2); replies[1] != "Ivy says the east shore is unmapped." {
		t.Fatalf("replies = %#v", replies)
	}
	waitFor(t, "five hops", func() bool { return len(h.mail.Store().ReadAudit()) >= 5 })
	hops := h.mail.Store().ReadAudit()
	type hop struct {
		kind  string
		from  string
		to    string
		depth int
	}
	got := make([]hop, 0, len(hops))
	for _, record := range hops {
		got = append(got, hop{record.Kind, record.From, record.To, record.Depth})
	}
	want := []hop{
		{queenmail.KindMessage, ava, claude, 1},
		{queenmail.KindReply, claude, ava, 2},
		{queenmail.KindMessage, claude, ivy, 2},
		{queenmail.KindReply, ivy, claude, 3},
		{queenmail.KindReply, claude, ava, 4},
	}
	if len(got) != len(want) {
		t.Fatalf("hops = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("hop %d = %#v, want %#v (all %#v)", index, got[index], want[index], got)
		}
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_failed_turn_tells_the_writer
func TestAFailedTurnTellsTheWriter(t *testing.T) {
	h := newClaudeHarness(t, nil)
	h.queueError(t, "Not logged in · Please run /login")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	replies := h.waitReplies(t, 1)
	if replies[0] != "(Her reply failed: Not logged in · Please run /login)" {
		t.Fatalf("replies = %#v", replies)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_turn_that_runs_out_of_time_tells_the_writer
func TestATurnThatRunsOutOfTimeTellsTheWriter(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.Env["FAKE_CLAUDE_SLEEP"] = "5"
	})
	h.runner.turnTimeout = func(queenmail.ClaudeQueen) time.Duration { return 300 * time.Millisecond }
	started := time.Now()

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	replies := h.waitReplies(t, 1)
	if replies[0] != "(Her reply failed: her turn ran out of time after 30 min)" {
		t.Fatalf("replies = %#v", replies)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
	waitFor(t, "the killed turn's process tree", func() bool { return h.runner.trackedCount() == 0 })
}
