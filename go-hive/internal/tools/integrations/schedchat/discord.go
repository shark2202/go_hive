package schedchat

// Discord: guild/channel/message/reaction actions (7 actions).

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"
)

// --------------------------------------------------------------- discord ---

// discordTool mirrors gen_catalog.py output for discord (7).
var discordTool = &Tool{
	ID:          "discord_tool",
	Name:        "discord",
	DisplayName: "Discord",
	Summary:     "Discord integration (7 actions).",
	Actions: []Action{
		{
			Name:     "discord_create_reaction",
			Summary:  "Add a reaction to a Discord message.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "message_id", "emoji"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message_id": param("string"),
				"emoji":      param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_delete_message",
			Summary:  "Delete a message from a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "message_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_get_channel",
			Summary:  "Get detailed information about a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_get_messages",
			Summary:  "Get recent messages from a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"limit":      paramDefault("integer", 50),
				"before":     paramDefault("string", ""),
				"after":      paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_list_channels",
			Summary:  "List channels for a Discord guild (server).",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"guild_id"},
			Params: map[string]any{
				"guild_id":  param("string"),
				"text_only": paramDefault("boolean", true),
				"account":   paramDefault("string", ""),
			},
		},
		{
			Name:    "discord_list_guilds",
			Summary: "List Discord guilds (servers) the bot is a member of.",
			Source:  "discord_tool/discord_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_send_message",
			Summary:  "Send a message to a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "content"},
			Params: map[string]any{
				"channel_id": param("string"),
				"content":    param("string"),
				"tts":        paramDefault("boolean", false),
				"account":    paramDefault("string", ""),
			},
		},
	},
}

// discordImpls binds every discord action to its implementation.
func discordImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"discord_create_reaction": discordCreateReaction,
		"discord_delete_message":  discordDeleteMessage,
		"discord_get_channel":     discordGetChannel,
		"discord_get_messages":    discordGetMessages,
		"discord_list_channels":   discordListChannels,
		"discord_list_guilds":     discordListGuilds,
		"discord_send_message":    discordSendMessage,
	}
}

// discordIsTextChannel reports channel types 0 and 5.
func discordIsTextChannel(value any) bool {
	number, ok := value.(float64)
	if !ok {
		return false
	}
	return number == 0 || number == 5
}

// discordListGuilds lists the bot's guilds.
func discordListGuilds(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "discord_tool", http.MethodGet, "/users/@me/guilds", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"guilds": parsed, "success": true}, nil
}

// discordListChannels lists guild channels, optionally text-only.
func discordListChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	guildID := stringArg(input, "guild_id")
	if guildID == "" {
		return map[string]any{"error": "guild_id is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "discord_tool", http.MethodGet,
		"/guilds/"+url.PathEscape(guildID)+"/channels", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	if !boolArg(input, "text_only", true) {
		return map[string]any{"channels": parsed, "success": true}, nil
	}
	channels := make([]any, 0)
	for _, raw := range asSlice(parsed) {
		channel := asMap(raw)
		if discordIsTextChannel(channel["type"]) {
			channels = append(channels, channel)
		}
	}
	return map[string]any{"channels": channels, "success": true}, nil
}

// discordSendMessage sends a channel message.
func discordSendMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	content := stringArg(input, "content")
	if utf8.RuneCountInString(content) > DiscordMaxMessage {
		return map[string]any{
			"error":      fmt.Sprintf("Message exceeds %d character limit", DiscordMaxMessage),
			"max_length": DiscordMaxMessage,
			"provided":   utf8.RuneCountInString(content),
		}, nil
	}
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	body := map[string]any{"content": content, "tts": boolArg(input, "tts", false)}
	parsed, errDict := call(ctx, rt, "discord_tool", http.MethodPost,
		"/channels/"+url.PathEscape(channelID)+"/messages", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "message": parsed}, nil
}

// discordGetMessages reads recent channel messages.
func discordGetMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	limit := intArg(input, "limit", 50)
	if limit > 100 {
		limit = 100
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	if before := stringArg(input, "before"); before != "" {
		query.Set("before", before)
	}
	if after := stringArg(input, "after"); after != "" {
		query.Set("after", after)
	}
	parsed, errDict := call(ctx, rt, "discord_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID)+"/messages", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"messages": parsed, "success": true}, nil
}

// discordGetChannel reads one channel.
func discordGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "discord_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channel": parsed, "success": true}, nil
}

// discordCreateReaction adds a reaction to a message.
func discordCreateReaction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	messageID := stringArg(input, "message_id")
	emoji := stringArg(input, "emoji")
	if channelID == "" || messageID == "" || emoji == "" {
		return map[string]any{"error": "channel_id, message_id and emoji are required"}, nil
	}
	path := "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID) +
		"/reactions/" + quotePath(emoji) + "/@me"
	if _, errDict := call(ctx, rt, "discord_tool", http.MethodPut, path, nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true}, nil
}

// discordDeleteMessage deletes a message.
func discordDeleteMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	messageID := stringArg(input, "message_id")
	if channelID == "" || messageID == "" {
		return map[string]any{"error": "channel_id and message_id are required"}, nil
	}
	path := "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID)
	if _, errDict := call(ctx, rt, "discord_tool", http.MethodDelete, path, nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "deleted_message_id": messageID}, nil
}
