//go:build !windows

package builtin

// Reliability probes for terminal_exec: cancelling the context must take down
// the whole process group (shell + children), not just the shell.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Parity-exempt: timeout must kill grandchildren that inherited the capture
// pipes; upstream has no equivalent exec test (its lifecycle test covers the
// job manager). The foreground command has to stay alive for the deadline to
// fire — a command that backgrounds a child and exits is a success upstream.
func TestTerminalExecTimeoutKillsBackgroundChildHoldingPipe(t *testing.T) {
	tb := New(t.TempDir())
	start := time.Now()
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":         "(sleep 30 & echo $! > child.pid); sleep 30",
		"timeout_seconds": 0.3,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout took %s; a background child kept the capture pipe open", elapsed)
	}
	payload := decodeExecPayload(t, res.Content)
	if payload["timed_out"] != true {
		t.Fatalf("result = %+v, want a flagged timeout", res)
	}
	raw, err := os.ReadFile(tb.root + "/child.pid")
	if err != nil {
		t.Fatalf("child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid parse: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child pid %d survived the group kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Parity-exempt: context-cancellation kill path for terminal_exec; the Python
// reference has a per-call timeout test but no cancel-in-flight test.
func TestTerminalExecContextCancelKillsProcessGroup(t *testing.T) {
	tb := New(t.TempDir())
	pidFile := tb.root + "/bash.pid"

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := tb.Execute(ctx, ToolTerminalExec, map[string]any{
		"command": "echo $$ > bash.pid; sleep 30",
	})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation took %s; the process group was not killed promptly", elapsed)
	}
	if err == nil && !res.IsError {
		t.Fatalf("cancelled command was not reported as an error: %+v", res)
	}
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation error = %v, want context cancellation", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid parse: %v (raw %q)", err, raw)
	}
	// After the group kill the shell must be gone; ESRCH is the proof.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell pid %d still alive after cancellation (kill err=%v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
