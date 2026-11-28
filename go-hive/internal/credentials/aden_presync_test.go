package credentials

// Parity: framework/credentials/validation.py::_presync_aden_tokens

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeAden is the httptest backend for the pre-sync tests: one list endpoint,
// one get-token endpoint, and switches for the fail-soft paths. No real
// network and no real secret is involved.
type fakeAden struct {
	url           string
	integrations  []map[string]any
	accessToken   string
	listStatus    int
	getStatus     int
	malformedList bool
	dropList      bool

	mu       sync.Mutex
	listHits int
	getHits  map[string]int
}

func newFakeAden(t *testing.T, accessToken string, integrations ...map[string]any) *fakeAden {
	t.Helper()
	fake := &fakeAden{accessToken: accessToken, integrations: integrations, getHits: map[string]int{}}
	server := httptest.NewServer(fake)
	fake.url = server.URL
	t.Cleanup(server.Close)
	t.Setenv(AdenBaseURLEnvVar, server.URL)
	return fake
}

func (f *fakeAden) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/credentials":
		f.mu.Lock()
		f.listHits++
		f.mu.Unlock()
		if f.dropList {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		if f.listStatus != 0 {
			http.Error(w, "aden list failure", f.listStatus)
			return
		}
		if f.malformedList {
			_, _ = w.Write([]byte("not-json"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"integrations": f.integrations})
	case strings.HasPrefix(r.URL.Path, "/v1/credentials/"):
		integrationID := strings.TrimPrefix(r.URL.Path, "/v1/credentials/")
		f.mu.Lock()
		f.getHits[integrationID]++
		f.mu.Unlock()
		if f.getStatus != 0 {
			http.Error(w, "aden get failure", f.getStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"integration_id": integrationID,
			"access_token":   f.accessToken,
			"token_type":     "Bearer",
			"provider":       "github",
		})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAden) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gets := 0
	for _, count := range f.getHits {
		gets += count
	}
	return f.listHits, gets
}

func githubIntegration() map[string]any {
	return map[string]any{
		"integration_id": "github-integration-id",
		"provider":       "github",
		"alias":          "octocat",
		"status":         "active",
	}
}

func envBackedAgentStore() *CredentialStore {
	return NewCredentialStore(WithStorage(NewEnvVarStorage(RegistryEnvMapping())))
}

// Parity: framework/credentials/validation.py::_presync_aden_tokens — an
// active Aden integration is listed, its access token is fetched under
// credential_key and exported to the spec's env var before presence checks.
func TestValidateAgentCredentialsAdenPresyncExportsToken(t *testing.T) {
	t.Setenv(AdenEnvVar, "test-aden-key")
	t.Setenv("GITHUB_TOKEN", "")
	fake := newFakeAden(t, "gh-fresh-token", githubIntegration())

	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	result, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "gh-fresh-token" {
		t.Fatalf("GITHUB_TOKEN = %q, want pre-synced token", got)
	}
	if len(result.Credentials) != 1 || !result.Credentials[0].Available {
		t.Fatalf("credentials = %#v, want github available from pre-synced env", result.Credentials)
	}
	listHits, getHits := fake.counts()
	if listHits != 1 || getHits != 1 {
		t.Fatalf("requests = list %d / get %d, want 1/1", listHits, getHits)
	}
	if fake.getHits["github-integration-id"] != 1 {
		t.Fatalf("get hits = %#v, want exactly the listed integration", fake.getHits)
	}
}

// An explicit user export wins while force is false (upstream env-wins rule);
// Aden is still listed but no integration is fetched.
// Parity: framework/credentials/validation.py::_presync_aden_tokens
func TestValidateAgentCredentialsAdenPresyncKeepsUserEnv(t *testing.T) {
	t.Setenv(AdenEnvVar, "test-aden-key")
	t.Setenv("GITHUB_TOKEN", "user-exported-token")
	fake := newFakeAden(t, "gh-aden-token", githubIntegration())

	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	if _, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{SkipVerification: true}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "user-exported-token" {
		t.Fatalf("GITHUB_TOKEN = %q, want user export preserved", got)
	}
	listHits, getHits := fake.counts()
	if listHits != 1 || getHits != 0 {
		t.Fatalf("requests = list %d / get %d, want 1/0", listHits, getHits)
	}
}

// ForceRefresh overwrites an already-exported env var with Aden's fresh token
// (the credentials modal after reauthorization).
// Parity: framework/credentials/validation.py::_presync_aden_tokens
func TestValidateAgentCredentialsAdenPresyncForceOverwrites(t *testing.T) {
	t.Setenv(AdenEnvVar, "test-aden-key")
	t.Setenv("GITHUB_TOKEN", "stale-token")
	fake := newFakeAden(t, "gh-reauthorized-token", githubIntegration())

	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	_, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{
		SkipVerification: true,
		ForceRefresh:     true,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "gh-reauthorized-token" {
		t.Fatalf("GITHUB_TOKEN = %q, want forced refresh", got)
	}
	if _, getHits := fake.counts(); getHits != 1 {
		t.Fatalf("get hits = %d, want 1 with force", getHits)
	}
}

// An Aden-supported spec absent from Aden's list is skipped, not fetched by
// name, and stays a normal missing status.
// Parity: framework/credentials/validation.py::_presync_aden_tokens
func TestValidateAgentCredentialsAdenPresyncSkipsMissing(t *testing.T) {
	t.Setenv(AdenEnvVar, "test-aden-key")
	t.Setenv("GITHUB_TOKEN", "")
	fake := newFakeAden(t, "unused-token")

	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	result, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "" {
		t.Fatalf("GITHUB_TOKEN = %q, want empty when absent from Aden", got)
	}
	if len(result.Credentials) != 1 || result.Credentials[0].Available {
		t.Fatalf("credentials = %#v, want github missing", result.Credentials)
	}
	listHits, getHits := fake.counts()
	if listHits != 1 || getHits != 0 {
		t.Fatalf("requests = list %d / get %d, want 1/0 (no arbitrary ids)", listHits, getHits)
	}
}

// Aden failures are diagnostics, never validation failures.
// Parity: framework/credentials/validation.py::_presync_aden_tokens
func TestValidateAgentCredentialsAdenPresyncFailSoft(t *testing.T) {
	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}

	run := func(t *testing.T) AgentCredentialValidationResult {
		t.Helper()
		result, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{SkipVerification: true})
		if err != nil {
			t.Fatalf("validate returned an Aden error: %v", err)
		}
		return result
	}

	t.Run("list http error", func(t *testing.T) {
		t.Setenv(AdenEnvVar, "test-aden-key")
		t.Setenv("GITHUB_TOKEN", "")
		fake := newFakeAden(t, "unused-token")
		fake.listStatus = http.StatusInternalServerError

		result := run(t)
		if got := os.Getenv("GITHUB_TOKEN"); got != "" {
			t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
		}
		if len(result.Credentials) != 1 || result.Credentials[0].Available {
			t.Fatalf("credentials = %#v, want github missing", result.Credentials)
		}
	})

	t.Run("malformed list payload", func(t *testing.T) {
		t.Setenv(AdenEnvVar, "test-aden-key")
		t.Setenv("GITHUB_TOKEN", "")
		fake := newFakeAden(t, "unused-token")
		fake.malformedList = true

		run(t)
		if got := os.Getenv("GITHUB_TOKEN"); got != "" {
			t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
		}
	})

	t.Run("get failure continues", func(t *testing.T) {
		t.Setenv(AdenEnvVar, "test-aden-key")
		t.Setenv("GITHUB_TOKEN", "")
		fake := newFakeAden(t, "unused-token", githubIntegration())
		fake.getStatus = http.StatusInternalServerError

		run(t)
		if got := os.Getenv("GITHUB_TOKEN"); got != "" {
			t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
		}
		if _, getHits := fake.counts(); getHits != 1 {
			t.Fatalf("get hits = %d, want 1", getHits)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		t.Setenv(AdenEnvVar, "test-aden-key")
		t.Setenv("GITHUB_TOKEN", "")
		fake := newFakeAden(t, "unused-token")
		fake.dropList = true
		// One attempt keeps the connection-drop check fast; the production
		// default still retries transport failures.
		client, err := NewAdenCredentialClient(AdenClientConfig{
			BaseURL:       fake.url,
			APIKey:        "test-aden-key",
			RetryAttempts: 1,
		})
		if err != nil {
			t.Fatalf("build client: %v", err)
		}
		result, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{
			SkipVerification: true,
			AdenClient:       client,
		})
		if err != nil {
			t.Fatalf("validate returned an Aden error: %v", err)
		}
		if got := os.Getenv("GITHUB_TOKEN"); got != "" {
			t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
		}
		if len(result.Credentials) != 1 || result.Credentials[0].Available {
			t.Fatalf("credentials = %#v, want github missing", result.Credentials)
		}
		if listHits, _ := fake.counts(); listHits != 1 {
			t.Fatalf("list hits = %d, want 1", listHits)
		}
	})
}

// Without ADEN_API_KEY the pre-sync is a no-op: no client, no requests.
// Parity: framework/credentials/validation.py::validate_agent_credentials
func TestValidateAgentCredentialsAdenPresyncNoKeyNoop(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	t.Setenv("GITHUB_TOKEN", "")
	fake := newFakeAden(t, "unused-token")
	fake.listStatus = http.StatusInternalServerError

	nodes := []AgentNode{{Tools: []string{"github_create_issue"}}}
	result, err := ValidateAgentCredentials(nodes, envBackedAgentStore(), ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.HasAdenKey {
		t.Fatal("HasAdenKey = true without ADEN_API_KEY")
	}
	if listHits, getHits := fake.counts(); listHits != 0 || getHits != 0 {
		t.Fatalf("requests = list %d / get %d, want 0/0", listHits, getHits)
	}
}
