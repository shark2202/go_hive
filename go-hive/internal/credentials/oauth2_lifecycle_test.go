package credentials

import (
	"net/http"
	"testing"
	"time"
)

// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
func TestTokenLifecycleManagerGetValidToken(t *testing.T) {
	t.Run("fresh token is returned without a request", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		future := time.Now().UTC().Add(time.Hour)
		credential := &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "fresh-access", ExpiresAt: &future},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": credential})))
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)

		token := manager.GetValidToken()
		if token == nil || token.AccessToken != "fresh-access" {
			t.Fatalf("token = %+v", token)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("fresh token triggered %d requests", server.RequestCount())
		}

		// The cached token short-circuits the store lookup.
		manager.InvalidateCache()
		if token := manager.SyncGetValidToken(); token == nil || token.AccessToken != "fresh-access" {
			t.Fatalf("token after invalidation = %+v", token)
		}
	})

	t.Run("expired token is refreshed and cached", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, `{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-2"}`)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		past := time.Now().UTC().Add(-time.Hour)
		credential := &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "stale", ExpiresAt: &past},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": credential})))

		refreshed := []*OAuth2Token{}
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)
		manager.OnTokenRefreshed = func(token *OAuth2Token) { refreshed = append(refreshed, token) }

		token := manager.GetValidToken()
		if token == nil || token.AccessToken != "rotated-access" {
			t.Fatalf("token = %+v", token)
		}
		if len(refreshed) != 1 || refreshed[0].AccessToken != "rotated-access" {
			t.Fatalf("callback = %+v", refreshed)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("token requests = %d", server.RequestCount())
		}
		assertFormField(t, server.Request(0).Form, "grant_type", "refresh_token")

		stored, err := store.GetCredential("oauth-cred", false)
		if err != nil || stored == nil {
			t.Fatalf("stored credential = %+v err = %v", stored, err)
		}
		if stored.CredentialType != CredentialTypeOAuth2 || stored.ProviderID != "oauth2" || !stored.AutoRefresh {
			t.Fatalf("saved credential = %+v", stored)
		}
		if value, _ := stored.GetKeyValue("access_token"); value != "rotated-access" {
			t.Fatalf("saved access_token = %q", value)
		}
		if value, _ := stored.GetKeyValue("refresh_token"); value != "refresh-2" {
			t.Fatalf("saved refresh_token = %q", value)
		}

		if token := manager.GetValidToken(); token == nil || token.AccessToken != "rotated-access" {
			t.Fatalf("cached token = %+v", token)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("cached token triggered a request: %d", server.RequestCount())
		}
	})

	t.Run("missing credential", func(t *testing.T) {
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))
		manager := NewTokenLifecycleManager(provider, "absent", store, 0)
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
	})

	t.Run("credential without access token", func(t *testing.T) {
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: "https://oauth.example.test/token"})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		credential := &CredentialObject{ID: "oauth-cred", Keys: NewKeyMap(NewCredentialKey("refresh_token", "refresh-1"))}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": credential})))
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
	})
}

// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
func TestTokenLifecycleManagerRefreshFailures(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	expiredCredential := func(refreshToken string) *CredentialObject {
		keys := NewKeyMap(&CredentialKey{Name: "access_token", Value: "stale", ExpiresAt: &past})
		if refreshToken != "" {
			keys.Set(NewCredentialKey("refresh_token", refreshToken))
		}
		return &CredentialObject{ID: "oauth-cred", CredentialType: CredentialTypeOAuth2, Keys: keys}
	}

	t.Run("no refresh token needs reauthorization", func(t *testing.T) {
		server := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
		provider, err := NewBaseOAuth2Provider(&OAuth2Config{TokenURL: server.URL("/token")})
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{
			"oauth-cred": expiredCredential(""),
		})))
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("requests = %d", server.RequestCount())
		}
	})

	t.Run("invalid grant invalidates the cache", func(t *testing.T) {
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
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{
			"oauth-cred": expiredCredential("refresh-1"),
		})))
		failed := []string{}
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)
		manager.OnRefreshFailed = func(message string) { failed = append(failed, message) }
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
		// Python returns before on_refresh_failed for invalid_grant.
		if len(failed) != 0 {
			t.Fatalf("on_refresh_failed called for invalid_grant: %v", failed)
		}
	})

	t.Run("transient failure keeps a still valid token", func(t *testing.T) {
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
		// Expires in ten minutes: outside the five-minute expired buffer but
		// inside this manager's thirty-minute refresh buffer.
		soon := time.Now().UTC().Add(10 * time.Minute)
		credential := &CredentialObject{
			ID: "oauth-cred",
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "still-valid", ExpiresAt: &soon},
				NewCredentialKey("refresh_token", "refresh-1"),
			),
		}
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{"oauth-cred": credential})))
		failed := []string{}
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 30*time.Minute)
		manager.OnRefreshFailed = func(message string) { failed = append(failed, message) }

		token := manager.GetValidToken()
		if token == nil || token.AccessToken != "still-valid" {
			t.Fatalf("token = %+v", token)
		}
		if len(failed) != 1 || failed[0] == "" {
			t.Fatalf("on_refresh_failed = %v", failed)
		}
	})

	t.Run("transient failure with an expired token returns nothing", func(t *testing.T) {
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
		store := NewCredentialStore(WithStorage(NewInMemoryStorage(map[string]*CredentialObject{
			"oauth-cred": expiredCredential("refresh-1"),
		})))
		manager := NewTokenLifecycleManager(provider, "oauth-cred", store, 0)
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
	})
}

// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
func TestTokenLifecycleManagerAcquireRevokeAndFormat(t *testing.T) {
	server := newOAuth2TestServer(t, func(request oauth2TestRequest, _ int) oauth2TestResponse {
		if request.Path == "/revoke" {
			return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: "{}"}
		}
		return oauth2TestResponse{
			Status:      http.StatusOK,
			ContentType: "application/json",
			Body:        `{"access_token":"cc-access","token_type":"Bearer","expires_in":3600,"scope":"read"}`,
		}
	})
	provider, err := NewBaseOAuth2Provider(&OAuth2Config{
		TokenURL:      server.URL("/token"),
		RevocationURL: server.URL("/revoke"),
		ClientID:      "client-1",
		ClientSecret:  "secret-1",
		DefaultScopes: []string{"read"},
	})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))
	manager := NewTokenLifecycleManager(provider, "service-account", store, 0)

	if headers := manager.GetRequestHeaders(); len(headers) != 0 {
		t.Fatalf("headers without a token = %v", headers)
	}
	if kwargs := manager.GetRequestKwargs(); len(kwargs) != 0 {
		t.Fatalf("kwargs without a token = %v", kwargs)
	}

	token, err := manager.AcquireTokenClientCredentials(nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if token.AccessToken != "cc-access" {
		t.Fatalf("token = %+v", token)
	}
	assertFormField(t, server.Request(0).Form, "grant_type", "client_credentials")
	assertFormField(t, server.Request(0).Form, "scope", "read")

	stored, err := store.GetCredential("service-account", false)
	if err != nil || stored == nil {
		t.Fatalf("stored credential = %+v err = %v", stored, err)
	}
	if stored.CredentialType != CredentialTypeOAuth2 || stored.ProviderID != "oauth2" || !stored.AutoRefresh {
		t.Fatalf("saved credential = %+v", stored)
	}
	if value, _ := stored.GetKeyValue("scope"); value != "read" {
		t.Fatalf("saved scope = %q", value)
	}
	if _, ok := stored.GetKeyValue("refresh_token"); ok {
		t.Fatal("client-credentials token stored a refresh_token")
	}

	headers := manager.GetRequestHeaders()
	if headers["Authorization"] != "Bearer cc-access" {
		t.Fatalf("headers = %v", headers)
	}
	kwargs := manager.GetRequestKwargs()
	kwargsHeaders, ok := kwargs["headers"].(map[string]string)
	if !ok || kwargsHeaders["Authorization"] != "Bearer cc-access" {
		t.Fatalf("kwargs = %v", kwargs)
	}

	if !manager.Revoke() {
		t.Fatal("revoke reported failure")
	}
	if revoked := server.Request(server.RequestCount() - 1); revoked.Path != "/revoke" {
		t.Fatalf("revocation request = %+v", revoked)
	}
	if credential, err := store.GetCredential("service-account", false); err != nil || credential != nil {
		t.Fatalf("credential after revoke = %+v err = %v", credential, err)
	}
}
