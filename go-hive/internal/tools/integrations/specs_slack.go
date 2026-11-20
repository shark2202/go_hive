package integrations

import "net/http"

// Slack's Web API is uniformly POST <base>/<method> with a JSON body; a
// handful of actions compose multiple calls and live in slack_local.go.
type slackRoute struct {
	action     string
	method     string
	body       []string
	rename     map[string]string
	join       []string
	builder    func(map[string]any) map[string]any
	constant   map[string]any
	pagination *PaginationSpec
}

func slackSpecs() map[string]RestSpec {
	// conversations.list and users.list return at most one page plus a
	// response_metadata.next_cursor; the declarative walk follows it so the
	// list actions return every channel/user the bot can see.
	slackCursor := func(items string) *PaginationSpec {
		return &PaginationSpec{
			ItemsPath:      items,
			NextCursorPath: "response_metadata.next_cursor",
			CursorParam:    "cursor",
		}
	}
	routes := []slackRoute{
		{action: "slack_send_message", method: "chat.postMessage"},
		{action: "slack_list_channels", method: "conversations.list", pagination: slackCursor("channels")},
		{action: "slack_get_channel_history", method: "conversations.history"},
		{action: "slack_add_reaction", method: "reactions.add", rename: map[string]string{"timestamp": "timestamp"}},
		{action: "slack_get_user_info", method: "users.info", rename: map[string]string{"user_id": "user"}},
		{action: "slack_update_message", method: "chat.update"},
		{action: "slack_delete_message", method: "chat.delete"},
		{action: "slack_schedule_message", method: "chat.scheduleMessage"},
		{action: "slack_create_channel", method: "conversations.create"},
		{action: "slack_archive_channel", method: "conversations.archive"},
		{
			action: "slack_invite_to_channel", method: "conversations.invite",
			join: []string{"user_ids"},
		},
		{action: "slack_set_channel_topic", method: "conversations.setTopic"},
		{action: "slack_remove_reaction", method: "reactions.remove"},
		{action: "slack_list_users", method: "users.list", pagination: slackCursor("members")},
		{action: "slack_search_messages", method: "search.messages"},
		{action: "slack_get_thread_replies", method: "conversations.replies", rename: map[string]string{"thread_ts": "ts"}},
		{action: "slack_pin_message", method: "pins.add"},
		{action: "slack_unpin_message", method: "pins.remove"},
		{action: "slack_list_pins", method: "pins.list"},
		{action: "slack_add_bookmark", method: "bookmarks.add"},
		{action: "slack_list_scheduled_messages", method: "chat.scheduledMessages.list"},
		{action: "slack_delete_scheduled_message", method: "chat.deleteScheduledMessage"},
		{action: "slack_get_permalink", method: "chat.getPermalink", rename: map[string]string{"message_ts": "message_ts"}},
		{action: "slack_send_ephemeral", method: "chat.postEphemeral"},
		{action: "slack_post_blocks", method: "chat.postMessage"},
		{
			action: "slack_open_modal", method: "views.open",
			body: []string{}, builder: slackModalView,
		},
		{
			action: "slack_update_home_tab", method: "views.publish",
			body: []string{}, builder: slackHomeView,
		},
		{
			action: "slack_set_status", method: "users.profile.set",
			body: []string{}, builder: slackProfile,
		},
		{action: "slack_set_presence", method: "users.setPresence"},
		{action: "slack_get_presence", method: "users.getPresence", rename: map[string]string{"user_id": "user"}},
		{action: "slack_create_reminder", method: "reminders.add"},
		{action: "slack_list_reminders", method: "reminders.list"},
		{action: "slack_delete_reminder", method: "reminders.delete"},
		{action: "slack_create_usergroup", method: "usergroups.create"},
		{
			action: "slack_update_usergroup_members", method: "usergroups.users.update",
			body: []string{"usergroup_id", "user_ids"}, rename: map[string]string{"user_ids": "users"}, join: []string{"users"},
		},
		{action: "slack_list_usergroups", method: "usergroups.list"},
		{action: "slack_list_emoji", method: "emoji.list"},
		{
			action: "slack_create_canvas", method: "canvases.create",
			body: []string{}, builder: slackCreateCanvas,
		},
		{
			action: "slack_edit_canvas", method: "canvases.edit",
			body: []string{}, builder: slackEditCanvas,
		},
		{action: "slack_find_user_by_email", method: "users.lookupByEmail", rename: map[string]string{"email": "email"}},
		{action: "slack_kick_user_from_channel", method: "conversations.kick"},
		{action: "slack_delete_file", method: "files.delete"},
		{action: "slack_get_team_stats", method: "team.info"},
		{action: "slack_get_channel_info", method: "conversations.info"},
		{action: "slack_list_files", method: "files.list"},
		{action: "slack_get_file_info", method: "files.info"},
	}
	specs := make(map[string]RestSpec, len(routes))
	for _, route := range routes {
		spec := RestSpec{
			Method:     http.MethodPost,
			Path:       "/" + route.method,
			Body:       route.body,
			Rename:     route.rename,
			JoinLists:  route.join,
			Build:      route.builder,
			Constants:  route.constant,
			Pagination: route.pagination,
		}
		specs[route.action] = spec
	}
	return specs
}

func slackModalView(input map[string]any) map[string]any {
	view := map[string]any{
		"type":   "modal",
		"title":  map[string]any{"type": "plain_text", "text": stringArg(input, "title")},
		"blocks": input["blocks"],
	}
	if label := stringArg(input, "submit_label"); label != "" {
		view["submit"] = map[string]any{"type": "plain_text", "text": label}
	}
	if label := stringArg(input, "close_label"); label != "" {
		view["close"] = map[string]any{"type": "plain_text", "text": label}
	}
	if callback := stringArg(input, "callback_id"); callback != "" {
		view["callback_id"] = callback
	}
	return map[string]any{"trigger_id": stringArg(input, "trigger_id"), "view": view}
}

func slackHomeView(input map[string]any) map[string]any {
	return map[string]any{
		"user_id": stringArg(input, "user_id"),
		"view": map[string]any{
			"type":   "home",
			"blocks": input["blocks"],
		},
	}
}

func slackProfile(input map[string]any) map[string]any {
	profile := map[string]any{}
	if text := stringArg(input, "status_text"); text != "" {
		profile["status_text"] = text
	}
	if emoji := stringArg(input, "status_emoji"); emoji != "" {
		profile["status_emoji"] = emoji
	}
	if minutes := intArg(input, "expiration_minutes", 0); minutes > 0 {
		profile["status_expiration"] = minutes * 60
	}
	return map[string]any{"profile": profile}
}

func slackCreateCanvas(input map[string]any) map[string]any {
	return map[string]any{
		"title": stringArg(input, "title"),
		"document_content": map[string]any{
			"type":     "markdown",
			"markdown": stringArg(input, "markdown_content"),
		},
	}
}

func slackEditCanvas(input map[string]any) map[string]any {
	operation := stringArg(input, "operation")
	if operation == "" {
		operation = "insert_after"
	}
	return map[string]any{
		"canvas_id": stringArg(input, "canvas_id"),
		"changes": []any{map[string]any{
			"operation": operation,
			"document_content": map[string]any{
				"type":     "markdown",
				"markdown": stringArg(input, "markdown_content"),
			},
		}},
	}
}
