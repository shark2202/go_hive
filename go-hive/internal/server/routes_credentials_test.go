package server

// Parity: framework/server/tests/test_api.py::TestCredentials

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

type credentialRouteTestStore struct {
	ids         []string
	credentials map[string]*credentials.CredentialObject
	unreadable  map[string]bool
	saveErr     error
	deleted     map[string]bool
}

func (s *credentialRouteTestStore) ListCredentials() ([]string, error) {
	return s.ids, nil
}

func (s *credentialRouteTestStore) GetCredential(credentialID string, _ bool) (*credentials.CredentialObject, error) {
	if s.unreadable[credentialID] {
		return nil, &credentials.CredentialDecryptionError{}
	}
	return s.credentials[credentialID], nil
}

func (s *credentialRouteTestStore) SaveCredential(credential *credentials.CredentialObject) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.credentials == nil {
		s.credentials = map[string]*credentials.CredentialObject{}
	}
	s.credentials[credential.ID] = credential
	return nil
}

func (s *credentialRouteTestStore) DeleteCredential(credentialID string) (bool, error) {
	if s.deleted == nil {
		s.deleted = map[string]bool{}
	}
	deleted := s.deleted[credentialID]
	return deleted, nil
}

func credentialRouteTestMux(deps CredentialRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterCredentialRoutes(mux, deps)
	return mux
}

func credentialRouteTestObject(id string, keys map[string]string) *credentials.CredentialObject {
	credential := credentials.NewCredentialObject(id)
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	for _, name := range names {
		credential.Keys.Set(credentials.NewCredentialKey(name, keys[name]))
	}
	return credential
}

func credentialRouteJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	mapping, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want object", value)
	}
	return mapping
}

func credentialRouteJSONList(t *testing.T, value any) []any {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("value is %T, want list", value)
	}
	return list
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_list_credentials_empty
func TestCredentialRoutesListEmpty(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := len(credentialRouteJSONList(t, body["credentials"])); got != 0 {
		t.Fatalf("credentials = %d, want 0", got)
	}
	if got := len(credentialRouteJSONList(t, body["unreadable_credentials"])); got != 0 {
		t.Fatalf("unreadable_credentials = %d, want 0", got)
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_list_credentials_skips_unreadable_encrypted_entry
func TestCredentialRoutesListSkipsUnreadableEntry(t *testing.T) {
	store := &credentialRouteTestStore{
		ids:        []string{"good_cred", "bad_cred"},
		unreadable: map[string]bool{"bad_cred": true},
		credentials: map[string]*credentials.CredentialObject{
			"good_cred": credentialRouteTestObject("good_cred", map[string]string{"api_key": "secret"}),
		},
	}
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: store})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	list := credentialRouteJSONList(t, body["credentials"])
	if len(list) != 1 {
		t.Fatalf("credentials = %d, want 1", len(list))
	}
	if got := credentialRouteJSONMap(t, list[0])["credential_id"]; got != "good_cred" {
		t.Fatalf("credential_id = %v", got)
	}
	unreadable := credentialRouteJSONList(t, body["unreadable_credentials"])
	if len(unreadable) != 1 || unreadable[0] != "bad_cred" {
		t.Fatalf("unreadable_credentials = %#v", unreadable)
	}
	if strings.Contains(recorder.Body.String(), "secret") {
		t.Fatal("list response leaked a secret value")
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_get_credential_unreadable_returns_recoverable_conflict
func TestCredentialRoutesGetUnreadableReturnsConflict(t *testing.T) {
	store := &credentialRouteTestStore{unreadable: map[string]bool{"bad_cred": true}}
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: store})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/bad_cred", "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["credential_id"] != "bad_cred" {
		t.Fatalf("credential_id = %v", body["credential_id"])
	}
	if body["recoverable"] != true {
		t.Fatalf("recoverable = %v", body["recoverable"])
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_specs_availability_treats_decryption_error_as_unavailable
func TestCredentialSpecsAvailabilityTreatsDecryptionErrorAsUnavailable(t *testing.T) {
	store := &credentialRouteTestStore{unreadable: map[string]bool{"exa_search": true}}
	if isAvailableForSpecs(store, "exa_search") {
		t.Fatal("an unreadable credential must be reported unavailable")
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_save_and_list_credential
func TestCredentialRoutesSaveAndList(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"brave_search","keys":{"api_key":"test-key-123"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["saved"] != "brave_search" {
		t.Fatalf("saved = %v", body["saved"])
	}

	_, listed := doSessionJSON(t, mux, http.MethodGet, "/api/credentials", "")
	list := credentialRouteJSONList(t, listed["credentials"])
	if len(list) != 1 {
		t.Fatalf("credentials = %d, want 1", len(list))
	}
	entry := credentialRouteJSONMap(t, list[0])
	if entry["credential_id"] != "brave_search" {
		t.Fatalf("credential_id = %v", entry["credential_id"])
	}
	names := credentialRouteJSONList(t, entry["key_names"])
	if len(names) != 1 || names[0] != "api_key" {
		t.Fatalf("key_names = %#v", names)
	}
	if strings.Contains(recorder.Body.String(), "test-key-123") {
		t.Fatal("save response leaked a secret value")
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_get_credential
func TestCredentialRoutesGet(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store: credentials.ForTesting(map[string]map[string]string{"test_cred": {"api_key": "secret-value"}}),
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/test_cred", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["credential_id"] != "test_cred" {
		t.Fatalf("credential_id = %v", body["credential_id"])
	}
	names := credentialRouteJSONList(t, body["key_names"])
	if len(names) != 1 || names[0] != "api_key" {
		t.Fatalf("key_names = %#v", names)
	}
	if strings.Contains(recorder.Body.String(), "secret-value") {
		t.Fatal("get response leaked a secret value")
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_get_credential_not_found
func TestCredentialRoutesGetNotFound(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/nonexistent", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "Credential 'nonexistent' not found" {
		t.Fatalf("error = %v", body["error"])
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_delete_credential
func TestCredentialRoutesDelete(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store: credentials.ForTesting(map[string]map[string]string{"test_cred": {"api_key": "val"}}),
	})
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/test_cred", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["deleted"] != true {
		t.Fatalf("deleted = %v", body["deleted"])
	}
	recorder, _ = doSessionJSON(t, mux, http.MethodGet, "/api/credentials/test_cred", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("deleted credential still resolves: %d", recorder.Code)
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_delete_credential_not_found
func TestCredentialRoutesDeleteNotFound(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/nonexistent", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "Credential 'nonexistent' not found" {
		t.Fatalf("error = %v", body["error"])
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_save_credential_missing_fields
func TestCredentialRoutesSaveMissingFields(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	for _, payload := range []string{`{}`, `{"credential_id":"x"}`, `{"credential_id":"x","keys":{}}`} {
		recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials", payload)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("payload %s: status = %d, body = %s", payload, recorder.Code, recorder.Body.String())
		}
		if body["error"] != "credential_id and keys are required" {
			t.Fatalf("payload %s: error = %v", payload, body["error"])
		}
	}
}

// Parity: framework/server/tests/test_api.py::TestCredentials::test_save_overwrites_existing
func TestCredentialRoutesSaveOverwritesExisting(t *testing.T) {
	store := credentials.ForTesting(map[string]map[string]string{"test_cred": {"api_key": "old-value"}})
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: store})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"test_cred","keys":{"api_key":"new-value"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	value, err := store.GetKey("test_cred", "api_key")
	if err != nil || value == nil || *value != "new-value" {
		t.Fatalf("stored key = %v, err = %v", value, err)
	}
}

// Parity-exempt: no upstream HTTP test covers the aden_api_key branch; pins the encrypted-store reuse and env hand-off.
func TestCredentialRoutesSaveAdenKeyUsesEncryptedStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv(credentials.AdenEnvVar, "")

	mux := credentialRouteTestMux(CredentialRouteDeps{Home: hivehome.Home{Root: home}})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"aden_api_key","keys":{"api_key":"  sk-aden  "}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["saved"] != "aden_api_key" {
		t.Fatalf("saved = %v", body["saved"])
	}
	encrypted, err := credentials.NewEncryptedFileStorage(filepath.Join(home, "credentials"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	stored, err := encrypted.Load(credentials.AdenCredentialID)
	if err != nil || stored == nil {
		t.Fatalf("stored credential = %v, err = %v", stored, err)
	}
	if value := stored.GetKey("api_key"); value == nil || *value != "sk-aden" {
		t.Fatalf("stored api_key = %v", value)
	}
	if err := os.Unsetenv(credentials.AdenEnvVar); err != nil {
		t.Fatalf("unset env: %v", err)
	}
	loaded, _ := credentials.LoadAdenAPIKey()
	if loaded != "sk-aden" {
		t.Fatalf("LoadAdenAPIKey = %q", loaded)
	}
}

// Parity-exempt: generic CRUD path over the real encrypted store; pins Fernet reuse and the index upsert.
func TestCredentialRoutesEncryptedStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	mux := credentialRouteTestMux(CredentialRouteDeps{Home: hivehome.Home{Root: home}})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"brave_search","keys":{"api_key":"sk-encrypted"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	encrypted, err := credentials.NewEncryptedFileStorage(filepath.Join(home, "credentials"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	stored, err := encrypted.Load("brave_search")
	if err != nil || stored == nil {
		t.Fatalf("stored credential = %v, err = %v", stored, err)
	}
	if value := stored.GetKey("api_key"); value == nil || *value != "sk-encrypted" {
		t.Fatalf("stored api_key = %v", value)
	}
	index, err := os.ReadFile(filepath.Join(home, "credentials", "metadata", "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(index), "brave_search") {
		t.Fatalf("index does not list the saved credential: %s", index)
	}

	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/brave_search", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["credential_id"] != "brave_search" || strings.Contains(recorder.Body.String(), "sk-encrypted") {
		t.Fatalf("get body = %s", recorder.Body.String())
	}
}

// Parity-exempt: read-only-store 503 envelope; Python's test_api uses an in-memory store, so this branch has no upstream route test.
func TestCredentialRoutesSaveReadOnlyStoreReturnsLocked(t *testing.T) {
	store := credentials.WithEnvStorage(map[string]string{"openai": "OPENAI_API_KEY"})
	mux := credentialRouteTestMux(CredentialRouteDeps{Store: store})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"openai","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "credentials_store_locked" {
		t.Fatalf("error = %v", body["error"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "HIVE_CREDENTIAL_KEY") {
		t.Fatalf("detail = %v", body["detail"])
	}
}

// Parity-exempt: upstream delete path pops PROVIDER_ENV_VARS; pins the env fallback cleanup.
func TestCredentialRoutesDeleteClearsProviderEnvVar(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "present")
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store: credentials.ForTesting(map[string]map[string]string{"openai": {"api_key": "x"}}),
	})
	recorder, _ := doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/openai", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, present := os.LookupEnv("OPENAI_API_KEY"); present {
		t.Fatal("OPENAI_API_KEY must be cleared after deleting the credential")
	}
}

// Parity-exempt: oauth-status payload (accounts + aden bit + millisecond stamp) has no upstream route test.
func TestCredentialRoutesOAuthStatusPayload(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	accounts := map[string][]map[string]any{
		"notion": {{"provider": "notion", "alias": "work", "identity": map[string]any{"email": "a@b.c"}, "source": "aden", "credential_id": "notion_token"}},
	}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Accounts: func() map[string][]map[string]any { return accounts },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/oauth-status", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["has_aden_key"] != true {
		t.Fatalf("has_aden_key = %v", body["has_aden_key"])
	}
	grouped := credentialRouteJSONMap(t, body["accounts_by_provider"])
	if got := len(credentialRouteJSONList(t, grouped["notion"])); got != 1 {
		t.Fatalf("notion accounts = %d", got)
	}
	if fetched, ok := body["fetched_at"].(float64); !ok || fetched <= 0 {
		t.Fatalf("fetched_at = %v", body["fetched_at"])
	}
}

// Parity-exempt: resync without an Aden key returns the documented 400 envelope; upstream route test absent.
func TestCredentialRoutesResyncRequiresAdenKey(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	mux := credentialRouteTestMux(CredentialRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/resync", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "Aden API key not configured" {
		t.Fatalf("error = %v", body["error"])
	}
	if got := len(credentialRouteJSONMap(t, body["accounts_by_provider"])); got != 0 {
		t.Fatalf("accounts_by_provider = %d", got)
	}
}

// Parity-exempt: resync success path delegates to the Aden hook and returns the account snapshot; no upstream route test.
func TestCredentialRoutesResyncCallsHookAndReturnsAccounts(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	called := 0
	accounts := map[string][]map[string]any{"github": {{"provider": "github", "alias": "default"}}}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Resync:   func() error { called++; return nil },
		Accounts: func() map[string][]map[string]any { return accounts },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/resync", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if called != 1 {
		t.Fatalf("resync hook calls = %d", called)
	}
	if body["synced"] != true {
		t.Fatalf("synced = %v", body["synced"])
	}
	if got := len(credentialRouteJSONList(t, credentialRouteJSONMap(t, body["accounts_by_provider"])["github"])); got != 1 {
		t.Fatalf("github accounts = %d", got)
	}
}

// Parity-exempt: validate-key request validation (invalid JSON, missing fields) has no upstream route test.
func TestCredentialRoutesValidateKeyRequiresFields(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/validate-key", "not json")
	if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid JSON body" {
		t.Fatalf("invalid JSON: status = %d, body = %v", recorder.Code, body["error"])
	}
	for _, payload := range []string{`{}`, `{"provider_id":"openai"}`, `{"api_key":"sk"}`} {
		recorder, body = doSessionJSON(t, mux, http.MethodPost, "/api/credentials/validate-key", payload)
		if recorder.Code != http.StatusBadRequest || body["error"] != "provider_id and api_key are required" {
			t.Fatalf("payload %s: status = %d, body = %v", payload, recorder.Code, body["error"])
		}
	}
}

// Parity-exempt: validate-key delegates to the injected checker and maps failures to valid=null; no upstream route test.
func TestCredentialRoutesValidateKeyDelegatesToChecker(t *testing.T) {
	var gotProvider, gotKey string
	mux := credentialRouteTestMux(CredentialRouteDeps{
		ValidateKey: func(providerID, apiKey string) (map[string]any, error) {
			gotProvider, gotKey = providerID, apiKey
			return map[string]any{"valid": false, "message": "key rejected"}, nil
		},
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/validate-key",
		`{"provider_id":" anthropic ","api_key":" sk-ant "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotProvider != "anthropic" || gotKey != "sk-ant" {
		t.Fatalf("checker got %q/%q", gotProvider, gotKey)
	}
	if body["valid"] != false || body["message"] != "key rejected" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: unknown providers short-circuit with valid=true and no network call; mirrors PROVIDERS.get fallback.
func TestCredentialRoutesValidateKeyUnknownProvider(t *testing.T) {
	mux := credentialRouteTestMux(CredentialRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/validate-key",
		`{"provider_id":"mystery","api_key":"sk"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["valid"] != true {
		t.Fatalf("valid = %v", body["valid"])
	}
	if body["message"] != "No health check for mystery" {
		t.Fatalf("message = %v", body["message"])
	}
}

// Parity-exempt: specs payload assembly (synthetic Aden row, provider-keyed accounts, availability rules) has no upstream route test.
func TestCredentialRoutesSpecsPayload(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	specs := []CredentialSpecView{
		{
			Name:                  "notion",
			CredentialID:          "notion_token",
			EnvVar:                "NOTION_API_KEY",
			Description:           "Notion",
			AdenSupported:         true,
			DirectAPIKeySupported: true,
			CredentialKey:         "api_key",
			AdenProviderName:      "notion",
		},
		{
			Name:                  "exa_search",
			CredentialID:          "exa_search",
			EnvVar:                "EXA_API_KEY",
			AdenSupported:         true,
			DirectAPIKeySupported: false,
		},
	}
	accounts := map[string][]map[string]any{
		"notion": {{"provider": "notion", "alias": "work"}},
	}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Specs:      func() []CredentialSpecView { return specs },
		SpecsStore: func() credentialRouteStore { return credentials.ForTesting(nil) },
		Accounts:   func() map[string][]map[string]any { return accounts },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/specs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	rows := credentialRouteJSONList(t, body["specs"])
	if len(rows) != 3 {
		t.Fatalf("specs = %d, want 3", len(rows))
	}
	adenRow := credentialRouteJSONMap(t, rows[0])
	if adenRow["credential_id"] != credentials.AdenCredentialID || adenRow["available"] != true {
		t.Fatalf("aden row = %#v", adenRow)
	}
	notionRow := credentialRouteJSONMap(t, rows[1])
	if notionRow["available"] != true {
		t.Fatalf("notion availability = %v", notionRow["available"])
	}
	if got := len(credentialRouteJSONList(t, notionRow["accounts"])); got != 1 {
		t.Fatalf("notion accounts = %d", got)
	}
	exaRow := credentialRouteJSONMap(t, rows[2])
	if exaRow["available"] != false {
		t.Fatalf("exa availability = %v", exaRow["available"])
	}
	if body["has_aden_key"] != true {
		t.Fatalf("has_aden_key = %v", body["has_aden_key"])
	}
}

// Parity-exempt: specs availability reads the env-backed store built from spec env vars; no upstream route test.
func TestCredentialRoutesSpecsAvailabilityUsesEnvStore(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv("BRAVE_API_KEY", "set")
	specs := []CredentialSpecView{{
		Name:                  "brave_search",
		CredentialID:          "brave_search",
		EnvVar:                "BRAVE_API_KEY",
		DirectAPIKeySupported: true,
	}}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Specs: func() []CredentialSpecView { return specs },
		SpecsStore: func() credentialRouteStore {
			return credentials.WithEnvStorage(map[string]string{"brave_search": "BRAVE_API_KEY"})
		},
		Accounts: func() map[string][]map[string]any { return map[string][]map[string]any{} },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/specs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	row := credentialRouteJSONMap(t, credentialRouteJSONList(t, body["specs"])[0])
	if row["available"] != true {
		t.Fatalf("availability = %v", row["available"])
	}
}

// Parity-exempt: composition check that the credential group shares a mux with the already-ported agent routes.
func TestCredentialRoutesCoexistWithAgentRoutes(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := http.NewServeMux()
	RegisterAgentRoutes(mux, AgentRouteDeps{Home: home})
	RegisterCredentialRoutes(mux, CredentialRouteDeps{Store: credentials.ForTesting(nil)})
	RegisterSoulRoutes(mux, SoulRouteDeps{Home: home})
	RegisterChannelRoutes(mux, ChannelRouteDeps{})
	for _, target := range []string{"/api/credentials", "/api/credentials/specs", "/api/channels"} {
		recorder, _ := doSessionJSON(t, mux, http.MethodGet, target, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", target, recorder.Code, recorder.Body.String())
		}
	}
}

// Parity-exempt: credential events pair save/delete with the spec's Aden provider; no upstream route test asserts the hook.
func TestCredentialRoutesPublishesProviderEvents(t *testing.T) {
	type event struct {
		connected bool
		id        string
		provider  string
	}
	var events []event
	specs := []CredentialSpecView{{Name: "notion", CredentialID: "notion_token", AdenProviderName: "notion"}}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store: credentials.ForTesting(nil),
		Specs: func() []CredentialSpecView { return specs },
		PublishCredentialEvent: func(connected bool, credentialID, provider string) {
			events = append(events, event{connected, credentialID, provider})
		},
	})
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"notion_token","keys":{"api_key":"x"}}`); recorder.Code != http.StatusCreated {
		t.Fatalf("save status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/notion_token", ""); recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if !events[0].connected || events[0].id != "notion_token" || events[0].provider != "notion" {
		t.Fatalf("save event = %#v", events[0])
	}
	if events[1].connected || events[1].id != "notion_token" || events[1].provider != "notion" {
		t.Fatalf("delete event = %#v", events[1])
	}
}

// Parity-exempt: the Go-only env bridge must run after a dashboard save; no upstream HTTP test asserts process-environment updates.
func TestCredentialRoutesSaveRefreshesProcessEnv(t *testing.T) {
	calls := 0
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store:   &credentialRouteTestStore{},
		SyncEnv: func() { calls++ },
	})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"brave_search","keys":{"api_key":"sk-test"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if calls != 1 {
		t.Fatalf("env sync calls = %d, want 1", calls)
	}
}

// Parity-exempt: resync pulls fresh account tokens, so the Go-only env bridge runs again; no upstream HTTP test asserts it.
func TestCredentialRoutesResyncRefreshesProcessEnv(t *testing.T) {
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	resynced, syncs := 0, 0
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Store:   credentials.ForTesting(nil),
		Resync:  func() error { resynced++; return nil },
		SyncEnv: func() { syncs++ },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/resync", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["synced"] != true {
		t.Fatalf("synced = %v", body["synced"])
	}
	if resynced != 1 || syncs != 1 {
		t.Fatalf("resynced = %d, env syncs = %d", resynced, syncs)
	}
}

// Parity-exempt: end-to-end dashboard save path — the saved key must be readable through the env var tools call.
func TestCredentialRoutesSaveInjectsProcessEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv("HIVE_TEST_DASHBOARD_KEY", "")

	specs := []CredentialSpecView{{
		Name:                  "brave_search",
		CredentialID:          "brave_search",
		EnvVar:                "HIVE_TEST_DASHBOARD_KEY",
		DirectAPIKeySupported: true,
		CredentialKey:         "api_key",
	}}
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:  hivehome.Home{Root: home},
		Specs: func() []CredentialSpecView { return specs },
	})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/credentials",
		`{"credential_id":"brave_search","keys":{"api_key":"sk-dashboard"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := os.Getenv("HIVE_TEST_DASHBOARD_KEY"); got != "sk-dashboard" {
		t.Fatalf("env = %q, want the key saved from the dashboard", got)
	}
}

// Parity-exempt: serve startup exports the catalog providers' saved keys before tools are built; no upstream Go startup test exists.
func TestSyncCredentialEnvExportsCatalogProviderKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)

	specs := credentialSpecsFromCatalog()
	if len(specs) == 0 {
		t.Skip("model catalog has no API-key providers")
	}
	spec := specs[0]
	if spec.EnvVar == "" || spec.CredentialID == "" {
		t.Fatalf("catalog spec = %#v", spec)
	}
	t.Setenv(spec.EnvVar, "")

	storage, err := credentials.NewEncryptedFileStorage(filepath.Join(home, "credentials"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	credential := credentials.NewCredentialObject(spec.CredentialID)
	credential.Keys.Set(credentials.NewCredentialKey("api_key", "sk-catalog-saved"))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	result := SyncCredentialEnv(hivehome.Home{Root: home})
	if got := os.Getenv(spec.EnvVar); got != "sk-catalog-saved" {
		t.Fatalf("%s = %q, want the saved key", spec.EnvVar, got)
	}
	if len(result.Injected) != 1 || result.Injected[0] != spec.EnvVar {
		t.Fatalf("injected = %#v", result.Injected)
	}
}
