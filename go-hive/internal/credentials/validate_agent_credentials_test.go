package credentials

// Parity: framework/credentials/validation.py::validate_agent_credentials /
// compute_unavailable_tools / CredentialValidationResult

import (
	"strings"
	"testing"
)

type stubAgentStore struct {
	available map[string]bool
	values    map[string]string
	objects   map[string]*CredentialObject
	saved     []*CredentialObject
}

func newStubAgentStore(available map[string]bool, values map[string]string) *stubAgentStore {
	return &stubAgentStore{available: available, values: values, objects: map[string]*CredentialObject{}}
}

func (s *stubAgentStore) IsAvailable(credentialID string) bool {
	return s.available[credentialID]
}

func (s *stubAgentStore) Get(credentialID string) (*string, error) {
	if value, present := s.values[credentialID]; present {
		return &value, nil
	}
	return nil, nil
}

func (s *stubAgentStore) GetCredential(credentialID string, _ bool) (*CredentialObject, error) {
	if object, present := s.objects[credentialID]; present {
		return object, nil
	}
	if !s.available[credentialID] {
		return nil, nil
	}
	object := NewCredentialObject(credentialID)
	object.SetKey("api_key", "value", nil, nil)
	s.objects[credentialID] = object
	return object, nil
}

func (s *stubAgentStore) SaveCredential(credential *CredentialObject) error {
	s.objects[credential.ID] = credential
	s.saved = append(s.saved, credential)
	return nil
}

func rowsByName(result AgentCredentialValidationResult) map[string]AgentCredentialStatus {
	rows := make(map[string]AgentCredentialStatus, len(result.Credentials))
	for _, status := range result.Credentials {
		rows[status.CredentialName] = status
	}
	return rows
}

// Parity: framework/credentials/validation.py::validate_agent_credentials —
// presence only (verify off): declared tools map to their credentials, missing
// ones report available=false with valid unset.
func TestValidateAgentCredentialsPresence(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"github_create_issue", "notion_search"}, NodeType: "event_loop"}}
	store := newStubAgentStore(map[string]bool{"github": true}, map[string]string{})

	result, err := ValidateAgentCredentials(nodes, store, ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.HasAdenKey {
		t.Fatalf("has_aden_key = true without ADEN_API_KEY")
	}
	if len(result.Credentials) != 2 {
		t.Fatalf("credentials = %d, want 2 (%#v)", len(result.Credentials), result.Credentials)
	}
	// Tools iterate sorted, so github_create_issue precedes notion_search.
	if result.Credentials[0].CredentialName != "github" || result.Credentials[1].CredentialName != "notion" {
		t.Fatalf("order = %q,%q", result.Credentials[0].CredentialName, result.Credentials[1].CredentialName)
	}
	github := result.Credentials[0]
	if !github.Available || github.Valid != nil || github.ValidationMessage != nil {
		t.Fatalf("github = %#v", github)
	}
	if github.EnvVar != "GITHUB_TOKEN" || github.CredentialID != "github" || github.CredentialKey != "access_token" {
		t.Fatalf("github fields = %#v", github)
	}
	if len(github.Tools) != 1 || github.Tools[0] != "github_create_issue" {
		t.Fatalf("github tools = %#v", github.Tools)
	}
	if len(github.NodeTypes) != 0 {
		t.Fatalf("github node_types = %#v", github.NodeTypes)
	}
	notion := result.Credentials[1]
	if notion.Available {
		t.Fatalf("notion available = true")
	}
	if len(result.Failed()) != 1 || result.Failed()[0].CredentialName != "notion" {
		t.Fatalf("failed = %#v", result.Failed())
	}
	if got := result.FailedCredNames(); len(got) != 1 || got[0] != "notion" {
		t.Fatalf("failed names = %#v", got)
	}
}

// Parity: validation.py::validate_agent_credentials — a required=False spec in
// the single-provider branch is recorded as checked but never reported.
func TestValidateAgentCredentialsSkipsNonRequiredSpec(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"news_sentiment"}}}
	store := newStubAgentStore(map[string]bool{}, map[string]string{})
	result, err := ValidateAgentCredentials(nodes, store, ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(result.Credentials) != 0 {
		t.Fatalf("credentials = %#v, want none (finlight.required=false)", result.Credentials)
	}
}

// Parity: validation.py::validate_agent_credentials — multi-provider tools are
// any-of: with nothing available every alternative is reported with the tool
// name as its group; with one available only that provider is reported.
func TestValidateAgentCredentialsAlternativeGroups(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"send_email"}}}

	missing, err := ValidateAgentCredentials(nodes, newStubAgentStore(map[string]bool{}, map[string]string{}), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(missing.Credentials) != 2 {
		t.Fatalf("credentials = %d, want 2", len(missing.Credentials))
	}
	for _, status := range missing.Credentials {
		if status.AlternativeGroup == nil || *status.AlternativeGroup != "send_email" {
			t.Fatalf("alternative_group = %#v", status.AlternativeGroup)
		}
	}

	rows := rowsByName(missing)
	if _, present := rows["google"]; !present {
		t.Fatalf("google row missing: %#v", rows)
	}
	if _, present := rows["resend"]; !present {
		t.Fatalf("resend row missing: %#v", rows)
	}

	satisfied, err := ValidateAgentCredentials(nodes, newStubAgentStore(map[string]bool{"resend": true}, map[string]string{}), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(satisfied.Credentials) != 1 || satisfied.Credentials[0].CredentialName != "resend" {
		t.Fatalf("satisfied = %#v", satisfied.Credentials)
	}
	if satisfied.Credentials[0].AlternativeGroup != nil {
		t.Fatalf("satisfied group = %#v", satisfied.Credentials[0].AlternativeGroup)
	}
	if satisfied.HasErrors() {
		t.Fatalf("satisfied has errors: %#v", satisfied.Failed())
	}
}

// Parity: validation.py::validate_agent_credentials phase 2 — present
// credentials with a health endpoint are checked; the result fills valid and
// validation_message (and identity details persist best-effort).
func TestValidateAgentCredentialsHealthCheckAndIdentity(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	store := newStubAgentStore(map[string]bool{"github": true}, map[string]string{"github": "gh-token"})

	invalid := false
	result, err := ValidateAgentCredentials(nodes, store, ValidateAgentCredentialsOptions{
		HealthCheck: func(credentialName, credentialValue, endpoint, method string) HealthCheckResult {
			if credentialName != "github" || credentialValue != "gh-token" {
				t.Fatalf("health check args = %q/%q", credentialName, credentialValue)
			}
			if endpoint != "https://api.github.com/user" || method != "GET" {
				t.Fatalf("health check endpoint/method = %q/%q", endpoint, method)
			}
			return HealthCheckResult{Valid: invalid, Message: "GitHub token is invalid or expired"}
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(result.Credentials) != 1 {
		t.Fatalf("credentials = %d", len(result.Credentials))
	}
	status := result.Credentials[0]
	if status.Valid == nil || *status.Valid {
		t.Fatalf("valid = %#v", status.Valid)
	}
	if status.ValidationMessage == nil || *status.ValidationMessage != "GitHub token is invalid or expired" {
		t.Fatalf("message = %#v", status.ValidationMessage)
	}
	if !result.HasErrors() {
		t.Fatalf("invalid credential not reported as failed")
	}

	valid := true
	result, err = ValidateAgentCredentials(nodes, store, ValidateAgentCredentialsOptions{
		HealthCheck: func(string, string, string, string) HealthCheckResult {
			return HealthCheckResult{
				Valid:   valid,
				Message: "GitHub token valid (user: octocat)",
				Details: map[string]any{"identity": map[string]any{"username": "octocat"}},
			}
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("identity persistence saves = %d, want 1", len(store.saved))
	}
	if value, ok := store.saved[0].GetKeyValue("_identity_username"); !ok || value != "octocat" {
		t.Fatalf("_identity_username = %q/%v", value, ok)
	}
}

// A credential with no value never reaches the checker (reference: `if not
// value: continue`), and SkipVerification leaves valid unset.
func TestValidateAgentCredentialsSkipsEmptyValueAndVerification(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	calls := 0
	opts := ValidateAgentCredentialsOptions{HealthCheck: func(string, string, string, string) HealthCheckResult {
		calls++
		return HealthCheckResult{Valid: true, Message: "ok"}
	}}
	emptyValue := newStubAgentStore(map[string]bool{"github": true}, map[string]string{})
	if _, err := ValidateAgentCredentials(nodes, emptyValue, opts); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if calls != 0 {
		t.Fatalf("checker calls = %d, want 0 for an empty value", calls)
	}

	skip := ValidateAgentCredentialsOptions{SkipVerification: true, HealthCheck: func(string, string, string, string) HealthCheckResult {
		calls++
		return HealthCheckResult{Valid: true, Message: "ok"}
	}}
	withValue := newStubAgentStore(map[string]bool{"github": true}, map[string]string{"github": "tok"})
	result, err := ValidateAgentCredentials(nodes, withValue, skip)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if calls != 0 {
		t.Fatalf("checker calls = %d with verification skipped", calls)
	}
	if result.Credentials[0].Valid != nil {
		t.Fatalf("valid = %#v, want nil", result.Credentials[0].Valid)
	}
}

// Parity: validation.py::compute_unavailable_tools — failed credentials report
// their tools for spawn-time filtering.
func TestComputeUnavailableTools(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	nodes := []AgentNode{{Tools: []string{"github_create_issue", "notion_search"}}}
	dropped, messages := ComputeUnavailableTools(nodes, newStubAgentStore(map[string]bool{}, map[string]string{}))
	if !dropped["github_create_issue"] || !dropped["notion_search"] || len(dropped) != 2 {
		t.Fatalf("dropped = %#v", dropped)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if !strings.Contains(messages[0], "GITHUB_TOKEN (missing) → drops 1 tool(s): github_create_issue") {
		t.Fatalf("message = %q", messages[0])
	}

	satisfied, messages := ComputeUnavailableTools(nodes, newStubAgentStore(map[string]bool{"github": true, "notion": true}, map[string]string{}))
	if len(satisfied) != 0 || len(messages) != 0 {
		t.Fatalf("satisfied dropped = %#v messages = %#v", satisfied, messages)
	}
}

// Aden-only credentials missing while ADEN_API_KEY is set are surfaced as
// aden_not_connected, not as plain missing credentials.
func TestValidateAgentCredentialsAdenNotConnected(t *testing.T) {
	t.Setenv(AdenEnvVar, "aden-key")
	// google is the registry's only Aden-supported, direct-key-unsupported spec;
	// gmail_reply_email is declared by it alone.
	nodes := []AgentNode{{Tools: []string{"gmail_reply_email"}}}
	result, err := ValidateAgentCredentials(nodes, newStubAgentStore(map[string]bool{}, map[string]string{}), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.HasAdenKey {
		t.Fatalf("has_aden_key = false with ADEN_API_KEY set")
	}
	if len(result.Credentials) != 1 || !result.Credentials[0].AdenNotConnected {
		t.Fatalf("credentials = %#v", result.Credentials)
	}
	if names := result.FailedCredNames(); len(names) != 0 {
		t.Fatalf("failed names = %#v, want none (aden-not-connected excluded)", names)
	}
	message := result.FormatErrorMessage()
	if !strings.Contains(message, "Aden integrations not connected") || !strings.Contains(message, "hive.adenhq.com") {
		t.Fatalf("message = %q", message)
	}
}
