package browserbridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Error codes returned by the bridge. They are stable strings so HTTP and
// in-process callers can branch without parsing messages.
const (
	CodeBadRequest       = "bad_request"
	CodeUnknownCommand   = "unknown_command"
	CodeNotConnected     = "not_connected"
	CodeAmbiguousProfile = "ambiguous_profile"
	CodeTimeout          = "timeout"
	CodeConnectionLost   = "connection_lost"
	CodeExtensionError   = "extension_error"
	CodeCDPError         = "cdp_error"
	CodeNotFound         = "not_found"
	CodeInvalidState     = "invalid_state"
	CodeNoBrowserProfile = "no_browser_profile"
	CodeUnsupportedExt   = "unsupported_extension"
	CodeConflict         = "conflict"
	CodeNoSuchProfile    = "no_such_profile"
	CodeCreateFailed     = "create_failed"
	CodeUngroupNoOp      = "ungroup_no_op"
	CodeRateLimited      = "rate_limited"
)

// Error is a structured bridge failure.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func newError(code, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable}
}

func badRequest(format string, args ...any) *Error {
	return newError(CodeBadRequest, fmt.Sprintf(format, args...), false)
}

// AsError converts any error into a structured *Error. Unknown errors are
// wrapped as extension_error so callers always get a readable code.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return newError(CodeExtensionError, err.Error(), false)
}

// errorFromWire extracts the extension's error payload. The extension sends
// `{id, error}` where error is normally a string but may be an object.
func errorFromWire(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Message != "" {
			return obj.Message
		}
		if obj.Error != "" {
			return obj.Error
		}
	}
	return strings.TrimSpace(string(raw))
}
