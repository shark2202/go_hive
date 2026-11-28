package hiveconf

// Subscription-token refresh for the Claude Code and Codex OAuth credentials.
//
// Upstream get_claude_code_token/get_codex_token refresh an expired access
// token through the provider's OAuth endpoint (an RFC 6749 refresh-token
// grant), write the rotated credentials back to the CLI's own file, and fall
// back to the stale token when the refresh fails or no refresh token exists.
// This file implements that flow for the Go readers. Two bounded guards are
// added on top of upstream's single attempt: transient failures (network,
// 408/425/429/5xx) are retried a fixed number of times, and concurrent callers
// that notice the same expired token share one grant.
//
// Parity: core/framework/loader/agent_loader.py::get_claude_code_token
// Parity: core/framework/loader/agent_loader.py::_refresh_claude_code_token
// Parity: core/framework/loader/agent_loader.py::_save_refreshed_credentials
// Parity: core/framework/loader/agent_loader.py::get_codex_token
// Parity: core/framework/loader/agent_loader.py::_refresh_codex_token
// Parity: core/framework/loader/agent_loader.py::_save_refreshed_codex_credentials
// Parity: core/framework/loader/agent_loader.py::_is_codex_token_expired

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// claudeOAuthClientID is the Claude Code CLI's public OAuth client id.
	claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// codexOAuthClientID is the Codex CLI's public OAuth client id.
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// tokenRefreshBuffer refreshes a token this long before expiry, exactly as
	// upstream's _TOKEN_REFRESH_BUFFER_SECS (5 minutes).
	tokenRefreshBuffer = 5 * time.Minute
	// codexTokenLifetime infers Codex token age from last_refresh because
	// auth.json has no expiresAt field (upstream _CODEX_TOKEN_LIFETIME_SECS).
	codexTokenLifetime = time.Hour
	// tokenRefreshTimeout bounds one whole refresh (every retry included).
	tokenRefreshTimeout = 15 * time.Second
	// tokenRefreshAttempts is the bounded retry count: one attempt plus two
	// retries on a transient failure.
	tokenRefreshAttempts = 3
	// tokenResponseLimit bounds a token endpoint response body.
	tokenResponseLimit = 1 << 20
)

// Test seams. Production never changes these after init; tests replace them
// before calling a reader (no test runs in parallel with another in-package
// test, so the package-level assignments are race-free).
var (
	claudeOAuthEndpoint    = "https://console.anthropic.com/v1/oauth/token"
	codexOAuthEndpoint     = "https://auth.openai.com/oauth/token"
	subscriptionHTTPClient = &http.Client{Timeout: tokenRefreshTimeout}
	subscriptionNow        = time.Now
	subscriptionRetryBase  = 100 * time.Millisecond
	subscriptionSleep      = sleepContext
)

var errSubscriptionNoRefreshToken = errors.New("hiveconf: subscription token has no refresh token")

// oauthRefreshRequest is one provider's refresh-token grant.
type oauthRefreshRequest struct {
	endpoint     string
	clientID     string
	refreshToken string
}

// oauthTokenGrant is the parsed token endpoint payload.
type oauthTokenGrant struct {
	accessToken  string
	refreshToken string
	idToken      string
	expiresIn    time.Duration
}

// claudeTokenExpired applies upstream's expiresAt check: expiresAt is in
// milliseconds and a missing/non-numeric value reads as 0 (expired).
func claudeTokenExpired(oauth map[string]any, now time.Time) bool {
	expiresAt, ok := int64Value(oauth["expiresAt"])
	if !ok {
		return true
	}
	return expiresAt <= now.UnixMilli()+tokenRefreshBuffer.Milliseconds()
}

// codexTokenExpired infers expiry from last_refresh + one hour, falling back
// to the file mtime when last_refresh is absent. An unparsable timestamp is
// treated as expired, matching _is_codex_token_expired.
func codexTokenExpired(path string, auth map[string]any, now time.Time) bool {
	lastRefresh, ok := codexLastRefresh(path, auth)
	if !ok {
		return true
	}
	expiresAt := lastRefresh.Add(codexTokenLifetime)
	return !now.Before(expiresAt.Add(-tokenRefreshBuffer))
}

func codexLastRefresh(path string, auth map[string]any) (time.Time, bool) {
	value, present := auth["last_refresh"]
	if !present || value == nil {
		info, err := os.Stat(path)
		if err != nil {
			return time.Time{}, false
		}
		return info.ModTime(), true
	}
	if text, ok := value.(string); ok {
		return parseISO8601(text)
	}
	seconds, ok := floatValue(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, int64(seconds*float64(time.Second))), true
}

// parseISO8601 accepts the spellings Python's datetime.fromisoformat takes
// from Codex: a trailing Z, a numeric offset, or a space date/time separator.
// Naive timestamps are interpreted in local time, as .timestamp() does.
func parseISO8601(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if strings.HasSuffix(text, "Z") {
		text = strings.TrimSuffix(text, "Z") + "+00:00"
	}
	if len(text) > 10 && text[10] == ' ' {
		text = text[:10] + "T" + text[11:]
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05Z0700",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// refreshClaudeCredentials re-checks the file before spending a request: a
// concurrent caller (or another process) may have refreshed it already, and a
// rotated refresh token must win over the stale one.
func refreshClaudeCredentials(ctx context.Context, path, fallbackRefreshToken string) (string, error) {
	refreshToken := strings.TrimSpace(fallbackRefreshToken)
	if creds, err := readJSONObject(path); err == nil {
		if oauth, ok := creds["claudeAiOauth"].(map[string]any); ok {
			accessToken, _ := oauth["accessToken"].(string)
			if strings.TrimSpace(accessToken) != "" && !claudeTokenExpired(oauth, subscriptionNow()) {
				return accessToken, nil
			}
			if rotated, _ := oauth["refreshToken"].(string); strings.TrimSpace(rotated) != "" {
				refreshToken = strings.TrimSpace(rotated)
			}
		}
	}
	if refreshToken == "" {
		return "", errSubscriptionNoRefreshToken
	}
	grant, err := requestOAuthTokenWithRetry(ctx, oauthRefreshRequest{
		endpoint:     claudeOAuthEndpoint,
		clientID:     claudeOAuthClientID,
		refreshToken: refreshToken,
	})
	if err != nil {
		return "", err
	}
	saveClaudeCredentials(path, grant, subscriptionNow())
	return grant.accessToken, nil
}

// refreshCodexCredentials is refreshClaudeCredentials for ~/.codex/auth.json.
func refreshCodexCredentials(ctx context.Context, path, fallbackRefreshToken string) (string, error) {
	refreshToken := strings.TrimSpace(fallbackRefreshToken)
	if auth, err := readJSONObject(path); err == nil {
		if tokens, ok := auth["tokens"].(map[string]any); ok {
			accessToken, _ := tokens["access_token"].(string)
			if strings.TrimSpace(accessToken) != "" && !codexTokenExpired(path, auth, subscriptionNow()) {
				return accessToken, nil
			}
			if rotated, _ := tokens["refresh_token"].(string); strings.TrimSpace(rotated) != "" {
				refreshToken = strings.TrimSpace(rotated)
			}
		}
	}
	if refreshToken == "" {
		return "", errSubscriptionNoRefreshToken
	}
	grant, err := requestOAuthTokenWithRetry(ctx, oauthRefreshRequest{
		endpoint:     codexOAuthEndpoint,
		clientID:     codexOAuthClientID,
		refreshToken: refreshToken,
	})
	if err != nil {
		return "", err
	}
	saveCodexCredentials(path, grant, subscriptionNow())
	return grant.accessToken, nil
}

// requestOAuthTokenWithRetry performs the refresh grant with bounded retries.
// Only transient failures (network errors, 408/425/429/5xx) are retried; an
// invalid grant or a malformed success payload fails immediately.
func requestOAuthTokenWithRetry(ctx context.Context, request oauthRefreshRequest) (oauthTokenGrant, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", request.refreshToken)
	form.Set("client_id", request.clientID)
	encoded := form.Encode()

	var lastErr error
	for attempt := 0; attempt < tokenRefreshAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return oauthTokenGrant{}, err
		}
		grant, retryable, err := requestOAuthToken(ctx, request.endpoint, encoded)
		if err == nil {
			return grant, nil
		}
		lastErr = err
		if !retryable || attempt+1 >= tokenRefreshAttempts {
			break
		}
		if err := subscriptionSleep(ctx, tokenRetryDelay(attempt)); err != nil {
			return oauthTokenGrant{}, err
		}
	}
	return oauthTokenGrant{}, lastErr
}

// requestOAuthToken performs one POST. retryable reports whether the failure
// is transient (the retry loop may try again).
func requestOAuthToken(ctx context.Context, endpoint, encodedForm string) (oauthTokenGrant, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodedForm))
	if err != nil {
		return oauthTokenGrant{}, false, fmt.Errorf("hiveconf: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	response, err := subscriptionHTTPClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return oauthTokenGrant{}, false, ctxErr
		}
		return oauthTokenGrant{}, true, fmt.Errorf("hiveconf: token request failed: %w", err)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, tokenResponseLimit))
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return oauthTokenGrant{}, false, ctxErr
		}
		return oauthTokenGrant{}, true, fmt.Errorf("hiveconf: read token response: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := retryableTokenStatus(response.StatusCode)
		return oauthTokenGrant{}, retryable, fmt.Errorf("hiveconf: token endpoint HTTP %d: %s",
			response.StatusCode, summarizeTokenBody(body))
	}
	grant, err := parseOAuthTokenGrant(body)
	if err != nil {
		return oauthTokenGrant{}, false, err
	}
	return grant, false, nil
}

func retryableTokenStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

func tokenRetryDelay(attempt int) time.Duration {
	if attempt >= 8 {
		return subscriptionRetryBase << 8
	}
	return subscriptionRetryBase << attempt
}

func parseOAuthTokenGrant(body []byte) (oauthTokenGrant, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return oauthTokenGrant{}, fmt.Errorf("hiveconf: decode token response: %w", err)
	}
	if code, _ := payload["error"].(string); strings.TrimSpace(code) != "" {
		description, _ := payload["error_description"].(string)
		if description != "" {
			return oauthTokenGrant{}, fmt.Errorf("hiveconf: token endpoint error %s: %s", code, description)
		}
		return oauthTokenGrant{}, fmt.Errorf("hiveconf: token endpoint error %s", code)
	}
	accessToken, _ := payload["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return oauthTokenGrant{}, errors.New("hiveconf: token response has no access_token")
	}
	grant := oauthTokenGrant{accessToken: strings.TrimSpace(accessToken)}
	if rotated, _ := payload["refresh_token"].(string); strings.TrimSpace(rotated) != "" {
		grant.refreshToken = strings.TrimSpace(rotated)
	}
	if idToken, _ := payload["id_token"].(string); strings.TrimSpace(idToken) != "" {
		grant.idToken = strings.TrimSpace(idToken)
	}
	if seconds, ok := floatValue(payload["expires_in"]); ok && seconds > 0 {
		grant.expiresIn = time.Duration(seconds * float64(time.Second))
	}
	return grant, nil
}

func summarizeTokenBody(body []byte) string {
	const limit = 240
	text := strings.TrimSpace(string(body))
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// saveClaudeCredentials writes the rotated token back to the credentials file,
// preserving every unrelated field. Keychain support is intentionally out of
// scope for the dependency-free Go reader (the file is its credential source).
func saveClaudeCredentials(path string, grant oauthTokenGrant, now time.Time) {
	creds, err := readJSONObject(path)
	if err != nil {
		return
	}
	oauth, _ := creds["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["accessToken"] = grant.accessToken
	if grant.refreshToken != "" {
		oauth["refreshToken"] = grant.refreshToken
	}
	if grant.expiresIn > 0 {
		oauth["expiresAt"] = now.Add(grant.expiresIn).UnixMilli()
	}
	creds["claudeAiOauth"] = oauth
	_ = writeJSONAtomically(path, creds, 0o600)
}

// saveCodexCredentials writes the rotated tokens and the refresh timestamp
// back to auth.json, preserving the account metadata.
func saveCodexCredentials(path string, grant oauthTokenGrant, now time.Time) {
	auth, err := readJSONObject(path)
	if err != nil {
		return
	}
	tokens, _ := auth["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = grant.accessToken
	if grant.refreshToken != "" {
		tokens["refresh_token"] = grant.refreshToken
	}
	if grant.idToken != "" {
		tokens["id_token"] = grant.idToken
	}
	auth["tokens"] = tokens
	auth["last_refresh"] = now.UTC().Format(time.RFC3339Nano)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = writeJSONAtomically(path, auth, 0o600)
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("hiveconf: credential file is not a JSON object")
	}
	return doc, nil
}

// writeJSONAtomically replaces path through a same-directory temp file so a
// concurrent reader never observes a half-written credential file.
func writeJSONAtomically(path string, doc map[string]any, mode os.FileMode) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".hiveconf-token-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
		if asFloat, err := typed.Float64(); err == nil {
			return int64(asFloat), true
		}
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	}
	return 0, false
}

// subscriptionRefreshFlight is one in-flight refresh shared by every caller
// that asks for the same credential file.
type subscriptionRefreshFlight struct {
	done  chan struct{}
	token string
	err   error
}

var (
	subscriptionRefreshMu      sync.Mutex
	subscriptionRefreshFlights = map[string]*subscriptionRefreshFlight{}
)

// refreshSubscriptionToken runs refresh once per key at a time. A caller
// joining an in-flight refresh waits for its result or returns promptly when
// its own context is cancelled; it never blocks on a grant it cannot use.
func refreshSubscriptionToken(ctx context.Context, key string, refresh func(context.Context) (string, error)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, tokenRefreshTimeout)
	defer cancel()

	subscriptionRefreshMu.Lock()
	if flight, ok := subscriptionRefreshFlights[key]; ok {
		subscriptionRefreshMu.Unlock()
		select {
		case <-flight.done:
			return flight.token, flight.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	flight := &subscriptionRefreshFlight{done: make(chan struct{})}
	subscriptionRefreshFlights[key] = flight
	subscriptionRefreshMu.Unlock()

	token, err := refresh(ctx)

	subscriptionRefreshMu.Lock()
	flight.token, flight.err = token, err
	delete(subscriptionRefreshFlights, key)
	subscriptionRefreshMu.Unlock()
	close(flight.done)
	return token, err
}

// resetSubscriptionRefreshFlights is a test seam: every flight finishes and
// removes itself, so this only guards against a failed test leaking state.
func resetSubscriptionRefreshFlights() {
	subscriptionRefreshMu.Lock()
	subscriptionRefreshFlights = map[string]*subscriptionRefreshFlight{}
	subscriptionRefreshMu.Unlock()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
