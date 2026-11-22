package server

// Production wiring for POST /api/credentials/resync: with a caller-supplied
// Home and no explicit Resync hook, the local default is armed so the route no
// longer answers 500. Upstream's Aden token fetch has no Go counterpart yet
// (separate backlog item); the local default re-reads the bootstrap keys and
// re-runs the store → env bridge.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: core/framework/server/routes_credentials.py::handle_resync_credentials
func TestCredentialRoutesResyncDefaultHookArmed(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	t.Setenv("HIVE_HOME", home.Root)
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:     home,
		Accounts: func() map[string][]map[string]any { return map[string][]map[string]any{} },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/resync", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["synced"] != true {
		t.Fatalf("synced = %#v", body["synced"])
	}
	if got := len(credentialRouteJSONMap(t, body["accounts_by_provider"])); got != 0 {
		t.Fatalf("accounts_by_provider = %d", got)
	}
}

// The local resync default re-reads a key another process persisted (the
// observable part of the reference's force-resync: a saved credential becomes
// visible without a restart).
// Parity: core/framework/server/routes_credentials.py::handle_resync_credentials
func TestResyncLocalCredentialsReloadsPersistedKey(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	t.Setenv("HIVE_HOME", home.Root)
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	if err := os.MkdirAll(home.SecretsDir(), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.SecretsDir(), "credential_key"), []byte("persisted-key-material"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	routes := &CredentialRoutes{deps: CredentialRouteDeps{Home: home}, homeGiven: true}
	if err := routes.resyncLocalCredentials(); err != nil {
		t.Fatalf("resyncLocalCredentials: %v", err)
	}
	if got := os.Getenv(credentials.CredentialKeyEnvVar); got != "persisted-key-material" {
		t.Fatalf("HIVE_CREDENTIAL_KEY = %q after resync", got)
	}
}
