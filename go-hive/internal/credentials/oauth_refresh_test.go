package credentials

// Parity-exempt: Go-only store→environment bridge that exposes OAuth2 refresh
// material to the integration modules. Upstream refreshes inside the
// credential store instead, so no Python test mirrors these bindings.

import (
	"os"
	"testing"
	"time"
)

// saveOAuthEnvCredential stores one OAuth2 credential with the upstream key
// names and an access-token expiry.
func saveOAuthEnvCredential(t *testing.T, basePath, credentialID string, expiresAt time.Time) {
	t.Helper()
	storage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	credential := NewCredentialObject(credentialID)
	credential.CredentialType = CredentialTypeOAuth2
	credential.Keys.Set(NewCredentialKey("client_id", credentialID+"-client"))
	credential.Keys.Set(NewCredentialKey("client_secret", credentialID+"-secret"))
	credential.Keys.Set(NewCredentialKey("refresh_token", credentialID+"-refresh"))
	access := NewCredentialKey("access_token", credentialID+"-access")
	utc := expiresAt.UTC()
	access.ExpiresAt = &utc
	credential.Keys.Set(access)
	if err := storage.Save(credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
}

// Parity-exempt: asserts the exported variable names the module OAuth
// declarations read; no upstream equivalent (Python keeps them in the store).
func TestOAuthRefreshEnvBindingsExposeStoredRefreshMaterial(t *testing.T) {
	basePath := newEnvSyncStore(t)
	expiry := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	saveOAuthEnvCredential(t, basePath, "google", expiry)

	for _, name := range []string{
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_REFRESH_TOKEN", "GOOGLE_ACCESS_TOKEN_EXPIRES_AT",
	} {
		_ = os.Unsetenv(name)
		t.Cleanup(func() { _ = os.Unsetenv(name) })
	}

	result := SyncEnvFromStore(basePath, OAuthRefreshEnvBindings())
	if len(result.Missing) != 0 {
		t.Fatalf("missing = %v", result.Missing)
	}
	if got := os.Getenv("GOOGLE_CLIENT_ID"); got != "google-client" {
		t.Fatalf("client id = %q", got)
	}
	if got := os.Getenv("GOOGLE_CLIENT_SECRET"); got != "google-secret" {
		t.Fatalf("client secret = %q", got)
	}
	if got := os.Getenv("GOOGLE_REFRESH_TOKEN"); got != "google-refresh" {
		t.Fatalf("refresh token = %q", got)
	}
	if got := os.Getenv("GOOGLE_ACCESS_TOKEN_EXPIRES_AT"); got != expiry.Format(time.RFC3339) {
		t.Fatalf("expiry = %q, want %q", got, expiry.Format(time.RFC3339))
	}
}

// Parity-exempt: an unset expiry variable must stay unset so the refresher
// treats the token as "unknown expiry" rather than "expired".
func TestOAuthRefreshEnvBindingsSkipMissingExpiry(t *testing.T) {
	basePath := newEnvSyncStore(t)
	storage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	credential := NewCredentialObject("hubspot")
	credential.Keys.Set(NewCredentialKey("refresh_token", "hubspot-refresh"))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	t.Setenv("HUBSPOT_REFRESH_TOKEN", "")
	t.Setenv("HUBSPOT_ACCESS_TOKEN_EXPIRES_AT", "")

	SyncEnvFromStore(basePath, OAuthRefreshEnvBindings())
	if got := os.Getenv("HUBSPOT_REFRESH_TOKEN"); got != "hubspot-refresh" {
		t.Fatalf("refresh token = %q", got)
	}
	if got := os.Getenv("HUBSPOT_ACCESS_TOKEN_EXPIRES_AT"); got != "" {
		t.Fatalf("expiry = %q, want unset", got)
	}
}

// Parity-exempt: binding names must be unique or the sync loop would publish
// one value under another credential's variable.
func TestOAuthRefreshEnvBindingsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, binding := range OAuthRefreshEnvBindings() {
		if binding.CredentialID == "" || binding.EnvVar == "" {
			t.Fatalf("incomplete binding: %+v", binding)
		}
		if binding.CredentialKey == "" && binding.ExpiryKey == "" {
			t.Fatalf("binding reads nothing: %+v", binding)
		}
		if seen[binding.EnvVar] {
			t.Fatalf("duplicate env var %q", binding.EnvVar)
		}
		seen[binding.EnvVar] = true
	}
	if len(seen) != 4*3 {
		t.Fatalf("bindings = %d, want 12", len(seen))
	}
}
