package credentials

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderIdentity(t *testing.T) {
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider.ProviderID() != "oauth2" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}
	types := provider.SupportedTypes()
	if !supports(types, CredentialTypeOAuth2) || !supports(types, CredentialTypeBearerToken) {
		t.Fatalf("types = %v", types)
	}
	if !provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeOAuth2}) {
		t.Fatal("oauth2 credential not handled")
	}
	if !provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeBearerToken}) {
		t.Fatal("bearer token not handled")
	}
	if provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeAPIKey}) {
		t.Fatal("api key handled by oauth2 provider")
	}

	// OAuth2Config defaults mirror the Python dataclass field defaults.
	if provider.config.TokenPlacement != TokenPlacementHeaderBearer {
		t.Fatalf("placement = %q", provider.config.TokenPlacement)
	}
	if provider.config.QueryParamName != "access_token" {
		t.Fatalf("query param = %q", provider.config.QueryParamName)
	}
	if provider.config.RequestTimeout != 30*time.Second {
		t.Fatalf("timeout = %v", provider.config.RequestTimeout)
	}

	custom, err := NewBaseOAuth2ProviderWithID(&OAuth2Config{TokenURL: "https://oauth.example.test/token"}, "custom_oauth2")
	if err != nil {
		t.Fatalf("custom id: %v", err)
	}
	if custom.ProviderID() != "custom_oauth2" {
		t.Fatalf("custom provider id = %q", custom.ProviderID())
	}

	if _, err := NewBaseOAuth2Provider(nil); err == nil || !strings.Contains(err.Error(), "token_url is required") {
		t.Fatalf("nil config error = %v", err)
	}
	if _, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: ""}); err == nil || !strings.Contains(err.Error(), "token_url is required") {
		t.Fatalf("missing token_url error = %v", err)
	}
	if _, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:       "https://oauth.example.test/token",
		TokenPlacement: TokenPlacementHeaderCustom,
	}); err == nil || !strings.Contains(err.Error(), "custom_header_name is required") {
		t.Fatalf("custom header error = %v", err)
	}
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderAuthorizationURL(t *testing.T) {
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:         "https://oauth.example.test/token",
		AuthorizationURL: "https://auth.example.test/authorize",
		ClientID:         "client-1",
		DefaultScopes:    []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	raw, err := provider.GetAuthorizationURL("state-1", "https://app.example.test/callback", nil, nil)
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	if !strings.HasPrefix(raw, "https://auth.example.test/authorize?") {
		t.Fatalf("url = %q", raw)
	}
	// Python's urlencode keeps the insertion order: client_id first.
	if !strings.HasPrefix(strings.TrimPrefix(raw, "https://auth.example.test/authorize?"), "client_id=client-1&redirect_uri=") {
		t.Fatalf("query order = %q", raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client-1" ||
		query.Get("redirect_uri") != "https://app.example.test/callback" ||
		query.Get("response_type") != "code" ||
		query.Get("state") != "state-1" ||
		query.Get("scope") != "read write" {
		t.Fatalf("query = %v", query)
	}

	override, err := provider.GetAuthorizationURL("state-2", "https://app.example.test/callback", []string{"only"}, map[string]string{"prompt": "consent"})
	if err != nil {
		t.Fatalf("authorization url with extras: %v", err)
	}
	overrideQuery, _ := url.Parse(override)
	if overrideQuery.Query().Get("scope") != "only" || overrideQuery.Query().Get("prompt") != "consent" {
		t.Fatalf("override query = %v", overrideQuery.Query())
	}

	noAuth, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := noAuth.GetAuthorizationURL("state", "https://app.example.test/cb", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "authorization_url not configured") {
		t.Fatalf("unconfigured error = %v", err)
	}
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderExchangeCode(t *testing.T) {
	server := newOAuth2TestServerJSON(t, `{"access_token":"code-access","token_type":"Bearer","expires_in":3600,"refresh_token":"code-refresh","scope":"read write","custom":"kept"}`)
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:         server.URL("/oauth/token"),
		ClientID:         "client-1",
		ClientSecret:     "secret-1",
		ExtraTokenParams: map[string]string{"audience": "crm"},
		ExtraHeaders:     map[string]string{"X-Extra": "header-value"},
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	token, err := provider.ExchangeCode("auth-code", "https://app.example.test/callback", map[string]string{"code_verifier": "verifier-1"})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if server.RequestCount() != 1 {
		t.Fatalf("requests = %d", server.RequestCount())
	}
	request := server.Request(0)
	if request.Method != http.MethodPost || request.Path != "/oauth/token" {
		t.Fatalf("request = %+v", request)
	}
	if request.ContentType != "application/x-www-form-urlencoded" || request.Accept != "application/json" {
		t.Fatalf("headers = %+v", request)
	}
	if request.Headers.Get("X-Extra") != "header-value" {
		t.Fatalf("extra header = %q", request.Headers.Get("X-Extra"))
	}
	assertFormField(t, request.Form, "grant_type", "authorization_code")
	assertFormField(t, request.Form, "client_id", "client-1")
	assertFormField(t, request.Form, "client_secret", "secret-1")
	assertFormField(t, request.Form, "code", "auth-code")
	assertFormField(t, request.Form, "redirect_uri", "https://app.example.test/callback")
	assertFormField(t, request.Form, "audience", "crm")
	assertFormField(t, request.Form, "code_verifier", "verifier-1")

	if token.AccessToken != "code-access" || token.TokenType != "Bearer" {
		t.Fatalf("token = %+v", token)
	}
	if token.RefreshToken != "code-refresh" || token.Scope != "read write" {
		t.Fatalf("token = %+v", token)
	}
	if token.ExpiresAt == nil {
		t.Fatal("expires_at not set from expires_in")
	}
	if seconds := token.ExpiresInSeconds(); seconds == nil || *seconds < 3500 || *seconds > 3600 {
		t.Fatalf("expires_in_seconds = %v", seconds)
	}
	if token.RawResponse["custom"] != "kept" {
		t.Fatalf("raw response = %v", token.RawResponse)
	}
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderClientCredentialsGrant(t *testing.T) {
	server := newOAuth2TestServerJSON(t, `{"access_token":"cc-access","token_type":"Bearer","expires_in":600}`)
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:      server.URL("/token"),
		ClientID:      "client-1",
		ClientSecret:  "secret-1",
		DefaultScopes: []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	if _, err := provider.ClientCredentialsGrant(nil, nil); err != nil {
		t.Fatalf("grant: %v", err)
	}
	assertFormField(t, server.Request(0).Form, "grant_type", "client_credentials")
	assertFormField(t, server.Request(0).Form, "scope", "read write")

	if _, err := provider.ClientCredentialsGrant([]string{"only"}, nil); err != nil {
		t.Fatalf("grant with scopes: %v", err)
	}
	assertFormField(t, server.Request(1).Form, "scope", "only")

	noScopes, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := noScopes.ClientCredentialsGrant(nil, nil); err != nil {
		t.Fatalf("grant without scopes: %v", err)
	}
	assertNoFormField(t, server.Request(2).Form, "scope")
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderRefreshAccessToken(t *testing.T) {
	server := newOAuth2TestServerJSON(t, `{"access_token":"refreshed-access","token_type":"Bearer","expires_in":3600}`)
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:     server.URL("/token"),
		ClientID:     "client-1",
		ClientSecret: "secret-1",
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	if _, err := provider.RefreshAccessToken("refresh-1", nil, nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	form := server.Request(0).Form
	assertFormField(t, form, "grant_type", "refresh_token")
	assertFormField(t, form, "refresh_token", "refresh-1")
	assertFormField(t, form, "client_id", "client-1")
	assertFormField(t, form, "client_secret", "secret-1")
	assertNoFormField(t, form, "scope")

	if _, err := provider.RefreshAccessToken("refresh-1", []string{"read"}, map[string]string{"audience": "crm"}); err != nil {
		t.Fatalf("refresh with scopes: %v", err)
	}
	assertFormField(t, server.Request(1).Form, "scope", "read")
	assertFormField(t, server.Request(1).Form, "audience", "crm")
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderTokenResponseParsing(t *testing.T) {
	t.Run("form encoded", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{
				Status:      http.StatusOK,
				ContentType: "text/plain",
				Body:        "access_token=form-access&token_type=Bearer&expires_in=1200&refresh_token=form-refresh",
			}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		token, err := provider.RefreshAccessToken("refresh-1", nil, nil)
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if token.AccessToken != "form-access" || token.RefreshToken != "form-refresh" || token.TokenType != "Bearer" {
			t.Fatalf("token = %+v", token)
		}
		if seconds := token.ExpiresInSeconds(); seconds == nil || *seconds < 1100 || *seconds > 1200 {
			t.Fatalf("expires_in_seconds = %v", seconds)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: `{"access_token":`}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		if token, err := provider.RefreshAccessToken("refresh-1", nil, nil); err == nil || token != nil {
			t.Fatalf("token = %+v err = %v", token, err)
		}
	})

	t.Run("200 without access token", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"token_type":"Bearer","expires_in":60}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		if token, err := provider.RefreshAccessToken("refresh-1", nil, nil); err == nil || token != nil ||
			!strings.Contains(err.Error(), "missing access_token") {
			t.Fatalf("token = %+v err = %v", token, err)
		}
	})

	t.Run("error response", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{
				Status:      http.StatusBadRequest,
				ContentType: "application/json",
				Body:        `{"error":"invalid_grant","error_description":"refresh token revoked"}`,
			}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		token, err := provider.RefreshAccessToken("refresh-1", nil, nil)
		if token != nil {
			t.Fatalf("token = %+v", token)
		}
		var oauthErr *OAuth2Error
		if !errors.As(err, &oauthErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		if oauthErr.ErrorCode != "invalid_grant" || oauthErr.Description != "refresh token revoked" || oauthErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("oauth error = %+v", oauthErr)
		}
		if err.Error() != "invalid_grant: refresh token revoked" {
			t.Fatalf("message = %q", err.Error())
		}
	})

	t.Run("error key with 200", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"error":"invalid_scope","error_description":"bad scope"}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		_, err = provider.ClientCredentialsGrant(nil, nil)
		var oauthErr *OAuth2Error
		if !errors.As(err, &oauthErr) || oauthErr.ErrorCode != "invalid_scope" || oauthErr.StatusCode != http.StatusOK {
			t.Fatalf("error = %+v", err)
		}
	})

	t.Run("non json error body", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{Status: http.StatusInternalServerError, ContentType: "text/plain", Body: "upstream boom"}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		_, err = provider.RefreshAccessToken("refresh-1", nil, nil)
		var oauthErr *OAuth2Error
		if !errors.As(err, &oauthErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		if oauthErr.ErrorCode != "unknown_error" || oauthErr.Description != "upstream boom" || oauthErr.StatusCode != 500 {
			t.Fatalf("oauth error = %+v", oauthErr)
		}
	})
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderValidateAndShouldRefresh(t *testing.T) {
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	if provider.Validate(NewCredentialObject("without-token")) {
		t.Fatal("credential without access_token validated")
	}

	future := time.Now().UTC().Add(time.Hour)
	valid := &CredentialObject{ID: "valid", Keys: NewKeyMap(&CredentialKey{Name: "access_token", Value: "t", ExpiresAt: &future})}
	if !provider.Validate(valid) {
		t.Fatal("unexpired token rejected")
	}
	if provider.ShouldRefresh(valid) {
		t.Fatal("token an hour out flagged for refresh")
	}

	past := time.Now().UTC().Add(-time.Minute)
	expired := &CredentialObject{ID: "expired", Keys: NewKeyMap(&CredentialKey{Name: "access_token", Value: "t", ExpiresAt: &past})}
	if provider.Validate(expired) {
		t.Fatal("expired token validated")
	}
	if !provider.ShouldRefresh(expired) {
		t.Fatal("expired token not flagged for refresh")
	}

	// ShouldRefresh applies the five-minute buffer; Validate does not.
	within := time.Now().UTC().Add(4 * time.Minute)
	buffered := &CredentialObject{ID: "buffered", Keys: NewKeyMap(&CredentialKey{Name: "access_token", Value: "t", ExpiresAt: &within})}
	if !provider.ShouldRefresh(buffered) {
		t.Fatal("token inside the refresh buffer not flagged")
	}
	if !provider.Validate(buffered) {
		t.Fatal("Validate must not apply the five-minute buffer")
	}

	outside := time.Now().UTC().Add(6 * time.Minute)
	fresh := &CredentialObject{ID: "fresh", Keys: NewKeyMap(&CredentialKey{Name: "access_token", Value: "t", ExpiresAt: &outside})}
	if provider.ShouldRefresh(fresh) {
		t.Fatal("token outside the refresh buffer flagged")
	}

	noExpiry := &CredentialObject{ID: "no-expiry", Keys: NewKeyMap(NewCredentialKey("access_token", "t"))}
	if provider.ShouldRefresh(noExpiry) {
		t.Fatal("token without expires_at flagged for refresh")
	}
	if !provider.Validate(noExpiry) {
		t.Fatal("token without expires_at rejected")
	}
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderRefreshCredential(t *testing.T) {
	newCredential := func(refreshToken string) *CredentialObject {
		past := time.Now().UTC().Add(-time.Hour)
		return &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "stale", ExpiresAt: &past},
				NewCredentialKey("refresh_token", refreshToken),
			),
		}
	}

	t.Run("missing refresh token", func(t *testing.T) {
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		credential := &CredentialObject{ID: "oauth-cred", Keys: NewKeyMap(NewCredentialKey("access_token", "stale"))}
		_, err = provider.Refresh(credential)
		var refreshErr *CredentialRefreshError
		if !errors.As(err, &refreshErr) || err.Error() != "Credential 'oauth-cred' has no refresh_token" {
			t.Fatalf("error = %T %q", err, err)
		}
	})

	t.Run("invalid grant", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{
				Status:      http.StatusBadRequest,
				ContentType: "application/json",
				Body:        `{"error":"invalid_grant","error_description":"revoked"}`,
			}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		credential := newCredential("refresh-1")
		_, err = provider.Refresh(credential)
		var refreshErr *CredentialRefreshError
		if !errors.As(err, &refreshErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		if err.Error() != "Refresh token for 'oauth-cred' is invalid or revoked. Re-authorization required." {
			t.Fatalf("message = %q", err.Error())
		}
		if value, _ := credential.GetKeyValue("access_token"); value != "stale" {
			t.Fatalf("credential mutated on failure: %q", value)
		}
		if credential.LastRefreshed != nil {
			t.Fatal("last_refreshed set on failure")
		}
	})

	t.Run("server failure", func(t *testing.T) {
		server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
			return oauth2TestResponse{
				Status:      http.StatusInternalServerError,
				ContentType: "application/json",
				Body:        `{"error":"server_error","error_description":"boom"}`,
			}
		})
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		_, err = provider.Refresh(newCredential("refresh-1"))
		if err == nil || !strings.HasPrefix(err.Error(), "Failed to refresh 'oauth-cred': server_error: boom") {
			t.Fatalf("error = %v", err)
		}
		var refreshErr *CredentialRefreshError
		if !errors.As(err, &refreshErr) {
			t.Fatalf("error type = %T", err)
		}
	})

	t.Run("transport error escapes unwrapped", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		server.Close()
		_, err = provider.Refresh(newCredential("refresh-1"))
		var refreshErr *CredentialRefreshError
		if err == nil || errors.As(err, &refreshErr) {
			t.Fatalf("transport error = %T %v", err, err)
		}
	})

	t.Run("rotates tokens", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-2"}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		credential := newCredential("refresh-1")
		refreshed, err := provider.Refresh(credential)
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if refreshed != credential {
			t.Fatal("refresh did not return the same credential")
		}
		value, _ := refreshed.GetKeyValue("access_token")
		if value != "rotated-access" {
			t.Fatalf("access_token = %q", value)
		}
		rotated, _ := refreshed.GetKeyValue("refresh_token")
		if rotated != "refresh-2" {
			t.Fatalf("refresh_token = %q", rotated)
		}
		key := refreshed.Keys.Get("access_token")
		if key.ExpiresAt == nil || time.Until(*key.ExpiresAt) < 50*time.Minute {
			t.Fatalf("expires_at = %v", key.ExpiresAt)
		}
		if refreshed.LastRefreshed == nil {
			t.Fatal("last_refreshed not set")
		}
		assertFormField(t, server.Request(0).Form, "refresh_token", "refresh-1")
	})

	t.Run("keeps refresh token when not reissued", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		credential := newCredential("refresh-1")
		if _, err := provider.Refresh(credential); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		value, _ := credential.GetKeyValue("refresh_token")
		if value != "refresh-1" {
			t.Fatalf("refresh_token = %q", value)
		}
	})
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderRevoke(t *testing.T) {
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider.RevokeToken("token", "access_token") {
		t.Fatal("revocation without revocation_url succeeded")
	}
	tokens := &CredentialObject{ID: "oauth-cred", Keys: NewKeyMap(
		NewCredentialKey("access_token", "access-1"),
		NewCredentialKey("refresh_token", "refresh-1"),
	)}
	if provider.Revoke(tokens) {
		t.Fatal("credential revoke succeeded without revocation_url")
	}
	if !provider.Revoke(NewCredentialObject("empty")) {
		t.Fatal("empty credential revoke must succeed")
	}

	server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: "{}"}
	})
	revoking, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:      server.URL("/token"),
		RevocationURL: server.URL("/revoke"),
		ClientID:      "client-1",
		ClientSecret:  "secret-1",
		ExtraHeaders:  map[string]string{"X-Extra": "header-value"},
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if !revoking.RevokeToken("access-1", "access_token") {
		t.Fatal("revocation rejected")
	}
	request := server.Request(0)
	if request.Method != http.MethodPost || request.Path != "/revoke" {
		t.Fatalf("request = %+v", request)
	}
	assertFormField(t, request.Form, "token", "access-1")
	assertFormField(t, request.Form, "token_type_hint", "access_token")
	assertFormField(t, request.Form, "client_id", "client-1")
	assertFormField(t, request.Form, "client_secret", "secret-1")
	if request.Accept != "application/json" || request.Headers.Get("X-Extra") != "header-value" {
		t.Fatalf("headers = %+v", request)
	}

	if !revoking.Revoke(tokens) {
		t.Fatal("credential revoke failed")
	}
	if server.RequestCount() != 3 {
		t.Fatalf("requests = %d", server.RequestCount())
	}
	if hint := server.Request(1).Form.Get("token_type_hint"); hint != "access_token" {
		t.Fatalf("first hint = %q", hint)
	}
	if hint := server.Request(2).Form.Get("token_type_hint"); hint != "refresh_token" {
		t.Fatalf("second hint = %q", hint)
	}

	failing := newOAuth2TestServer(t, func(request oauth2TestRequest, _ int) oauth2TestResponse {
		if request.Form.Get("token_type_hint") == "refresh_token" {
			return oauth2TestResponse{Status: http.StatusInternalServerError, ContentType: "application/json", Body: "{}"}
		}
		return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: "{}"}
	})
	partial, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:      failing.URL("/token"),
		RevocationURL: failing.URL("/revoke"),
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if partial.Revoke(tokens) {
		t.Fatal("partial revocation reported success")
	}
}

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
func TestBaseOAuth2ProviderFormatForRequest(t *testing.T) {
	token := &OAuth2Token{AccessToken: "tok", TokenType: "Bearer"}

	bearer, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	headers, ok := bearer.FormatForRequest(token)["headers"].(map[string]string)
	if !ok || headers["Authorization"] != "Bearer tok" {
		t.Fatalf("bearer headers = %v", headers)
	}

	custom, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:         "https://oauth.example.test/token",
		TokenPlacement:   TokenPlacementHeaderCustom,
		CustomHeaderName: "X-Access-Token",
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	headers = custom.FormatForRequest(token)["headers"].(map[string]string)
	if headers["X-Access-Token"] != "tok" {
		t.Fatalf("custom headers = %v", headers)
	}

	query, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:       "https://oauth.example.test/token",
		TokenPlacement: TokenPlacementQueryParam,
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	params := query.FormatForRequest(token)["params"].(map[string]string)
	if params["access_token"] != "tok" {
		t.Fatalf("query params = %v", params)
	}

	body, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:       "https://oauth.example.test/token",
		TokenPlacement: TokenPlacementBodyParam,
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	data := body.FormatForRequest(token)["data"].(map[string]string)
	if data["access_token"] != "tok" {
		t.Fatalf("body data = %v", data)
	}

	unknown, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:       "https://oauth.example.test/token",
		TokenPlacement: TokenPlacement("unknown"),
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if len(unknown.FormatForRequest(token)) != 0 {
		t.Fatalf("unknown placement = %v", unknown.FormatForRequest(token))
	}

	credential := &CredentialObject{ID: "oauth-cred", Keys: NewKeyMap(
		NewCredentialKey("access_token", "tok"),
		NewCredentialKey("token_type", "MAC"),
	)}
	headers = bearer.FormatCredentialForRequest(credential)["headers"].(map[string]string)
	if headers["Authorization"] != "MAC tok" {
		t.Fatalf("credential headers = %v", headers)
	}
	noType := &CredentialObject{ID: "oauth-cred", Keys: NewKeyMap(NewCredentialKey("access_token", "tok"))}
	headers = bearer.FormatCredentialForRequest(noType)["headers"].(map[string]string)
	if headers["Authorization"] != "Bearer tok" {
		t.Fatalf("credential default headers = %v", headers)
	}
	if len(bearer.FormatCredentialForRequest(NewCredentialObject("empty"))) != 0 {
		t.Fatal("credential without access_token formatted")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_register_provider
func TestOAuth2ProviderRegistrationThroughStore(t *testing.T) {
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)), WithProviders(provider))
	if got := store.GetProvider("oauth2"); got != provider {
		t.Fatalf("registered provider = %v", got)
	}
	if got := store.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2}); got != provider {
		t.Fatalf("provider for oauth2 credential = %v", got)
	}
	if got := store.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeBearerToken}); got != provider {
		t.Fatalf("provider for bearer credential = %v", got)
	}
	byID := &CredentialObject{CredentialType: CredentialTypeCustom, ProviderID: "oauth2"}
	if got := store.GetProviderForCredential(byID); got != provider {
		t.Fatalf("provider by explicit id = %v", got)
	}

	// The default store stays StaticProvider-only: generic OAuth2 is opt-in.
	defaultStore := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))
	if got := defaultStore.GetProvider("oauth2"); got != nil {
		t.Fatalf("default store registered oauth2: %v", got)
	}
	if got := defaultStore.GetProvider("static"); got == nil {
		t.Fatal("default store lost StaticProvider")
	}
	if got := defaultStore.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2}); got != nil {
		t.Fatalf("default store handled oauth2: %v", got)
	}

	// RegisterProvider after construction works too.
	late := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))
	late.RegisterProvider(provider)
	if got := late.GetProvider("oauth2"); got != provider {
		t.Fatalf("late provider = %v", got)
	}

	hubSpot := NewHubSpotOAuth2Provider("client-1", "secret-1", nil)
	zoho := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{})
	subclassStore := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)), WithProviders(hubSpot, zoho))
	if got := subclassStore.GetProvider("hubspot_oauth2"); got != hubSpot {
		t.Fatalf("hubspot provider = %v", got)
	}
	if got := subclassStore.GetProvider("zoho_crm_oauth2"); got != zoho {
		t.Fatalf("zoho provider = %v", got)
	}
}

// Parity: core/framework/credentials/store.py::CredentialStore
// Covers get_credential's refresh-on-access path with a real OAuth2 provider,
// plus the negative control showing nothing refreshes without registration.
func TestOAuth2RefreshOnAccess(t *testing.T) {
	expiredCredential := func() *CredentialObject {
		past := time.Now().UTC().Add(-time.Hour)
		return &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			ProviderID:     "oauth2",
			AutoRefresh:    true,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "stale-access", ExpiresAt: &past},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
	}

	t.Run("registered provider refreshes", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-2"}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{
			TokenURL:     server.URL("/token"),
			ClientID:     "client-1",
			ClientSecret: "secret-1",
		})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		storage := NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": expiredCredential()})
		store := NewCredentialStore(WithStorage(storage), WithProviders(provider))

		value, err := store.GetKey("oauth-cred", "access_token")
		if err != nil || value == nil || *value != "rotated-access" {
			t.Fatalf("access token = %v err = %v", value, err)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("token requests = %d", server.RequestCount())
		}
		form := server.Request(0).Form
		assertFormField(t, form, "grant_type", "refresh_token")
		assertFormField(t, form, "refresh_token", "refresh-1")
		assertFormField(t, form, "client_id", "client-1")
		assertFormField(t, form, "client_secret", "secret-1")

		stored, err := storage.Load("oauth-cred")
		if err != nil || stored == nil {
			t.Fatalf("stored credential = %+v err = %v", stored, err)
		}
		if rotated, _ := stored.GetKeyValue("access_token"); rotated != "rotated-access" {
			t.Fatalf("persisted access_token = %q", rotated)
		}
		if rotated, _ := stored.GetKeyValue("refresh_token"); rotated != "refresh-2" {
			t.Fatalf("persisted refresh_token = %q", rotated)
		}
		if stored.LastRefreshed == nil {
			t.Fatal("last_refreshed not persisted")
		}
		if key := stored.Keys.Get("access_token"); key.ExpiresAt == nil || time.Until(*key.ExpiresAt) < 50*time.Minute {
			t.Fatalf("persisted expiry = %v", key.ExpiresAt)
		}

		// The second read is served from the store cache.
		if _, err := store.GetKey("oauth-cred", "access_token"); err != nil {
			t.Fatalf("cached read: %v", err)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("cached read hit the token endpoint: %d", server.RequestCount())
		}
	})

	t.Run("fresh token is not refreshed", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		future := time.Now().UTC().Add(time.Hour)
		credential := &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			ProviderID:     "oauth2",
			AutoRefresh:    true,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "fresh-access", ExpiresAt: &future},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
		store := NewCredentialStore(
			WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": credential})),
			WithProviders(provider),
		)
		value, err := store.GetKey("oauth-cred", "access_token")
		if err != nil || value == nil || *value != "fresh-access" {
			t.Fatalf("access token = %v err = %v", value, err)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("fresh token triggered %d token requests", server.RequestCount())
		}
	})

	t.Run("without provider registration", func(t *testing.T) {
		// The provider exists and points at a live token endpoint; the store
		// simply never learns about it. Nothing may refresh.
		server := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
		if _, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")}); err != nil {
			t.Fatalf("provider: %v", err)
		}
		storage := NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": expiredCredential()})
		store := NewCredentialStore(WithStorage(storage))

		value, err := store.GetKey("oauth-cred", "access_token")
		if err != nil || value == nil || *value != "stale-access" {
			t.Fatalf("access token = %v err = %v", value, err)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("unregistered provider hit the token endpoint %d times", server.RequestCount())
		}
	})
}

// Parity: core/framework/credentials/store.py::CredentialStore
// Refresh failure keeps the stale credential for best-effort reads and raises
// CredentialExpiredError only when the caller asks for it.
func TestOAuth2RefreshFailureThroughStore(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	newExpired := func() *CredentialObject {
		return &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			ProviderID:     "oauth2",
			AutoRefresh:    true,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "stale-access", ExpiresAt: &past},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
	}

	server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{
			Status:      http.StatusBadRequest,
			ContentType: "application/json",
			Body:        `{"error":"invalid_grant","error_description":"revoked"}`,
		}
	})
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	storage := NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": newExpired()})
	store := NewCredentialStore(WithStorage(storage), WithProviders(provider))
	credential, err := store.GetCredential("oauth-cred", true)
	if err != nil || credential == nil {
		t.Fatalf("best-effort read = %+v err = %v", credential, err)
	}
	if value, _ := credential.GetKeyValue("access_token"); value != "stale-access" {
		t.Fatalf("stale token = %q", value)
	}

	store.ClearCache()
	_, err = store.GetCredentialEx("oauth-cred", true, true)
	var expiredErr *CredentialExpiredError
	if !errors.As(err, &expiredErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if expiredErr.CredentialID != "oauth-cred" {
		t.Fatalf("expired error = %+v", expiredErr)
	}
	if !strings.Contains(err.Error(), "Reauthorization required.") {
		t.Fatalf("message = %q", err.Error())
	}

	// A transport failure is not a CredentialRefreshError upstream either, so
	// the store surfaces the error instead of the stale credential.
	transport := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
	transportProvider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: transport.URL("/token")})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	transport.Close()
	transportStore := NewCredentialStore(
		WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": newExpired()})),
		WithProviders(transportProvider),
	)
	credential, err = transportStore.GetCredential("oauth-cred", true)
	var refreshErr *CredentialRefreshError
	if err == nil || credential != nil || errors.As(err, &refreshErr) {
		t.Fatalf("transport failure = %+v err = %T %v", credential, err, err)
	}
}
