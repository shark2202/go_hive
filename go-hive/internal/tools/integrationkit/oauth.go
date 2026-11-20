package integrationkit

// Standard OAuth2 refresh-token support (RFC 6749 §6) for modules whose
// upstream credential is an OAuth2 access token.
//
// Upstream, refresh happens in the credential layer: core/framework/
// credentials/store.py refreshes an expired credential through its provider
// before the tool sees it, and BaseOAuth2Provider.refresh_access_token does
// the POST form grant_type=refresh_token request. The Go port resolves module
// credentials from the environment, so the same grant lives here: a module
// declares OAuth on its ModuleConfig, and every call resolves its token
// through Runtime.AccessToken instead of replaying a static value.
//
// The refresher caches the minted token per (token endpoint, client id,
// refresh token) and de-duplicates concurrent grants, so a burst of tool calls
// that all notice an expired token performs exactly one token request. A
// failed refresh is never silent: it surfaces as an OAuthRefreshError whose
// ValueError is the {"error": ...} payload a tool returns.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRefreshBuffer refreshes a token this long before it expires; the
	// value matches upstream BaseOAuth2Provider.should_refresh (5 minutes).
	DefaultRefreshBuffer = 5 * time.Minute
	// DefaultOAuthTimeout bounds one token request.
	DefaultOAuthTimeout = 30 * time.Second
	// maxTokenResponseBytes bounds the token endpoint response body.
	maxTokenResponseBytes = 1 << 20
)

// OAuth2Refresh describes one module's standard OAuth2 refresh-token source.
// All fields come from the module's credential (environment variables or the
// credential store); a nil *OAuth2Refresh means the module has no refresh path.
type OAuth2Refresh struct {
	// Module names the credential in error messages ("google").
	Module string
	// TokenURL is the provider's token endpoint. Required for a refresh.
	TokenURL     string
	ClientID     string
	ClientSecret string
	RefreshToken string

	// AccessToken is the token already in hand. It is used when it is not
	// known to be expired; ExpiresAt nil means "unknown", never "expired".
	AccessToken string
	ExpiresAt   *time.Time

	// ExtraParams are merged into the token request form (scope, audience).
	ExtraParams map[string]string

	// TokenEnv names the environment variable holding the static access
	// token; RefreshEnv names the refresh-token variable. Both are reported
	// when a token cannot be refreshed so the operator knows what to change.
	TokenEnv   string
	RefreshEnv string
}

// OAuthRefreshError is a refresh failure that must reach the caller. Code is
// the provider error code ("invalid_grant", "invalid_client") or a local
// classification ("no_refresh_token", "network_error"); ValueError renders the
// upstream-style value payload.
type OAuthRefreshError struct {
	Module      string
	Code        string
	Status      int
	Description string
	Message     string
	TokenEnv    string
	RefreshEnv  string
}

func (e *OAuthRefreshError) Error() string { return e.Message }

// ValueError is the {"error": ...} dictionary the tool returns. It is never
// silent: the message always names the credential and the variable to fix.
func (e *OAuthRefreshError) ValueError() map[string]any {
	out := map[string]any{"error": e.Message}
	if e.Code != "" {
		out["error_code"] = e.Code
	}
	if help := e.helpText(); help != "" {
		out["help"] = help
	}
	return out
}

func (e *OAuthRefreshError) helpText() string {
	switch e.Code {
	case "invalid_grant":
		if e.RefreshEnv != "" {
			return "Set " + e.RefreshEnv + " to a newly authorized refresh token (re-run the OAuth consent flow), " +
				"or replace " + fallbackName(e.TokenEnv, "the access token") + " with a fresh access token."
		}
	case "no_refresh_token":
		if e.RefreshEnv != "" {
			return "Static access tokens cannot be refreshed. Set " + e.RefreshEnv + " (with the OAuth client id and secret) " +
				"to enable the refresh-token grant, or replace " + fallbackName(e.TokenEnv, "the access token") + " with a new token."
		}
	}
	return ""
}

func fallbackName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

// TokenRefresher mints and caches access tokens from refresh tokens. It is
// safe for concurrent use; concurrent grants for the same credential share one
// request.
type TokenRefresher struct {
	// Client performs the token requests. Nil uses a client with a 30s timeout.
	Client *http.Client
	// Now supplies the clock; nil uses time.Now.
	Now func() time.Time

	mu     sync.Mutex
	values map[string]*refreshedToken
	calls  map[string]*refreshCall
}

type refreshedToken struct {
	accessToken  string
	refreshToken string
	expiresAt    *time.Time
}

type refreshCall struct {
	done  chan struct{}
	value *refreshedToken
	err   error
}

// NewTokenRefresher builds a refresher over client (nil for the default).
func NewTokenRefresher(client *http.Client) *TokenRefresher {
	if client == nil {
		client = &http.Client{Timeout: DefaultOAuthTimeout}
	}
	return &TokenRefresher{Client: client, values: map[string]*refreshedToken{}, calls: map[string]*refreshCall{}}
}

func (r *TokenRefresher) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *TokenRefresher) client() *http.Client {
	if r == nil {
		return &http.Client{Timeout: DefaultOAuthTimeout}
	}
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: DefaultOAuthTimeout}
}

// Token returns a usable access token: the cached refresh result, the
// still-valid static token, or a freshly minted one. A configuration with no
// refresh token returns the static token when it is not known to be expired
// and an OAuthRefreshError when it is.
func (r *TokenRefresher) Token(ctx context.Context, cfg *OAuth2Refresh) (string, error) {
	if cfg == nil {
		return "", nil
	}
	now := r.now()
	refreshToken := strings.TrimSpace(cfg.RefreshToken)
	if refreshToken == "" {
		return staticOAuthToken(cfg, now)
	}
	key := refreshKey(cfg)
	if value := r.cached(key, now); value != nil {
		return value.accessToken, nil
	}
	if strings.TrimSpace(cfg.AccessToken) != "" && !tokenExpired(now, cfg.ExpiresAt) {
		return strings.TrimSpace(cfg.AccessToken), nil
	}
	value, err := r.grant(ctx, cfg, key, r.latestRefreshToken(key, refreshToken), false)
	if err != nil {
		return "", err
	}
	return value.accessToken, nil
}

// Refresh forces a new grant, bypassing any cached token. It is the 401 retry
// path: the provider rejected the token we had, so a fresh one is required.
// Concurrent forced refreshes for the same credential still share one request,
// and the result replaces the normal cache entry so later calls use it.
func (r *TokenRefresher) Refresh(ctx context.Context, cfg *OAuth2Refresh) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.RefreshToken) == "" {
		return "", &OAuthRefreshError{
			Module:     moduleOf(cfg),
			Code:       "no_refresh_token",
			TokenEnv:   envOf(cfg),
			RefreshEnv: refreshEnvOf(cfg),
			Message:    staticOnlyMessage(cfg),
		}
	}
	key := refreshKey(cfg)
	value, err := r.grant(ctx, cfg, key, r.latestRefreshToken(key, strings.TrimSpace(cfg.RefreshToken)), true)
	if err != nil {
		return "", err
	}
	return value.accessToken, nil
}

// latestRefreshToken returns the refresh token of the newest cached grant:
// providers that rotate refresh tokens invalidate the old one, so later
// grants must use the rotated value.
func (r *TokenRefresher) latestRefreshToken(key, fallback string) string {
	if r == nil {
		return fallback
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if value := r.values[key]; value != nil && strings.TrimSpace(value.refreshToken) != "" {
		return strings.TrimSpace(value.refreshToken)
	}
	return fallback
}

func (r *TokenRefresher) cached(key string, now time.Time) *refreshedToken {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.values[key]
	if value == nil {
		return nil
	}
	if value.expiresAt != nil && !now.Before(value.expiresAt.Add(-DefaultRefreshBuffer)) {
		return nil
	}
	return value
}

// grant performs one refresh-token request, sharing it with every caller that
// asks for the same credential while it is in flight. The cache entry always
// lives under key; forced grants use a sibling in-flight slot so they never
// join a normal grant that produced the token the provider just rejected.
func (r *TokenRefresher) grant(ctx context.Context, cfg *OAuth2Refresh, key, refreshToken string, forced bool) (*refreshedToken, error) {
	callKey := key
	if forced {
		callKey = key + "\x00forced"
	}
	r.mu.Lock()
	if call, running := r.calls[callKey]; running {
		r.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.err != nil {
			return nil, call.err
		}
		return call.value, nil
	}
	if !forced {
		if value := r.values[key]; value != nil {
			r.mu.Unlock()
			return value, nil
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	r.calls[callKey] = call
	r.mu.Unlock()

	value, err := r.request(ctx, cfg, refreshToken)

	r.mu.Lock()
	call.value, call.err = value, err
	delete(r.calls, callKey)
	if err == nil {
		r.values[key] = value
	}
	r.mu.Unlock()
	close(call.done)
	if err != nil {
		return nil, err
	}
	return value, nil
}

// request is the RFC 6749 §6 refresh grant: a form POST carrying
// grant_type=refresh_token, the client credentials and the refresh token.
func (r *TokenRefresher) request(ctx context.Context, cfg *OAuth2Refresh, refreshToken string) (*refreshedToken, error) {
	if strings.TrimSpace(cfg.TokenURL) == "" {
		return nil, r.failure(cfg, "no_token_url", 0, "", "token endpoint is not configured")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	if strings.TrimSpace(cfg.ClientID) != "" {
		form.Set("client_id", strings.TrimSpace(cfg.ClientID))
	}
	if strings.TrimSpace(cfg.ClientSecret) != "" {
		form.Set("client_secret", strings.TrimSpace(cfg.ClientSecret))
	}
	form.Set("refresh_token", refreshToken)
	for name, value := range cfg.ExtraParams {
		if name != "" && value != "" {
			form.Set(name, value)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, r.failure(cfg, "request_error", 0, "", err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, r.failure(cfg, "network_error", 0, "", err.Error())
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if readErr != nil {
		return nil, r.failure(cfg, "read_error", resp.StatusCode, "", readErr.Error())
	}

	payload, parseErr := parseTokenPayload(raw, resp.Header.Get("Content-Type"))
	if parseErr != nil && resp.StatusCode/100 != 2 {
		return nil, r.failure(cfg, "http_error", resp.StatusCode, "", strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode/100 != 2 {
		code, description := tokenErrorFields(payload, raw)
		return nil, r.failure(cfg, code, resp.StatusCode, description, "")
	}
	if code, description := tokenErrorFields(payload, raw); code != "" {
		return nil, r.failure(cfg, code, resp.StatusCode, description, "")
	}
	accessToken := strings.TrimSpace(scalarString(payload["access_token"]))
	if accessToken == "" {
		return nil, r.failure(cfg, "invalid_response", resp.StatusCode, "", "token response has no access_token")
	}
	value := &refreshedToken{accessToken: accessToken, refreshToken: refreshToken}
	if rotated := strings.TrimSpace(scalarString(payload["refresh_token"])); rotated != "" {
		value.refreshToken = rotated
	}
	if seconds, ok := tokenSeconds(payload["expires_in"]); ok && seconds > 0 {
		expiresAt := r.now().Add(time.Duration(seconds) * time.Second)
		value.expiresAt = &expiresAt
	}
	return value, nil
}

func (r *TokenRefresher) failure(cfg *OAuth2Refresh, code string, status int, description, detail string) *OAuthRefreshError {
	failure := &OAuthRefreshError{
		Module:      moduleOf(cfg),
		Code:        code,
		Status:      status,
		Description: description,
		TokenEnv:    envOf(cfg),
		RefreshEnv:  refreshEnvOf(cfg),
	}
	module := fallbackName(failure.Module, "credential")
	switch code {
	case "invalid_grant":
		// Upstream BaseOAuth2Provider.refresh message, kept verbatim.
		failure.Message = fmt.Sprintf("Refresh token for '%s' is invalid or revoked. Re-authorization required.", module)
	default:
		pieces := []string{code}
		if description != "" {
			pieces = append(pieces, description)
		} else if detail != "" {
			pieces = append(pieces, detail)
		}
		failure.Message = fmt.Sprintf("OAuth refresh for '%s' failed: %s", module, strings.Join(pieces, ": "))
		if status != 0 && description == "" && detail == "" {
			failure.Message = fmt.Sprintf("OAuth refresh for '%s' failed: HTTP %d", module, status)
		}
	}
	return failure
}

func moduleOf(cfg *OAuth2Refresh) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Module)
}

func envOf(cfg *OAuth2Refresh) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.TokenEnv)
}

func refreshEnvOf(cfg *OAuth2Refresh) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.RefreshEnv)
}

func staticOnlyMessage(cfg *OAuth2Refresh) string {
	module := fallbackName(moduleOf(cfg), "credential")
	tokenEnv := fallbackName(envOf(cfg), "the static access token")
	return fmt.Sprintf(
		"Access token for '%s' from %s has expired and cannot be refreshed: no refresh token configured.",
		module, tokenEnv,
	)
}

// staticOAuthToken returns the token already in hand. Without a known expiry
// there is nothing to prove it stale, so it is returned unchanged; a known
// expiry in the past is an explicit failure naming the variable.
func staticOAuthToken(cfg *OAuth2Refresh, now time.Time) (string, error) {
	token := strings.TrimSpace(cfg.AccessToken)
	if token != "" && !tokenExpired(now, cfg.ExpiresAt) {
		return token, nil
	}
	if token == "" {
		return "", &OAuthRefreshError{
			Module:     moduleOf(cfg),
			Code:       "no_refresh_token",
			TokenEnv:   envOf(cfg),
			RefreshEnv: refreshEnvOf(cfg),
			Message: fmt.Sprintf(
				"Credentials for '%s' are not configured: set %s or %s.",
				fallbackName(moduleOf(cfg), "credential"),
				fallbackName(envOf(cfg), "the access token variable"),
				fallbackName(refreshEnvOf(cfg), "the refresh token variable"),
			),
		}
	}
	return "", &OAuthRefreshError{
		Module:     moduleOf(cfg),
		Code:       "no_refresh_token",
		TokenEnv:   envOf(cfg),
		RefreshEnv: refreshEnvOf(cfg),
		Message:    staticOnlyMessage(cfg),
	}
}

func tokenExpired(now time.Time, expiresAt *time.Time) bool {
	if expiresAt == nil {
		return false
	}
	return !now.Before(expiresAt.UTC().Add(-DefaultRefreshBuffer))
}

// refreshKey identifies one refresh grant. The hash keeps credentials out of
// map keys; rotating a refresh token starts a fresh entry.
func refreshKey(cfg *OAuth2Refresh) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(cfg.TokenURL),
		strings.TrimSpace(cfg.ClientID),
		strings.TrimSpace(cfg.RefreshToken),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// parseTokenPayload decodes a token response: JSON when possible, otherwise
// the form-encoded shape a few providers return (upstream
// BaseOAuth2Provider._parse_form_response).
func parseTokenPayload(raw []byte, contentType string) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if strings.Contains(strings.ToLower(contentType), "json") || strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
	values, err := url.ParseQuery(trimmed)
	if err != nil {
		return nil, err
	}
	payload = make(map[string]any, len(values))
	for name, list := range values {
		if len(list) == 1 {
			payload[name] = list[0]
		} else {
			payload[name] = list
		}
	}
	return payload, nil
}

// tokenErrorFields extracts the RFC 6749 error code and description.
func tokenErrorFields(payload map[string]any, raw []byte) (string, string) {
	code := strings.TrimSpace(scalarString(payload["error"]))
	if code == "" {
		return "", ""
	}
	description := strings.TrimSpace(scalarString(payload["error_description"]))
	if description == "" {
		description = strings.TrimSpace(string(raw))
		if len(description) > 200 {
			description = description[:200]
		}
	}
	return code, description
}

// tokenSeconds reads expires_in, which providers send as a number or string.
func tokenSeconds(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		seconds, err := typed.Float64()
		return seconds, err == nil
	case string:
		seconds, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return seconds, err == nil
	}
	return 0, false
}

// ParseTokenExpiry reads an expiry timestamp supplied through the environment.
// RFC 3339 is the documented form; bare unix seconds are accepted too.
func ParseTokenExpiry(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if at, err := time.Parse(time.RFC3339, trimmed); err == nil {
		utc := at.UTC()
		return &utc
	}
	if seconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		at := time.Unix(seconds, 0).UTC()
		return &at
	}
	return nil
}

// oauthSettings resolves the module's declared refresh source, applying the
// environment-default rules shared by every caller.
func (c *ModuleConfig) oauthSettings(getenv func(string) string) *OAuth2Refresh {
	if c == nil || c.OAuth == nil {
		return nil
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	settings := c.OAuth(getenv)
	if settings == nil {
		return nil
	}
	if strings.TrimSpace(settings.Module) == "" {
		settings.Module = c.ID
	}
	if strings.TrimSpace(settings.TokenEnv) == "" && len(c.EnvVars) > 0 {
		settings.TokenEnv = strings.Join(c.EnvVars, " or ")
	}
	return settings
}

func (rt *Runtime) moduleConfig(moduleID string) (*ModuleConfig, func(string) string) {
	getenv := os.Getenv
	if rt != nil && rt.Getenv != nil {
		getenv = rt.Getenv
	}
	if rt == nil || rt.Modules == nil {
		return nil, getenv
	}
	return rt.Modules[moduleID], getenv
}

// tokenRefresher returns the runtime's refresher, creating it over the shared
// transport on first use. Runtime.ForCall copies the pointer so every call of
// one registry shares the token cache and the single-flight bookkeeping.
func (rt *Runtime) tokenRefresher() *TokenRefresher {
	if rt == nil {
		return NewTokenRefresher(nil)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Tokens == nil {
		var client *http.Client
		if rt.HTTP != nil {
			client = rt.HTTP.Inner
		}
		rt.Tokens = NewTokenRefresher(client)
	}
	return rt.Tokens
}

// ModuleToken resolves a module credential for one call, refreshing it when
// the module declares OAuth. missing is non-empty only when no credential
// source resolved and no refresh token can mint one.
func (rt *Runtime) ModuleToken(ctx context.Context, cfg *ModuleConfig) (string, []string, error) {
	if cfg == nil {
		return "", nil, nil
	}
	_, getenv := rt.moduleConfig(cfg.ID)
	token, missing := cfg.ResolvedToken(getenv)
	token = strings.TrimSpace(token)
	settings := cfg.oauthSettings(getenv)
	if settings == nil {
		return token, missing, nil
	}
	settings.AccessToken = token
	if strings.TrimSpace(settings.RefreshToken) != "" {
		// A refresh token is a complete credential: the static access-token
		// variables need not be set.
		missing = nil
	}
	if token == "" && strings.TrimSpace(settings.RefreshToken) == "" {
		return "", missing, nil
	}
	refreshed, err := rt.tokenRefresher().Token(ctx, settings)
	if err != nil {
		return "", nil, err
	}
	return refreshed, nil, nil
}

// AccessToken resolves the module's access token for one call.
func (rt *Runtime) AccessToken(ctx context.Context, moduleID string) (string, error) {
	cfg, _ := rt.moduleConfig(moduleID)
	token, _, err := rt.ModuleToken(ctx, cfg)
	return token, err
}

// RefreshAccessToken forces a new OAuth2 access token (the 401 retry path).
func (rt *Runtime) RefreshAccessToken(ctx context.Context, moduleID string) (string, error) {
	cfg, getenv := rt.moduleConfig(moduleID)
	if cfg == nil {
		return "", nil
	}
	settings := cfg.oauthSettings(getenv)
	if settings == nil {
		return "", nil
	}
	if token, _ := cfg.ResolvedToken(getenv); strings.TrimSpace(settings.AccessToken) == "" {
		settings.AccessToken = strings.TrimSpace(token)
	}
	return rt.tokenRefresher().Refresh(ctx, settings)
}

// OAuthConfigured reports whether the module declares an OAuth2 refresh
// source at all (with or without a refresh token).
func (rt *Runtime) OAuthConfigured(moduleID string) bool {
	cfg, getenv := rt.moduleConfig(moduleID)
	return cfg != nil && cfg.oauthSettings(getenv) != nil
}

// OAuthRefreshable reports whether the module can mint a new token.
func (rt *Runtime) OAuthRefreshable(moduleID string) bool {
	cfg, getenv := rt.moduleConfig(moduleID)
	if cfg == nil {
		return false
	}
	settings := cfg.oauthSettings(getenv)
	return settings != nil && strings.TrimSpace(settings.RefreshToken) != ""
}

// UnauthorizedResolution is what a module should do after its provider
// returned 401: retry once with Token, or return Failure unchanged.
type UnauthorizedResolution struct {
	Token   string
	Retry   bool
	Failure map[string]any
}

// ResolveUnauthorized turns a 401 into either one refreshed retry or an
// explicit value error. A module with only a static access token gets
// no_refresh_token naming the variable that must be replaced.
func (rt *Runtime) ResolveUnauthorized(ctx context.Context, moduleID, display string) UnauthorizedResolution {
	cfg, getenv := rt.moduleConfig(moduleID)
	if cfg == nil {
		return UnauthorizedResolution{}
	}
	settings := cfg.oauthSettings(getenv)
	if settings == nil {
		return UnauthorizedResolution{}
	}
	if token, _ := cfg.ResolvedToken(getenv); strings.TrimSpace(settings.AccessToken) == "" {
		settings.AccessToken = strings.TrimSpace(token)
	}
	if strings.TrimSpace(settings.RefreshToken) == "" {
		failure := &OAuthRefreshError{
			Module:     settings.Module,
			Code:       "no_refresh_token",
			TokenEnv:   settings.TokenEnv,
			RefreshEnv: settings.RefreshEnv,
		}
		failure.Message = fmt.Sprintf(
			"%s access token from %s cannot be refreshed: no refresh token configured.",
			display, fallbackName(settings.TokenEnv, "the access-token variable"),
		)
		return UnauthorizedResolution{Failure: failure.ValueError()}
	}
	token, err := rt.tokenRefresher().Refresh(ctx, settings)
	if err != nil {
		var refreshErr *OAuthRefreshError
		if errors.As(err, &refreshErr) {
			return UnauthorizedResolution{Failure: refreshErr.ValueError()}
		}
		return UnauthorizedResolution{Failure: map[string]any{"error": err.Error()}}
	}
	return UnauthorizedResolution{Token: token, Retry: true}
}
