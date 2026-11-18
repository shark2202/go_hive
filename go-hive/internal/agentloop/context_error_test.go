package agentloop

import (
	"errors"
	"fmt"
	"testing"
)

// Parity-exempt: Go-only unit tests for context_error.go, which mirrors

// Parity-exempt: context_error.go has no dedicated Python test module.
func TestContextTooLargeMessageVariants(t *testing.T) {
	cases := []string{
		"context length exceeded",
		"context window is full",
		"this model's maximum context length is 8192 tokens",
		"prompt is too long",
		"input is too large for the model",
	}
	for _, message := range cases {
		if !IsContextTooLargeError(errors.New(message)) {
			t.Errorf("IsContextTooLargeError(%q) = false, want true", message)
		}
	}
}

// Parity-exempt: context_error.go has no dedicated Python test module.
func TestNonContextErrorsAreNotContextTooLarge(t *testing.T) {
	cases := []string{
		"rate limit reached",
		"invalid api key",
		"connection reset by peer",
	}
	for _, message := range cases {
		if IsContextTooLargeError(errors.New(message)) {
			t.Errorf("IsContextTooLargeError(%q) = true, want false", message)
		}
	}
}

type ContextWindowError struct{ detail string }

func (e ContextWindowError) Error() string { return e.detail }

// Parity-exempt: context_error.go has no dedicated Python test module.
func TestContextWindowTypeNameDetected(t *testing.T) {
	err := ContextWindowError{detail: "provider rejected the request"}
	if !IsContextTooLargeError(err) {
		t.Fatalf("error type containing ContextWindow should be recognised")
	}
}

// Parity-exempt: context_error.go has no dedicated Python test module.
func TestWrappedContextTooLargeErrorDetected(t *testing.T) {
	inner := errors.New("maximum context length is 128000 tokens")
	wrapped := fmt.Errorf("completion failed: %w", inner)
	if !IsContextTooLargeError(wrapped) {
		t.Fatalf("wrapped context error should be recognised")
	}
}

// Parity-exempt: context_error.go has no dedicated Python test module.
func TestNilErrorIsNotContextTooLarge(t *testing.T) {
	if IsContextTooLargeError(nil) {
		t.Fatalf("nil error should not be reported as context-too-large")
	}
}
