package credentials

import (
	"errors"
	"strings"
	"testing"
)

// Parity-exempt: LocalCredentialRegistry.validate_account has no upstream pytest; these pin registry.py's missing-account, missing-key, checker, and status-update branches.
func TestLocalValidateAccountSuccessPersistsStatusAndIdentity(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	registry := NewLocalCredentialRegistry(storage)
	if _, err := registry.SaveAccount("github", "work", "gh-key", false, nil); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	registry.healthCheck = func(credentialName, credentialValue string, extraKeys map[string]string) HealthCheckResult {
		if credentialName != "github" || credentialValue != "gh-key" {
			t.Fatalf("checker got %q/%q", credentialName, credentialValue)
		}
		return HealthCheckResult{
			Valid:   true,
			Message: "GitHub credentials valid",
			Details: map[string]any{"identity": map[string]any{"username": "octocat", "company": "Acme"}},
		}
	}

	result, err := registry.ValidateAccount("github", "work")
	if err != nil {
		t.Fatalf("ValidateAccount: %v", err)
	}
	if !result.Valid || result.Message != "GitHub credentials valid" {
		t.Fatalf("result = %#v", result)
	}
	credential, err := registry.GetAccount("github", "work")
	if err != nil || credential == nil {
		t.Fatalf("account = %#v, %v", credential, err)
	}
	if status, _ := credential.GetKeyValue("_status"); status != "active" {
		t.Fatalf("_status = %q", status)
	}
	if username, _ := credential.GetKeyValue("_identity_username"); username != "octocat" {
		t.Fatalf("_identity_username = %q", username)
	}
	if _, present := credential.GetKeyValue("_identity_company"); present {
		t.Fatal("non-CredentialIdentity fields must be filtered from the account")
	}
	if credential.LastRefreshed == nil {
		t.Fatal("last_refreshed must be updated")
	}
}

// Parity-exempt: failed validate_account persistence has no upstream pytest.
func TestLocalValidateAccountFailurePersistsFailedStatus(t *testing.T) {
	registry := NewLocalCredentialRegistry(NewInMemoryStorage(nil))
	if _, err := registry.SaveAccount("github", "work", "bad-key", false, nil); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	registry.healthCheck = func(string, string, map[string]string) HealthCheckResult {
		return HealthCheckResult{Valid: false, Message: "GitHub token is invalid", Details: map[string]any{"status_code": 401}}
	}

	result, err := registry.ValidateAccount("github", "work")
	if err != nil {
		t.Fatalf("ValidateAccount: %v", err)
	}
	if result.Valid || result.Message != "GitHub token is invalid" {
		t.Fatalf("result = %#v", result)
	}
	credential, _ := registry.GetAccount("github", "work")
	if status, _ := credential.GetKeyValue("_status"); status != "failed" {
		t.Fatalf("_status = %q", status)
	}
	if credential.LastRefreshed == nil {
		t.Fatal("a failed validation still records last_refreshed upstream")
	}
}

// Parity-exempt: missing-account KeyError path has no upstream pytest.
func TestLocalValidateAccountMissingAccountReturnsError(t *testing.T) {
	registry := NewLocalCredentialRegistry(NewInMemoryStorage(nil))
	_, err := registry.ValidateAccount("github", "missing")
	if err == nil {
		t.Fatal("expected an error for a missing account")
	}
	var notFound *LocalAccountNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error type = %T", err)
	}
	if !strings.Contains(err.Error(), "No local account found: github/missing") {
		t.Fatalf("error = %v", err)
	}
}

// Parity-exempt: the stored-account-without-api_key result has no upstream pytest.
func TestLocalValidateAccountMissingKeyReturnsInvalidResult(t *testing.T) {
	registry := NewLocalCredentialRegistry(NewInMemoryStorage(nil))
	if _, err := registry.SaveAccount("github", "work", "", false, nil); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	called := false
	registry.healthCheck = func(string, string, map[string]string) HealthCheckResult {
		called = true
		return HealthCheckResult{Valid: true}
	}

	result, err := registry.ValidateAccount("github", "work")
	if err != nil {
		t.Fatalf("ValidateAccount: %v", err)
	}
	if called {
		t.Fatal("checker must not run without an api_key")
	}
	if result.Valid || result.Message != "No api_key stored for this account" {
		t.Fatalf("result = %#v", result)
	}
}

// Parity-exempt: checker exception handling ("Health check error") has no upstream pytest.
func TestLocalValidateAccountCheckerPanicBecomesInvalidResult(t *testing.T) {
	registry := NewLocalCredentialRegistry(NewInMemoryStorage(nil))
	if _, err := registry.SaveAccount("github", "work", "gh-key", false, nil); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	registry.healthCheck = func(string, string, map[string]string) HealthCheckResult {
		panic("checker exploded")
	}

	result, err := registry.ValidateAccount("github", "work")
	if err != nil {
		t.Fatalf("ValidateAccount: %v", err)
	}
	if result.Valid || !strings.Contains(result.Message, "Health check error: checker exploded") {
		t.Fatalf("result = %#v", result)
	}
	if result.Details["error"] != "checker exploded" {
		t.Fatalf("details = %#v", result.Details)
	}
	credential, _ := registry.GetAccount("github", "work")
	if status, _ := credential.GetKeyValue("_status"); status != "failed" {
		t.Fatalf("_status = %q", status)
	}
}

// Parity-exempt: cse_id forwarding into validate_account's checker has no upstream pytest.
func TestLocalValidateAccountPassesCSEID(t *testing.T) {
	registry := NewLocalCredentialRegistry(NewInMemoryStorage(nil))
	if _, err := registry.SaveAccount("google_search", "work", "g-key", false, map[string]string{"cse_id": "cse-1"}); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	var got map[string]string
	registry.healthCheck = func(_ string, _ string, extraKeys map[string]string) HealthCheckResult {
		got = extraKeys
		return HealthCheckResult{Valid: true}
	}
	if _, err := registry.ValidateAccount("google_search", "work"); err != nil {
		t.Fatalf("ValidateAccount: %v", err)
	}
	if got["cse_id"] != "cse-1" {
		t.Fatalf("extra keys = %#v", got)
	}
}
