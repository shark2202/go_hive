package llm

// Hive streamToken refresh seam. The hive proxy rejects a managed streamToken
// with `hive_stream_token_invalid`; the runtime's rotation loop (started by
// internal/server) installs a refresher here so a call that lost the race with
// the loop can refresh once and replay instead of failing the turn.
//
// Parity: core/framework/llm/litellm.py (hive_stream_token_invalid ->
// try_refresh_now -> retry the same call once),
// core/framework/server/streamtoken_refresh.py::try_refresh_now

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
)

// StreamTokenRefresher re-mints the managed hive streamToken and persists it.
// It returns the token to retry with and true on success.
type StreamTokenRefresher func(ctx context.Context) (string, bool)

// hiveStreamTokenInvalidMarker is the body detail the hive LLM proxy returns
// when the presented streamToken is expired or revoked.
const hiveStreamTokenInvalidMarker = "hive_stream_token_invalid"

var streamTokenRefresher atomic.Pointer[StreamTokenRefresher]

// SetStreamTokenRefresher installs (or, with nil, removes) the process-wide
// refresher. Outside a cloud runtime nothing installs one and the retry seam
// stays inert.
func SetStreamTokenRefresher(refresher StreamTokenRefresher) {
	if refresher == nil {
		streamTokenRefresher.Store(nil)
		return
	}
	streamTokenRefresher.Store(&refresher)
}

// RefreshStreamToken forces a refresh through the installed refresher. It
// reports false when no refresher is installed or the refresh failed.
func RefreshStreamToken(ctx context.Context) (string, bool) {
	refresher := streamTokenRefresher.Load()
	if refresher == nil {
		return "", false
	}
	return (*refresher)(ctx)
}

// IsHiveStreamTokenInvalid reports whether a provider failure is the hive proxy
// rejecting our streamToken: an auth status whose detail carries the marker.
// Any other 401/403 (bad key, missing scope) stays a terminal error.
//
// Parity: core/framework/llm/litellm.py (the hive_stream_token_invalid branch)
func IsHiveStreamTokenInvalid(err error) bool {
	if err == nil {
		return false
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	if providerErr.StatusCode != http.StatusUnauthorized && providerErr.StatusCode != http.StatusForbidden {
		return false
	}
	return strings.Contains(providerErr.Message, hiveStreamTokenInvalidMarker)
}

// retryAfterStreamTokenRefresh refreshes the streamToken once per call and
// reports the replacement token when the request should be replayed.
func retryAfterStreamTokenRefresh(ctx context.Context, err error, alreadyRetried *bool) (string, bool) {
	if *alreadyRetried || !IsHiveStreamTokenInvalid(err) {
		return "", false
	}
	token, ok := RefreshStreamToken(ctx)
	if !ok || token == "" {
		return "", false
	}
	*alreadyRetried = true
	return token, true
}
