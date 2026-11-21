package schedchat

// Mattermost: teams, channels, posts and reactions (7 actions).

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"
)

// ------------------------------------------------------------ mattermost ---

// mattermostTool mirrors gen_catalog.py output for mattermost (7).
var mattermostTool = &Tool{
	ID:          "mattermost_tool",
	Name:        "mattermost",
	DisplayName: "Mattermost",
	Summary:     "Mattermost integration (7 actions).",
	Actions: []Action{
		{
			Name:     "mattermost_create_reaction",
			Summary:  "Add a reaction to a Mattermost post.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"post_id", "emoji_name"},
			Params: map[string]any{
				"post_id":    param("string"),
				"emoji_name": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_delete_post",
			Summary:  "Delete a post from Mattermost.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"post_id"},
			Params: map[string]any{
				"post_id": param("string"),
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_get_channel",
			Summary:  "Get detailed information about a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_get_posts",
			Summary:  "Get posts from a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"per_page":   paramDefault("integer", 60),
				"page":       paramDefault("integer", 0),
				"before":     paramDefault("string", ""),
				"after":      paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_list_channels",
			Summary:  "List public channels for a Mattermost team.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id":  param("string"),
				"per_page": paramDefault("integer", 100),
				"account":  paramDefault("string", ""),
			},
		},
		{
			Name:    "mattermost_list_teams",
			Summary: "List Mattermost teams the authenticated user belongs to.",
			Source:  "mattermost_tool/mattermost_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_send_message",
			Summary:  "Send a message (post) to a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id", "message"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message":    param("string"),
				"root_id":    paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
	},
}

// mattermostImpls binds every mattermost action to its implementation.
func mattermostImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"mattermost_create_reaction": mattermostCreateReaction,
		"mattermost_delete_post":     mattermostDeletePost,
		"mattermost_get_channel":     mattermostGetChannel,
		"mattermost_get_posts":       mattermostGetPosts,
		"mattermost_list_channels":   mattermostListChannels,
		"mattermost_list_teams":      mattermostListTeams,
		"mattermost_send_message":    mattermostSendMessage,
	}
}

// mattermostListTeams lists the user's teams.
func mattermostListTeams(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "mattermost_tool", http.MethodGet, "/users/me/teams", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"teams": parsed, "success": true}, nil
}

// mattermostListChannels lists public team channels.
func mattermostListChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	teamID := stringArg(input, "team_id")
	if teamID == "" {
		return map[string]any{"error": "team_id is required"}, nil
	}
	perPage := intArg(input, "per_page", 100)
	if perPage > 200 {
		perPage = 200
	}
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(perPage))
	parsed, errDict := call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/teams/"+url.PathEscape(teamID)+"/channels", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channels": parsed, "success": true}, nil
}

// mattermostGetChannel reads one channel.
func mattermostGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channel": parsed, "success": true}, nil
}

// mattermostSendMessage creates a post.
func mattermostSendMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	message := stringArg(input, "message")
	if utf8.RuneCountInString(message) > MattermostMaxText {
		return map[string]any{
			"error":      fmt.Sprintf("Message exceeds %d character limit", MattermostMaxText),
			"max_length": MattermostMaxText,
			"provided":   utf8.RuneCountInString(message),
		}, nil
	}
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	body := map[string]any{"channel_id": channelID, "message": message}
	if rootID := stringArg(input, "root_id"); rootID != "" {
		body["root_id"] = rootID
	}
	parsed, errDict := call(ctx, rt, "mattermost_tool", http.MethodPost, "/posts", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "post": parsed}, nil
}

// mattermostGetPosts reads channel posts.
func mattermostGetPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	perPage := intArg(input, "per_page", 60)
	if perPage > 200 {
		perPage = 200
	}
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(perPage))
	query.Set("page", strconv.Itoa(intArg(input, "page", 0)))
	if before := stringArg(input, "before"); before != "" {
		query.Set("before", before)
	}
	if after := stringArg(input, "after"); after != "" {
		query.Set("after", after)
	}
	parsed, errDict := call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID)+"/posts", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"posts": parsed, "success": true}, nil
}

// mattermostCreateReaction adds a reaction, resolving the user id
// from /users/me exactly like the upstream client.
func mattermostCreateReaction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	postID := stringArg(input, "post_id")
	emojiName := stringArg(input, "emoji_name")
	if postID == "" || emojiName == "" {
		return map[string]any{"error": "post_id and emoji_name are required"}, nil
	}
	me, errDict := call(ctx, rt, "mattermost_tool", http.MethodGet, "/users/me", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	userID := asString(asMap(me)["id"])
	body := map[string]any{"user_id": userID, "post_id": postID, "emoji_name": emojiName}
	if _, errDict := call(ctx, rt, "mattermost_tool", http.MethodPost, "/reactions", nil, body); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true}, nil
}

// mattermostDeletePost deletes a post.
func mattermostDeletePost(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	postID := stringArg(input, "post_id")
	if postID == "" {
		return map[string]any{"error": "post_id is required"}, nil
	}
	if _, errDict := call(ctx, rt, "mattermost_tool", http.MethodDelete,
		"/posts/"+url.PathEscape(postID), nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "deleted_post_id": postID}, nil
}
