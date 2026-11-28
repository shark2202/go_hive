package server

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
// Parity: framework/server/routes_credentials.py::handle_delete_local_credential

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type failingLocalRegistry struct {
	saveErr   error
	deleteErr error
}

func (r *failingLocalRegistry) SaveAccount(string, string, string, bool, map[string]string) (credentials.LocalAccountInfo, error) {
	return credentials.LocalAccountInfo{}, r.saveErr
}

func (r *failingLocalRegistry) DeleteAccount(string, string) (bool, error) {
	return false, r.deleteErr
}

// recordingLocalRegistry captures the runHealthCheck decision and returns a
// canned health outcome so the route's status/identity/valid/message wiring is
// testable without a live provider.
type recordingLocalRegistry struct {
	runHealthCheck bool
	info           credentials.LocalAccountInfo
	extraKeys      map[string]string
}

func (r *recordingLocalRegistry) SaveAccount(credentialID, alias, apiKey string, runHealthCheck bool, extraKeys map[string]string) (credentials.LocalAccountInfo, error) {
	r.runHealthCheck = runHealthCheck
	r.extraKeys = extraKeys
	info := r.info
	if info.CredentialID == "" {
		info.CredentialID = credentialID
		info.Alias = alias
	}
	return info, nil
}

func (r *recordingLocalRegistry) DeleteAccount(string, string) (bool, error) {
	return false, nil
}

func localCredentialTestMux(t *testing.T, deps LocalCredentialRouteDeps) (*http.ServeMux, *credentials.LocalCredentialRegistry) {
	t.Helper()
	if deps.Registry == nil {
		registry, err := credentials.LocalCredentialRegistryAtPath(deps.Home.CredentialsDir())
		if err != nil {
			t.Fatalf("local registry: %v", err)
		}
		deps.Registry = registry
		mux := http.NewServeMux()
		RegisterLocalCredentialRoutes(mux, deps)
		return mux, registry
	}
	mux := http.NewServeMux()
	RegisterLocalCredentialRoutes(mux, deps)
	return mux, nil
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
func TestLocalCredentialRoutesSaveAndDelete(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux, registry := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home})

	// A custom provider has no registered checker, so upstream skips the
	// health check: status=active and valid/message stay null.
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"custom_local","account":"work","keys":{"api_key":"CL-secret","cse_id":"cse-1"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["saved"] != "custom_local/work" || body["status"] != "active" {
		t.Fatalf("body = %#v", body)
	}
	if body["valid"] != nil || body["message"] != nil {
		t.Fatalf("health fields = %#v/%#v, want nulls", body["valid"], body["message"])
	}
	if identity := credentialRouteJSONMap(t, body["identity"]); len(identity) != 0 {
		t.Fatalf("identity = %#v, want empty", identity)
	}
	// Secrets never travel back, whether in a field or in an error message.
	if strings.Contains(recorder.Body.String(), "CL-secret") || strings.Contains(recorder.Body.String(), "cse-1") {
		t.Fatalf("response echoed a secret: %s", recorder.Body.String())
	}

	account, err := registry.GetAccount("custom_local", "work")
	if err != nil || account == nil {
		t.Fatalf("stored account = %#v, err = %v", account, err)
	}
	if value, ok := account.GetKeyValue("api_key"); !ok || value != "CL-secret" {
		t.Fatalf("api_key = %q, present = %v", value, ok)
	}
	if value, ok := account.GetKeyValue("cse_id"); !ok || value != "cse-1" {
		t.Fatalf("cse_id = %q, present = %v", value, ok)
	}
	if alias, ok := account.GetKeyValue("_alias"); !ok || alias != "work" {
		t.Fatalf("_alias = %q, present = %v", alias, ok)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/local/custom_local/work", "")
	if recorder.Code != http.StatusOK || body["deleted"] != true {
		t.Fatalf("delete status = %d, body = %#v", recorder.Code, body)
	}
	account, err = registry.GetAccount("custom_local", "work")
	if err != nil || account != nil {
		t.Fatalf("account after delete = %#v, err = %v", account, err)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/local/custom_local/work", "")
	if recorder.Code != http.StatusNotFound ||
		!strings.Contains(body["error"].(string), "custom_local/work") {
		t.Fatalf("second delete status = %d, body = %#v", recorder.Code, body)
	}
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
func TestLocalCredentialRoutesValidation(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux, _ := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home})

	cases := []struct {
		name   string
		target string
		body   string
		error  string
	}{
		{"invalid json", "/api/credentials/local", "{", "Invalid JSON body"},
		{"not an object", "/api/credentials/local", `[]`, "Invalid JSON body"},
		{"missing id", "/api/credentials/local", `{"keys":{"api_key":"x"}}`, "a valid credential_id is required"},
		{"space in id", "/api/credentials/local", `{"credential_id":"bad id","keys":{"api_key":"x"}}`, "a valid credential_id is required"},
		{"slash in id", "/api/credentials/local", `{"credential_id":"bad/id","keys":{"api_key":"x"}}`, "a valid credential_id is required"},
		{"slash in account", "/api/credentials/local", `{"credential_id":"ok","account":"a/b","keys":{"api_key":"x"}}`, "account alias must not contain spaces or slashes"},
		{"missing keys", "/api/credentials/local", `{"credential_id":"ok"}`, "non-empty keys are required"},
		{"empty keys", "/api/credentials/local", `{"credential_id":"ok","keys":{}}`, "non-empty keys are required"},
		{"blank values", "/api/credentials/local", `{"credential_id":"ok","keys":{"api_key":"   "}}`, "no non-empty values provided"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder, body := doSessionJSON(t, mux, http.MethodPost, testCase.target, testCase.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			message, _ := body["error"].(string)
			if !strings.Contains(message, testCase.error) {
				t.Fatalf("error = %q, want %q", message, testCase.error)
			}
		})
	}
}

// Parity: framework/server/routes_credentials.py::handle_delete_local_credential
func TestLocalCredentialRoutesDeleteRejectsUnsafePathValues(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	routes := &LocalCredentialRoutes{deps: LocalCredentialRouteDeps{Home: home}}

	for _, values := range []struct{ id, alias string }{
		{"..", "work"},
		{"brave_search", ".."},
		{"bad/id", "work"},
		{"brave_search", "bad\\alias"},
	} {
		request := httptest.NewRequest(http.MethodDelete, "/api/credentials/local/x/y", nil)
		request.SetPathValue("credential_id", values.id)
		request.SetPathValue("alias", values.alias)
		recorder := httptest.NewRecorder()
		routes.handleDeleteLocalCredential(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("id = %q alias = %q: status = %d, body = %s",
				values.id, values.alias, recorder.Code, recorder.Body.String())
		}
	}
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
func TestLocalCredentialRoutesDefaultAccountAndPrimaryHint(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux, registry := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"custom","keys":{"cse_id":"cse","access_token":"token-value","username":"ann"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["saved"] != "custom/default" {
		t.Fatalf("saved = %v", body["saved"])
	}
	account, err := registry.GetAccount("custom", "default")
	if err != nil || account == nil {
		t.Fatalf("account = %#v, err = %v", account, err)
	}
	// access_token is the first matching primary hint.
	if value, _ := account.GetKeyValue("api_key"); value != "token-value" {
		t.Fatalf("api_key = %q, want the access_token value", value)
	}
	if value, ok := account.GetKeyValue("access_token"); !ok || value != "token-value" {
		t.Fatalf("access_token = %q, present = %v", value, ok)
	}
	if value, ok := account.GetKeyValue("username"); !ok || value != "ann" {
		t.Fatalf("username = %q, present = %v", value, ok)
	}
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential — providers with a registered checker are validated; status/identity/valid/message come from the health result.
func TestLocalCredentialRoutesSaveReportsHealthOutcome(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	registry := &recordingLocalRegistry{info: credentials.LocalAccountInfo{
		Status:   "failed",
		Identity: credentials.CredentialIdentity{Email: stringPtr("ops@example.com")},
		Health:   &credentials.HealthCheckResult{Valid: false, Message: "GitHub token is invalid or expired"},
	}}
	mux, _ := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home, Registry: registry})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"github","account":"work","keys":{"api_key":"gh-secret"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !registry.runHealthCheck {
		t.Fatal("github has a registered checker; runHealthCheck must be true")
	}
	if body["status"] != "failed" || body["valid"] != false ||
		body["message"] != "GitHub token is invalid or expired" {
		t.Fatalf("body = %#v", body)
	}
	if identity := credentialRouteJSONMap(t, body["identity"]); identity["email"] != "ops@example.com" {
		t.Fatalf("identity = %#v", identity)
	}

	registry = &recordingLocalRegistry{}
	mux, _ = localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home, Registry: registry})
	recorder, body = doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"custom_provider","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("custom status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if registry.runHealthCheck {
		t.Fatal("a provider without a checker must not run a health check")
	}
	if body["valid"] != nil || body["message"] != nil {
		t.Fatalf("custom health fields = %#v/%#v, want nulls", body["valid"], body["message"])
	}
}

func stringPtr(value string) *string { return &value }

// TestLocalCredentialRoutesRunsRealHealthCheck exercises the production path:
// the real LocalCredentialRegistry, the registered zoho_crm checker, and the
// route. Before P0-5 the route hardcoded run_health_check=false, leaving
// status=active with valid/message null despite a failed key. ZOHO_API_DOMAIN
// points the checker at a local server so no network is used.
//
// Parity: framework/server/routes_credentials.py::_save_local_credential
func TestLocalCredentialRoutesRunsRealHealthCheck(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv("HIVE_HOME", home.Root)

	var checkedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("ZOHO_API_DOMAIN", server.URL)

	mux, registry := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"zoho_crm","account":"work","keys":{"api_key":"zoho-secret"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["status"] != "active" || body["valid"] != true ||
		body["message"] != "Zoho CRM credentials valid" {
		t.Fatalf("body = %#v, want a completed health check", body)
	}
	if checkedPath != "/crm/v2/users" {
		t.Fatalf("health-check path = %q, want /crm/v2/users", checkedPath)
	}

	account, err := registry.GetAccount("zoho_crm", "work")
	if err != nil || account == nil {
		t.Fatalf("stored account = %#v, err = %v", account, err)
	}
	if status, _ := account.GetKeyValue("_status"); status != "active" {
		t.Fatalf("_status = %q, want active", status)
	}
	if account.LastRefreshed == nil {
		t.Fatal("last_refreshed must be persisted after a completed health check")
	}
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
func TestLocalCredentialRoutesStorageFailure(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	failing := &failingLocalRegistry{saveErr: errors.New("disk full"), deleteErr: errors.New("disk full")}
	mux, _ := localCredentialTestMux(t, LocalCredentialRouteDeps{Home: home, Registry: failing})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"ok","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(body["error"].(string), "Failed to store credential") {
		t.Fatalf("save status = %d, body = %#v", recorder.Code, body)
	}
	if strings.Contains(recorder.Body.String(), `"x"`) {
		t.Fatalf("failure response echoed the key: %s", recorder.Body.String())
	}

	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/credentials/local/ok/default", "")
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(body["error"].(string), "Failed to delete credential") {
		t.Fatalf("delete status = %d, body = %#v", recorder.Code, body)
	}
}
