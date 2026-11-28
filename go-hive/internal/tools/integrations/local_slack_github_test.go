package integrations

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Parity-exempt: upstream covers slack_get_conversation_context only
// (test_slack_tool.py::TestSlackGetConversationContext::test_get_conversation_context_success);
// slack_get_messages_for_analysis has no upstream test, so the composite is pinned here.
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
	users, _ := withUsers["users_in_conversation"].([]any)
	if len(users) != 1 || users[0] != "U1" {
		t.Fatalf("users = %v", users)
	}
	if withUsers["message_count"] != 2 {
		t.Fatalf("context = %v", withUsers)
	}
}

// Parity: tools/tests/tools/test_github_tool.py has no get_user_emails test;
// this pins the upstream profile+public-events lookup and its projection.
func TestGitHubUserEmails(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/events/public") {
			return 200, `[{"type":"PushEvent","payload":{"commits":[{"author":{"email":"a@example.com"}},{"author":{"email":"123+octocat@users.noreply.github.com"}},{"author":{"email":"a@example.com"}}]}}]`
		}
		return 200, `{"id":7,"login":"acme"}`
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
	object := result.(map[string]any)
	if object["username"] != "acme" || object["total"] != 1 {
		t.Fatalf("result = %v", object)
	}
	emails, _ := object["emails"].([]any)
	if len(emails) != 1 {
		t.Fatalf("emails = %v", emails)
	}
	entry := emails[0].(map[string]any)
	if entry["email"] != "a@example.com" || entry["source"] != "commit" {
		t.Fatalf("entry = %v", entry)
	}
	requests := provider.snapshot()
	if len(requests) != 2 || requests[0].path != "/users/acme" || requests[1].path != "/users/acme/events/public" {
		t.Fatalf("requests = %v", requests)
	}
}
