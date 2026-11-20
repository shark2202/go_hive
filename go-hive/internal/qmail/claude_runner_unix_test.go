//go:build unix

package qmail

// The process-tree guarantees of a turn are OS-level (upstream starts runs in
// their own session and kills the group); this file exercises them for real.
import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Parity-exempt: 上游用 start_new_session + os.killpg 实现进程树终止，但没有
// pytest 真的 spawn 一个孙进程；这里让 fake claude 起一个后台子进程并断言它随
// 回合被杀。
func TestTurnTimeoutKillsTheWholeProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	h := newClaudeHarness(t, func(_ *Config, queen *ClaudeQueen) {
		queen.Env["FAKE_CLAUDE_SLEEP"] = "30"
		queen.Env["FAKE_CLAUDE_CHILD_MARKER"] = marker
	})
	h.runner.turnTimeout = func(ClaudeQueen) time.Duration { return 250 * time.Millisecond }

	if _, err := h.mail.Send(ava, "session_ava", claude, "Hello?", nil); err != nil {
		t.Fatal(err)
	}
	replies := h.waitReplies(t, 1)
	if replies[0] != "(Her reply failed: her turn ran out of time after 30 min)" {
		t.Fatalf("replies = %#v", replies)
	}
	// The child would touch the marker one second in; if only the fake's shell
	// had been killed, the marker would appear.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the turn's grandchild survived the timeout kill")
	}
}
