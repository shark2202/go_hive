package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
)

// StreamTransientMaxRetries caps retries of transient stream failures
// (connection resets, timeouts, gateway/server unavailability). It is a
// separate, much lower cap than RateLimitMaxRetries because a network blip
// rarely needs the long rate-limit backoff.
//
// Parity: core/framework/llm/litellm.py::STREAM_TRANSIENT_MAX_RETRIES
const StreamTransientMaxRetries = 3

// isStreamTransientStatus reports whether an HTTP status is the transient
// gateway/server family upstream marks recoverable (500/502/503/504, plus the
// request-timeout status).
func isStreamTransientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isStreamTransientError classifies a stream failure as transient.
//
// Billing and auth failures are permanent. A caller cancellation is terminal;
// a request-scoped deadline (the per-request timeout) or a network error is
// retryable.
//
// Parity: core/framework/llm/litellm.py::_is_stream_transient_error
func isStreamTransientError(err error) bool {
	if err == nil {
		return false
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.StatusCode == http.StatusPaymentRequired {
			return false
		}
		return isStreamTransientStatus(providerErr.StatusCode)
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}
