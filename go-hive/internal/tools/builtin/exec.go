package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const killGrace = 5 * time.Second

// terminalExec implements terminal_exec: /bin/bash -lc in the sandbox root,
// separated stdout/stderr capture, bounded output, and process-group kill on
// timeout.
func (t *Toolbox) terminalExec(ctx context.Context, input map[string]any) (Result, error) {
	command, ok := stringArg(input, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return fail("terminal_exec: command is required"), nil
	}
	maxTimeout := t.maxTimeout
	if maxTimeout <= 0 {
		maxTimeout = MaxCommandTimeout
	}
	timeout := DefaultCommandTimeout
	if raw, present := input["timeout_seconds"]; present && raw != nil {
		seconds, ok := finiteNumberArg(input, "timeout_seconds")
		if !ok {
			return fail("terminal_exec: timeout_seconds must be a finite number"), nil
		}
		if seconds <= 0 {
			return fail("terminal_exec: timeout_seconds must be greater than 0"), nil
		}
		if seconds > maxTimeout.Seconds() {
			return fail("terminal_exec: timeout_seconds must be at most %.0f", maxTimeout.Seconds()), nil
		}
		timeout = time.Duration(seconds * float64(time.Second))
	}

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	limit := t.outputLimit
	if limit <= 0 {
		limit = MaxCommandOutputBytes
	}
	stdout := newCappedBuffer(limit)
	stderr := newCappedBuffer(limit)

	cmd := exec.Command("/bin/bash", "-lc", command)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fail("terminal_exec: failed to start /bin/bash: %v", err), nil
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	timedOut := false
	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		// Kill the whole group so children that inherited the pipes cannot
		// keep Wait blocked. Losing the race with a normal exit is fine.
		_ = killProcessGroup(cmd)
		select {
		case waitErr = <-done:
		case <-time.After(killGrace):
			return Result{}, fmt.Errorf("terminal_exec: command timed out after %s and could not be killed", timeout)
		}
	case <-ctx.Done():
		_ = killProcessGroup(cmd)
		select {
		case <-done:
		case <-time.After(killGrace):
			return Result{}, fmt.Errorf("terminal_exec: cancelled and could not kill command: %w", ctx.Err())
		}
		return Result{}, ctx.Err()
	}

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	content := formatExecContent(exitCode, timedOut, stdout, stderr)
	if waitErr != nil && !timedOut && exitCode == 0 {
		content += fmt.Sprintf("\nwait_error: %v", waitErr)
	}
	return Result{Content: content, IsError: timedOut || exitCode != 0}, nil
}

// formatExecContent renders the command envelope: exit status, timeout and
// truncation flags, then the captured streams.
func formatExecContent(exitCode int, timedOut bool, stdout, stderr *cappedBuffer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", exitCode)
	if timedOut {
		b.WriteString("timed_out: true\n")
	}
	if stdout.Truncated() || stderr.Truncated() {
		fmt.Fprintf(&b, "output_truncated: true (limit %d bytes per stream)\n", stdout.Limit())
	}
	b.WriteString("stdout:\n")
	out := stdout.String()
	b.WriteString(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("stderr:\n")
	b.WriteString(stderr.String())
	return b.String()
}

// cappedBuffer keeps at most limit bytes and records whether it dropped any.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if len(p) <= remaining {
		_, _ = b.buf.Write(p)
		return len(p), nil
	}
	if remaining > 0 {
		_, _ = b.buf.Write(p[:remaining])
	}
	b.truncated = true
	// Report success for the dropped bytes so the copying goroutine does not
	// treat truncation as a broken pipe and terminate the command.
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func (b *cappedBuffer) Limit() int { return b.limit }
