package social

// YouTube Data API v3: catalog entry, eight actions and their
// response projections.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var youtubeTool = &Tool{
	ID:          "youtube_tool",
	Name:        "youtube",
	DisplayName: "YouTube",
	Summary:     "YouTube Data API integration (8 actions).",
	Actions: []Action{
		{
			Name:     "youtube_search_videos",
			Summary:  "Search for YouTube videos by keyword.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":           param("string"),
				"max_results":     paramDefault("integer", 10),
				"order":           paramDefault("string", "relevance"),
				"published_after": paramDefault("string", ""),
				"region_code":     paramDefault("string", ""),
				"video_duration":  paramDefault("string", ""),
				"video_type":      paramDefault("string", ""),
			},
		},
		{
			Name:     "youtube_get_video_details",
			Summary:  "Get detailed information for one or more YouTube videos.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"video_ids"},
			Params: map[string]any{
				"video_ids": param("string"),
			},
		},
		{
			Name:    "youtube_get_channel",
			Summary: "Get YouTube channel information by channel ID, username, or handle.",
			Source:  "youtube_tool/youtube_tool.py",
			Params: map[string]any{
				"channel_id": paramDefault("string", ""),
				"username":   paramDefault("string", ""),
				"handle":     paramDefault("string", ""),
			},
		},
		{
			Name:     "youtube_list_channel_videos",
			Summary:  "List recent videos from a YouTube channel.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id":  param("string"),
				"max_results": paramDefault("integer", 20),
				"order":       paramDefault("string", "date"),
			},
		},
		{
			Name:     "youtube_get_playlist",
			Summary:  "Get playlist details and its video items.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"playlist_id"},
			Params: map[string]any{
				"playlist_id": param("string"),
				"max_results": paramDefault("integer", 20),
			},
		},
		{
			Name:     "youtube_search_channels",
			Summary:  "Search for YouTube channels by keyword.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"max_results": paramDefault("integer", 10),
				"order":       paramDefault("string", "relevance"),
			},
		},
		{
			Name:     "youtube_get_video_comments",
			Summary:  "Get top-level comments on a YouTube video.",
			Source:   "youtube_tool/youtube_tool.py",
			Required: []string{"video_id"},
			Params: map[string]any{
				"video_id":    param("string"),
				"max_results": paramDefault("integer", 20),
				"order":       paramDefault("string", "relevance"),
			},
		},
		{
			Name:    "youtube_get_video_categories",
			Summary: "Get available YouTube video categories for a region.",
			Source:  "youtube_tool/youtube_tool.py",
			Params: map[string]any{
				"region_code": paramDefault("string", "US"),
			},
		},
	},
}

func youtubeImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"youtube_search_videos":        youtubeSearchVideos,
		"youtube_get_video_details":    youtubeGetVideoDetails,
		"youtube_get_channel":          youtubeGetChannel,
		"youtube_list_channel_videos":  youtubeListChannelVideos,
		"youtube_get_playlist":         youtubeGetPlaylist,
		"youtube_search_channels":      youtubeSearchChannels,
		"youtube_get_video_comments":   youtubeGetVideoComments,
		"youtube_get_video_categories": youtubeGetVideoCategories,
	}
}

// ----------------------------------------------------------------- YouTube ---

// youtubeAuth resolves the API key or returns the upstream error.
func youtubeAuth(rt *Runtime) (string, map[string]any) {
	key := moduleToken(rt, youtubeModuleID)
	if key == "" {
		return "", map[string]any{
			"error": "YOUTUBE_API_KEY not set",
			"help":  "Get an API key at https://console.cloud.google.com/apis/credentials",
		}
	}
	return key, nil
}

// youtubeGET performs one Data API GET with the key attached.
func youtubeGET(
	ctx context.Context,
	rt *Runtime,
	key, endpoint string,
	params url.Values,
) (any, map[string]any) {
	base := moduleBaseURL(rt, youtubeModuleID)
	if base == "" {
		base = youtubeDefaultBase
	}
	params.Set("key", key)
	res, err := doRequest(ctx, rt, providerRequest{
		Method: http.MethodGet,
		URL:    base + "/" + endpoint,
		Query:  params,
	})
	if err != nil {
		return nil, networkFailure(youtubeModuleID, ctx, err)
	}
	if res.Status == http.StatusForbidden {
		reason := ""
		if errorsList := asSlice(digValue(res.Parsed, "error", "errors")); len(errorsList) > 0 {
			reason = asString(asMap(errorsList[0])["reason"])
		}
		if reason == "quotaExceeded" {
			return nil, map[string]any{
				"error": "YouTube API quota exceeded. Try again tomorrow or request a quota increase.",
			}
		}
		detail := reason
		if detail == "" {
			detail = string(res.Body)
		}
		return nil, map[string]any{"error": "Forbidden: " + detail}
	}
	if res.Status != http.StatusOK {
		return nil, map[string]any{
			"error": fmt.Sprintf("YouTube API error %d: %s", res.Status, truncateTextClamped(string(res.Body), maxErrorBytes)),
		}
	}
	return res.Parsed, nil
}

// humanizeDuration converts ISO 8601 durations to human text.
func humanizeDuration(duration string) string {
	if duration == "" || !strings.HasPrefix(duration, "PT") {
		return duration
	}
	rest := duration[2:]
	hours, minutes, seconds := 0, 0, 0
	for _, unit := range []struct {
		token  string
		target *int
	}{
		{"H", &hours},
		{"M", &minutes},
		{"S", &seconds},
	} {
		index := strings.Index(rest, unit.token)
		if index < 0 {
			continue
		}
		value := rest[:index]
		rest = rest[index+1:]
		if parsed, err := strconv.Atoi(value); err == nil {
			*unit.target = parsed
		}
	}
	parts := make([]string, 0, 3)
	if hours != 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes != 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds != 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, "")
}

func youtubeSearchVideos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	maxResults := clampInt(intArg(input, "max_results", 10), 1, 50)
	params := url.Values{
		"part":       {"snippet"},
		"q":          {query},
		"type":       {"video"},
		"maxResults": {strconv.Itoa(maxResults)},
		"order":      {stringArg(input, "order")},
	}
	for _, optional := range []struct{ arg, param string }{
		{"published_after", "publishedAfter"},
		{"region_code", "regionCode"},
		{"video_duration", "videoDuration"},
		{"video_type", "videoType"},
	} {
		if value := stringArg(input, optional.arg); value != "" {
			params.Set(optional.param, value)
		}
	}
	data, fail := youtubeGET(ctx, rt, key, "search", params)
	if fail != nil {
		return fail, nil
	}
	results := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		snippet := asMap(item["snippet"])
		results = append(results, map[string]any{
			"videoId":      asString(digValue(item, "id", "videoId")),
			"title":        asString(snippet["title"]),
			"channelTitle": asString(snippet["channelTitle"]),
			"channelId":    asString(snippet["channelId"]),
			"publishedAt":  asString(snippet["publishedAt"]),
			"description":  asString(snippet["description"]),
			"thumbnail":    asString(digValue(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{
		"query":         query,
		"results":       results,
		"total_results": intValue(digValue(data, "pageInfo", "totalResults")),
	}, nil
}

func youtubeGetVideoDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	videoIDs := stringArg(input, "video_ids")
	if videoIDs == "" {
		return map[string]any{"error": "video_ids is required"}, nil
	}
	data, fail := youtubeGET(ctx, rt, key, "videos", url.Values{
		"part": {"snippet,contentDetails,statistics"},
		"id":   {videoIDs},
	})
	if fail != nil {
		return fail, nil
	}
	videos := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		snippet := asMap(item["snippet"])
		stats := asMap(item["statistics"])
		content := asMap(item["contentDetails"])
		rawDuration := asString(content["duration"])
		videos = append(videos, map[string]any{
			"videoId":      asString(item["id"]),
			"title":        asString(snippet["title"]),
			"description":  asString(snippet["description"]),
			"channelTitle": asString(snippet["channelTitle"]),
			"channelId":    asString(snippet["channelId"]),
			"publishedAt":  asString(snippet["publishedAt"]),
			"tags":         sliceOrEmpty(snippet["tags"]),
			"categoryId":   asString(snippet["categoryId"]),
			"duration":     humanizeDuration(rawDuration),
			"duration_raw": rawDuration,
			"viewCount":    intValue(stats["viewCount"]),
			"likeCount":    intValue(stats["likeCount"]),
			"commentCount": intValue(stats["commentCount"]),
			"thumbnail":    asString(digValue(snippet, "thumbnails", "high", "url")),
		})
	}
	return map[string]any{"videos": videos}, nil
}

func youtubeGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	params := url.Values{"part": {"snippet,statistics,contentDetails"}}
	switch {
	case stringArg(input, "channel_id") != "":
		params.Set("id", stringArg(input, "channel_id"))
	case stringArg(input, "username") != "":
		params.Set("forUsername", stringArg(input, "username"))
	case stringArg(input, "handle") != "":
		params.Set("forHandle", stringArg(input, "handle"))
	default:
		return map[string]any{"error": "Provide one of: channel_id, username, or handle"}, nil
	}
	data, fail := youtubeGET(ctx, rt, key, "channels", params)
	if fail != nil {
		return fail, nil
	}
	items := asSlice(asMap(data)["items"])
	if len(items) == 0 {
		return map[string]any{"error": "Channel not found"}, nil
	}
	item := asMap(items[0])
	snippet := asMap(item["snippet"])
	stats := asMap(item["statistics"])
	return map[string]any{
		"channelId":         asString(item["id"]),
		"title":             asString(snippet["title"]),
		"description":       asString(snippet["description"]),
		"customUrl":         asString(snippet["customUrl"]),
		"publishedAt":       asString(snippet["publishedAt"]),
		"subscriberCount":   intValue(stats["subscriberCount"]),
		"videoCount":        intValue(stats["videoCount"]),
		"viewCount":         intValue(stats["viewCount"]),
		"thumbnail":         asString(digValue(snippet, "thumbnails", "high", "url")),
		"uploadsPlaylistId": asString(digValue(item, "contentDetails", "relatedPlaylists", "uploads")),
	}, nil
}

func youtubeListChannelVideos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	maxResults := clampInt(intArg(input, "max_results", 20), 1, 50)
	data, fail := youtubeGET(ctx, rt, key, "search", url.Values{
		"part":       {"snippet"},
		"channelId":  {channelID},
		"type":       {"video"},
		"maxResults": {strconv.Itoa(maxResults)},
		"order":      {stringArg(input, "order")},
	})
	if fail != nil {
		return fail, nil
	}
	videos := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		snippet := asMap(item["snippet"])
		videos = append(videos, map[string]any{
			"videoId":     asString(digValue(item, "id", "videoId")),
			"title":       asString(snippet["title"]),
			"publishedAt": asString(snippet["publishedAt"]),
			"description": asString(snippet["description"]),
			"thumbnail":   asString(digValue(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{"channel_id": channelID, "videos": videos}, nil
}

func youtubeGetPlaylist(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	playlistID := stringArg(input, "playlist_id")
	if playlistID == "" {
		return map[string]any{"error": "playlist_id is required"}, nil
	}
	maxResults := clampInt(intArg(input, "max_results", 20), 1, 50)
	playlistData, fail := youtubeGET(ctx, rt, key, "playlists", url.Values{
		"part": {"snippet,contentDetails"},
		"id":   {playlistID},
	})
	if fail != nil {
		return fail, nil
	}
	playlistItems := asSlice(asMap(playlistData)["items"])
	if len(playlistItems) == 0 {
		return map[string]any{"error": "Playlist not found"}, nil
	}
	playlist := asMap(playlistItems[0])
	playlistSnippet := asMap(playlist["snippet"])
	itemsData, fail := youtubeGET(ctx, rt, key, "playlistItems", url.Values{
		"part":       {"snippet,contentDetails"},
		"playlistId": {playlistID},
		"maxResults": {strconv.Itoa(maxResults)},
	})
	if fail != nil {
		return fail, nil
	}
	items := make([]any, 0)
	for _, raw := range asSlice(asMap(itemsData)["items"]) {
		item := asMap(raw)
		snippet := asMap(item["snippet"])
		items = append(items, map[string]any{
			"videoId":      asString(digValue(snippet, "resourceId", "videoId")),
			"title":        asString(snippet["title"]),
			"position":     intValue(snippet["position"]),
			"channelTitle": asString(snippet["videoOwnerChannelTitle"]),
			"thumbnail":    asString(digValue(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{
		"playlistId":   playlistID,
		"title":        asString(playlistSnippet["title"]),
		"description":  asString(playlistSnippet["description"]),
		"channelTitle": asString(playlistSnippet["channelTitle"]),
		"itemCount":    intValue(digValue(playlist, "contentDetails", "itemCount")),
		"items":        items,
	}, nil
}

func youtubeSearchChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	maxResults := clampInt(intArg(input, "max_results", 10), 1, 50)
	data, fail := youtubeGET(ctx, rt, key, "search", url.Values{
		"part":       {"snippet"},
		"q":          {query},
		"type":       {"channel"},
		"maxResults": {strconv.Itoa(maxResults)},
		"order":      {stringArg(input, "order")},
	})
	if fail != nil {
		return fail, nil
	}
	results := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		snippet := asMap(item["snippet"])
		results = append(results, map[string]any{
			"channelId":   asString(digValue(item, "id", "channelId")),
			"title":       asString(snippet["title"]),
			"description": asString(snippet["description"]),
			"thumbnail":   asString(digValue(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func youtubeGetVideoComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	videoID := stringArg(input, "video_id")
	if videoID == "" {
		return map[string]any{"error": "video_id is required"}, nil
	}
	maxResults := clampInt(intArg(input, "max_results", 20), 1, 100)
	data, fail := youtubeGET(ctx, rt, key, "commentThreads", url.Values{
		"part":       {"snippet"},
		"videoId":    {videoID},
		"maxResults": {strconv.Itoa(maxResults)},
		"order":      {stringArg(input, "order")},
		"textFormat": {"plainText"},
	})
	if fail != nil {
		return fail, nil
	}
	comments := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		top := asMap(digValue(item, "snippet", "topLevelComment", "snippet"))
		comments = append(comments, map[string]any{
			"author":      asString(top["authorDisplayName"]),
			"text":        asString(top["textDisplay"]),
			"likeCount":   intValue(top["likeCount"]),
			"publishedAt": asString(top["publishedAt"]),
			"replyCount":  intValue(digValue(item, "snippet", "totalReplyCount")),
		})
	}
	return map[string]any{"video_id": videoID, "comments": comments}, nil
}

func youtubeGetVideoCategories(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := youtubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	regionCode := stringArg(input, "region_code")
	data, fail := youtubeGET(ctx, rt, key, "videoCategories", url.Values{
		"part":       {"snippet"},
		"regionCode": {regionCode},
	})
	if fail != nil {
		return fail, nil
	}
	categories := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["items"]) {
		item := asMap(raw)
		categories = append(categories, map[string]any{
			"id":    asString(item["id"]),
			"title": asString(digValue(item, "snippet", "title")),
		})
	}
	return map[string]any{"region_code": regionCode, "categories": categories}, nil
}
