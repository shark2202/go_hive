package credentials

// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
//
// These tests cover CredentialStore.NewLifecycleManager, the Go-only store
// factory that pairs the store with a TokenLifecycleManager. Upstream has no
// store helper; callers build TokenLifecycleManager(provider, credential_id,
// store) directly, so the factory must produce the same wiring.

import (
	"net/http"
	"testing"
	"time"
)

// TestCredentialStoreNewLifecycleManager proves the factory returns a manager
// bound to the receiver store: a fresh token is served without a request, an
// expired token refreshes through the fake token server and persists, and
// InvalidateCache forces the next read back through the store.
//
// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
func TestCredentialStoreNewLifecycleManager(t *testing.T) {
	t.Run("fresh token is served from the store and cached", func(t *testing.T) {
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

		manager := store.NewLifecycleManager(provider, "oauth-cred", 0)
		if manager == nil {
			t.Fatal("NewLifecycleManager returned nil")
		}
		if manager.Store != store || manager.Provider != provider || manager.CredentialID != "oauth-cred" {
			t.Fatalf("manager wiring = %+v", manager)
		}
		if manager.RefreshBuffer != defaultTokenRefreshBuffer {
			t.Fatalf("RefreshBuffer = %v, want %v", manager.RefreshBuffer, defaultTokenRefreshBuffer)
		}

		token := manager.GetValidToken()
		if token == nil || token.AccessToken != "fresh-access" {
			t.Fatalf("token = %+v", token)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("fresh token triggered %d requests", server.RequestCount())
		}

		// Replacing the stored credential does not disturb the manager cache.
		rotated := &CredentialObject{
			ID:             "oauth-cred",
			CredentialType: CredentialTypeOAuth2,
			Keys:           NewKeyMap(NewCredentialKey("access_token", "rotated-externally")),
		}
		if err := store.SaveCredential(rotated); err != nil {
			t.Fatalf("save: %v", err)
		}
		if token := manager.GetValidToken(); token == nil || token.AccessToken != "fresh-access" {
			t.Fatalf("cached token = %+v", token)
		}

		// Invalidating makes the next read reload from the store.
		manager.InvalidateCache()
		if token := manager.GetValidToken(); token == nil || token.AccessToken != "rotated-externally" {
			t.Fatalf("token after invalidation = %+v", token)
		}
		if server.RequestCount() != 0 {
			t.Fatalf("non-expiring token triggered %d requests", server.RequestCount())
		}
	})

	t.Run("expired token refreshes through the store", func(t *testing.T) {
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

		manager := store.NewLifecycleManager(provider, "oauth-cred", 0)
		refreshed := []*OAuth2Token{}
		manager.OnTokenRefreshed = func(token *OAuth2Token) { refreshed = append(refreshed, token) }

		token := manager.GetValidToken()
		if token == nil || token.AccessToken != "rotated-access" {
			t.Fatalf("token = %+v", token)
		}
		if len(refreshed) != 1 || refreshed[0].AccessToken != "rotated-access" {
			t.Fatalf("on_token_refreshed = %+v", refreshed)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("token requests = %d", server.RequestCount())
		}
		assertFormField(t, server.Request(0).Form, "grant_type", "refresh_token")
		assertFormField(t, server.Request(0).Form, "refresh_token", "refresh-1")

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

		// The refreshed token is cached by the manager.
		if token := manager.GetValidToken(); token == nil || token.AccessToken != "rotated-access" {
			t.Fatalf("cached token = %+v", token)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("cached token triggered a request: %d", server.RequestCount())
		}

		// After invalidation the manager reloads the credential the refresh
		// persisted; it is still valid, so no new token request is made.
		manager.InvalidateCache()
		if token := manager.GetValidToken(); token == nil || token.AccessToken != "rotated-access" {
			t.Fatalf("token after invalidation = %+v", token)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("post-invalidation token triggered a request: %d", server.RequestCount())
		}
	})

	t.Run("failed refresh keeps the credential for reauthorization", func(t *testing.T) {
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

		manager := store.NewLifecycleManager(provider, "oauth-cred", 0)
		if token := manager.GetValidToken(); token != nil {
			t.Fatalf("token = %+v", token)
		}
		if server.RequestCount() != 1 {
			t.Fatalf("token requests = %d", server.RequestCount())
		}
		// The manager reports reauthorization but leaves the credential in the
		// store for the caller to replace.
		if stored, err := store.GetCredential("oauth-cred", false); err != nil || stored == nil {
			t.Fatalf("credential after failed refresh = %+v err = %v", stored, err)
		}
	})
}
