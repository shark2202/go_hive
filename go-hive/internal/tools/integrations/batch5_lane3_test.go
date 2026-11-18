package integrations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// containsAll reports whether every substring is present.
func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

// batch5Lane3TestRegistry points the four Google modules (and the Docs drive
// host) at one fake upstream and installs a shared test credential.
func batch5Lane3TestRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(batch5Lane3SheetsModule, server.URL),
		WithBaseURL(batch5Lane3DocsModule, server.URL),
		WithBaseURL(batch5Lane3DriveModule, server.URL),
		WithBaseURL(batch5Lane3GmailModule, server.URL),
		WithBaseURL(batch5Lane3CalModule, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(batch5Lane3SheetsModule, "test-token"),
		WithCredential(batch5Lane3DocsModule, "test-token"),
		WithCredential(batch5Lane3GmailModule, "test-token"),
		WithCredential(batch5Lane3CalModule, "test-token"),
	}
	return newBatch5Lane3Registry(append(base, opts...)...), recorder
}

// batch5Lane3RequireBearer asserts the shared Google access token is sent.
func batch5Lane3RequireBearer(t *testing.T, recorder *batch4Recorder) {
	t.Helper()
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// has no equivalent registry surface.
func TestBatch5Lane3Catalog(t *testing.T) {
	registry := newBatch5Lane3Registry()
	want := map[string]int{
		"google_sheets": 10,
		"google_docs":   11,
		"gmail":         10,
		"calendar":      8,
	}
	for name, count := range want {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if len(tool.Actions) != count {
			t.Fatalf("%s actions = %d, want %d", name, len(tool.Actions), count)
		}
		for _, action := range tool.Actions {
			if registry.impls[name][action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
	if registry.ActionCount() != 39 {
		t.Fatalf("ActionCount = %d, want 39", registry.ActionCount())
	}
	if registry.ImplementedActions() != 39 {
		t.Fatalf("ImplementedActions = %d, want 39", registry.ImplementedActions())
	}
}

// Parity-exempt: missing credentials are a Go-side contract shared with the
// other ported batches.
func TestBatch5Lane3MissingCredentials(t *testing.T) {
	registry := newBatch5Lane3Registry(WithGetenv(func(string) string { return "" }))
	for _, tool := range []string{"google_sheets", "google_docs", "gmail", "calendar"} {
		action := registry.tools[tool].Actions[0].Name
		result := batch4Execute(t, registry, tool, map[string]any{"action": action})
		if _, ok := result["error"]; !ok {
			t.Fatalf("%s without credential = %v", tool, result)
		}
	}
}
