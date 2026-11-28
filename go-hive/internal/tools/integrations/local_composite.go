package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// registerREST attaches the declarative provider specs together with the
// upstream response envelope each action returns (audit item A34).
func registerREST(r *Registry) {
	r.AddSpecs("stripe", withShapes(stripeSpecs(), stripeShapes()))
	r.AddSpecs("slack", withShapes(slackSpecs(), slackShapes()))
	r.AddSpecs("github", withShapes(githubSpecs(), githubShapes()))
	r.AddSpecs("telegram", withShapes(telegramSpecs(), telegramShapes()))
	r.AddSpecs("trello", withShapes(trelloSpecs(), trelloShapes()))
	r.AddSpecs("airtable", withShapes(airtableSpecs(), airtableShapes()))
}

// registerLocal attaches the hand-written implementations: local file tools,
// risk scoring, and the composite provider actions that need more than one
// HTTP round-trip.
func registerLocal(r *Registry) {
	r.AddImpl("get_current_time", "get_current_time", getCurrentTime)
	r.AddImpl("get_account_info", "get_account_info", getAccountInfo)
	r.AddImpl("stripe", "stripe_get_subscription_status", stripeSubscriptionStatus)
	r.AddImpl("stripe", "stripe_cancel_subscription", stripeCancelSubscription)
	r.AddImpl("csv", "csv_read", csvRead)
	r.AddImpl("csv", "csv_write", csvWrite)
	r.AddImpl("csv", "csv_append", csvAppend)
	r.AddImpl("csv", "csv_info", csvInfo)
	r.AddImpl("risk_score", "risk_score", riskScore)

	r.AddImpl("slack", "slack_send_dm", slackSendDM)
	r.AddImpl("slack", "slack_upload_file", slackUploadFile)
	r.AddImpl("slack", "slack_get_messages_for_analysis", slackMessagesForAnalysis)
	r.AddImpl("slack", "slack_get_conversation_context", slackConversationContext)
	r.AddImpl("slack", "slack_get_team_stats", slackTeamStats)
	r.AddImpl("slack", "slack_trigger_workflow", slackTriggerWorkflow)

	r.AddImpl("github", "github_list_repos", githubListRepos)
	r.AddImpl("github", "github_get_user_emails", githubUserEmails)
	r.AddImpl("github", "github_list_workflow_runs", githubWorkflowRuns)
}

// slackCall performs one Slack Web API call and surfaces provider errors.
func slackCall(ctx context.Context, rt *Runtime, method string, body map[string]any) (map[string]any, bool) {
	result, err := rt.CallHelper(ctx, "slack_tool", "Slack", RestSpec{
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
// files.getUploadURLExternal -> POST bytes -> files.completeUploadExternal.
func slackUploadFile(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	content := stringArg(input, "content")
	channel := stringArg(input, "channel")
	filename := stringArg(input, "filename")
	title := stringArg(input, "title")
	comment := stringArg(input, "comment")
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
	request.Header.Set("Content-Type", "application/octet-stream")
	upload, err := rt.HTTP.Do(request)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	upload.Body.Close()
	if upload.StatusCode != http.StatusOK {
		return map[string]any{"error": fmt.Sprintf("File upload failed: %d", upload.StatusCode)}, nil
	}

	entry := map[string]any{"id": fileID, "title": title}
	if title == "" {
		entry["title"] = filename
	}
	complete := map[string]any{"files": []any{entry}}
	if channel != "" {
		complete["channel_id"] = channel
	}
	if comment != "" {
		complete["initial_comment"] = comment
	}
	completed, ok := slackCall(ctx, rt, "files.completeUploadExternal", complete)
	if !ok {
		return completed, nil
	}
	files, _ := completed["files"].([]any)
	var file map[string]any
	if len(files) > 0 {
		file, _ = files[0].(map[string]any)
	}
	return map[string]any{"success": true, "file": map[string]any{
		"id":        shapeText(file["id"]),
		"name":      shapeText(file["name"]),
		"title":     shapeText(file["title"]),
		"permalink": shapeText(file["permalink"]),
	}}, nil
}

// slackTeamStats ports the two-call upstream aggregate: team.info then a
// one-row users.list sample for the member-count note.
func slackTeamStats(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	team, ok := slackCall(ctx, rt, "team.info", map[string]any{})
	if !ok {
		return team, nil
	}
	users, usersOK := slackCall(ctx, rt, "users.list", map[string]any{"limit": 1})
	info := shapeDict(team, "team")
	members := []any{}
	if usersOK {
		members = shapeList(users, "members")
	}
	return map[string]any{
		"team_name":           shapeText(info["name"]),
		"team_domain":         shapeText(info["domain"]),
		"team_id":             shapeText(info["id"]),
		"member_count_sample": len(members),
		"note":                "For exact member count, paginate through users.list",
	}, nil
}

// slackMessagesForAnalysis mirrors slack_get_messages_for_analysis: it
// enriches each history row (bot flag, reactions, thread markers) and appends
// thread replies when asked, leaving the interpretation to the model.
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
	messages := shapeList(history, "messages")
	if len(messages) == 0 {
		return map[string]any{"channel": channel, "messages": []any{}, "count": 0}, nil
	}
	includeThreads := boolArg(input, "include_threads", true)
	enriched := make([]any, 0, len(messages))
	for _, item := range messages {
		message := shapeMap(item)
		reactions := make([]any, 0)
		for _, raw := range shapeList(message, "reactions") {
			reaction := shapeMap(raw)
			count := reaction["count"]
			if count == nil {
				count = float64(0)
			}
			reactions = append(reactions, map[string]any{
				"emoji": shapeText(reaction["name"]),
				"count": count,
			})
		}
		replyCount := shapeNumber(message, "reply_count")
		hasReplies := shapeInt(replyCount) > 0
		row := map[string]any{
			"text":             shapeText(message["text"]),
			"user":             shapeText(message["user"]),
			"ts":               shapeText(message["ts"]),
			"is_bot":           shapeTruthy(message["bot_id"]),
			"reactions":        reactions,
			"reply_count":      replyCount,
			"is_thread_parent": shapeTruthy(message["thread_ts"]) && hasReplies,
			"is_thread_reply":  shapeTruthy(message["parent_user_id"]),
		}
		if includeThreads && hasReplies {
			thread, ok := slackCall(ctx, rt, "conversations.replies", map[string]any{
				"channel": channel,
				"ts":      stringArg(message, "ts"),
			})
			if ok {
				replies := shapeList(thread, "messages")
				projected := make([]any, 0, len(replies))
				for _, rawReply := range replies[1:] {
					reply := shapeMap(rawReply)
					projected = append(projected, map[string]any{
						"text": shapeText(reply["text"]),
						"user": shapeText(reply["user"]),
					})
				}
				row["thread_replies"] = projected
			}
		}
		enriched = append(enriched, row)
	}
	return map[string]any{
		"channel":  channel,
		"count":    len(enriched),
		"messages": enriched,
		"note": "Use your intelligence to analyze this data. " +
			"Look for patterns, unanswered questions, " +
			"engagement levels, sentiment and anything user asks for.",
	}, nil
}

// slackConversationContext mirrors slack_get_conversation_context: resolve
// user ids to real names in first-appearance order and report the roster.
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
	messages := shapeList(history, "messages")
	includeUsers := boolArg(input, "include_user_info", true)
	cache := map[string]string{}
	order := []string{}
	projected := make([]any, 0, len(messages))
	for _, item := range messages {
		message := shapeMap(item)
		userID := stringArg(message, "user")
		if userID == "" {
			userID = "unknown"
		}
		name := userID
		if includeUsers && userID != "unknown" {
			if _, seen := cache[userID]; !seen {
				name = userID
				if info, ok := slackCall(ctx, rt, "users.info", map[string]any{"user": userID}); ok {
					if user, ok := info["user"].(map[string]any); ok {
						if realName := stringArg(user, "real_name"); realName != "" {
							name = realName
						}
					}
				}
				cache[userID] = name
				order = append(order, userID)
			}
			name = cache[userID]
		}
		projected = append(projected, map[string]any{
			"user_id":     userID,
			"user_name":   name,
			"text":        shapeText(message["text"]),
			"ts":          shapeText(message["ts"]),
			"has_replies": shapeInt(shapeNumber(message, "reply_count")) > 0,
		})
	}
	users := make([]any, 0, len(order))
	for _, userID := range order {
		users = append(users, cache[userID])
	}
	return map[string]any{
		"channel":               channel,
		"message_count":         len(projected),
		"messages":              projected,
		"users_in_conversation": users,
	}, nil
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
	if response.StatusCode != http.StatusOK {
		return map[string]any{"error": fmt.Sprintf("Workflow trigger failed: %d", response.StatusCode)}, nil
	}
	return map[string]any{"success": true, "status_code": response.StatusCode}, nil
}

// githubListRepos mirrors the username-or-authenticated branch.
func githubListRepos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	spec := RestSpec{Method: http.MethodGet, Path: "/user/repos", Rename: map[string]string{"limit": "per_page"}}
	if username := stringArg(input, "username"); username != "" {
		spec.Path = "/users/{username}/repos"
	}
	result, err := rt.CallHelper(ctx, "github_tool", "GitHub", spec, input)
	if err != nil {
		return result, err
	}
	return githubSuccess(result, input), nil
}

// githubUserEmails mirrors github_get_user_emails: the profile lookup is made
// first (upstream reads its raw "email" key, which the {"success","data"}
// envelope never carries), then public PushEvent commits supply the addresses.
func githubUserEmails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	// The upstream profile call contributes no address: it inspects
	// profile.get("email") on the wrapped {"success","data"} document. The
	// request is still made so request traces match.
	_, _ = rt.CallHelper(ctx, "github_tool", "GitHub", RestSpec{
		Method: http.MethodGet,
		Path:   "/users/{username}",
	}, input)

	events, _ := rt.CallHelper(ctx, "github_tool", "GitHub", RestSpec{
		Method:    http.MethodGet,
		Path:      "/users/{username}/events/public",
		Constants: map[string]any{"per_page": 30},
	}, input)
	emails := []githubEmailEntry{}
	seen := map[string]bool{}
	for _, value := range shapeArray(events) {
		event := shapeMap(value)
		if stringArg(event, "type") != "PushEvent" {
			continue
		}
		for _, rawCommit := range shapeList(shapeDict(event, "payload"), "commits") {
			commit := shapeMap(rawCommit)
			address := stringArg(shapeDict(commit, "author"), "email")
			if address == "" || !strings.Contains(address, "@") ||
				strings.Contains(strings.ToLower(address), "noreply") || seen[address] {
				continue
			}
			seen[address] = true
			emails = append(emails, githubEmailEntry{Email: address, Source: "commit"})
		}
	}
	out := make([]any, 0, len(emails))
	for _, entry := range emails {
		out = append(out, map[string]any{"email": entry.Email, "source": entry.Source})
	}
	return map[string]any{"username": username, "emails": out, "total": len(out)}, nil
}

func githubWorkflowRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	spec := RestSpec{Method: http.MethodGet, Path: "/repos/{owner}/{repo}/actions/runs", Rename: map[string]string{"limit": "per_page"}}
	if workflow := stringArg(input, "workflow_id"); workflow != "" {
		spec.Path = "/repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs"
	}
	result, err := rt.CallHelper(ctx, "github_tool", "GitHub", spec, input)
	if err != nil {
		return result, err
	}
	return githubSuccess(result, input), nil
}
