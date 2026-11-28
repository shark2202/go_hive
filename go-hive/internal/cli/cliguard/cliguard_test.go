package cliguard

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// captureStderrRedirect runs fn with os.Stderr redirected to a pipe and returns
// what was written.
func captureStderrRedirect(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	previous := os.Stderr
	os.Stderr = write
	done := make(chan string, 1)
	go func() {
		var builder strings.Builder
		buffer := make([]byte, 4096)
		for {
			count, readErr := read.Read(buffer)
			if count > 0 {
				builder.Write(buffer[:count])
			}
			if readErr != nil {
				break
			}
		}
		done <- builder.String()
	}()
	fn()
	_ = write.Close()
	os.Stderr = previous
	text := <-done
	_ = read.Close()
	return text
}

// TestGuardMapsPanicToPythonCrash pins the top-level presentation of a Go
// panic: CPython reports an uncaught exception as a traceback on stderr with
// exit status 1, and an agent branching on exit codes must see the same class
// of failure from both CLIs. Go's runtime would otherwise dump the stack and
// exit 2.
//
// Parity-exempt: the behavior is CPython's interpreter-level uncaught-exception
// presentation; upstream has no unit test for the process exit status.
func TestGuardMapsPanicToPythonCrash(t *testing.T) {
	var code int
	text := captureStderrRedirect(t, func() {
		code = Guard(func() int { panic(errors.New("boom")) })
	})
	if code != tracker.ExitDomain {
		t.Fatalf("code = %d, want %d (Python's uncaught-exception exit)", code, tracker.ExitDomain)
	}
	if !strings.HasPrefix(text, "Traceback (most recent call last):\n") {
		t.Fatalf("stderr = %q, want a traceback header", text)
	}
	if !strings.Contains(text, "errorString: boom") {
		t.Fatalf("stderr = %q, want the `<type>: <value>` tail", text)
	}
	if !strings.Contains(text, "goroutine") {
		t.Fatalf("stderr = %q, want the real Go stack", text)
	}
}

// TestGuardPassesNormalExitsThrough pins the no-panic path: the guard must
// not alter classified exit codes.
//
// Parity-exempt: see TestGuardMapsPanicToPythonCrash.
func TestGuardPassesNormalExitsThrough(t *testing.T) {
	if got := Guard(func() int { return 7 }); got != 7 {
		t.Fatalf("guard altered a normal exit code: %d", got)
	}
}
