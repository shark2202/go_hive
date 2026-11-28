package credentials

// Parity: core/framework/credentials/local/registry.py::LocalCredentialRegistry.save_account
//
// Local saves run the same health check as Python: a valid key stores
// status=active with identity, an invalid key stores status=failed, and a
// checker exception stores status=unknown. The route reports valid/message
// from the returned health result.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Parity-exempt: save_account's health-checked path (active status, identity filtering, last_refreshed) has no upstream pytest; Python tests only save with run_health_check=False.
func TestLocalSaveAccountRunsHealthCheckAndPersistsOutcome(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	registry := NewLocalCredentialRegistry(storage)
	registry.healthCheck = func(credentialName, credentialValue string, extraKeys map[string]string) HealthCheckResult {
		if credentialName != "brevo" || credentialValue != "brevo-key" {
			t.Fatalf("checker got %q/%q", credentialName, credentialValue)
		}
		return HealthCheckResult{
			Valid:   true,
			Message: "Brevo credentials valid",
			Details: map[string]any{"identity": map[string]any{
				"email":    "ops@example.com",
				"company":  "Acme",
				"username": 42,
			}},
		}
	}

	info, err := registry.SaveAccount("brevo", "work", "brevo-key", true, nil)
	if err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if info.Status != "active" {
		t.Fatalf("status = %q, want active", info.Status)
	}
	if info.Health == nil || !info.Health.Valid || info.Health.Message != "Brevo credentials valid" {
		t.Fatalf("health = %#v", info.Health)
	}
	if info.Identity.Email == nil || *info.Identity.Email != "ops@example.com" {
		t.Fatalf("identity = %#v", info.Identity)
	}
	if info.LastValidated == nil {
		t.Fatal("last_validated must be set when a check runs")
	}

	credential, err := storage.Load("brevo/work")
	if err != nil || credential == nil {
		t.Fatalf("stored credential = %#v, err = %v", credential, err)
	}
	if status, _ := credential.GetKeyValue("_status"); status != "active" {
		t.Fatalf("_status = %q", status)
	}
	if email, _ := credential.GetKeyValue("_identity_email"); email != "ops@example.com" {
		t.Fatalf("_identity_email = %q", email)
	}
	// Health metadata outside CredentialIdentity's fields is not persisted.
	if _, present := credential.GetKeyValue("_identity_company"); present {
		t.Fatal("_identity_company must be filtered out")
	}
	if _, present := credential.GetKeyValue("_identity_username"); present {
		t.Fatal("non-string identity values must be filtered out")
	}
	if credential.LastRefreshed == nil {
		t.Fatal("last_refreshed must be persisted when a check runs")
	}
}

// Parity-exempt: failed health-check persistence (status=failed + last_refreshed) has no upstream pytest.
func TestLocalSaveAccountPersistsFailureStatus(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	registry := NewLocalCredentialRegistry(storage)
	registry.healthCheck = func(string, string, map[string]string) HealthCheckResult {
		return HealthCheckResult{Valid: false, Message: "Brevo API key is invalid", Details: map[string]any{"status_code": 401}}
	}

	info, err := registry.SaveAccount("brevo", "default", "bad-key", true, nil)
	if err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if info.Status != "failed" {
		t.Fatalf("status = %q, want failed", info.Status)
	}
	if info.Health == nil || info.Health.Valid || info.Health.Message != "Brevo API key is invalid" {
		t.Fatalf("health = %#v", info.Health)
	}
	credential, _ := storage.Load("brevo/default")
	if status, _ := credential.GetKeyValue("_status"); status != "failed" {
		t.Fatalf("_status = %q", status)
	}
	if info.LastValidated == nil {
		t.Fatal("a failed check still records last_refreshed (upstream parity)")
	}
}

// Parity-exempt: the run_health_check=False save path is only exercised indirectly upstream; no pytest asserts its nil health/identity.
func TestLocalSaveAccountWithoutCheckKeepsActiveAndNoHealth(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	registry := NewLocalCredentialRegistry(storage)

	info, err := registry.SaveAccount("custom_provider", "default", "key", false, nil)
	if err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if info.Status != "active" || info.Health != nil || info.LastValidated != nil {
		t.Fatalf("info = %#v", info)
	}
	credential, _ := storage.Load("custom_provider/default")
	if credential.LastRefreshed != nil {
		t.Fatal("last_refreshed must stay nil when no check runs")
	}
}

// Parity-exempt: save_account's checker-exception path (status=unknown) has no upstream pytest.
func TestLocalSaveAccountCheckerExceptionStoresUnknown(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	registry := NewLocalCredentialRegistry(storage)
	registry.healthCheck = func(string, string, map[string]string) HealthCheckResult {
		panic("checker exploded")
	}

	info, err := registry.SaveAccount("brevo", "default", "key", true, nil)
	if err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if info.Status != "unknown" || info.Health != nil {
		t.Fatalf("info = %#v, want status unknown and nil health", info)
	}
	if info.LastValidated == nil {
		t.Fatal("the exception path still records last_refreshed upstream")
	}
}

// Parity: tools/src/aden_tools/credentials/health_check.py::GoogleSearchHealthChecker.check — cse_id turns the partial format check into a live probe.
func TestLocalSaveAccountPassesCSEIDToGoogleSearchChecker(t *testing.T) {
	var gotQuery map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{
			"key": r.URL.Query().Get("key"),
			"cx":  r.URL.Query().Get("cx"),
			"q":   r.URL.Query().Get("q"),
			"num": r.URL.Query().Get("num"),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	previous := googleCustomSearchEndpoint
	googleCustomSearchEndpoint = server.URL
	t.Cleanup(func() { googleCustomSearchEndpoint = previous })

	info, err := NewLocalCredentialRegistry(NewInMemoryStorage(nil)).
		SaveAccount("google_search", "default", "g-key", true, map[string]string{"cse_id": "cse-1"})
	if err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if info.Status != "active" || info.Health == nil ||
		info.Health.Message != "Google Custom Search credentials valid" {
		t.Fatalf("info = %#v", info)
	}
	if gotQuery["key"] != "g-key" || gotQuery["cx"] != "cse-1" || gotQuery["q"] != "test" || gotQuery["num"] != "1" {
		t.Fatalf("query = %#v", gotQuery)
	}
}

// Parity-exempt: Go-only HasHealthChecker gate mirrors the health_check.py HEALTH_CHECKERS lookup; upstream has no pytest for the availability gate.
func TestHasHealthCheckerMatchesLocalSaveGate(t *testing.T) {
	if !HasHealthChecker("brave_search") {
		t.Fatal("brave_search has a checker")
	}
	if HasHealthChecker("custom_provider") {
		t.Fatal("custom_provider has no checker")
	}
}
