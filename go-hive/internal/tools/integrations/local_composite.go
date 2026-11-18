package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// registerREST attaches the declarative provider specs.
func registerREST(r *Registry) {
	r.addSpecs("stripe", stripeSpecs())
	r.addSpecs("slack", slackSpecs())
	r.addSpecs("github", githubSpecs())
	r.addSpecs("telegram", telegramSpecs())
	r.addSpecs("trello", trelloSpecs())
	r.addSpecs("airtable", airtableSpecs())
}

// registerLocal attaches the hand-written implementations: local file tools,
// risk scoring, and the composite provider actions that need more than one
// HTTP round-trip.
func registerLocal(r *Registry) {
	r.addImpl("get_current_time", "get_current_time", getCurrentTime)
	r.addImpl("get_account_info", "get_account_info", getAccountInfo)
	r.addImpl("stripe", "stripe_get_subscription_status", stripeSubscriptionStatus)
	r.addImpl("stripe", "stripe_cancel_subscription", stripeCancelSubscription)
	r.addImpl("csv", "csv_read", csvRead)
	r.addImpl("csv", "csv_write", csvWrite)
	r.addImpl("csv", "csv_append", csvAppend)
	r.addImpl("csv", "csv_info", csvInfo)
	r.addImpl("risk_score", "risk_score", riskScore)

	r.addImpl("slack", "slack_send_dm", slackSendDM)
	r.addImpl("slack", "slack_upload_file", slackUploadFile)
	r.addImpl("slack", "slack_get_messages_for_analysis", slackMessagesForAnalysis)
	r.addImpl("slack", "slack_get_conversation_context", slackConversationContext)
	r.addImpl("slack", "slack_trigger_workflow", slackTriggerWorkflow)

	r.addImpl("github", "github_list_repos", githubListRepos)
	r.addImpl("github", "github_get_user_emails", githubUserEmails)
	r.addImpl("github", "github_list_workflow_runs", githubWorkflowRuns)
}

func (r *Registry) addSpecs(tool string, specs map[string]RestSpec) {
	if r.specs[tool] == nil {
		r.specs[tool] = map[string]RestSpec{}
	}
	for action, spec := range specs {
		r.specs[tool][action] = spec
	}
}

func (r *Registry) addImpl(tool, action string, fn ActionFunc) {
	if r.impls[tool] == nil {
		r.impls[tool] = map[string]ActionFunc{}
	}
	r.impls[tool][action] = fn
}

// slackCall performs one Slack Web API call and surfaces provider errors.
func slackCall(ctx context.Context, rt *Runtime, method string, body map[string]any) (map[string]any, bool) {
	result, err := rt.callHelper(ctx, "slack_tool", "Slack", RestSpec{
		Method: http.MethodPost,
		Path:   "/" + method,
	}, body)
	if err != nil {
		return map[string]any{"error": err.Error()}, false
	}
	object, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"error": "Unexpected Slack response", "response": result}, false
	}
	if _, failed := object["error"]; failed {
		return object, false
	}
	return object, true
}

// slackSendDM opens a DM channel then posts the message.
func slackSendDM(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	userID := stringArg(input, "user_id")
	text := stringArg(input, "text")
	if userID == "" {
		return map[string]any{"error": "user_id is required"}, nil
	}
	if text == "" {
		return map[string]any{"error": "text is required"}, nil
	}
	opened, ok := slackCall(ctx, rt, "conversations.open", map[string]any{"users": userID})
	if !ok {
		return opened, nil
	}
	channel, _ := opened["channel"].(map[string]any)
	channelID := stringArg(channel, "id")
	if channelID == "" {
		return map[string]any{"error": "Failed to open DM channel"}, nil
	}
	sent, ok := slackCall(ctx, rt, "chat.postMessage", map[string]any{"channel": channelID, "text": text})
	if !ok {
		return sent, nil
	}
	return map[string]any{"success": true, "channel": channelID, "ts": sent["ts"]}, nil
}

// slackUploadFile implements Slack's external upload flow:
// files.getUploadURLExternal -> PUT bytes -> files.completeUploadExternal.
func slackUploadFile(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	channel := stringArg(input, "channel")
	content := stringArg(input, "content")
	filename := stringArg(input, "filename")
	if channel == "" || filename == "" {
		return map[string]any{"error": "channel and filename are required"}, nil
	}
	opened, ok := slackCall(ctx, rt, "files.getUploadURLExternal", map[string]any{
		"filename": filename,
		"length":   len(content),
	})
	if !ok {
		return opened, nil
	}
	uploadURL := stringArg(opened, "upload_url")
	fileID := stringArg(opened, "file_id")
	if uploadURL == "" || fileID == "" {
		return map[string]any{"error": "Slack did not return an upload URL"}, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, strings.NewReader(content))
	if err != nil {
		return map[string]any{"error": "Failed to build upload request: " + err.Error()}, nil
	}
	response, err := rt.HTTP.Do(request)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	response.Body.Close()

	file := map[string]any{"id": fileID}
	if title := stringArg(input, "title"); title != "" {
		file["title"] = title
	}
	complete := map[string]any{"files": []any{file}, "channel_id": channel}
	if comment := stringArg(input, "comment"); comment != "" {
		complete["initial_comment"] = comment
	}
	completed, ok := slackCall(ctx, rt, "files.completeUploadExternal", complete)
	if !ok {
		return completed, nil
	}
	return map[string]any{"success": true, "file": completed["files"]}, nil
}

// slackMessagesForAnalysis returns structured history for the model to reason
// over (the upstream tool is intentionally analysis-free).
func slackMessagesForAnalysis(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	channel := stringArg(input, "channel")
	if channel == "" {
		return map[string]any{"error": "channel is required"}, nil
	}
	limit := intArg(input, "limit", 100)
	if limit > 100 {
		limit = 100
	}
	history, ok := slackCall(ctx, rt, "conversations.history", map[string]any{"channel": channel, "limit": limit})
	if !ok {
		return history, nil
	}
	messages, _ := history["messages"].([]any)
	return map[string]any{
		"channel":  channel,
		"messages": messages,
		"count":    len(messages),
	}, nil
}

// slackConversationContext augments history with the user directory entries
// referenced by those messages.
func slackConversationContext(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	channel := stringArg(input, "channel")
	if channel == "" {
		return map[string]any{"error": "channel is required"}, nil
	}
	limit := intArg(input, "limit", 20)
	history, ok := slackCall(ctx, rt, "conversations.history", map[string]any{"channel": channel, "limit": limit})
	if !ok {
		return history, nil
	}
	messages, _ := history["messages"].([]any)
	users := map[string]any{}
	if boolArg(input, "include_user_info", true) {
		seen := map[string]bool{}
		for _, item := range messages {
			message, ok := item.(map[string]any)
			if !ok {
				continue
			}
			userID := stringArg(message, "user")
			if userID == "" || seen[userID] {
				continue
			}
			seen[userID] = true
			if info, ok := slackCall(ctx, rt, "users.info", map[string]any{"user": userID}); ok {
				users[userID] = info["user"]
			}
		}
	}
	return map[string]any{"channel": channel, "messages": messages, "users": users, "count": len(messages)}, nil
}

// slackTriggerWorkflow posts to a user-supplied Workflow Builder webhook.
func slackTriggerWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	webhook := stringArg(input, "webhook_url")
	if webhook == "" {
		return map[string]any{"error": "webhook_url is required"}, nil
	}
	var payload any = map[string]any{}
	if raw := stringArg(input, "payload"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return map[string]any{"error": "Invalid payload JSON: " + err.Error()}, nil
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": "Invalid payload: " + err.Error()}, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, strings.NewReader(string(encoded)))
	if err != nil {
		return map[string]any{"error": "Invalid webhook_url: " + err.Error()}, nil
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := rt.HTTP.Do(request)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return map[string]any{"error": fmt.Sprintf("Workflow webhook returned HTTP %d", response.StatusCode)}, nil
	}
	return map[string]any{"success": true, "status_code": response.StatusCode}, nil
}

// githubListRepos mirrors the username-or-authenticated branch.
func githubListRepos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	spec := RestSpec{Method: http.MethodGet, Path: "/user/repos", Rename: map[string]string{"limit": "per_page"}}
	if username := stringArg(input, "username"); username != "" {
		spec.Path = "/users/{username}/repos"
	}
	return rt.callHelper(ctx, "github_tool", "GitHub", spec, input)
}

func githubUserEmails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return rt.callHelper(ctx, "github_tool", "GitHub", RestSpec{Method: http.MethodGet, Path: "/user/emails"}, input)
}

func githubWorkflowRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	spec := RestSpec{Method: http.MethodGet, Path: "/repos/{owner}/{repo}/actions/runs", Rename: map[string]string{"limit": "per_page"}}
	if workflow := stringArg(input, "workflow_id"); workflow != "" {
		spec.Path = "/repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs"
	}
	return rt.callHelper(ctx, "github_tool", "GitHub", spec, input)
}
