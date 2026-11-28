package integrations

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackSendDM::test_send_dm_success
func TestSlackSendDMTwoStep(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "conversations.open") {
			return 200, `{"ok":true,"channel":{"id":"D1"}}`
		}
		return 200, `{"ok":true,"ts":"1700000000.1"}`
	})
	registry := New(
		WithBaseURL("slack_tool", provider.server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_send_dm", "user_id": "U1", "text": "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["success"] != true || object["channel"] != "D1" {
		t.Fatalf("result = %v", object)
	}
	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].path != "/conversations.open" || requests[1].path != "/chat.postMessage" {
		t.Fatalf("paths = %s, %s", requests[0].path, requests[1].path)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackUploadFile::test_upload_file_success
func TestSlackUploadFileThreeStep(t *testing.T) {
	var provider *fakeProvider
	provider = newFakeProvider(t, func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "files.getUploadURLExternal"):
			return 200, `{"ok":true,"upload_url":"` + provider.server.URL + `/upload","file_id":"F1"}`
		case r.URL.Path == "/upload":
			return 200, `OK`
		default:
			return 200, `{"ok":true,"files":[{"id":"F1","title":"report"}]}`
		}
	})
	registry := New(
		WithBaseURL("slack_tool", provider.server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_upload_file", "channel": "C1", "content": "report body", "filename": "r.txt", "title": "report",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["success"] != true {
		t.Fatalf("result = %v", object)
	}
	requests := provider.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[1].path != "/upload" || string(requests[1].body) != "report body" {
		t.Fatalf("upload = %s %q", requests[1].path, requests[1].body)
	}
	if requests[2].path != "/files.completeUploadExternal" {
		t.Fatalf("complete path = %s", requests[2].path)
	}
}

// Parity-exempt: upstream has no slack_trigger_workflow test; the webhook POST and the
// invalid-payload branch are pinned here only.
func TestSlackTriggerWorkflowWebhook(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `ok`
	})
	registry := New(WithHTTPClient(provider.server.Client()))
	result, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action":      "slack_trigger_workflow",
		"webhook_url": provider.server.URL + "/hook",
		"payload":     `{"event":"deploy"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["success"] != true {
		t.Fatalf("result = %v", object)
	}
	request := provider.snapshot()[0]
	if request.path != "/hook" || !strings.Contains(string(request.body), "deploy") {
		t.Fatalf("request = %s %s", request.path, request.body)
	}

	invalid, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_trigger_workflow", "webhook_url": provider.server.URL + "/hook", "payload": "{",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(invalid.(map[string]any)["error"].(string), "Invalid payload JSON") {
		t.Fatalf("invalid = %v", invalid)
	}
}

// Parity: tools/tests/tools/test_github_tool.py::TestGitHubClient::test_list_repos, tools/tests/tools/test_github_tool.py::TestGitHubClient::test_list_repos_authenticated_user
func TestGitHubListReposBranches(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `[]`
	})
	registry := New(
		WithBaseURL("github_tool", provider.server.URL),
		WithCredential("github_tool", "ghp_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_list_repos", "username": "acme", "limit": 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_list_repos",
	}); err != nil {
		t.Fatal(err)
	}
	requests := provider.snapshot()
	if requests[0].path != "/users/acme/repos" || requests[0].query["per_page"] != "5" {
		t.Fatalf("first = %s %v", requests[0].path, requests[0].query)
	}
	if requests[1].path != "/user/repos" {
		t.Fatalf("second = %s", requests[1].path)
	}
}

// Parity-exempt: upstream has no github_list_workflow_runs test; the workflow-runs path
// is pinned here only.
func TestGitHubWorkflowRunsBranch(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"workflow_runs":[]}`
	})
	registry := New(
		WithBaseURL("github_tool", provider.server.URL),
		WithCredential("github_tool", "ghp_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_list_workflow_runs", "owner": "acme", "repo": "widgets", "workflow_id": "ci.yml",
	}); err != nil {
		t.Fatal(err)
	}
	if path := provider.snapshot()[0].path; path != "/repos/acme/widgets/actions/workflows/ci.yml/runs" {
		t.Fatalf("path = %s", path)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackSendMessage::test_send_message_channel_not_found
func TestSlackErrorEnvelope(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"ok":false,"error":"channel_not_found"}`
	})
	registry := New(
		WithBaseURL("slack_tool", provider.server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_list_channels",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["error"] != "Channel not found or bot is not a member" || object["error_code"] != "channel_not_found" {
		t.Fatalf("result = %v", object)
	}
}
