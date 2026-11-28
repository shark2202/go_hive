package credentials

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newAdenTestClient(t *testing.T, serverURL, tenantID string) *AdenCredentialClient {
	t.Helper()
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       serverURL,
		APIKey:        "test-api-key",
		TenantID:      tenantID,
		Timeout:       2 * time.Second,
		RetryAttempts: 2,
		RetryDelay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func adenWriteJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

type adenRoundTripFunc func(*http.Request) (*http.Response, error)

func (f adenRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCredentialResponse::test_from_dict_basic
func TestAdenCredentialResponseFromDictBasic(t *testing.T) {
	data := map[string]any{
		"access_token": "ghp_xxxxx",
		"token_type":   "Bearer",
		"provider":     "github",
		"alias":        "Work",
	}

	response, err := AdenCredentialResponseFromDict(data, "Z2l0aHViOldvcms6MTIzNDU")
	if err != nil {
		t.Fatalf("AdenCredentialResponseFromDict: %v", err)
	}
	if response.IntegrationID != "Z2l0aHViOldvcms6MTIzNDU" {
		t.Fatalf("integration id = %q", response.IntegrationID)
	}
	if response.AccessToken != "ghp_xxxxx" {
		t.Fatalf("access token = %q", response.AccessToken)
	}
	if response.Provider != "github" || response.IntegrationType() != "github" {
		t.Fatalf("provider = %q integration type = %q", response.Provider, response.IntegrationType())
	}
	if response.TokenType != "Bearer" {
		t.Fatalf("token type = %q", response.TokenType)
	}
	if response.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", response.ExpiresAt)
	}
	if response.Scopes == nil || len(response.Scopes) != 0 {
		t.Fatalf("scopes = %#v, want empty slice", response.Scopes)
	}
	if response.Metadata == nil || len(response.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty map", response.Metadata)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCredentialResponse::test_from_dict_full
func TestAdenCredentialResponseFromDictFull(t *testing.T) {
	data := map[string]any{
		"access_token": "token123",
		"token_type":   "Bearer",
		"expires_at":   "2026-01-28T15:30:00Z",
		"provider":     "hubspot",
		"alias":        "My HubSpot",
		"email":        "test@example.com",
		"scopes":       []any{"read", "write"},
		"metadata":     map[string]any{"key": "value"},
	}

	response, err := AdenCredentialResponseFromDict(data, "aHVic3BvdDp0ZXN0")
	if err != nil {
		t.Fatalf("AdenCredentialResponseFromDict: %v", err)
	}
	if response.IntegrationID != "aHVic3BvdDp0ZXN0" {
		t.Fatalf("integration id = %q", response.IntegrationID)
	}
	if response.AccessToken != "token123" || response.Provider != "hubspot" {
		t.Fatalf("response = %#v", response)
	}
	if response.Alias != "My HubSpot" || response.Email != "test@example.com" {
		t.Fatalf("alias/email = %q/%q", response.Alias, response.Email)
	}
	if response.ExpiresAt == nil {
		t.Fatal("expires_at = nil, want parsed time")
	}
	if !response.ExpiresAt.Equal(time.Date(2026, 1, 28, 15, 30, 0, 0, time.UTC)) {
		t.Fatalf("expires_at = %v", response.ExpiresAt)
	}
	if len(response.Scopes) != 2 || response.Scopes[0] != "read" || response.Scopes[1] != "write" {
		t.Fatalf("scopes = %#v", response.Scopes)
	}
	if response.Metadata["key"] != "value" {
		t.Fatalf("metadata = %#v", response.Metadata)
	}
}

// TestAdenCredentialResponseMetadataFallsBackToEmail covers upstream's
// metadata = data.get("metadata") or {} then {"email": email}.
// Parity: core/framework/credentials/aden/client.py::AdenCredentialResponse.from_dict
func TestAdenCredentialResponseMetadataFallsBackToEmail(t *testing.T) {
	response, err := AdenCredentialResponseFromDict(map[string]any{
		"access_token": "token",
		"email":        "test@example.com",
	}, "")
	if err != nil {
		t.Fatalf("AdenCredentialResponseFromDict: %v", err)
	}
	if response.Metadata["email"] != "test@example.com" {
		t.Fatalf("metadata = %#v, want email identity", response.Metadata)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenIntegrationInfo::test_from_dict
func TestAdenIntegrationInfoFromDict(t *testing.T) {
	info := AdenIntegrationInfoFromDict(map[string]any{
		"integration_id": "c2xhY2s6V29yayBTbGFjazoxMjM0NQ",
		"provider":       "slack",
		"alias":          "Work Slack",
		"status":         "active",
		"email":          "user@example.com",
		"expires_at":     "2026-02-20T21:46:04.863Z",
	})

	if info.IntegrationID != "c2xhY2s6V29yayBTbGFjazoxMjM0NQ" {
		t.Fatalf("integration id = %q", info.IntegrationID)
	}
	if info.Provider != "slack" || info.IntegrationType() != "slack" {
		t.Fatalf("provider = %q integration type = %q", info.Provider, info.IntegrationType())
	}
	if info.Alias != "Work Slack" || info.Email != "user@example.com" || info.Status != "active" {
		t.Fatalf("info = %#v", info)
	}
	if info.ExpiresAt == nil {
		t.Fatal("expires_at = nil, want parsed time")
	}
	if !info.ExpiresAt.Equal(time.Date(2026, 2, 20, 21, 46, 4, 863_000_000, time.UTC)) {
		t.Fatalf("expires_at = %v", info.ExpiresAt)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenIntegrationInfo::test_from_dict_minimal
func TestAdenIntegrationInfoFromDictMinimal(t *testing.T) {
	info := AdenIntegrationInfoFromDict(map[string]any{
		"integration_id": "Z29vZ2xlOlRpbW90aHk6MTYwNjc",
		"provider":       "google",
		"alias":          "Timothy",
		"status":         "requires_reauth",
	})

	if info.Email != "" {
		t.Fatalf("email = %q, want empty", info.Email)
	}
	if info.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", info.ExpiresAt)
	}
	if info.Status != "requires_reauth" {
		t.Fatalf("status = %q", info.Status)
	}
}

// Parity-exempt: Python from_dict defaults status to "unknown" but no Python test
// asserts the default; this pins the Go behavior.
func TestAdenIntegrationInfoFromDictUnknownStatus(t *testing.T) {
	info := AdenIntegrationInfoFromDict(map[string]any{"integration_id": "abc"})
	if info.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", info.Status)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenClientConfig.__post_init__
func TestAdenClientConfigMissingAPIKey(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	t.Setenv(AdenBaseURLEnvVar, "")

	client, err := NewAdenClientFromEnv()
	if client != nil {
		t.Fatal("client = non-nil with no API key")
	}
	var configErr *AdenConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %v, want *AdenConfigError", err)
	}
	if !strings.Contains(err.Error(), "ADEN_API_KEY") {
		t.Fatalf("error = %q, want ADEN_API_KEY guidance", err.Error())
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenClientConfig.__post_init__
func TestAdenClientConfigReadsEnv(t *testing.T) {
	t.Setenv(AdenEnvVar, "env-key")
	t.Setenv(AdenBaseURLEnvVar, "http://localhost:8889")

	client, err := NewAdenClientFromEnv()
	if err != nil {
		t.Fatalf("NewAdenClientFromEnv: %v", err)
	}
	defer client.Close()

	config := client.Config()
	if config.APIKey != "env-key" {
		t.Fatalf("api key = %q", config.APIKey)
	}
	if config.BaseURL != "http://localhost:8889" {
		t.Fatalf("base url = %q", config.BaseURL)
	}
}

// Parity: core/framework/credentials/store.py::CredentialStore._build_aden_backend
func TestAdenClientConfigDefaultBaseURLAndDefaults(t *testing.T) {
	t.Setenv(AdenEnvVar, "env-key")
	t.Setenv(AdenBaseURLEnvVar, "")

	client, err := NewAdenClientFromEnv()
	if err != nil {
		t.Fatalf("NewAdenClientFromEnv: %v", err)
	}
	defer client.Close()

	config := client.Config()
	if config.BaseURL != AdenDefaultBaseURL {
		t.Fatalf("base url = %q, want %q", config.BaseURL, AdenDefaultBaseURL)
	}
	if config.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v", config.Timeout)
	}
	if config.RetryAttempts != 3 {
		t.Fatalf("retry attempts = %d", config.RetryAttempts)
	}
	if config.RetryDelay != time.Second {
		t.Fatalf("retry delay = %v", config.RetryDelay)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.list_integrations
func TestAdenListIntegrations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/credentials" {
			t.Errorf("path = %q, want /v1/credentials", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "hive-credential-store/1.0" {
			t.Errorf("user agent = %q", got)
		}
		if got := r.Header.Get("X-Tenant-ID"); got != "test-tenant" {
			t.Errorf("tenant header = %q", got)
		}
		adenWriteJSON(w, http.StatusOK, `{"integrations":[
			{"integration_id":"Z29vZ2xlOlRpbW90aHk6MTYwNjc6MTM2ODQ","provider":"google","alias":"Timothy","status":"active","email":"timothy@acho.io","expires_at":"2026-02-20T21:46:04.863Z"},
			{"integration_id":"c2xhY2s6dGVzdDo5OTk=","provider":"slack","alias":"Work Slack"}
		]}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL+"/", "test-tenant")
	integrations, err := client.ListIntegrations()
	if err != nil {
		t.Fatalf("ListIntegrations: %v", err)
	}
	if len(integrations) != 2 {
		t.Fatalf("integrations = %d, want 2", len(integrations))
	}
	if integrations[0].Provider != "google" || integrations[0].Alias != "Timothy" || integrations[0].Status != "active" {
		t.Fatalf("first integration = %#v", integrations[0])
	}
	if integrations[0].ExpiresAt == nil {
		t.Fatal("first integration expires_at = nil")
	}
	if integrations[1].Status != "unknown" || integrations[1].Email != "" || integrations[1].ExpiresAt != nil {
		t.Fatalf("second integration = %#v", integrations[1])
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._parse_json
func TestAdenListIntegrationsToleratesBOM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("\xEF\xBB\xBF" + `{"integrations":[{"integration_id":"abc","provider":"slack","alias":"Work","status":"active"}]}`))
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	integrations, err := client.ListIntegrations()
	if err != nil {
		t.Fatalf("ListIntegrations with BOM: %v", err)
	}
	if len(integrations) != 1 || integrations[0].IntegrationID != "abc" {
		t.Fatalf("integrations = %#v", integrations)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.get_credential
func TestAdenGetCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/credentials/aHVic3BvdDp0ZXN0OjEzNjExOjExNTI1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-Tenant-ID"); got != "" {
			t.Errorf("tenant header = %q, want empty", got)
		}
		adenWriteJSON(w, http.StatusOK, `{
			"access_token":"token123",
			"token_type":"Bearer",
			"expires_at":"2026-01-28T15:30:00Z",
			"provider":"hubspot",
			"alias":"My HubSpot",
			"email":"test@example.com",
			"scopes":["read","write"],
			"metadata":{"portal_id":"12345"}
		}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	credential, err := client.GetCredential("aHVic3BvdDp0ZXN0OjEzNjExOjExNTI1")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if credential == nil {
		t.Fatal("credential = nil")
	}
	if credential.AccessToken != "token123" || credential.TokenType != "Bearer" {
		t.Fatalf("credential = %#v", credential)
	}
	if credential.IntegrationID != "aHVic3BvdDp0ZXN0OjEzNjExOjExNTI1" {
		t.Fatalf("integration id = %q", credential.IntegrationID)
	}
	if credential.Metadata["portal_id"] != "12345" {
		t.Fatalf("metadata = %#v", credential.Metadata)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.get_credential
func TestAdenGetCredentialNotFoundReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusNotFound, `{"message":"not found"}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	credential, err := client.GetCredential("missing")
	if err != nil {
		t.Fatalf("GetCredential error = %v, want nil", err)
	}
	if credential != nil {
		t.Fatalf("credential = %#v, want nil", credential)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.request_refresh
func TestAdenRequestRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/credentials/aHVic3BvdDp0ZXN0/refresh" {
			t.Errorf("path = %q", r.URL.Path)
		}
		adenWriteJSON(w, http.StatusOK, `{
			"access_token":"refreshed-token",
			"token_type":"Bearer",
			"expires_at":"2026-02-20T12:00:00Z",
			"provider":"hubspot",
			"alias":"My HubSpot",
			"scopes":[]
		}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	credential, err := client.RequestRefresh("aHVic3BvdDp0ZXN0")
	if err != nil {
		t.Fatalf("RequestRefresh: %v", err)
	}
	if credential.AccessToken != "refreshed-token" {
		t.Fatalf("access token = %q", credential.AccessToken)
	}
	if credential.IntegrationID != "aHVic3BvdDp0ZXN0" {
		t.Fatalf("integration id = %q", credential.IntegrationID)
	}
	if credential.ExpiresAt == nil {
		t.Fatal("expires_at = nil")
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.validate_token
func TestAdenValidateToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/credentials/aHVic3BvdDp0ZXN0/validate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		adenWriteJSON(w, http.StatusOK, `{"valid":true,"status":"active","expires_at":"2026-02-20T21:46:04.863Z","error":null}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	result, err := client.ValidateToken("aHVic3BvdDp0ZXN0")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if valid, ok := result["valid"].(bool); !ok || !valid {
		t.Fatalf("result = %#v", result)
	}
	typed := AdenValidateResultFromMap(result)
	if !typed.Valid || typed.Status != "active" || typed.Error != nil {
		t.Fatalf("typed result = %#v", typed)
	}
	roundTrip := typed.ToMap()
	if roundTrip["valid"] != true || roundTrip["error"] != nil {
		t.Fatalf("ToMap = %#v", roundTrip)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.health_check
func TestAdenHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("authorization = %q", got)
		}
		adenWriteJSON(w, http.StatusOK, `{"status":"ok","version":"1.0"}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	result := client.HealthCheck()
	if result["status"] != "ok" || result["version"] != "1.0" {
		t.Fatalf("health = %#v", result)
	}
	latency, ok := result["latency_ms"].(float64)
	if !ok || latency < 0 {
		t.Fatalf("latency_ms = %#v", result["latency_ms"])
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.health_check
func TestAdenHealthCheckDegraded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusServiceUnavailable, `{}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	result := client.HealthCheck()
	if result["status"] != "degraded" || result["error"] != "HTTP 503" {
		t.Fatalf("health = %#v", result)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.health_check
func TestAdenHealthCheckUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close()

	client := newAdenTestClient(t, serverURL, "")
	result := client.HealthCheck()
	if result["status"] != "unhealthy" {
		t.Fatalf("health = %#v", result)
	}
	if message, _ := result["error"].(string); message == "" {
		t.Fatalf("health error = %#v", result["error"])
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenAuthenticationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusUnauthorized, `{"message":"unauthorized"}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	_, err := client.ListIntegrations()

	var authErr *AdenAuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want *AdenAuthenticationError", err)
	}
	var baseErr *AdenClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("error = %v, want unwrap to *AdenClientError", err)
	}
	if err.Error() != "Agent API key is invalid or revoked" {
		t.Fatalf("message = %q", err.Error())
	}
	if strings.Contains(err.Error(), "test-api-key") {
		t.Fatalf("error leaked the API key: %q", err.Error())
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRequestRefreshNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusNotFound, `{}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	_, err := client.RequestRefresh("missing")

	var notFound *AdenNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want *AdenNotFoundError", err)
	}
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", notFound.StatusCode)
	}
	if !strings.Contains(err.Error(), "Integration not found: /v1/credentials/missing/refresh") {
		t.Fatalf("message = %q", err.Error())
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenForbiddenErrorUsesServerMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusForbidden, `{"message":"Tenant mismatch"}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	_, err := client.ListIntegrations()

	var baseErr *AdenClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("error = %v, want *AdenClientError", err)
	}
	if err.Error() != "Tenant mismatch" || baseErr.StatusCode != http.StatusForbidden {
		t.Fatalf("error = %q status = %d", err.Error(), baseErr.StatusCode)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRateLimitError(t *testing.T) {
	t.Run("retry-after header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			adenWriteJSON(w, http.StatusTooManyRequests, `{}`)
		}))
		defer server.Close()

		client := newAdenTestClient(t, server.URL, "")
		_, err := client.ListIntegrations()

		var rateErr *AdenRateLimitError
		if !errors.As(err, &rateErr) {
			t.Fatalf("error = %v, want *AdenRateLimitError", err)
		}
		if rateErr.RetryAfter != 7 {
			t.Fatalf("retry after = %d, want 7", rateErr.RetryAfter)
		}
		if err.Error() != "Rate limited by Aden server" {
			t.Fatalf("message = %q", err.Error())
		}
	})

	t.Run("retry-after default", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adenWriteJSON(w, http.StatusTooManyRequests, `{}`)
		}))
		defer server.Close()

		client := newAdenTestClient(t, server.URL, "")
		_, err := client.ListIntegrations()

		var rateErr *AdenRateLimitError
		if !errors.As(err, &rateErr) {
			t.Fatalf("error = %v, want *AdenRateLimitError", err)
		}
		if rateErr.RetryAfter != 60 {
			t.Fatalf("retry after = %d, want 60", rateErr.RetryAfter)
		}
	})
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRefreshError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusBadRequest, `{
			"error":"refresh_failed",
			"message":"Refresh token revoked",
			"requires_reauthorization":true,
			"reauthorization_url":"https://app.open-hive.com/reauth"
		}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	_, err := client.RequestRefresh("aHVic3BvdDp0ZXN0")

	var refreshErr *AdenRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("error = %v, want *AdenRefreshError", err)
	}
	if !refreshErr.RequiresReauthorization {
		t.Fatal("requires_reauthorization = false")
	}
	if refreshErr.ReauthorizationURL != "https://app.open-hive.com/reauth" {
		t.Fatalf("reauthorization url = %q", refreshErr.ReauthorizationURL)
	}
	if err.Error() != "Refresh token revoked" {
		t.Fatalf("message = %q", err.Error())
	}
	var baseErr *AdenClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("error = %v, want unwrap to *AdenClientError", err)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenBadRequestError(t *testing.T) {
	t.Run("generic", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adenWriteJSON(w, http.StatusBadRequest, `{"message":"Invalid integration id"}`)
		}))
		defer server.Close()

		client := newAdenTestClient(t, server.URL, "")
		_, err := client.RequestRefresh("bad")

		var baseErr *AdenClientError
		if !errors.As(err, &baseErr) {
			t.Fatalf("error = %v, want *AdenClientError", err)
		}
		if err.Error() != "Bad request: Invalid integration id" {
			t.Fatalf("message = %q", err.Error())
		}
	})

	t.Run("refresh message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adenWriteJSON(w, http.StatusBadRequest, `{"message":"Refresh failed"}`)
		}))
		defer server.Close()

		client := newAdenTestClient(t, server.URL, "")
		_, err := client.RequestRefresh("bad")

		var refreshErr *AdenRefreshError
		if !errors.As(err, &refreshErr) {
			t.Fatalf("error = %v, want *AdenRefreshError", err)
		}
		if refreshErr.RequiresReauthorization || refreshErr.ReauthorizationURL != "" {
			t.Fatalf("refresh error = %#v", refreshErr)
		}
	})
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusInternalServerError, `{"message":"boom"}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	_, err := client.ListIntegrations()

	var baseErr *AdenClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("error = %v, want *AdenClientError", err)
	}
	if baseErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", baseErr.StatusCode)
	}
	if err.Error() != "Aden request failed: HTTP 500" {
		t.Fatalf("message = %q", err.Error())
	}
}

// TestAdenDoesNotRetryStatusErrors covers upstream: only ConnectError and
// TimeoutException are retried; 400/401/403/404/429 raise immediately.
// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenDoesNotRetryStatusErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				adenWriteJSON(w, status, `{}`)
			}))
			defer server.Close()

			client, err := NewAdenCredentialClient(AdenClientConfig{
				BaseURL:       server.URL,
				APIKey:        "test-api-key",
				RetryAttempts: 3,
				RetryDelay:    time.Millisecond,
			})
			if err != nil {
				t.Fatalf("NewAdenCredentialClient: %v", err)
			}
			defer client.Close()

			if _, err := client.ListIntegrations(); err == nil {
				t.Fatalf("status %d returned nil error", status)
			}
			if hits.Load() != 1 {
				t.Fatalf("requests = %d, want 1 (no retry on %d)", hits.Load(), status)
			}
		})
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRetriesOnConnectionFailure(t *testing.T) {
	var attempts atomic.Int32
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       "http://aden.invalid",
		APIKey:        "test-api-key",
		RetryAttempts: 3,
		RetryDelay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	client.httpClient = &http.Client{Transport: adenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) < 3 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"integrations":[]}`)),
			Request:    request,
		}, nil
	})}

	integrations, err := client.ListIntegrations()
	if err != nil {
		t.Fatalf("ListIntegrations: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	if integrations == nil || len(integrations) != 0 {
		t.Fatalf("integrations = %#v, want empty slice", integrations)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenConnectionFailureExhaustsRetries(t *testing.T) {
	var attempts atomic.Int32
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       "http://aden.invalid",
		APIKey:        "test-api-key",
		RetryAttempts: 2,
		RetryDelay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	client.httpClient = &http.Client{Transport: adenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})}

	_, err = client.ListIntegrations()
	if err == nil {
		t.Fatal("ListIntegrations error = nil")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if !strings.HasPrefix(err.Error(), "Failed to connect to Aden server: ") {
		t.Fatalf("message = %q", err.Error())
	}
	var baseErr *AdenClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("error = %v, want *AdenClientError", err)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRetriesOnTimeout(t *testing.T) {
	var attempts atomic.Int32
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       "http://aden.invalid",
		APIKey:        "test-api-key",
		RetryAttempts: 2,
		RetryDelay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	client.httpClient = &http.Client{Transport: adenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, context.DeadlineExceeded
	})}

	_, err = client.ListIntegrations()
	if err == nil {
		t.Fatal("ListIntegrations error = nil")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("message = %q", err.Error())
	}
}

// TestAdenDoesNotRetryNonNetworkErrors covers the retry classifier: a response
// body read failure is not a connection error and fails on the first try.
// Parity-exempt: a Go net/http response-body read failure has no httpx analogue,
// and no Python test exercises the retry classifier.
func TestAdenDoesNotRetryNonNetworkErrors(t *testing.T) {
	var attempts atomic.Int32
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       "http://aden.invalid",
		APIKey:        "test-api-key",
		RetryAttempts: 3,
		RetryDelay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	client.httpClient = &http.Client{Transport: adenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(adenFailingReader{}),
			Request:    request,
		}, nil
	})}

	if _, err := client.ListIntegrations(); err == nil {
		t.Fatal("ListIntegrations error = nil")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

type adenFailingReader struct{}

func (adenFailingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// TestAdenRetryBackoffGrows verifies the exponential delay:
// RetryDelay * 2^attempt (20ms + 40ms for three attempts).
// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient._request_with_retry
func TestAdenRetryBackoffGrows(t *testing.T) {
	var attempts atomic.Int32
	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       "http://aden.invalid",
		APIKey:        "test-api-key",
		RetryAttempts: 3,
		RetryDelay:    20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	client.httpClient = &http.Client{Transport: adenRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})}

	start := time.Now()
	if _, err := client.ListIntegrations(); err == nil {
		t.Fatal("ListIntegrations error = nil")
	}
	elapsed := time.Since(start)
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	if elapsed < 60*time.Millisecond {
		t.Fatalf("elapsed = %v, want at least 60ms (20ms + 40ms backoff)", elapsed)
	}
}

// Parity: core/framework/credentials/aden/client.py::AdenCredentialClient.close
func TestAdenCloseIsIdempotentAndClientRemainsUsable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adenWriteJSON(w, http.StatusOK, `{"integrations":[]}`)
	}))
	defer server.Close()

	client := newAdenTestClient(t, server.URL, "")
	if err := client.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := client.ListIntegrations(); err != nil {
		t.Fatalf("ListIntegrations after Close: %v", err)
	}
}
