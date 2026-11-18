package integrations

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Parity-exempt: upstream has no Python test; behavior ported from
// tools/src/aden_tools/tools/slack_tool/slack_tool.py::get_messages_for_analysis
// and ::get_conversation_context.
func TestSlackAnalysisComposites(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "users.info") {
			return 200, `{"ok":true,"user":{"id":"U1","name":"alice"}}`
		}
		return 200, `{"ok":true,"messages":[{"text":"hi","user":"U1"},{"text":"hello","user":"U1"}]}`
	})
	registry := New(
		WithBaseURL("slack_tool", provider.server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(provider.server.Client()),
	)
	analysis, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_get_messages_for_analysis", "channel": "C1", "limit": 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	object := analysis.(map[string]any)
	if object["count"] != 2 || object["channel"] != "C1" {
		t.Fatalf("analysis = %v", object)
	}

	contextResult, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_get_conversation_context", "channel": "C1", "limit": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	withUsers := contextResult.(map[string]any)
	users, _ := withUsers["users"].(map[string]any)
	if len(users) != 1 || users["U1"] == nil {
		t.Fatalf("users = %v", users)
	}
}

// Parity-exempt: upstream has no Python test; behavior ported from
// tools/src/aden_tools/tools/github_tool/github_tool.py::github_get_user_emails.
func TestGitHubUserEmails(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `[{"email":"a@example.com","primary":true}]`
	})
	registry := New(
		WithBaseURL("github_tool", provider.server.URL),
		WithCredential("github_tool", "ghp_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_get_user_emails", "username": "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	emails, ok := result.([]any)
	if !ok || len(emails) != 1 {
		t.Fatalf("result = %v", result)
	}
	if path := provider.snapshot()[0].path; path != "/user/emails" {
		t.Fatalf("path = %s", path)
	}
}
