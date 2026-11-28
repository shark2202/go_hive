package integrations

import (
	"sort"
	"strings"
)

// Slack's upstream tool rebuilds a small projection per action; only the file
// and user lookup endpoints (and the two removal actions that answer
// {"ok":true}) return the wire body, and those register rawShape explicitly.
func slackShapes() map[string]shapeFunc {
	return map[string]shapeFunc{
		"slack_send_message":             slackSendMessageShape,
		"slack_list_channels":            slackListChannelsShape,
		"slack_get_channel_history":      slackHistoryShape,
		"slack_add_reaction":             slackOKShape,
		"slack_get_user_info":            slackUserInfoShape,
		"slack_update_message":           slackUpdateMessageShape,
		"slack_delete_message":           slackOKShape,
		"slack_schedule_message":         slackScheduleShape,
		"slack_create_channel":           slackCreateChannelShape,
		"slack_archive_channel":          slackOKShape,
		"slack_invite_to_channel":        slackOKShape,
		"slack_set_channel_topic":        slackSetTopicShape,
		"slack_remove_reaction":          slackOKShape,
		"slack_list_users":               slackListUsersShape,
		"slack_search_messages":          slackSearchShape,
		"slack_get_thread_replies":       slackThreadShape,
		"slack_pin_message":              slackOKShape,
		"slack_unpin_message":            slackOKShape,
		"slack_list_pins":                slackListPinsShape,
		"slack_add_bookmark":             slackBookmarkShape,
		"slack_list_scheduled_messages":  slackScheduledShape,
		"slack_delete_scheduled_message": slackOKShape,
		"slack_get_permalink":            slackPermalinkShape,
		"slack_send_ephemeral":           slackEphemeralShape,
		"slack_post_blocks":              slackPostBlocksShape,
		"slack_open_modal":               slackViewShape,
		"slack_update_home_tab":          slackViewShape,
		"slack_set_status":               slackSetStatusShape,
		"slack_set_presence":             slackSetPresenceShape,
		"slack_get_presence":             slackGetPresenceShape,
		"slack_create_reminder":          slackCreateReminderShape,
		"slack_list_reminders":           slackListRemindersShape,
		"slack_delete_reminder":          slackDeleteReminderShape,
		"slack_create_usergroup":         slackCreateUsergroupShape,
		"slack_update_usergroup_members": slackUpdateUsergroupShape,
		"slack_list_usergroups":          slackListUsergroupsShape,
		"slack_list_emoji":               slackListEmojiShape,
		"slack_create_canvas":            slackCreateCanvasShape,
		"slack_edit_canvas":              slackEditCanvasShape,
		"slack_find_user_by_email":       rawShape,
		"slack_kick_user_from_channel":   rawShape,
		"slack_delete_file":              rawShape,
		"slack_get_channel_info":         slackChannelInfoShape,
		"slack_list_files":               slackListFilesShape,
		"slack_get_file_info":            slackFileInfoShape,
	}
}

// slackOKShape mirrors the actions whose upstream wrapper only reports
// success (reactions, pin/unpin, archive, invite, deletes).
func slackOKShape(parsed any, _ map[string]any) any {
	return map[string]any{"success": true}
}

// slackSendMessageShape mirrors slack_send_message:
//
//	{"success":true,"channel":...,"ts":...,"message":result.get("message",{})}
func slackSendMessageShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{
		"success": true,
		"channel": shapeText(object["channel"]),
		"ts":      shapeText(object["ts"]),
		"message": shapeDict(object, "message"),
	}
}

// slackListChannelsShape mirrors slack_list_channels.
func slackListChannelsShape(parsed any, _ map[string]any) any {
	channels := shapeList(shapeMap(parsed), "channels")
	out := make([]any, 0, len(channels))
	for _, value := range channels {
		channel := shapeMap(value)
		out = append(out, map[string]any{
			"id":          shapeText(channel["id"]),
			"name":        shapeText(channel["name"]),
			"is_private":  shapeBool(channel, "is_private", false),
			"num_members": shapeNumber(channel, "num_members"),
			"topic":       shapeTextOr(shapeAt(channel, "topic.value"), ""),
		})
	}
	return map[string]any{"success": true, "channels": out, "count": len(out)}
}

// slackHistoryShape mirrors slack_get_channel_history.
func slackHistoryShape(parsed any, _ map[string]any) any {
	messages := shapeList(shapeMap(parsed), "messages")
	out := make([]any, 0, len(messages))
	for _, value := range messages {
		message := shapeMap(value)
		out = append(out, map[string]any{
			"ts":        shapeText(message["ts"]),
			"user":      shapeText(message["user"]),
			"text":      shapeText(message["text"]),
			"type":      shapeText(message["type"]),
			"thread_ts": shapeText(message["thread_ts"]),
		})
	}
	return map[string]any{"success": true, "messages": out, "count": len(out)}
}

// slackUserInfoShape mirrors slack_get_user_info.
func slackUserInfoShape(parsed any, _ map[string]any) any {
	user := shapeDict(shapeMap(parsed), "user")
	profile := shapeDict(user, "profile")
	return map[string]any{
		"success": true,
		"user": map[string]any{
			"id":        shapeText(user["id"]),
			"name":      shapeText(user["name"]),
			"real_name": shapeText(user["real_name"]),
			"email":     shapeText(profile["email"]),
			"title":     shapeText(profile["title"]),
			"is_admin":  shapeBool(user, "is_admin", false),
			"is_bot":    shapeBool(user, "is_bot", false),
			"tz":        shapeText(user["tz"]),
		},
	}
}

// slackUpdateMessageShape mirrors slack_update_message.
func slackUpdateMessageShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{
		"success": true,
		"channel": shapeText(object["channel"]),
		"ts":      shapeText(object["ts"]),
	}
}

// slackScheduleShape mirrors slack_schedule_message.
func slackScheduleShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{
		"success":              true,
		"scheduled_message_id": shapeText(object["scheduled_message_id"]),
		"post_at":              shapeText(object["post_at"]),
	}
}

// slackCreateChannelShape mirrors slack_create_channel.
func slackCreateChannelShape(parsed any, _ map[string]any) any {
	channel := shapeDict(shapeMap(parsed), "channel")
	return map[string]any{
		"success": true,
		"channel": map[string]any{
			"id":         shapeText(channel["id"]),
			"name":       shapeText(channel["name"]),
			"is_private": shapeBool(channel, "is_private", false),
		},
	}
}

// slackSetTopicShape mirrors slack_set_channel_topic.
func slackSetTopicShape(parsed any, _ map[string]any) any {
	return map[string]any{"success": true, "topic": shapeText(shapeMap(parsed)["topic"])}
}

// slackListUsersShape mirrors slack_list_users, which skips deleted members.
func slackListUsersShape(parsed any, _ map[string]any) any {
	members := shapeList(shapeMap(parsed), "members")
	out := make([]any, 0, len(members))
	for _, value := range members {
		user := shapeMap(value)
		if shapeBool(user, "deleted", false) {
			continue
		}
		out = append(out, map[string]any{
			"id":        shapeText(user["id"]),
			"name":      shapeText(user["name"]),
			"real_name": shapeText(user["real_name"]),
			"is_admin":  shapeBool(user, "is_admin", false),
			"is_bot":    shapeBool(user, "is_bot", false),
		})
	}
	return map[string]any{"success": true, "users": out, "count": len(out)}
}

// slackSearchShape mirrors slack_search_messages.
func slackSearchShape(parsed any, _ map[string]any) any {
	messages := shapeDict(shapeMap(parsed), "messages")
	matches := shapeList(messages, "matches")
	out := make([]any, 0, len(matches))
	for _, value := range matches {
		match := shapeMap(value)
		out = append(out, map[string]any{
			"text":      shapeText(match["text"]),
			"user":      shapeText(match["user"]),
			"channel":   shapeText(shapeAt(match, "channel.name")),
			"ts":        shapeText(match["ts"]),
			"permalink": shapeText(match["permalink"]),
		})
	}
	total := messages["total"]
	if total == nil {
		total = float64(0)
	}
	return map[string]any{"success": true, "total": total, "messages": out}
}

// slackThreadShape mirrors slack_get_thread_replies.
func slackThreadShape(parsed any, _ map[string]any) any {
	messages := shapeList(shapeMap(parsed), "messages")
	out := make([]any, 0, len(messages))
	for _, value := range messages {
		message := shapeMap(value)
		out = append(out, map[string]any{
			"ts":   shapeText(message["ts"]),
			"user": shapeText(message["user"]),
			"text": shapeText(message["text"]),
		})
	}
	return map[string]any{"success": true, "messages": out, "count": len(out)}
}

// slackListPinsShape mirrors slack_list_pins.
func slackListPinsShape(parsed any, _ map[string]any) any {
	items := shapeList(shapeMap(parsed), "items")
	out := make([]any, 0, len(items))
	for _, value := range items {
		item := shapeMap(value)
		out = append(out, map[string]any{
			"type":    shapeText(item["type"]),
			"created": shapeText(item["created"]),
			"message": shapeText(shapeAt(item, "message.text")),
		})
	}
	return map[string]any{"success": true, "pins": out, "count": len(out)}
}

// slackBookmarkShape mirrors slack_add_bookmark.
func slackBookmarkShape(parsed any, _ map[string]any) any {
	bookmark := shapeDict(shapeMap(parsed), "bookmark")
	return map[string]any{
		"success": true,
		"bookmark": map[string]any{
			"id":    shapeText(bookmark["id"]),
			"title": shapeText(bookmark["title"]),
			"link":  shapeText(bookmark["link"]),
		},
	}
}

// slackScheduledShape mirrors slack_list_scheduled_messages.
func slackScheduledShape(parsed any, _ map[string]any) any {
	messages := shapeList(shapeMap(parsed), "scheduled_messages")
	out := make([]any, 0, len(messages))
	for _, value := range messages {
		message := shapeMap(value)
		out = append(out, map[string]any{
			"id":         shapeText(message["id"]),
			"channel_id": shapeText(message["channel_id"]),
			"post_at":    shapeText(message["post_at"]),
			"text":       shapeText(message["text"]),
		})
	}
	return map[string]any{"success": true, "scheduled_messages": out, "count": len(out)}
}

// slackPermalinkShape mirrors slack_get_permalink.
func slackPermalinkShape(parsed any, _ map[string]any) any {
	return map[string]any{"success": true, "permalink": shapeText(shapeMap(parsed)["permalink"])}
}

// slackEphemeralShape mirrors slack_send_ephemeral.
func slackEphemeralShape(parsed any, _ map[string]any) any {
	return map[string]any{"success": true, "message_ts": shapeText(shapeMap(parsed)["message_ts"])}
}

// slackPostBlocksShape mirrors slack_post_blocks.
func slackPostBlocksShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{
		"success": true,
		"channel": shapeText(object["channel"]),
		"ts":      shapeText(object["ts"]),
	}
}

// slackViewShape mirrors slack_open_modal / slack_update_home_tab.
func slackViewShape(parsed any, _ map[string]any) any {
	return map[string]any{"success": true, "view_id": shapeText(shapeAt(shapeMap(parsed), "view.id"))}
}

// slackSetStatusShape mirrors slack_set_status, which echoes the requested
// status rather than the provider profile.
func slackSetStatusShape(_ any, input map[string]any) any {
	return map[string]any{
		"success":      true,
		"status_text":  shapeText(input["status_text"]),
		"status_emoji": shapeText(input["status_emoji"]),
	}
}

// slackSetPresenceShape mirrors slack_set_presence.
func slackSetPresenceShape(_ any, input map[string]any) any {
	return map[string]any{"success": true, "presence": shapeText(input["presence"])}
}

// slackGetPresenceShape mirrors slack_get_presence (no success key).
func slackGetPresenceShape(parsed any, input map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{
		"user_id":   shapeText(input["user_id"]),
		"presence":  shapeText(object["presence"]),
		"online":    shapeBool(object, "online", false),
		"auto_away": shapeBool(object, "auto_away", false),
	}
}

// slackCreateReminderShape mirrors slack_create_reminder.
func slackCreateReminderShape(parsed any, _ map[string]any) any {
	reminder := shapeDict(shapeMap(parsed), "reminder")
	return map[string]any{
		"success":     true,
		"reminder_id": shapeText(reminder["id"]),
		"text":        shapeText(reminder["text"]),
		"time":        shapeText(reminder["time"]),
	}
}

// slackListRemindersShape mirrors slack_list_reminders (no success key).
func slackListRemindersShape(parsed any, _ map[string]any) any {
	reminders := shapeList(shapeMap(parsed), "reminders")
	out := make([]any, 0, len(reminders))
	for _, value := range reminders {
		reminder := shapeMap(value)
		out = append(out, map[string]any{
			"id":          shapeText(reminder["id"]),
			"text":        shapeText(reminder["text"]),
			"time":        shapeText(reminder["time"]),
			"complete_ts": shapeText(reminder["complete_ts"]),
		})
	}
	return map[string]any{"count": len(out), "reminders": out}
}

// slackDeleteReminderShape mirrors slack_delete_reminder.
func slackDeleteReminderShape(_ any, input map[string]any) any {
	return map[string]any{"success": true, "deleted": shapeText(input["reminder_id"])}
}

// slackCreateUsergroupShape mirrors slack_create_usergroup.
func slackCreateUsergroupShape(parsed any, _ map[string]any) any {
	group := shapeDict(shapeMap(parsed), "usergroup")
	return map[string]any{
		"success":      true,
		"usergroup_id": shapeText(group["id"]),
		"name":         shapeText(group["name"]),
		"handle":       shapeText(group["handle"]),
	}
}

// slackUpdateUsergroupShape mirrors slack_update_usergroup_members, which
// counts the submitted comma-separated user_ids.
func slackUpdateUsergroupShape(_ any, input map[string]any) any {
	return map[string]any{
		"success":       true,
		"usergroup_id":  shapeText(input["usergroup_id"]),
		"members_count": len(slackSplitCSV(shapeString(input["user_ids"]))),
	}
}

// slackListUsergroupsShape mirrors slack_list_usergroups (no success key).
func slackListUsergroupsShape(parsed any, _ map[string]any) any {
	groups := shapeList(shapeMap(parsed), "usergroups")
	out := make([]any, 0, len(groups))
	for _, value := range groups {
		group := shapeMap(value)
		out = append(out, map[string]any{
			"id":         shapeText(group["id"]),
			"name":       shapeText(group["name"]),
			"handle":     shapeText(group["handle"]),
			"user_count": shapeNumber(group, "user_count"),
		})
	}
	return map[string]any{"count": len(out), "usergroups": out}
}

// slackListEmojiShape mirrors slack_list_emoji: the first 100 emoji names,
// the first ten name/url pairs, and the total count. Go maps do not preserve
// the provider's insertion order, so the names are sorted for determinism.
func slackListEmojiShape(parsed any, _ map[string]any) any {
	emoji := shapeDict(shapeMap(parsed), "emoji")
	names := make([]string, 0, len(emoji))
	for name := range emoji {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 100 {
		names = names[:100]
	}
	sample := map[string]any{}
	for i, name := range names {
		if i >= 10 {
			break
		}
		sample[name] = emoji[name]
	}
	return map[string]any{"count": len(emoji), "emoji": names, "sample": sample}
}

// slackCreateCanvasShape mirrors slack_create_canvas, echoing the title.
func slackCreateCanvasShape(parsed any, input map[string]any) any {
	return map[string]any{
		"success":   true,
		"canvas_id": shapeText(shapeMap(parsed)["canvas_id"]),
		"title":     shapeText(input["title"]),
	}
}

// slackEditCanvasShape mirrors slack_edit_canvas, echoing the operation.
func slackEditCanvasShape(parsed any, input map[string]any) any {
	return map[string]any{
		"success":   true,
		"canvas_id": shapeText(shapeMap(parsed)["canvas_id"]),
		"operation": shapeText(input["operation"]),
	}
}

// slackChannelInfoShape mirrors slack_get_channel_info.
func slackChannelInfoShape(parsed any, _ map[string]any) any {
	channel := shapeDict(shapeMap(parsed), "channel")
	return map[string]any{
		"id":          shapeText(channel["id"]),
		"name":        shapeText(channel["name"]),
		"is_channel":  channel["is_channel"],
		"is_private":  channel["is_private"],
		"is_archived": channel["is_archived"],
		"is_general":  channel["is_general"],
		"topic":       shapeTextOr(shapeAt(channel, "topic.value"), ""),
		"purpose":     shapeTextOr(shapeAt(channel, "purpose.value"), ""),
		"num_members": channel["num_members"],
		"creator":     channel["creator"],
		"created":     channel["created"],
	}
}

// slackListFilesShape mirrors slack_list_files.
func slackListFilesShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	files := shapeList(object, "files")
	out := make([]any, 0, len(files))
	for _, value := range files {
		file := shapeMap(value)
		out = append(out, map[string]any{
			"id":        shapeText(file["id"]),
			"name":      shapeText(file["name"]),
			"title":     shapeText(file["title"]),
			"mimetype":  shapeText(file["mimetype"]),
			"filetype":  shapeText(file["filetype"]),
			"size":      file["size"],
			"user":      shapeText(file["user"]),
			"created":   file["created"],
			"permalink": shapeText(file["permalink"]),
		})
	}
	paging := shapeDict(object, "paging")
	total := paging["total"]
	if total == nil {
		total = len(out)
	}
	page := paging["page"]
	if page == nil {
		page = float64(1)
	}
	pages := paging["pages"]
	if pages == nil {
		pages = float64(1)
	}
	return map[string]any{
		"files": out,
		"count": len(out),
		"total": total,
		"page":  page,
		"pages": pages,
	}
}

// slackFileInfoShape mirrors slack_get_file_info.
func slackFileInfoShape(parsed any, _ map[string]any) any {
	file := shapeDict(shapeMap(parsed), "file")
	shares := shapeDict(file, "shares")
	public := shapeDict(shares, "public")
	names := make([]string, 0, len(public))
	for name := range public {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 10 {
		names = names[:10]
	}
	comments := file["comments_count"]
	if comments == nil {
		comments = float64(0)
	}
	return map[string]any{
		"id":             shapeText(file["id"]),
		"name":           shapeText(file["name"]),
		"title":          shapeText(file["title"]),
		"mimetype":       shapeText(file["mimetype"]),
		"filetype":       shapeText(file["filetype"]),
		"size":           file["size"],
		"user":           shapeText(file["user"]),
		"created":        file["created"],
		"permalink":      shapeText(file["permalink"]),
		"url_private":    shapeText(file["url_private"]),
		"channels":       shapeList(file, "channels"),
		"shares":         names,
		"comments_count": comments,
	}
}

// shapeString renders an input scalar as a string; nil reads as "".
func shapeString(value any) string {
	text, _ := value.(string)
	return text
}

// slackSplitCSV mirrors [u.strip() for u in user_ids.split(",") if u.strip()].
func slackSplitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
