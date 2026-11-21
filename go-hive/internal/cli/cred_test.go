package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/tracker"
)

type aliasStorage struct {
	*credentials.InMemoryStorage
	creds []*credentials.CredentialObject
}

func newAliasStorage(creds ...*credentials.CredentialObject) *aliasStorage {
	initial := map[string]*credentials.CredentialObject{}
	for _, cred := range creds {
		initial[cred.ID] = cred
	}
	return &aliasStorage{
		InMemoryStorage: credentials.NewInMemoryStorage(initial),
		creds:           creds,
	}
}

func (s *aliasStorage) LoadAllForProvider(providerName string) ([]*credentials.CredentialObject, error) {
	out := make([]*credentials.CredentialObject, 0, len(s.creds))
	for _, cred := range s.creds {
		if providerType := cred.ProviderType(); providerType != nil && *providerType == providerName {
			out = append(out, cred)
		}
	}
	return out, nil
}

func (s *aliasStorage) LoadByAlias(providerName, alias string) (*credentials.CredentialObject, error) {
	for _, cred := range s.creds {
		providerType := cred.ProviderType()
		credAlias := cred.Alias()
		if providerType != nil && *providerType == providerName && credAlias != nil && *credAlias == alias {
			return cred, nil
		}
	}
	return nil, nil
}

type fakeOAuthProvider struct {
	fail      bool
	refreshes int
}

func (p *fakeOAuthProvider) ProviderID() string { return "fake_oauth" }

func (p *fakeOAuthProvider) SupportedTypes() []credentials.CredentialType {
	return []credentials.CredentialType{credentials.CredentialTypeOAuth2}
}

func (p *fakeOAuthProvider) Refresh(credential *credentials.CredentialObject) (*credentials.CredentialObject, error) {
	p.refreshes++
	if p.fail {
		return nil, &credentials.CredentialRefreshError{}
	}
	fresh := cloneCredential(credential)
	expiresAt := time.Now().UTC().Add(time.Hour)
	fresh.SetKey("access_token", "ya29.FRESH", &expiresAt, nil)
	return fresh, nil
}

func (p *fakeOAuthProvider) Validate(*credentials.CredentialObject) bool { return true }

func (p *fakeOAuthProvider) ShouldRefresh(credential *credentials.CredentialObject) bool {
	key := credential.Keys.Get("access_token")
	return key != nil && key.ExpiresAt != nil && !time.Now().UTC().Before(*key.ExpiresAt)
}

func (p *fakeOAuthProvider) Revoke(*credentials.CredentialObject) bool { return false }

func (p *fakeOAuthProvider) CanHandle(credential *credentials.CredentialObject) bool {
	return credential.CredentialType == credentials.CredentialTypeOAuth2
}

func cloneCredential(cred *credentials.CredentialObject) *credentials.CredentialObject {
	clone := *cred
	keys := make([]*credentials.CredentialKey, 0, cred.Keys.Len())
	for _, key := range cred.Keys.Values() {
		copied := *key
		keys = append(keys, &copied)
	}
	clone.Keys = credentials.NewKeyMap(keys...)
	return &clone
}

func googleCred(alias string, expiresIn time.Duration) *credentials.CredentialObject {
	expiresAt := time.Now().UTC().Add(expiresIn)
	cred := credentials.NewCredentialObject("google_" + alias)
	cred.CredentialType = credentials.CredentialTypeOAuth2
	cred.ProviderID = "fake_oauth"
	cred.AutoRefresh = true
	cred.Keys.Set(&credentials.CredentialKey{Name: "access_token", Value: "ya29.STALE", ExpiresAt: &expiresAt})
	cred.Keys.Set(credentials.NewCredentialKey("_alias", alias))
	cred.Keys.Set(credentials.NewCredentialKey("_integration_type", "google"))
	return cred
}

type fakeCredAdapter struct {
	store    *credentials.CredentialStore
	accounts []map[string]any
}

func (a *fakeCredAdapter) Store() *credentials.CredentialStore { return a.store }

func (a *fakeCredAdapter) GetAllAccountInfo() []map[string]any { return a.accounts }

func runCred(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	// CredRunner.Run calls setupCredLogging, which installs process-global log
	// wiring (credentials.SetLogSink + log.SetOutput/SetFlags). Give each test
	// its own sink file and restore the globals on cleanup so an ambient
	// HIVE_CRED_LOG or a previous test's wiring cannot bleed into this one or
	// into later tests in the same binary.
	t.Setenv("HIVE_CRED_LOG", filepath.Join(t.TempDir(), "cred.log"))
	previousWriter, previousFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		credentials.SetLogSink(nil)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	var stdout, stderr bytes.Buffer
	runner := &CredRunner{Stdout: &stdout, Stderr: &stderr}
	code := runner.Run(argv)
	return code, stdout.String(), stderr.String()
}

func useCredAdapterFactory(t *testing.T, factory func() (credAdapter, error)) {
	t.Helper()
	original := credAdapterDefault
	credAdapterDefault = factory
	t.Cleanup(func() { credAdapterDefault = original })
}

func useCredAdapter(t *testing.T, adapter credAdapter) {
	t.Helper()
	useCredAdapterFactory(t, func() (credAdapter, error) { return adapter, nil })
}

func useLocalRegistry(t *testing.T, registry credentials.LocalRegistry) {
	t.Helper()
	original := credentials.LocalRegistryDefault
	credentials.LocalRegistryDefault = func() credentials.LocalRegistry { return registry }
	t.Cleanup(func() { credentials.LocalRegistryDefault = original })
}

func emptyLocalRegistry() credentials.LocalRegistry {
	return credentials.NewLocalCredentialRegistry(credentials.NewInMemoryStorage(nil))
}

func newStaleStore(provider *fakeOAuthProvider, cred *credentials.CredentialObject) *credentials.CredentialStore {
	return credentials.NewCredentialStore(
		credentials.WithStorage(newAliasStorage(cred)),
		credentials.WithProviders(provider),
		credentials.WithAutoRefresh(true),
	)
}

func decodeCredJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("decode JSON output %q: %v", out, err)
	}
	return body
}

func credErrorBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body = %#v, want an object", body["error"])
	}
	return raw
}

// Parity: tests/test_hive_cred_cli.py::test_token_refreshes_expired_oauth_before_printing
func TestTokenRefreshesExpiredOAuthBeforePrinting(t *testing.T) {
	provider := &fakeOAuthProvider{}
	useCredAdapter(t, &fakeCredAdapter{store: newStaleStore(provider, googleCred("email3", -5*time.Minute))})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, stderr := runCred(t, "token", "google", "--account", "email3", "--json")
	if code != 0 {
		t.Fatalf("Run(token --json) = %d, want 0 (stderr %q)", code, stderr)
	}
	body := decodeCredJSON(t, stdout)
	if body["ok"] != true {
		t.Fatalf("ok = %v, want true", body["ok"])
	}
	if body["value"] != "ya29.FRESH" {
		t.Fatalf("value = %v, want the refreshed token (the stale on-disk token must never be handed out)", body["value"])
	}
	if provider.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", provider.refreshes)
	}
	seconds, ok := body["expires_in_seconds"].(float64)
	if !ok || seconds <= 3000 {
		t.Fatalf("expires_in_seconds = %v, want > 3000", body["expires_in_seconds"])
	}
	if body["account"] != "email3" || body["key"] != "access_token" {
		t.Fatalf("account/key = %v/%v, want email3/access_token", body["account"], body["key"])
	}
}

// Parity: tests/test_hive_cred_cli.py::test_plain_output_is_the_value_only
func TestPlainOutputIsTheValueOnly(t *testing.T) {
	provider := &fakeOAuthProvider{}
	useCredAdapter(t, &fakeCredAdapter{store: newStaleStore(provider, googleCred("email3", -5*time.Minute))})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, stderr := runCred(t, "token", "google", "--account", "email3")
	if code != 0 {
		t.Fatalf("Run(token) = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "ya29.FRESH\n" {
		t.Fatalf("stdout = %q, want exactly the token plus newline", stdout)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_refresh_failure_is_a_reauth_exit_not_a_stale_token
func TestRefreshFailureIsAReauthExitNotAStaleToken(t *testing.T) {
	provider := &fakeOAuthProvider{fail: true}
	useCredAdapter(t, &fakeCredAdapter{store: newStaleStore(provider, googleCred("email3", -5*time.Minute))})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, _ := runCred(t, "token", "google", "--account", "email3", "--json")
	if code != tracker.ExitDomain {
		t.Fatalf("Run(token) = %d, want %d (EXIT_REAUTH)", code, tracker.ExitDomain)
	}
	body := decodeCredJSON(t, stdout)
	if body["ok"] != false || credErrorBody(t, body)["code"] != "reauth_required" {
		t.Fatalf("body = %#v, want ok=false and error.code=reauth_required", body)
	}
	if strings.Contains(stdout, "ya29") {
		t.Fatalf("stdout = %q, must not leak the stale token", stdout)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_unknown_account_is_not_found
func TestUnknownAccountIsNotFound(t *testing.T) {
	provider := &fakeOAuthProvider{}
	useCredAdapter(t, &fakeCredAdapter{store: newStaleStore(provider, googleCred("email3", -5*time.Minute))})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, _ := runCred(t, "token", "google", "--account", "Timothy", "--json")
	if code != tracker.ExitNotFound {
		t.Fatalf("Run(token --account Timothy) = %d, want %d", code, tracker.ExitNotFound)
	}
	body := decodeCredJSON(t, stdout)
	if credErrorBody(t, body)["code"] != "not_found" {
		t.Fatalf("error.code = %v, want not_found", credErrorBody(t, body)["code"])
	}
}

// Parity: tests/test_hive_cred_cli.py::test_not_found_names_the_id_namespace_that_actually_works
func TestNotFoundNamesTheIDNamespaceThatActuallyWorks(t *testing.T) {
	provider := &fakeOAuthProvider{}
	useCredAdapter(t, &fakeCredAdapter{store: newStaleStore(provider, googleCred("email3", -5*time.Minute))})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, _ := runCred(t, "token", "Z29vZ2xlOnJpY2hhcmQ6MTg3Njc6MTYzNzc", "--account", "richard", "--json")
	if code != tracker.ExitNotFound {
		t.Fatalf("Run(token opaque-id) = %d, want %d", code, tracker.ExitNotFound)
	}
	body := decodeCredJSON(t, stdout)
	message, _ := credErrorBody(t, body)["message"].(string)
	if !strings.Contains(message, "credential name") || !strings.Contains(message, "opaque") {
		t.Fatalf("message = %q, want it to name the credential-name namespace and warn about opaque ids", message)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_known_credential_ids_hint_reads_the_same_field_listings_report
func TestKnownCredentialIDsHintReadsTheSameField(t *testing.T) {
	useCredAdapter(t, &fakeCredAdapter{accounts: []map[string]any{
		{"credential_id": "google"},
		{"credential_id": "slack_token"},
		{"credential_id": "google"},
	}})
	if got := knownCredentialIDsHint(); got != " Known credential ids: google, slack_token." {
		t.Fatalf("hint = %q, want the deduplicated sorted ids", got)
	}

	useCredAdapterFactory(t, func() (credAdapter, error) { return nil, errors.New("store down") })
	if got := knownCredentialIDsHint(); got != "" {
		t.Fatalf("hint on adapter failure = %q, want empty", got)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_console_script_registered
func TestConsoleScriptRegistered(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "hive-cred", "main.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(data)
	if !strings.Contains(text, "package main") || !strings.Contains(text, "cli.NewCredRunner()") {
		t.Fatalf("%s does not wire the credential runner:\n%s", path, text)
	}
}

func accountsFixture(t *testing.T) *fakeCredAdapter {
	t.Helper()
	store := credentials.NewCredentialStore(
		credentials.WithStorage(newAliasStorage(googleCred("email3", time.Hour))),
		credentials.WithProviders(&fakeOAuthProvider{}),
		credentials.WithAutoRefresh(true),
	)
	return &fakeCredAdapter{
		store: store,
		accounts: []map[string]any{
			{"provider": "google", "alias": "email3", "identity": map[string]any{"email": "vincent@acho.io"}, "source": "aden", "status": "active"},
			{"provider": "google", "alias": "primary", "identity": map[string]any{"email": "vincent@adenhq.com"}, "source": "aden", "status": "active"},
		},
	}
}

// Parity: tests/test_hive_cred_cli.py::test_browse_lists_accounts_but_never_a_value
func TestBrowseListsAccountsButNeverAValue(t *testing.T) {
	useCredAdapter(t, accountsFixture(t))

	code, stdout, stderr := runCred(t, "browse", "--json")
	if code != 0 {
		t.Fatalf("Run(browse --json) = %d, want 0 (stderr %q)", code, stderr)
	}
	body := decodeCredJSON(t, stdout)
	if body["ok"] != true {
		t.Fatalf("ok = %v, want true", body["ok"])
	}
	connected, ok := body["connected"].([]any)
	if !ok {
		t.Fatalf("connected = %#v, want an array", body["connected"])
	}
	aliases := map[string]bool{}
	for _, item := range connected {
		entry, _ := item.(map[string]any)
		alias, _ := entry["alias"].(string)
		aliases[alias] = true
	}
	if !reflect.DeepEqual(aliases, map[string]bool{"email3": true, "primary": true}) {
		t.Fatalf("aliases = %v, want email3 and primary", aliases)
	}
	if len(connected) == 0 {
		t.Fatal("connected is empty, want at least one account")
	}
	first, _ := connected[0].(map[string]any)
	if !reflect.DeepEqual(first["identity"], map[string]any{"email": "vincent@acho.io"}) {
		t.Fatalf("connected[0].identity = %#v", first["identity"])
	}
	if strings.Contains(stdout, "ya29") {
		t.Fatalf("stdout = %q, must never contain a value", stdout)
	}

	code, stdout, _ = runCred(t, "browse", "acho")
	if code != 0 {
		t.Fatalf("Run(browse acho) = %d, want 0", code)
	}
	if !strings.Contains(stdout, `google "email3"`) || strings.Contains(stdout, "primary") {
		t.Fatalf("stdout = %q, want the acho account and not primary", stdout)
	}
	if strings.Contains(stdout, "ya29") {
		t.Fatalf("stdout = %q, must never contain a value", stdout)
	}
}

// Parity: framework/credentials/catalog.py::browse (connectable list cap + truncation)
func TestBrowseConnectableUsesCatalogSpecs(t *testing.T) {
	useCredAdapter(t, &fakeCredAdapter{store: nil})

	code, stdout, stderr := runCred(t, "browse", "--json")
	if code != 0 {
		t.Fatalf("Run(browse --json) = %d, want 0 (stderr %q)", code, stderr)
	}
	body := decodeCredJSON(t, stdout)
	connectable, ok := body["connectable"].([]any)
	if !ok {
		t.Fatalf("connectable = %#v, want an array", body["connectable"])
	}
	// Upstream caps the list at 40 and reports the rest via
	// connectable_truncated; the row set is the merged CREDENTIAL_SPECS dict.
	if len(connectable) != 40 {
		t.Fatalf("connectable length = %d, want 40", len(connectable))
	}
	truncated, _ := body["connectable_truncated"].(float64)
	if truncated <= 0 {
		t.Fatalf("connectable_truncated = %v, want > 0", body["connectable_truncated"])
	}
	previous := ""
	for _, item := range connectable {
		entry, _ := item.(map[string]any)
		id, _ := entry["credential_id"].(string)
		if id == "" || previous > id {
			t.Fatalf("connectable ids must be sorted, got %q after %q", id, previous)
		}
		previous = id
	}
}

// Parity: tests/test_hive_cred_cli.py::test_inspect_shows_key_names_not_values
func TestInspectShowsKeyNamesNotValues(t *testing.T) {
	useCredAdapter(t, accountsFixture(t))

	code, stdout, stderr := runCred(t, "inspect", "google_email3", "--json")
	if code != 0 {
		t.Fatalf("Run(inspect --json) = %d, want 0 (stderr %q)", code, stderr)
	}
	body := decodeCredJSON(t, stdout)
	if !reflect.DeepEqual(body["keys"], []any{"access_token"}) {
		t.Fatalf("keys = %#v, want [access_token]", body["keys"])
	}
	if body["type"] != "oauth2" {
		t.Fatalf("type = %v, want oauth2", body["type"])
	}
	if strings.Contains(stdout, "ya29") {
		t.Fatalf("stdout = %q, must never contain a value", stdout)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_token_resolves_a_collected_local_account
func TestTokenResolvesACollectedLocalAccount(t *testing.T) {
	registry := credentials.NewLocalCredentialRegistry(credentials.NewInMemoryStorage(nil))
	if _, err := registry.SaveAccount("stripe", "default", "sk_local_value", false, nil); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	useLocalRegistry(t, registry)
	useCredAdapter(t, &fakeCredAdapter{store: credentials.NewCredentialStore(
		credentials.WithStorage(newAliasStorage()),
	)})

	code, stdout, stderr := runCred(t, "token", "stripe")
	if code != 0 || stdout != "sk_local_value\n" {
		t.Fatalf("Run(token stripe) = %d, %q, want 0, %q (stderr %q)", code, stdout, "sk_local_value\n", stderr)
	}

	code, stdout, stderr = runCred(t, "token", "stripe", "--account", "default")
	if code != 0 || stdout != "sk_local_value\n" {
		t.Fatalf("Run(token stripe --account default) = %d, %q, want 0, %q (stderr %q)", code, stdout, "sk_local_value\n", stderr)
	}
}
