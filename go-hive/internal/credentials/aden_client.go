package credentials

// Parity: framework/credentials/aden/client.py
//
// HTTP client for the Aden authentication server. Aden holds OAuth secrets;
// runtime credential providers fetch short-lived access tokens internally.
// Agent-facing code selects accounts by alias instead.
//
// Endpoints (all authenticated with Bearer {api_key}):
//
//	GET  /v1/credentials                          — list integrations
//	GET  /v1/credentials/{integration_id}         — get access token (auto-refreshes)
//	POST /v1/credentials/{integration_id}/refresh — force refresh
//	GET  /v1/credentials/{integration_id}/validate — check validity
//
// Integration IDs are base64-encoded hashes assigned by the Aden platform,
// NOT provider names.
//
// Divergences (documented):
//   - Transport error text is Go-native (httpx exception strings do not exist
//     here); the "Failed to connect to Aden server: ..." wrapper matches.
//   - A malformed expires_at is left nil instead of failing the whole listing;
//     one bad row must not hide the rest of the team's integrations.
//   - Redirects are followed by net/http's default client policy, as httpx
//     does by default.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// AdenDefaultBaseURL is the canonical Aden host used when neither the
	// config nor ADEN_API_URL names one.
	// Parity: core/framework/credentials/store.py::CredentialStore._build_aden_backend
	AdenDefaultBaseURL = "https://app.open-hive.com"

	// AdenBaseURLEnvVar overrides the Aden base URL (local dev points it at
	// http://localhost:8889).
	AdenBaseURLEnvVar = "ADEN_API_URL"

	adenUserAgent = "hive-credential-store/1.0"

	adenDefaultTimeout       = 30 * time.Second
	adenDefaultRetryAttempts = 3
	adenDefaultRetryDelay    = time.Second
)

// AdenClientConfig configures an AdenCredentialClient. The zero value is
// usable when ADEN_API_KEY is set: BaseURL falls back to ADEN_API_URL and then
// AdenDefaultBaseURL, Timeout to 30s, RetryAttempts to 3 and RetryDelay to 1s.
// Parity: framework/credentials/aden/client.py::AdenClientConfig
type AdenClientConfig struct {
	BaseURL string
	// APIKey is the agent API key. When empty, ADEN_API_KEY is read.
	APIKey string
	// TenantID is optional; when set it is sent as X-Tenant-ID.
	TenantID string
	// Timeout bounds one HTTP round trip.
	Timeout time.Duration
	// RetryAttempts is the total number of attempts for connection failures
	// and timeouts (not HTTP status errors).
	RetryAttempts int
	// RetryDelay is the base exponential-backoff delay.
	RetryDelay time.Duration
}

// AdenConfigError reports a configuration problem before any request is made.
type AdenConfigError struct{ Message string }

func (e *AdenConfigError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// AdenClientError is the base error for Aden request failures. StatusCode is
// the HTTP status when the failure came from a response, 0 otherwise.
// Parity: framework/credentials/aden/client.py::AdenClientError
type AdenClientError struct {
	Message    string
	StatusCode int
}

func (e *AdenClientError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// AdenAuthenticationError reports an invalid or revoked agent API key (401).
// Parity: framework/credentials/aden/client.py::AdenAuthenticationError
type AdenAuthenticationError struct{ *AdenClientError }

func (e *AdenAuthenticationError) Unwrap() error { return e.AdenClientError }

// AdenNotFoundError reports a missing integration (404).
// Parity: framework/credentials/aden/client.py::AdenNotFoundError
type AdenNotFoundError struct{ *AdenClientError }

func (e *AdenNotFoundError) Unwrap() error { return e.AdenClientError }

// AdenRefreshError reports a failed token refresh (400). When
// RequiresReauthorization is set, a human must reconnect the integration at
// ReauthorizationURL.
// Parity: framework/credentials/aden/client.py::AdenRefreshError
type AdenRefreshError struct {
	*AdenClientError
	RequiresReauthorization bool
	ReauthorizationURL      string
}

func (e *AdenRefreshError) Unwrap() error { return e.AdenClientError }

// AdenRateLimitError reports a 429 with the server's Retry-After hint (60
// seconds when the header is missing or unparsable).
// Parity: framework/credentials/aden/client.py::AdenRateLimitError
type AdenRateLimitError struct {
	*AdenClientError
	RetryAfter int
}

func (e *AdenRateLimitError) Unwrap() error { return e.AdenClientError }

// AdenIntegrationInfo is one integration from GET /v1/credentials.
// Parity: framework/credentials/aden/client.py::AdenIntegrationInfo
type AdenIntegrationInfo struct {
	// IntegrationID is the base64-encoded hash assigned by Aden.
	IntegrationID string     `json:"integration_id"`
	Provider      string     `json:"provider"`
	Alias         string     `json:"alias"`
	Status        string     `json:"status"`
	Email         string     `json:"email"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// IntegrationType is the backward-compatible name upstream's old code reads.
// Parity: framework/credentials/aden/client.py::AdenIntegrationInfo.integration_type
func (i AdenIntegrationInfo) IntegrationType() string { return i.Provider }

// AdenCredentialResponse is the response from GET /v1/credentials/{id} and
// POST /v1/credentials/{id}/refresh.
// Parity: framework/credentials/aden/client.py::AdenCredentialResponse
type AdenCredentialResponse struct {
	// IntegrationID is the id used in the request.
	IntegrationID string `json:"integration_id,omitempty"`
	// AccessToken is the short-lived token for provider API calls.
	AccessToken string         `json:"access_token"`
	TokenType   string         `json:"token_type"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
	Provider    string         `json:"provider"`
	Alias       string         `json:"alias"`
	Email       string         `json:"email"`
	Scopes      []string       `json:"scopes"`
	Metadata    map[string]any `json:"metadata"`
}

// IntegrationType is the backward-compatible name upstream's old code reads.
// Parity: framework/credentials/aden/client.py::AdenCredentialResponse.integration_type
func (r AdenCredentialResponse) IntegrationType() string { return r.Provider }

// AdenValidateResult is the typed shape of GET
// /v1/credentials/{id}/validate. ValidateToken itself returns the generic
// map to keep unknown server fields visible.
// Parity: framework/credentials/aden/client.py::validate_token
type AdenValidateResult struct {
	Valid     bool    `json:"valid"`
	Status    string  `json:"status"`
	ExpiresAt string  `json:"expires_at"`
	Error     *string `json:"error"`
}

// adenDecodeJSON tolerates a UTF-8 BOM, matching upstream's
// response.content.decode("utf-8-sig").
func adenDecodeJSON(data []byte, target any) error {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("empty response body")
	}
	return json.Unmarshal(data, target)
}

func adenDecodeMap(data []byte) (map[string]any, error) {
	var out map[string]any
	if err := adenDecodeJSON(data, &out); err != nil {
		return nil, &AdenClientError{Message: "Failed to parse Aden response: " + err.Error()}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func adenMapString(data map[string]any, key, fallback string) string {
	value, present := data[key]
	if !present || value == nil {
		return fallback
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fallback
}

// adenParseTime mirrors datetime.fromisoformat(value.replace("Z", "+00:00")).
// Unparsable values degrade to nil instead of failing the whole response.
func adenParseTime(value any) *time.Time {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z0700",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return &parsed
		}
	}
	return nil
}

// AdenIntegrationInfoFromDict projects one API item onto AdenIntegrationInfo.
// Parity: framework/credentials/aden/client.py::AdenIntegrationInfo.from_dict
func AdenIntegrationInfoFromDict(data map[string]any) AdenIntegrationInfo {
	status := "unknown"
	if value, present := data["status"]; present {
		if text, ok := value.(string); ok {
			status = text
		}
	}
	return AdenIntegrationInfo{
		IntegrationID: adenMapString(data, "integration_id", ""),
		Provider:      adenMapString(data, "provider", ""),
		Alias:         adenMapString(data, "alias", ""),
		Status:        status,
		Email:         adenMapString(data, "email", ""),
		ExpiresAt:     adenParseTime(data["expires_at"]),
	}
}

// AdenCredentialResponseFromDict projects a get-token/refresh response onto
// AdenCredentialResponse. access_token is required, as upstream's
// data["access_token"].
// Parity: framework/credentials/aden/client.py::AdenCredentialResponse.from_dict
func AdenCredentialResponseFromDict(data map[string]any, integrationID string) (AdenCredentialResponse, error) {
	accessToken, ok := data["access_token"].(string)
	if !ok {
		return AdenCredentialResponse{}, &AdenClientError{Message: "Aden credential response missing access_token"}
	}
	if integrationID == "" {
		integrationID = adenMapString(data, "integration_id", "")
	}

	email := adenMapString(data, "email", "")
	metadata := map[string]any{}
	if raw, isMap := data["metadata"].(map[string]any); isMap && len(raw) > 0 {
		metadata = raw
	}
	if len(metadata) == 0 && email != "" {
		metadata = map[string]any{"email": email}
	}

	scopes := []string{}
	switch raw := data["scopes"].(type) {
	case []string:
		scopes = append(scopes, raw...)
	case []any:
		for _, item := range raw {
			if scope, isString := item.(string); isString {
				scopes = append(scopes, scope)
			}
		}
	}

	return AdenCredentialResponse{
		IntegrationID: integrationID,
		AccessToken:   accessToken,
		TokenType:     adenMapString(data, "token_type", "Bearer"),
		ExpiresAt:     adenParseTime(data["expires_at"]),
		Provider:      adenMapString(data, "provider", ""),
		Alias:         adenMapString(data, "alias", ""),
		Email:         email,
		Scopes:        scopes,
		Metadata:      metadata,
	}, nil
}

// AdenValidateResultFromMap projects the validate response map onto the typed
// shape. Unknown fields stay in the map returned by ValidateToken.
// Parity: framework/credentials/aden/client.py::validate_token
func AdenValidateResultFromMap(data map[string]any) AdenValidateResult {
	result := AdenValidateResult{
		Status:    adenMapString(data, "status", ""),
		ExpiresAt: adenMapString(data, "expires_at", ""),
	}
	if valid, ok := data["valid"].(bool); ok {
		result.Valid = valid
	}
	if text, ok := data["error"].(string); ok {
		result.Error = &text
	}
	return result
}

// ToMap renders the result back into the API shape (error: null when unset).
func (r AdenValidateResult) ToMap() map[string]any {
	var errValue any
	if r.Error != nil {
		errValue = *r.Error
	}
	return map[string]any{
		"valid":      r.Valid,
		"status":     r.Status,
		"expires_at": r.ExpiresAt,
		"error":      errValue,
	}
}

// AdenHTTPResponse is one raw, authenticated response from the Aden server.
type AdenHTTPResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// Latency measures time to response headers, matching httpx's
	// response.elapsed.
	Latency time.Duration
}

// AdenCredentialClient is the HTTP client for the Aden credential server.
// It is safe for concurrent use.
// Parity: framework/credentials/aden/client.py::AdenCredentialClient
type AdenCredentialClient struct {
	config     AdenClientConfig
	httpClient *http.Client
	headers    http.Header
}

// NewAdenCredentialClient validates and resolves config, then builds a client.
// An empty APIKey reads ADEN_API_KEY; an empty BaseURL reads ADEN_API_URL and
// falls back to AdenDefaultBaseURL. The API key is never included in errors or
// logs.
// Parity: framework/credentials/aden/client.py::AdenClientConfig.__post_init__
func NewAdenCredentialClient(config AdenClientConfig) (*AdenCredentialClient, error) {
	if config.APIKey == "" {
		config.APIKey = os.Getenv(AdenEnvVar)
	}
	if config.APIKey == "" {
		return nil, &AdenConfigError{Message: "Aden API key not provided. Either pass APIKey to AdenClientConfig or set the ADEN_API_KEY environment variable."}
	}

	if config.BaseURL == "" {
		config.BaseURL = os.Getenv(AdenBaseURLEnvVar)
	}
	if config.BaseURL == "" {
		config.BaseURL = AdenDefaultBaseURL
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")

	if config.Timeout <= 0 {
		config.Timeout = adenDefaultTimeout
	}
	if config.RetryAttempts <= 0 {
		config.RetryAttempts = adenDefaultRetryAttempts
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = adenDefaultRetryDelay
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+config.APIKey)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", adenUserAgent)
	if config.TenantID != "" {
		headers.Set("X-Tenant-ID", config.TenantID)
	}

	return &AdenCredentialClient{
		config:     config,
		httpClient: &http.Client{Timeout: config.Timeout},
		headers:    headers,
	}, nil
}

// NewAdenClientFromEnv builds a client from ADEN_API_KEY and ADEN_API_URL.
// This is the seam a future token pre-sync uses; it deliberately does not
// touch the credential store or validate agent credentials.
func NewAdenClientFromEnv() (*AdenCredentialClient, error) {
	return NewAdenCredentialClient(AdenClientConfig{})
}

// Config returns the resolved configuration. It never exposes request headers.
func (c *AdenCredentialClient) Config() AdenClientConfig {
	if c == nil {
		return AdenClientConfig{}
	}
	return c.config
}

func (c *AdenCredentialClient) endpoint(path string) string {
	if path == "" {
		return c.config.BaseURL
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(c.config.BaseURL, "/") + path
}

// Do performs one authenticated request with connection/timeout retries and
// returns the raw response. HTTP statuses are not mapped to errors here; the
// typed methods call adenStatusError so callers can choose how to treat 404.
func (c *AdenCredentialClient) Do(ctx context.Context, method, path string) (*AdenHTTPResponse, error) {
	if c == nil || c.httpClient == nil {
		return nil, &AdenClientError{Message: "Aden client is not initialized"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := c.config.RetryAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		response, err := c.doOnce(ctx, method, path)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		if !adenRetryableError(err) {
			return nil, &AdenClientError{Message: "Failed to connect to Aden server: " + err.Error()}
		}
		if attempt < attempts-1 {
			delay := c.config.RetryDelay * time.Duration(1<<uint(attempt))
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				lastErr = ctx.Err()
				return nil, &AdenClientError{Message: "Failed to connect to Aden server: " + lastErr.Error()}
			}
		}
	}
	return nil, &AdenClientError{Message: "Failed to connect to Aden server: " + lastErr.Error()}
}

// adenRetryableError mirrors upstream's retry classification: connection
// failures and timeouts retry, everything else (bad URL, read errors, context
// cancellation) fails immediately.
func adenRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (c *AdenCredentialClient) doOnce(ctx context.Context, method, path string) (*AdenHTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), nil)
	if err != nil {
		return nil, err
	}
	for key, values := range c.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	start := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	latency := time.Since(start)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return &AdenHTTPResponse{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       body,
		Latency:    latency,
	}, nil
}

// adenStatusError maps an HTTP status onto the Aden error types.
// Parity: framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func adenStatusError(response *AdenHTTPResponse, path string) error {
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return &AdenAuthenticationError{AdenClientError: &AdenClientError{
			Message:    "Agent API key is invalid or revoked",
			StatusCode: response.StatusCode,
		}}
	case http.StatusForbidden:
		message := "Forbidden"
		if data, err := adenDecodeMap(response.Body); err == nil {
			if text, ok := data["message"].(string); ok && text != "" {
				message = text
			}
		}
		return &AdenClientError{Message: message, StatusCode: response.StatusCode}
	case http.StatusNotFound:
		return &AdenNotFoundError{AdenClientError: &AdenClientError{
			Message:    "Integration not found: " + path,
			StatusCode: response.StatusCode,
		}}
	case http.StatusTooManyRequests:
		retryAfter := 60
		if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				retryAfter = parsed
			}
		}
		return &AdenRateLimitError{
			AdenClientError: &AdenClientError{Message: "Rate limited by Aden server", StatusCode: response.StatusCode},
			RetryAfter:      retryAfter,
		}
	case http.StatusBadRequest:
		data, err := adenDecodeMap(response.Body)
		if err != nil {
			return &AdenClientError{Message: "Bad request: " + err.Error(), StatusCode: response.StatusCode}
		}
		message := adenMapString(data, "message", "Bad request")
		serverError := adenMapString(data, "error", "")
		if serverError == "refresh_failed" || strings.Contains(strings.ToLower(message), "refresh") {
			refresh := &AdenRefreshError{AdenClientError: &AdenClientError{
				Message:    message,
				StatusCode: response.StatusCode,
			}}
			if value, ok := data["requires_reauthorization"].(bool); ok {
				refresh.RequiresReauthorization = value
			}
			refresh.ReauthorizationURL = adenMapString(data, "reauthorization_url", "")
			return refresh
		}
		return &AdenClientError{Message: "Bad request: " + message, StatusCode: response.StatusCode}
	}

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return &AdenClientError{
		Message:    fmt.Sprintf("Aden request failed: HTTP %d", response.StatusCode),
		StatusCode: response.StatusCode,
	}
}

// ListIntegrations lists all integrations for this agent's team.
// GET /v1/credentials → {"integrations": [...]}
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.list_integrations
func (c *AdenCredentialClient) ListIntegrations() ([]AdenIntegrationInfo, error) {
	const path = "/v1/credentials"
	response, err := c.Do(context.Background(), http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	if err := adenStatusError(response, path); err != nil {
		return nil, err
	}
	data, err := adenDecodeMap(response.Body)
	if err != nil {
		return nil, err
	}
	raw, _ := data["integrations"].([]any)
	integrations := make([]AdenIntegrationInfo, 0, len(raw))
	for _, item := range raw {
		if dict, ok := item.(map[string]any); ok {
			integrations = append(integrations, AdenIntegrationInfoFromDict(dict))
		}
	}
	return integrations, nil
}

// ListConnections is the upstream backward-compatible alias.
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.list_connections
func (c *AdenCredentialClient) ListConnections() ([]AdenIntegrationInfo, error) {
	return c.ListIntegrations()
}

// GetCredential gets the access token for an integration, or nil when Aden
// reports 404.
// GET /v1/credentials/{integration_id}
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.get_credential
func (c *AdenCredentialClient) GetCredential(integrationID string) (*AdenCredentialResponse, error) {
	path := "/v1/credentials/" + integrationID
	response, err := c.Do(context.Background(), http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	if statusErr := adenStatusError(response, path); statusErr != nil {
		var notFound *AdenNotFoundError
		if errors.As(statusErr, &notFound) {
			return nil, nil
		}
		return nil, statusErr
	}
	data, err := adenDecodeMap(response.Body)
	if err != nil {
		return nil, err
	}
	credential, err := AdenCredentialResponseFromDict(data, integrationID)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

// RequestRefresh forces a token refresh.
// POST /v1/credentials/{integration_id}/refresh
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.request_refresh
func (c *AdenCredentialClient) RequestRefresh(integrationID string) (*AdenCredentialResponse, error) {
	path := "/v1/credentials/" + integrationID + "/refresh"
	response, err := c.Do(context.Background(), http.MethodPost, path)
	if err != nil {
		return nil, err
	}
	if err := adenStatusError(response, path); err != nil {
		return nil, err
	}
	data, err := adenDecodeMap(response.Body)
	if err != nil {
		return nil, err
	}
	credential, err := AdenCredentialResponseFromDict(data, integrationID)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

// ValidateToken checks whether an integration's OAuth connection is valid.
// GET /v1/credentials/{integration_id}/validate
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.validate_token
func (c *AdenCredentialClient) ValidateToken(integrationID string) (map[string]any, error) {
	path := "/v1/credentials/" + integrationID + "/validate"
	response, err := c.Do(context.Background(), http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	if err := adenStatusError(response, path); err != nil {
		return nil, err
	}
	return adenDecodeMap(response.Body)
}

// HealthCheck checks Aden server health and never returns an error. A healthy
// response keeps the server's fields and adds latency_ms; non-200 responses
// are "degraded"; transport failures are "unhealthy".
// Parity: framework/credentials/aden/client.py::AdenCredentialClient.health_check
func (c *AdenCredentialClient) HealthCheck() map[string]any {
	if c == nil || c.httpClient == nil {
		return map[string]any{"status": "unhealthy", "error": "Aden client is not initialized"}
	}
	response, err := c.doOnce(context.Background(), http.MethodGet, "/health")
	if err != nil {
		return map[string]any{"status": "unhealthy", "error": err.Error()}
	}
	if response.StatusCode != http.StatusOK {
		return map[string]any{"status": "degraded", "error": fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	data, err := adenDecodeMap(response.Body)
	if err != nil {
		return map[string]any{"status": "unhealthy", "error": err.Error()}
	}
	data["latency_ms"] = float64(response.Latency) / float64(time.Millisecond)
	return data
}

// Close releases idle HTTP connections. It is idempotent and, like upstream's
// close(), does not prevent later requests.
func (c *AdenCredentialClient) Close() error {
	if c == nil || c.httpClient == nil {
		return nil
	}
	c.httpClient.CloseIdleConnections()
	return nil
}
