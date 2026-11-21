package claudecode

// The live check of the runner against the real Claude Code binary: a temp
// HIVE_HOME, a real letter to a configured queen, a real `claude -p` turn and
// the reply on its way back to the writer. It costs one small model call; it
// skips (with the reason) rather than fail when no Claude Code is configured
// on the machine.
import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/queenmail"
)

// paritySecret is what a stray credential looks like in output this test
// prints: everything printed goes through scrubSecrets first.
var (
	secretKeyPattern    = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
	secretBearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{6,}`)
)

func scrubSecrets(text string) string {
	text = secretKeyPattern.ReplaceAllString(text, "sk-<redacted>")
	return secretBearerPattern.ReplaceAllString(text, "Bearer <redacted>")
}

// realClaudeConfigDir is where the sign-in lives: an explicit override, else
// the default `~/.claude` when it has settings.
func realClaudeConfigDir(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("HIVE_CLAUDE_TEST_CONFIG_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".claude")
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		return dir
	}
	return ""
}

// Parity-exempt: 上游没有驱动真实 Claude Code 二进制的测试；这是本机联调：
// 临时 HIVE_HOME、真实发信、真实 `claude -p` 回合、回复回写，并记录 stderr
// 警告与 total_cost_usd（凭据一律不进日志）。
func TestClaudeCodeQueenRealTurnWhenClaudeIsConfigured(t *testing.T) {
	if os.Getenv("HIVE_CLAUDE_REAL_TEST") != "1" {
		// Opt-in: this test calls the real Claude Code binary and a real
		// model. Leaving it on by default makes CI depend on the network and
		// on whatever sign-in the machine happens to have; run it explicitly
		// with HIVE_CLAUDE_REAL_TEST=1 when validating the runner.
		t.Skip("real Claude Code turn is opt-in: set HIVE_CLAUDE_REAL_TEST=1")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH; the real-CLI check needs the Claude Code CLI")
	}
	configDir := realClaudeConfigDir(t)
	if configDir == "" {
		t.Skip("no Claude Code sign-in found: set HIVE_CLAUDE_TEST_CONFIG_DIR, or sign in at ~/.claude")
	}

	home := newMailHome(t)
	config := testConfig()
	queen := config.ClaudeCode[claude]
	queen.Command = []string{claudePath}
	// The plan signed in in her config folder pays; no API key anywhere.
	queen.Auth = "subscription"
	queen.APIKey = ""
	queen.ConfigDir = configDir
	queen.TimeoutMinutes = 3
	config.ClaudeCode[claude] = queen

	deliverer := &fakeDeliverer{}
	mail, store := newQueenMailService(t, home, config, deliverer)
	runner := NewRunner(mail, store)
	mail.SetClaudeRunner(runner)
	t.Cleanup(runner.Close)

	var mu sync.Mutex
	var observed []claudeTurnRun
	runner.observe = func(run claudeTurnRun) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, run)
	}
	var warnings []string
	var warningsMu sync.Mutex
	runner.logf = func(format string, args ...any) {
		warningsMu.Lock()
		defer warningsMu.Unlock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	// No SetBaseURL on purpose: this test is the turn itself, without the MCP
	// server injected (the runner says so in its warnings).
	if _, err := mail.Send(ava, "session_ava", claude, "Reply with exactly: OK", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(200 * time.Second)
	for {
		deliverer.mu.Lock()
		done := len(deliverer.replies) >= 1
		deliverer.mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	replies := hReplies(deliverer)
	if len(replies) == 0 {
		t.Fatalf("no reply within 200s; runner warnings: %#v", warnings)
	}
	if got := strings.TrimSpace(replies[0]); got != "OK" {
		t.Fatalf("her answer = %q (want OK)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 {
		t.Fatalf("observed runs = %d", len(observed))
	}
	run := observed[0]
	if run.timedOut || run.startErr != nil {
		t.Fatalf("run = %#v", run)
	}
	t.Logf("real claude -p: exit code %d", run.code)
	if stderr := strings.TrimSpace(scrubSecrets(run.stderr)); stderr != "" {
		t.Logf("real claude -p stderr: %s", stderr)
	}
	result := parseClaudeResult(run.stdout)
	if result == nil {
		t.Fatalf("no result object in %q", run.stdout)
	}
	if isError, _ := result["is_error"].(bool); isError {
		t.Fatalf("real turn failed: %v", result["result"])
	}
	cost, _ := result["total_cost_usd"].(float64)
	t.Logf("real claude -p cost: total_cost_usd=%v num_turns=%v", cost, result["num_turns"])
	if cost <= 0 {
		t.Fatalf("cost = %v (a real turn must report its cost)", cost)
	}

	// The session exists now: conversations.json says resumed from here on.
	conversation := mail.Store().ReadAudit()[0].ToSession
	saved, err := os.ReadFile(filepath.Join(home.Root, "claude_code", claude, "conversations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored storedConversations
	if err := json.Unmarshal(saved, &stored); err != nil {
		t.Fatal(err)
	}
	entry := stored.Conversations[strings.TrimPrefix(conversation, queenmail.ConversationPrefix+claude+":")]
	if !entry.Started {
		t.Fatalf("conversations.json = %s", saved)
	}
	records := mail.Store().ReadAudit()
	last := records[len(records)-1]
	if last.Kind != queenmail.KindReply || last.From != claude || last.To != ava || !strings.Contains(last.ToSession, "session_ava") {
		t.Fatalf("reply audit = %#v", last)
	}
}

func hReplies(deliverer *fakeDeliverer) []string {
	deliverer.mu.Lock()
	defer deliverer.mu.Unlock()
	return append([]string(nil), deliverer.replies...)
}
