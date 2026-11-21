package tracker

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestUnclassifiedErrorCrashesLikePython pins the harness's presentation of an
// error that is not a tracker.CLIError: upstream's errors.run catches only
// TrackerError, so anything else escapes as an uncaught exception — a traceback
// on stderr, exit 1, and NO JSON envelope even with --json. Go renders the same
// shape (header, frames, `<type>: <message>`) instead of laundering the bug
// into a domain_error envelope.
//
// Parity-exempt: upstream pins this through framework/trackerdb/errors.py::run,
// which has no unit test for the uncaught path in the vendored core tree.
func TestUnclassifiedErrorCrashesLikePython(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		code := Run(func() (any, error) { return nil, errors.New("mkdir /tmp/x: not a directory") }, asJSON, nil, &stdout, &stderr)
		if code != ExitDomain {
			t.Fatalf("asJSON=%v exit = %d, want %d", asJSON, code, ExitDomain)
		}
		if stdout.Len() != 0 {
			t.Fatalf("asJSON=%v stdout = %q, want empty (no envelope on a crash)", asJSON, stdout.String())
		}
		text := stderr.String()
		if !strings.HasPrefix(text, "Traceback (most recent call last):\n") {
			t.Fatalf("asJSON=%v stderr = %q, want a traceback header", asJSON, text)
		}
		if !strings.HasSuffix(text, "errorString: mkdir /tmp/x: not a directory\n") {
			t.Fatalf("asJSON=%v stderr = %q, want `<type>: <message>` tail", asJSON, text)
		}
		if !strings.Contains(text, "tracker.Run") {
			t.Fatalf("asJSON=%v stderr lacks the harness frame:\n%s", asJSON, text)
		}
	}
}

// TestClassifiedErrorStillRendersEnvelope guards the other half: a real
// CLIError keeps the envelope/one-liner contract.
//
// Parity: framework/trackerdb/errors.py::validation
func TestClassifiedErrorStillRendersEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(func() (any, error) { return nil, Validation("bad args") }, true, nil, &stdout, &stderr)
	if code != ExitValidation {
		t.Fatalf("exit = %d, want %d", code, ExitValidation)
	}
	if !strings.Contains(stdout.String(), `"code": "validation"`) {
		t.Fatalf("stdout = %q, want the validation envelope", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
