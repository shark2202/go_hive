package cli

// The session's credential view is the production wiring behind
// get_account_info and the `account` alias selector. These tests drive the real
// catalogue (and the shared tool registry the integrations install into) over a
// temporary encrypted credential store — no network, no host credentials.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations"
)

// sessionCredentialHome prepares a hermetic hive home: HIVE_HOME points at the
// temp dir so the persisted key file never escapes the test, and a fixed key is
// exported so the registry that saves accounts and the session view that reads
// them decrypt the same store.
func sessionCredentialHome(t *testing.T) hivehome.Home {
	t.Helper()
	root := testTempDir(t)
	t.Setenv("HIVE_HOME", root)
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate fernet key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	return hivehome.Home{Root: root}
}

// saveLocalAccounts writes named local API-key accounts the way the
// Integrations page does (POST /api/credentials/local).
func saveLocalAccounts(t *testing.T, home hivehome.Home, accounts ...[3]string) {
	t.Helper()
	registry, err := credentials.LocalCredentialRegistryAtPath(home.CredentialsDir())
	if err != nil {
		t.Fatalf("open local credential registry: %v", err)
	}
	for _, account := range accounts {
		if _, err := registry.SaveAccount(account[0], account[1], account[2], false, nil); err != nil {
			t.Fatalf("save account %v: %v", account, err)
		}
	}
}

// runAccountInfo executes get_account_info through the session catalogue and
// decodes the JSON payload the model sees.
func runAccountInfo(t *testing.T, catalogue *sessionToolCatalogue, input map[string]any) map[string]any {
	t.Helper()
	content, isError, err := catalogue.Execute(context.Background(), "get_account_info", input)
	if err != nil {
		t.Fatalf("get_account_info: %v", err)
	}
	if isError {
		t.Fatalf("get_account_info reported an error: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("decode %q: %v", content, err)
	}
	return payload
}

// accountInfoCount reads the numeric count from a decoded payload.
func accountInfoCount(t *testing.T, payload map[string]any) int {
	t.Helper()
	count, ok := payload["count"].(float64)
	if !ok {
		t.Fatalf("count = %v", payload["count"])
	}
	return int(count)
}

// accountInfoAccounts reads the accounts array from a decoded payload.
func accountInfoAccounts(t *testing.T, payload map[string]any) []any {
	t.Helper()
	accounts, ok := payload["accounts"].([]any)
	if !ok {
		t.Fatalf("accounts = %T", payload["accounts"])
	}
	return accounts
}

// Parity-exempt: upstream returns this shape when credentials is None; the Go
// session always wires a view, so "no credentials" must be an empty list.
func TestSessionAccountInfoWithoutCredentials(t *testing.T) {
	home := sessionCredentialHome(t)
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)

	payload := runAccountInfo(t, catalogue, nil)
	if count := accountInfoCount(t, payload); count != 0 {
		t.Fatalf("count = %d, payload = %v", count, payload)
	}
	if accounts := accountInfoAccounts(t, payload); len(accounts) != 0 {
		t.Fatalf("accounts = %v", accounts)
	}
	if _, configured := payload["message"]; configured {
		t.Fatalf("empty store must answer an empty list, not %q", payload["message"])
	}
}

// Parity-exempt: upstream get_account_info has no Python test; shape ported from
// tools/src/aden_tools/tools/account_info_tool/account_info_tool.py.
func TestSessionAccountInfoListsConnectedAccounts(t *testing.T) {
	home := sessionCredentialHome(t)
	saveLocalAccounts(t, home, [3]string{"hubspot", "default", "hubspot-default-token"})
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)

	payload := runAccountInfo(t, catalogue, nil)
	if count := accountInfoCount(t, payload); count != 1 {
		t.Fatalf("count = %d, payload = %v", count, payload)
	}
	account, ok := accountInfoAccounts(t, payload)[0].(map[string]any)
	if !ok {
		t.Fatalf("account = %T", accountInfoAccounts(t, payload)[0])
	}
	if account["provider"] != "hubspot" || account["alias"] != "default" {
		t.Fatalf("account = %v", account)
	}
	if account["source"] != "local" {
		t.Fatalf("source = %v", account["source"])
	}
}

// Parity-exempt: upstream get_account_info(provider=...) filters by provider;
// this exercises the multi-account store the local registry produces.
func TestSessionAccountInfoFiltersMultipleAccounts(t *testing.T) {
	home := sessionCredentialHome(t)
	saveLocalAccounts(t, home,
		[3]string{"hubspot", "work", "hubspot-work-token"},
		[3]string{"hubspot", "sandbox", "hubspot-sandbox-token"},
		[3]string{"google", "work", "google-work-token"},
	)
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)

	all := runAccountInfo(t, catalogue, nil)
	if count := accountInfoCount(t, all); count != 3 {
		t.Fatalf("all count = %d, payload = %v", count, all)
	}
	hubspotAccounts := runAccountInfo(t, catalogue, map[string]any{"provider": "hubspot"})
	if count := accountInfoCount(t, hubspotAccounts); count != 2 {
		t.Fatalf("hubspot count = %d, payload = %v", count, hubspotAccounts)
	}
	googleAccounts := runAccountInfo(t, catalogue, map[string]any{"provider": "google"})
	if count := accountInfoCount(t, googleAccounts); count != 1 {
		t.Fatalf("google count = %d, payload = %v", count, googleAccounts)
	}
	missingAccounts := runAccountInfo(t, catalogue, map[string]any{"provider": "discord"})
	if count := accountInfoCount(t, missingAccounts); count != 0 {
		t.Fatalf("discord count = %d, payload = %v", count, missingAccounts)
	}
}

// Parity: tools/tests/test_credentials_tool.py::test_adapter_resolves_collected_local_account
func TestSessionAccountSourceResolvesAliasTokens(t *testing.T) {
	home := sessionCredentialHome(t)
	saveLocalAccounts(t, home,
		[3]string{"hubspot", "work", "hubspot-work-token"},
		[3]string{"hubspot", "sandbox", "hubspot-sandbox-token"},
	)
	source := newSessionAccountSource(home)

	for _, want := range [][2]string{
		{"work", "hubspot-work-token"},
		{"sandbox", "hubspot-sandbox-token"},
	} {
		token, ok := source.AccountToken("hubspot", want[0])
		if !ok || token != want[1] {
			t.Fatalf("AccountToken(hubspot, %s) = %q, %v", want[0], token, ok)
		}
	}
	if _, ok := source.AccountToken("hubspot", "missing"); ok {
		t.Fatal("unknown alias resolved")
	}
	if _, ok := source.AccountToken("", "work"); ok {
		t.Fatal("empty provider resolved")
	}
	if _, ok := source.AccountToken("discord", "work"); ok {
		t.Fatal("alias leaked across providers")
	}

	accounts := source.Accounts("hubspot")
	if len(accounts) != 2 {
		t.Fatalf("accounts = %v", accounts)
	}
}

// Parity-exempt: the alias path mirrors upstream _get_token(account) ->
// get_by_alias("mattermost", account), which has no Python test; this proves the
// session wiring (shared registry -> batch runtime -> auth header).
func TestSessionAccountAliasRoutesThroughSharedRegistry(t *testing.T) {
	home := sessionCredentialHome(t)
	saveLocalAccounts(t, home, [3]string{"mattermost", "work", "mattermost-work-token"})

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	// Mattermost derives its base URL from MATTERMOST_URL at call time, so the
	// real batch installer (no test hooks) can be pointed at the fake server.
	t.Setenv("MATTERMOST_URL", server.URL)

	// The session attaches its view to the shared registry; the integration
	// batch is installed afterwards, exactly like the catalogue does.
	registry := tools.NewToolRegistry()
	registry.SetAccountSource(newSessionAccountSource(home))
	integrations.InstallBatch7Lane8(registry)

	result, err := registry.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name: "mattermost",
		Input: map[string]any{
			"action":  "mattermost_list_teams",
			"account": "work",
		},
	})
	if err != nil {
		t.Fatalf("mattermost call: %v", err)
	}
	if result.IsError {
		t.Fatalf("mattermost call failed: %s", result.Content)
	}
	if authorization != "Bearer mattermost-work-token" {
		t.Fatalf("Authorization = %q", authorization)
	}

	// Without an alias and without MATTERMOST_ACCESS_TOKEN the module has no
	// credential: the account must not be picked implicitly.
	result, err = registry.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name:  "mattermost",
		Input: map[string]any{"action": "mattermost_list_teams"},
	})
	if err != nil {
		t.Fatalf("mattermost call: %v", err)
	}
	var failure map[string]any
	if err := json.Unmarshal([]byte(result.Content), &failure); err != nil {
		t.Fatalf("decode %q: %v", result.Content, err)
	}
	if failure["error"] != "Mattermost credentials not configured" {
		t.Fatalf("no-alias result = %v", failure)
	}
}

// saveEncryptedAccounts writes OAuth-managed accounts the way the credential
// store does: _integration_type + _alias inside the encrypted record, so the
// account is reachable through the provider/alias index.
func saveEncryptedAccounts(t *testing.T, home hivehome.Home, creds ...*credentials.CredentialObject) {
	t.Helper()
	storage, err := credentials.NewEncryptedFileStorage(home.CredentialsDir(), "")
	if err != nil {
		t.Fatalf("open encrypted store: %v", err)
	}
	for _, cred := range creds {
		if err := storage.Save(cred); err != nil {
			t.Fatalf("save %s: %v", cred.ID, err)
		}
	}
}

// oauthSessionAccount builds one OAuth-managed account record.
func oauthSessionAccount(id, provider, alias, token string) *credentials.CredentialObject {
	cred := credentials.NewCredentialObject(id)
	cred.CredentialType = credentials.CredentialTypeOAuth2
	cred.Keys.Set(credentials.NewCredentialKey("access_token", token))
	cred.Keys.Set(credentials.NewCredentialKey("_alias", alias))
	cred.Keys.Set(credentials.NewCredentialKey("_integration_type", provider))
	return cred
}

// Parity-exempt: upstream resolves OAuth-managed multi-accounts through
// AdenCachedStorage.load_by_alias; the Go encrypted-store index is the new
// wiring and has no Python pytest counterpart.
func TestSessionAccountSourceResolvesOAuthManagedAliases(t *testing.T) {
	home := sessionCredentialHome(t)
	saveEncryptedAccounts(t, home,
		oauthSessionAccount("google_work", "google", "work", "oauth-work-token"),
		oauthSessionAccount("google_sandbox", "google", "sandbox", "oauth-sandbox-token"),
	)
	source := newSessionAccountSource(home)

	for _, want := range [][2]string{
		{"work", "oauth-work-token"},
		{"sandbox", "oauth-sandbox-token"},
	} {
		token, ok := source.AccountToken("google", want[0])
		if !ok || token != want[1] {
			t.Fatalf("AccountToken(google, %s) = %q, %v", want[0], token, ok)
		}
	}
	if _, ok := source.AccountToken("google", "missing"); ok {
		t.Fatal("unknown alias resolved")
	}
	if _, ok := source.AccountToken("github", "work"); ok {
		t.Fatal("alias leaked across providers")
	}
}

// Parity-exempt: when the encrypted index has no (provider, alias) record — a
// legacy account whose id carries the alias but whose record has no _alias key
// — resolution degrades to the local account registry (upstream
// store.get_credential_by_alias returns None and the adapter falls back).
func TestSessionAccountSourceFallsBackToLocalRegistry(t *testing.T) {
	home := sessionCredentialHome(t)
	storage, err := credentials.NewEncryptedFileStorage(home.CredentialsDir(), "")
	if err != nil {
		t.Fatalf("open encrypted store: %v", err)
	}
	legacy := credentials.NewCredentialObject("google/legacy")
	legacy.Keys.Set(credentials.NewCredentialKey("api_key", "legacy-token"))
	legacy.Keys.Set(credentials.NewCredentialKey("_integration_type", "google"))
	if err := storage.Save(legacy); err != nil {
		t.Fatalf("save legacy account: %v", err)
	}

	source := newSessionAccountSource(home)
	token, ok := source.AccountToken("google", "legacy")
	if !ok || token != "legacy-token" {
		t.Fatalf("AccountToken(google, legacy) = %q, %v", token, ok)
	}
	if _, ok := source.AccountToken("google", "missing"); ok {
		t.Fatal("unknown alias resolved")
	}
}
