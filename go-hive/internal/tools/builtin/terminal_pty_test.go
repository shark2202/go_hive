//go:build !windows

package builtin

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func terminalTestPTYTool(t *testing.T, tb *Toolbox, name string, input map[string]any) map[string]any {
	t.Helper()
	var (
		res Result
		err error
	)
	switch name {
	case ToolTerminalPTYOpen:
		res, err = tb.terminalPTYOpen(context.Background(), input)
	case ToolTerminalPTYRun:
		res, err = tb.terminalPTYRun(context.Background(), input)
	case ToolTerminalPTYClose:
		res, err = tb.terminalPTYClose(context.Background(), input)
	default:
		t.Fatalf("unknown pty tool %q", name)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return terminalTestDecode(t, res)
}

func terminalTestOpenPTY(t *testing.T, tb *Toolbox) string {
	t.Helper()
	opened := terminalTestPTYTool(t, tb, ToolTerminalPTYOpen, map[string]any{})
	sessionID, _ := opened["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open = %v, want session_id", opened)
	}
	t.Cleanup(func() {
		terminalTestPTYTool(t, tb, ToolTerminalPTYClose, map[string]any{"session_id": sessionID, "force": true})
	})
	return sessionID
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_open_close_basic
func TestTerminalPTYOpenCloseBasic(t *testing.T) {
	tb := New(t.TempDir())
	opened := terminalTestPTYTool(t, tb, ToolTerminalPTYOpen, map[string]any{})
	sessionID, _ := opened["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open = %v", opened)
	}
	if opened["shell"] != "/bin/bash" {
		t.Fatalf("shell = %v, want /bin/bash (never zsh)", opened["shell"])
	}
	if opened["pid"] == nil {
		t.Fatalf("open missing pid: %v", opened)
	}
	closed := terminalTestPTYTool(t, tb, ToolTerminalPTYClose, map[string]any{"session_id": sessionID})
	if closed["already_closed"] != false {
		t.Fatalf("already_closed = %v, want false", closed["already_closed"])
	}
	again := terminalTestPTYTool(t, tb, ToolTerminalPTYClose, map[string]any{"session_id": sessionID})
	if _, ok := again["error"]; !ok {
		t.Fatalf("second close = %v, want unknown session error", again)
	}
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_pty_run_command
func TestTerminalPTYRunCommand(t *testing.T) {
	tb := New(t.TempDir())
	sessionID := terminalTestOpenPTY(t, tb)

	result := terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id":  sessionID,
		"command":     "echo hello-pty",
		"timeout_sec": 5,
	})
	if result["timed_out"] != false {
		t.Fatalf("timed_out = %v (%v)", result["timed_out"], result)
	}
	if output, _ := result["output"].(string); !strings.Contains(output, "hello-pty") {
		t.Fatalf("output = %q, want hello-pty", output)
	}
	if result["prompt_after"] != true {
		t.Fatalf("prompt_after = %v", result["prompt_after"])
	}
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_pty_state_persists
func TestTerminalPTYStatePersists(t *testing.T) {
	tb := New(t.TempDir())
	sessionID := terminalTestOpenPTY(t, tb)

	terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id":  sessionID,
		"command":     "MY_VAR=42",
		"timeout_sec": 5,
	})
	result := terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id":  sessionID,
		"command":     "echo $MY_VAR",
		"timeout_sec": 5,
	})
	if output, _ := result["output"].(string); !strings.Contains(output, "42") {
		t.Fatalf("output = %q, want the persisted variable", output)
	}
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_raw_send_then_read_only
func TestTerminalPTYRawSendThenReadOnly(t *testing.T) {
	tb := New(t.TempDir())
	sessionID := terminalTestOpenPTY(t, tb)

	sent := terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id": sessionID,
		"command":    "read answer; echo GOT=$answer\n",
		"raw_send":   true,
	})
	if bytes, _ := sent["bytes_sent"].(float64); bytes <= 0 {
		t.Fatalf("bytes_sent = %v", sent["bytes_sent"])
	}
	terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id": sessionID,
		"command":    "abc-raw\n",
		"raw_send":   true,
	})
	time.Sleep(100 * time.Millisecond)
	drained := terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id":  sessionID,
		"read_only":   true,
		"timeout_sec": 2,
	})
	if output, _ := drained["output"].(string); !strings.Contains(output, "GOT=abc-raw") {
		t.Fatalf("drained = %q, want GOT=abc-raw", output)
	}
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_session_busy
func TestTerminalPTYSessionBusy(t *testing.T) {
	tb := New(t.TempDir())
	sessionID := terminalTestOpenPTY(t, tb)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = tb.terminalPTYRun(context.Background(), map[string]any{
			"session_id":  sessionID,
			"command":     "sleep 0.6",
			"timeout_sec": 5,
		})
	}()
	time.Sleep(150 * time.Millisecond)

	result := terminalTestPTYTool(t, tb, ToolTerminalPTYRun, map[string]any{
		"session_id":  sessionID,
		"command":     "echo nope",
		"timeout_sec": 1,
	})
	message, _ := result["error"].(string)
	if !strings.Contains(strings.ToLower(message), "busy") {
		t.Fatalf("concurrent run = %v, want busy error", result)
	}
	wg.Wait()
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_unknown_session
func TestTerminalPTYUnknownSession(t *testing.T) {
	tb := New(t.TempDir())
	for _, name := range []string{ToolTerminalPTYRun, ToolTerminalPTYClose} {
		input := map[string]any{"session_id": "pty_doesnotexist"}
		if name == ToolTerminalPTYRun {
			input["command"] = "ls"
		}
		result := terminalTestPTYTool(t, tb, name, input)
		if _, ok := result["error"]; !ok {
			t.Fatalf("%s unknown session = %v, want error", name, result)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_pty.py::test_open_close_basic
func TestTerminalPTYRegistryCap(t *testing.T) {
	t.Setenv("TERMINAL_TOOLS_MAX_PTY", "1")
	registry := newTerminalPTYRegistry()
	if registry.max != 1 {
		t.Fatalf("max = %d, want 1", registry.max)
	}
	first := &terminalPTYSession{SessionID: "pty_a"}
	if err := registry.add(first); err != nil {
		t.Fatalf("first add: %v", err)
	}
	second := &terminalPTYSession{SessionID: "pty_b"}
	if err := registry.add(second); err == nil {
		t.Fatal("second add succeeded past the cap")
	}
	registry.remove("pty_a")
	if err := registry.add(second); err != nil {
		t.Fatalf("add after remove: %v", err)
	}
}
