package astgrep

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a typed astgrep failure. Callers branch on the kind
// rather than on message text; the adapter turns these into model-visible
// tool errors and keeps infrastructure failures separate.
type ErrorKind string

const (
	// ErrInvalidInput covers an empty pattern, a bad skip, an empty
	// semicolon-list entry or a missing path.
	ErrInvalidInput ErrorKind = "invalid_input"
	// ErrUnsupportedLanguage covers an unknown lang override, an unknown
	// extension and the ambiguous .h extension.
	ErrUnsupportedLanguage ErrorKind = "unsupported_language"
	// ErrInvalidPattern covers a pattern that does not parse as one AST node.
	ErrInvalidPattern ErrorKind = "invalid_pattern"
	// ErrPathEscape covers .. traversal and symlinks leaving the workspace.
	ErrPathEscape ErrorKind = "path_escape"
	// ErrCancelled is returned when the caller's context is cancelled.
	ErrCancelled ErrorKind = "cancelled"
	// ErrTimeout is reserved for a per-call deadline; per-file parse timeouts
	// are reported as parse issues so other files keep scanning.
	ErrTimeout ErrorKind = "timeout"
)

// Error is the typed error returned by astgrep. Kind is stable API; Message
// is for humans and may change.
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds a typed error without an inner error.
func NewError(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// KindOf returns the typed kind of err, or an empty kind when err is not an
// astgrep Error.
func KindOf(err error) ErrorKind {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind
	}
	return ""
}

// IsKind reports whether err carries the given typed kind.
func IsKind(err error, kind ErrorKind) bool { return KindOf(err) == kind }
