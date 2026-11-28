package lsp

import (
	"errors"
	"fmt"
	"time"
)

// ErrorCode classifies a failure the tool caller can act on. The adapter maps
// these codes to tool output; the core never renders user-facing prose from
// them directly.
type ErrorCode string

const (
	// CodeInvalidArgument means the request is missing or has a malformed
	// field.
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeUnsupportedAction means the action exists in the wider design but
	// is not part of read-only v1.
	CodeUnsupportedAction ErrorCode = "unsupported_action"
	// CodeDisabled means lsp.json settings.enabled is false.
	CodeDisabled ErrorCode = "disabled"
	// CodeNoServer means no configured server matches the file, or the
	// matching server is disabled/undiscovered.
	CodeNoServer ErrorCode = "no_server"
	// CodeServerUnavailable means the server could not be started; the
	// message carries the reason and any bounded stderr tail.
	CodeServerUnavailable ErrorCode = "server_unavailable"
	// CodeRequestFailed means the server answered with a JSON-RPC error.
	CodeRequestFailed ErrorCode = "request_failed"
	// CodeTimeout means a bounded wait expired.
	CodeTimeout ErrorCode = "timeout"
	// CodePathRejected means a path escaped the workspace, was not a regular
	// file, or was otherwise unsafe.
	CodePathRejected ErrorCode = "path_rejected"
	// CodePositionRejected means the line/symbol could not be resolved.
	CodePositionRejected ErrorCode = "position_rejected"
	// CodeUnsupported means the negotiated server capabilities do not cover
	// the requested action.
	CodeUnsupported ErrorCode = "unsupported_by_server"
	// CodeInternal is an unexpected local failure.
	CodeInternal ErrorCode = "internal"
)

// Error is the typed error returned by Service.Execute and helpers.
type Error struct {
	Code    ErrorCode
	Message string
	// Details is optional structured evidence (server name, stderr tail).
	Details map[string]any
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return string(e.Code) + ": " + e.Message
}

// NewError builds a typed error.
func NewError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsCode reports whether err is a *Error with the given code.
func IsCode(err error, code ErrorCode) bool {
	var target *Error
	if errors.As(err, &target) {
		return target.Code == code
	}
	return false
}

// ErrTimeout is matched by TimeoutError.
var ErrTimeout = errors.New("lsp: request timed out")

// TimeoutError reports which bounded wait expired.
type TimeoutError struct {
	Operation string
	Timeout   time.Duration
}

func (e *TimeoutError) Error() string {
	if e.Timeout <= 0 {
		return fmt.Sprintf("lsp: %s timed out", e.Operation)
	}
	return fmt.Sprintf("lsp: %s timed out after %s", e.Operation, e.Timeout)
}

// Is lets errors.Is(err, ErrTimeout) hold.
func (e *TimeoutError) Is(target error) bool { return target == ErrTimeout }

// ExitError reports that the language server process ended. Stderr is the
// bounded, redacted tail captured from the process.
type ExitError struct {
	Reason string
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return "lsp: language server exited: " + e.Reason
	}
	return "lsp: language server exited: " + e.Reason + " (stderr: " + e.Stderr + ")"
}
