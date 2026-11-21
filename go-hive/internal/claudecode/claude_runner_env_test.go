package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/queenmail"
)

// Parity: framework/server/tests/test_claude_code_queens.py::test_her_runs_use_her_api_key_and_get_the_hive_mcp_server
func TestHerRunsUseHerAPIKeyAndGetTheHiveMCPServer(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "a-key-that-isnt-hers")
	t.Setenv("ANTHROPIC_BASE_URL", "https://some-other-gateway.invalid")
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "Noted.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)

	runs := h.runs(t)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	home := filepath.Join(h.home.Root, "claude_code", claude)
	if got := run.env["CLAUDE_CONFIG_DIR"]; got != filepath.Join(home, "config") {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", got)
	}
	if run.env["ANTHROPIC_API_KEY"] != "sk-ant-test-key" {
		t.Fatalf("ANTHROPIC_API_KEY = %q", run.env["ANTHROPIC_API_KEY"])
	}
	if got, present := run.env["ANTHROPIC_BASE_URL"]; present && got != "" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q (must be Hive's own to be left out)", got)
	}
	if run.cwd != resolvedPath(t, filepath.Join(home, "workspace")) {
		t.Fatalf("cwd = %q", run.cwd)
	}
	if argValue(run.args, "--permission-mode") != "default" {
		t.Fatalf("args = %#v", run.args)
	}
	if !containsString(run.args, "--strict-mcp-config") || !containsString(run.args, "--append-system-prompt") {
		t.Fatalf("args = %#v", run.args)
	}
	allowedAt := -1
	for index, arg := range run.args {
		if arg == "--allowedTools" {
			allowedAt = index
			break
		}
	}
	if allowedAt < 0 || allowedAt+2 >= len(run.args) ||
		run.args[allowedAt+1] != claudeMCPTools[0] || run.args[allowedAt+2] != claudeMCPTools[1] {
		t.Fatalf("allowed tools = %#v", run.args)
	}

	conversation := h.mail.Store().ReadAudit()[0].ToSession
	servers, _ := run.mcp["mcpServers"].(map[string]any)
	hive, _ := servers["hive"].(map[string]any)
	if hive == nil {
		t.Fatalf("mcp config = %#v", run.mcp)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if hive["command"] != executable {
		t.Fatalf("mcp command = %#v", hive["command"])
	}
	if args, _ := hive["args"].([]any); len(args) != 1 || args[0] != "queen-mail-mcp" {
		t.Fatalf("mcp args = %#v", hive["args"])
	}
	environment, _ := hive["env"].(map[string]any)
	if environment["HIVE_QUEEN_MAIL_URL"] != "http://127.0.0.1:9" ||
		environment["HIVE_QUEEN_MAIL_TOKEN"] != h.mail.TokenFor(conversation) ||
		environment["HIVE_QUEEN_MAIL_FROM"] != conversation {
		t.Fatalf("mcp env = %#v", environment)
	}
	if run.perm != "600" {
		t.Fatalf("mcp config mode = %q", run.perm)
	}
	mcpPath := argValue(run.args, "--mcp-config")
	if mcpPath == "" {
		t.Fatalf("args = %#v", run.args)
	}
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("the mcp config must be gone once the run ends: %v", err)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_with_full_permissions_the_mcp_tools_need_no_allowlist
func TestWithFullPermissionsTheMCPToolsNeedNoAllowlist(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.PermissionMode = "bypassPermissions"
	})
	h.queueResult(t, "Noted.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)

	run := h.runs(t)[0]
	if argValue(run.args, "--permission-mode") != "bypassPermissions" {
		t.Fatalf("args = %#v", run.args)
	}
	if containsString(run.args, "--allowedTools") {
		t.Fatalf("args = %#v", run.args)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_gateway_in_front_of_the_api_is_her_base_url
func TestAGatewayInFrontOfTheAPIIsHerBaseURL(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.APIBase = "https://gateway.example.invalid"
	})
	h.queueResult(t, "Noted.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)

	run := h.runs(t)[0]
	if run.env["ANTHROPIC_API_KEY"] != "sk-ant-test-key" || run.env["ANTHROPIC_BASE_URL"] != "https://gateway.example.invalid" {
		t.Fatalf("env = %#v", run.env)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_without_an_api_key_she_doesnt_run_and_the_writer_is_told_why
func TestWithoutAnAPIKeySheDoesntRunAndTheWriterIsToldWhy(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "a-key-that-isnt-hers")
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.APIKey = ""
	})

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	replies := h.waitReplies(t, 1)
	if !strings.HasPrefix(replies[0], "(Her reply failed: she has no API key.") {
		t.Fatalf("replies = %#v", replies)
	}
	if runs := h.runs(t); len(runs) != 0 {
		t.Fatalf("runs = %#v", runs)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_subscription_is_used_only_when_chosen_and_the_risk_is_logged_once
func TestASubscriptionIsUsedOnlyWhenChosenAndTheRiskIsLoggedOnce(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "a-key-that-isnt-hers")
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.APIKey = ""
		queen.Auth = "subscription"
	})
	var mu sync.Mutex
	var warnings []string
	h.runner.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	h.queueResult(t, "One.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "One", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)
	if _, err := h.mail.Send(ava, "session_ava", claude, "Two", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 2)

	home := filepath.Join(h.home.Root, "claude_code", claude)
	for _, run := range h.runs(t) {
		if run.env["CLAUDE_CONFIG_DIR"] != filepath.Join(home, "config") {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q", run.env["CLAUDE_CONFIG_DIR"])
		}
		if value, present := run.env["ANTHROPIC_API_KEY"]; present && value != "" {
			t.Fatalf("a subscription run must carry no API key: %q", value)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	atRisk := 0
	for _, line := range warnings {
		if strings.Contains(line, "at your own risk") {
			atRisk++
		}
	}
	if atRisk != 1 {
		t.Fatalf("risk warnings = %d (%#v)", atRisk, warnings)
	}
	found := false
	for _, line := range warnings {
		if strings.Contains(line, "queen_claude runs on the Claude subscription") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %#v", warnings)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_her_api_key_stays_out_of_her_configs_repr
func TestHerAPIKeyStaysOutOfHerConfigsRepr(t *testing.T) {
	h := newClaudeHarness(t, nil)
	entry, ok := h.mail.Config().Claude(claude)
	if !ok {
		t.Fatal("queen missing")
	}
	if entry.Auth != "api_key" || entry.APIKey != "sk-ant-test-key" {
		t.Fatalf("entry = %s", entry.String())
	}
	for _, rendered := range []string{fmt.Sprint(entry), fmt.Sprintf("%+v", entry)} {
		if strings.Contains(rendered, "sk-ant-test-key") {
			t.Fatalf("the API key leaked into %q", rendered)
		}
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_wrong_record_of_her_session_is_corrected_and_the_turn_tried_again
func TestAWrongRecordOfHerSessionIsCorrectedAndTheTurnTriedAgain(t *testing.T) {
	cases := []struct {
		name  string
		first []string
		used  []string
	}{
		{name: "lost", first: []string{"One.", "lost", "Two."}, used: []string{"--session-id", "--resume", "--session-id"}},
		{name: "taken", first: []string{"taken", "Two."}, used: []string{"--session-id", "--resume"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newClaudeHarness(t, nil)
			if testCase.name == "lost" {
				// Her first turn succeeds; then her session file is gone, so
				// resuming finds nothing.
				h.queueResult(t, "One.")
				h.queueMarker(t, "lost")
				h.queueResult(t, "Two.")
				if _, err := h.mail.Send(ava, "session_ava", claude, "One", nil); err != nil {
					t.Fatal(err)
				}
				h.waitReplies(t, 1)
			} else {
				// Hive stopped before noting the session began: starting it
				// again collides.
				h.queueMarker(t, "taken")
				h.queueResult(t, "Two.")
			}
			if _, err := h.mail.Send(ava, "session_ava", claude, "Two", nil); err != nil {
				t.Fatal(err)
			}
			replies := h.waitReplies(t, len(testCase.used)-1)
			if replies[len(replies)-1] != "Two." {
				t.Fatalf("replies = %#v", replies)
			}
			runs := h.runs(t)
			used := make([]string, 0, len(runs))
			for _, run := range runs {
				for _, arg := range run.args {
					if arg == "--session-id" || arg == "--resume" {
						used = append(used, arg)
						break
					}
				}
			}
			if strings.Join(used, " ") != strings.Join(testCase.used, " ") {
				t.Fatalf("used = %#v, want %#v", used, testCase.used)
			}
		})
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_her_runs_get_none_of_hives_secrets
func TestHerRunsGetNoneOfHivesSecrets(t *testing.T) {
	t.Setenv("HIVE_CREDENTIAL_KEY", "hive-master-key-0000")
	t.Setenv("OPENAI_API_KEY", "sk-openai-hive-secret")
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "Noted.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 1)

	run := h.runs(t)[0]
	for _, key := range []string{"HIVE_CREDENTIAL_KEY", "OPENAI_API_KEY"} {
		if _, present := run.env[key]; present {
			t.Fatalf("%s reached her run: %#v", key, run.env[key])
		}
	}
	if run.env["PATH"] == "" {
		t.Fatalf("PATH must be inherited: %#v", run.env)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_an_answer_that_happens_to_say_already_in_use_is_kept
func TestAnAnswerThatHappensToSayAlreadyInUseIsKept(t *testing.T) {
	h := newClaudeHarness(t, nil)
	h.queueResult(t, "Port 8080 is already in use, so I picked 8081.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "Start the server.", nil); err != nil {
		t.Fatal(err)
	}
	replies := h.waitReplies(t, 1)
	if replies[0] != "Port 8080 is already in use, so I picked 8081." {
		t.Fatalf("replies = %#v", replies)
	}
	if runs := h.runs(t); len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_closing_kills_a_running_turn
func TestClosingKillsARunningTurn(t *testing.T) {
	h := newClaudeHarness(t, func(_ *queenmail.Config, queen *queenmail.ClaudeQueen) {
		queen.Env["FAKE_CLAUDE_SLEEP"] = "30"
	})
	if _, err := h.mail.Send(ava, "session_ava", claude, "Take your time.", nil); err != nil {
		t.Fatal(err)
	}
	h.waitStarted(t, 0)
	waitFor(t, "her process tree", func() bool { return h.runner.trackedCount() > 0 })

	h.runner.Close()
	waitFor(t, "her killed process tree", func() bool { return h.runner.trackedCount() == 0 })
	time.Sleep(200 * time.Millisecond)
	h.deliverer.mu.Lock()
	defer h.deliverer.mu.Unlock()
	if len(h.deliverer.replies) != 0 {
		t.Fatalf("a killed turn must say nothing: %#v", h.deliverer.replies)
	}
}

// Parity-exempt: what may run at once is upstream's ThreadPoolExecutor size;
// test_claude_code_queens.py has no test that runs two conversations, so Go
// times the fake binary's overlaps itself.
func TestClaudeRunnerLimitsParallelTurns(t *testing.T) {
	counterDir := t.TempDir()
	h := newClaudeHarness(t, func(config *queenmail.Config, queen *queenmail.ClaudeQueen) {
		config.MaxParallelTurns = 1
		queen.Env["FAKE_CLAUDE_COUNTER"] = counterDir
		queen.Env["FAKE_CLAUDE_SLEEP"] = "0.3"
	})
	h.queueResult(t, "Noted.")
	h.queueResult(t, "Noted.")

	if _, err := h.mail.Send(ava, "session_ava", claude, "One", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mail.Send(ivy, "session_ivy", claude, "Two", nil); err != nil {
		t.Fatal(err)
	}
	h.waitReplies(t, 2)

	data, err := os.ReadFile(filepath.Join(counterDir, "counts"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	if len(lines) != 2 {
		t.Fatalf("counts = %#v", lines)
	}
	for _, line := range lines {
		if line != "1" {
			t.Fatalf("more than one turn ran at once: %#v", lines)
		}
	}
}
