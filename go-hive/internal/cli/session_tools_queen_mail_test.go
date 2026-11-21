package cli

// Tests for the queen-mail tools on the chat session catalogue. The upstream
// reference is core/framework/server/tests/test_queen_mail.py (tool section)
// and core/framework/tools/queen_mail_tools.py.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/queenmail"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

const (
	queenMailAva = "queen_ava"
	queenMailIvy = "queen_ivy"
	queenMailZed = "queen_zed"
)

// queenMailDeliverer stands in for the server adapter: a letter is "delivered"
// to a named session without a live queen behind it.
type queenMailDeliverer struct {
	toRef string
}

func (d *queenMailDeliverer) Deliver(_ context.Context, _, _, _ string, _ queenmail.Letter) (string, error) {
	if d.toRef == "" {
		return "", &queenmail.Error{Message: "no deliverer session"}
	}
	return d.toRef, nil
}

func (d *queenMailDeliverer) DeliverReply(_ context.Context, replyTo, _, _ string, _ queenmail.Letter) (string, error) {
	return replyTo, nil
}

func writeQueenMailTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// queenMailTestHome is a temp HIVE_HOME with two profile queens and a
// configuration.json the caller can tailor.
func queenMailTestHome(t *testing.T, config string) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(testTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	writeQueenMailTestFile(t, filepath.Join(home.QueensDir(), queenMailAva, "profile.yaml"), "name: Ava\ntitle: Navigator\n")
	writeQueenMailTestFile(t, filepath.Join(home.QueensDir(), queenMailIvy, "profile.yaml"), "name: Ivy\ntitle: Cartographer\n")
	writeQueenMailTestFile(t, home.ConfigurationFile(), config)
	return home
}

func queenMailConfig(t *testing.T, body string) string {
	t.Helper()
	return `{"queen_mail": ` + body + `}`
}

// queenMailCatalogue builds a session catalogue with queen mail enabled, the
// way the loader does for a queen chat session.
func queenMailCatalogue(t *testing.T, home hivehome.Home, queenName, sessionID string) (*sessionToolCatalogue, *queenmail.Mail) {
	t.Helper()
	deliverer := &queenMailDeliverer{toRef: "session_thread_" + queenName}
	mail := queenmail.New(home, queenmail.ConfigFromHive(hiveserver.HiveConfig(home)), deliverer)
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	catalogue.enableQueenMail(mail, sessionID, queenName)
	return catalogue, mail
}

func queenMailToolNames(catalogue *sessionToolCatalogue) map[string]bool {
	names := map[string]bool{}
	for _, def := range catalogue.Defs() {
		names[def.Name] = true
	}
	return names
}

func callQueenMailTool(t *testing.T, catalogue *sessionToolCatalogue, name string, input map[string]any) map[string]any {
	t.Helper()
	content, isError, err := catalogue.Execute(context.Background(), name, input)
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if isError {
		t.Fatalf("%s: reported a tool error for a JSON refusal: %s", name, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("%s: result is not JSON (%q): %v", name, content, err)
	}
	return payload
}

// Parity: framework/server/tests/test_queen_mail.py::test_the_tools_are_off_until_queen_mail_is_on
func TestQueenMailToolsAreOffUntilQueenMailIsOn(t *testing.T) {
	home := queenMailTestHome(t, `{}`)
	mail := queenmail.New(home, queenmail.ConfigFromHive(hiveserver.HiveConfig(home)), &queenMailDeliverer{})
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	catalogue.enableQueenMail(mail, "session_ava", queenMailAva)

	names := queenMailToolNames(catalogue)
	if names[ListQueensToolName] || names[MessageQueenToolName] {
		t.Fatalf("queen mail tools registered while off: %v", names)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_queen_outside_queen_mail_gets_no_tools
// Parity: framework/server/tests/test_queen_mail.py::test_the_tools_are_the_queens_alone_in_every_phase
func TestQueenMailToolsAreTheQueensAlone(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"]}`))

	// A queen outside the `queens` list gets neither tool.
	zed, _ := queenMailCatalogue(t, home, queenMailZed, "session_zed")
	if names := queenMailToolNames(zed); names[ListQueensToolName] || names[MessageQueenToolName] {
		t.Fatalf("queen_zed outside queen_mail got tools: %v", names)
	}

	// A colony worker's catalogue is built without queen mail: the worker pool
	// must never see the queens' letter tools.
	colonyPath := t.TempDir()
	base, declared, _ := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	for _, defs := range [][]llm.ToolDef{base, declared} {
		for _, def := range defs {
			if def.Name == ListQueensToolName || def.Name == MessageQueenToolName {
				t.Fatalf("worker surface has %s", def.Name)
			}
		}
	}

	// The participating queen gets both.
	ava, _ := queenMailCatalogue(t, home, queenMailAva, "session_ava")
	names := queenMailToolNames(ava)
	if !names[ListQueensToolName] || !names[MessageQueenToolName] {
		t.Fatalf("queen_ava tools = %v", names)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_the_tools_list_the_queens_and_send_a_letter
func TestQueenMailToolsListTheQueensAndSendALetter(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"]}`))
	catalogue, mail := queenMailCatalogue(t, home, queenMailAva, "session_ava")

	listed := callQueenMailTool(t, catalogue, ListQueensToolName, map[string]any{})
	queens, _ := listed["queens"].([]any)
	if len(queens) != 1 {
		t.Fatalf("queens = %#v", listed)
	}
	only, _ := queens[0].(map[string]any)
	if only["id"] != queenMailIvy || only["name"] != "Ivy" || only["title"] != "Cartographer" {
		t.Fatalf("queen = %#v", only)
	}

	sent := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{
		"queen_id": queenMailIvy,
		"message":  "Can you map the north shore?",
	})
	if sent["to"] != queenMailIvy || sent["depth"] != float64(1) {
		t.Fatalf("sent = %#v", sent)
	}
	hop, _ := sent["sent"].(string)
	if !strings.HasPrefix(hop, "hop_") {
		t.Fatalf("sent hop = %q", hop)
	}
	// The caller's own session is the letter's return address.
	if replyTo := mail.RouteOf(queenMailIvy, queenMailAva); replyTo != "session_ava" {
		t.Fatalf("route back = %q", replyTo)
	}

	refused := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{
		"queen_id": queenMailZed,
		"message":  "Hi",
	})
	if refused["error"] != "There's no queen 'queen_zed' to write to; list_queens names them" {
		t.Fatalf("refusal = %#v", refused)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_the_tools_say_so_when_queen_mail_isnt_running
func TestQueenMailToolsSaySoWhenQueenMailIsntRunning(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"]}`))
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	catalogue.enableQueenMail(nil, "session_ava", queenMailAva)

	names := queenMailToolNames(catalogue)
	if !names[ListQueensToolName] || !names[MessageQueenToolName] {
		t.Fatalf("enabled queen mail did not register: %v", names)
	}
	for _, tool := range []string{ListQueensToolName, MessageQueenToolName} {
		if result := callQueenMailTool(t, catalogue, tool, map[string]any{"queen_id": queenMailIvy, "message": "Hi"}); result["error"] != "Queen mail isn't running in this server" {
			t.Fatalf("%s = %#v", tool, result)
		}
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_letter_to_no_one_she_may_write_to_is_refused
// Parity: framework/server/tests/test_queen_mail.py::test_a_queen_outside_queen_mail_cant_write
func TestQueenMailToolsRefuseOwnIDEmptyMessageAndNonParticipants(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"]}`))
	catalogue, _ := queenMailCatalogue(t, home, queenMailAva, "session_ava")

	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"own id", map[string]any{"queen_id": queenMailAva, "message": "Hi"}, "That's your own id"},
		{"empty message", map[string]any{"queen_id": queenMailIvy, "message": "   "}, "The message is empty"},
		{"unknown queen", map[string]any{"queen_id": "queen_nobody", "message": "Hi"}, "There's no queen 'queen_nobody' to write to; list_queens names them"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := callQueenMailTool(t, catalogue, MessageQueenToolName, tc.input)
			if result["error"] != tc.want {
				t.Fatalf("error = %#v, want %q", result["error"], tc.want)
			}
		})
	}

	// A session whose queen was not picked yet registers the tools (upstream
	// does the same) but is refused when it writes: it is not a participant.
	unbound, _ := queenMailCatalogue(t, home, "", "session_unbound")
	refused := callQueenMailTool(t, unbound, MessageQueenToolName, map[string]any{"queen_id": queenMailIvy, "message": "Hi"})
	if refused["error"] != "You don't take part in queen mail (queen_mail in configuration.json)" {
		t.Fatalf("unbound refusal = %#v", refused)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_queen_sends_at_most_daily_limit_letters_a_day
func TestQueenMailToolsRespectTheDailyLimit(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"], "daily_limit": 1}`))
	catalogue, _ := queenMailCatalogue(t, home, queenMailAva, "session_ava")

	first := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{"queen_id": queenMailIvy, "message": "One"})
	if first["error"] != nil {
		t.Fatalf("first letter = %#v", first)
	}
	second := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{"queen_id": queenMailIvy, "message": "Two"})
	want := "You've sent 1 letters today, the most queen_mail.daily_limit allows"
	if second["error"] != want {
		t.Fatalf("second error = %#v, want %q", second["error"], want)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_chain_of_letters_stops_at_max_depth_until_someone_else_writes
func TestQueenMailToolsRespectMaxDepth(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"], "max_depth": 1}`))
	root := testTempDir(t)
	deliverer := &queenMailDeliverer{toRef: "session_ava"}
	mail := queenmail.New(home, queenmail.ConfigFromHive(hiveserver.HiveConfig(home)), deliverer)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	catalogue.enableQueenMail(mail, "session_ava", queenMailAva)

	// Ivy's letter lands in Ava's session: her next letter continues that
	// chain at depth 2, one hop past max_depth.
	if _, err := mail.Send(queenMailIvy, "session_ivy", queenMailAva, "North?", nil); err != nil {
		t.Fatal(err)
	}
	result := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{"queen_id": queenMailIvy, "message": "South?"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "the most queen_mail.max_depth allows") {
		t.Fatalf("max_depth refusal = %#v", result)
	}

	// Someone else speaks in Ava's conversation (the ChatRuntime calls this
	// hook on a person/timer/worker turn), so the chain starts over.
	mail.ForgetCause("session_ava")
	fresh := callQueenMailTool(t, catalogue, MessageQueenToolName, map[string]any{"queen_id": queenMailIvy, "message": "A new subject"})
	if fresh["depth"] != float64(1) {
		t.Fatalf("depth after a new chain = %#v", fresh)
	}
}

// Parity: core/framework/server/tests/test_claude_code_queens.py::test_message_queen_names_who_she_can_write_to
func TestQueenMailToolDescriptionNamesTheCorrespondents(t *testing.T) {
	home := queenMailTestHome(t, queenMailConfig(t, `{"enabled": true, "queens": ["queen_ava", "queen_ivy"]}`))
	catalogue, _ := queenMailCatalogue(t, home, queenMailAva, "session_ava")

	for _, def := range catalogue.Defs() {
		if def.Name != MessageQueenToolName {
			continue
		}
		if !strings.Contains(def.Description, "Queens you can write to: Ivy (queen_ivy, Cartographer).") {
			t.Fatalf("description = %q", def.Description)
		}
		return
	}
	t.Fatal("message_queen is missing")
}
