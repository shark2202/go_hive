package server

// Deterministic Aden fake for the resync route: no network, no real
// ADEN_API_KEY. The same client is injected into both the force-presync and
// the store upgrade so the configured path is observable end to end.

import (
	"net/http"
	"os"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type resyncFakeAdenClient struct {
	integrations []credentials.AdenIntegrationInfo
	responses    map[string]*credentials.AdenCredentialResponse
	listErr      error
	getErr       error

	listCalls int
	getCalls  int
	closed    bool
}

func (c *resyncFakeAdenClient) ListIntegrations() ([]credentials.AdenIntegrationInfo, error) {
	c.listCalls++
	if c.listErr != nil {
		return nil, c.listErr
	}
	return append([]credentials.AdenIntegrationInfo{}, c.integrations...), nil
}

func (c *resyncFakeAdenClient) GetCredential(integrationID string) (*credentials.AdenCredentialResponse, error) {
	c.getCalls++
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.responses[integrationID], nil
}

func (c *resyncFakeAdenClient) RequestRefresh(string) (*credentials.AdenCredentialResponse, error) {
	return nil, &credentials.AdenClientError{Message: "not implemented in resync fake"}
}

func (c *resyncFakeAdenClient) ValidateToken(string) (map[string]any, error) {
	return map[string]any{"valid": true}, nil
}

func (c *resyncFakeAdenClient) Close() error {
	c.closed = true
	return nil
}

func newResyncAdenHome(t *testing.T) (hivehome.Home, *resyncFakeAdenClient) {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	t.Setenv("HIVE_HOME", home.Root)
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	// The force presync exports GITHUB_TOKEN; register the cleanup before any
	// test writes it so the value cannot leak into env-availability tests.
	t.Setenv("GITHUB_TOKEN", "")
	client := &resyncFakeAdenClient{
		integrations: []credentials.AdenIntegrationInfo{{
			IntegrationID: "hash-github",
			Provider:      "github",
			Alias:         "default",
			Status:        "active",
		}},
		responses: map[string]*credentials.AdenCredentialResponse{
			"hash-github": {
				IntegrationID: "hash-github",
				AccessToken:   "gh-token",
				Provider:      "github",
				Alias:         "default",
			},
		},
	}
	return home, client
}

// Parity: core/framework/server/routes_credentials.py::handle_resync_credentials
func TestCredentialRoutesResyncForceFetchesAdenWhenConfigured(t *testing.T) {
	home, client := newResyncAdenHome(t)
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	t.Setenv("GITHUB_TOKEN", "")

	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:       home,
		AdenClient: client,
		Accounts: func() map[string][]map[string]any {
			return map[string][]map[string]any{"github": {{"provider": "github", "alias": "default"}}}
		},
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/resync", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["synced"] != true {
		t.Fatalf("synced = %#v", body["synced"])
	}
	if client.getCalls == 0 {
		t.Fatal("configured resync did not force-fetch an Aden credential")
	}
	if got := credentialRouteJSONMap(t, body["accounts_by_provider"]); len(got) != 1 {
		t.Fatalf("accounts_by_provider = %#v", got)
	}
	// The force presync exports the token under the spec's env var.
	if got := os.Getenv("GITHUB_TOKEN"); got != "gh-token" {
		t.Fatalf("GITHUB_TOKEN = %q, want gh-token", got)
	}
}

// Parity: core/framework/server/routes_credentials.py::handle_resync_credentials
func TestResyncAdenCredentialsConvergesLiveStore(t *testing.T) {
	home, client := newResyncAdenHome(t)
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	store := credentials.NewCredentialStore(credentials.WithStorage(credentials.NewInMemoryStorage(nil)))
	routes := &CredentialRoutes{
		deps:      CredentialRouteDeps{Home: home, AdenClient: client, Store: store},
		homeGiven: true,
	}
	if err := routes.resyncAdenCredentials(); err != nil {
		t.Fatalf("resyncAdenCredentials: %v", err)
	}
	credential, err := store.GetCredential("hash-github", false)
	if err != nil || credential == nil {
		t.Fatalf("live store credential = %#v, %v", credential, err)
	}
	if token, _ := credential.GetKeyValue("access_token"); token != "gh-token" {
		t.Fatalf("live store token = %q", token)
	}
	if store.GetProvider("aden_sync") == nil {
		t.Fatal("resync did not register the Aden provider on the live store")
	}
}

// Parity-exempt: downstream wrapper pins the no-key branch of the new default; the reference route returns 400 before its presync runs, which the existing RequiresAdenKey test covers.
func TestResyncCredentialsWithoutKeyDoesNotCallAden(t *testing.T) {
	home, client := newResyncAdenHome(t)
	t.Setenv(credentials.AdenEnvVar, "")
	routes := &CredentialRoutes{deps: CredentialRouteDeps{Home: home, AdenClient: client}, homeGiven: true}
	if err := routes.resyncCredentials(); err != nil {
		t.Fatalf("resyncCredentials: %v", err)
	}
	if client.listCalls != 0 || client.getCalls != 0 {
		t.Fatalf("unconfigured resync touched Aden: list=%d get=%d", client.listCalls, client.getCalls)
	}
}
