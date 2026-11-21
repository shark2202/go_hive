// Batch 7 lane 7 of the integration port: YouTube (8 actions), Reddit (7),
// Twitter/X (7), Twilio (7) and Pushover (7) — 36 actions over five modules.
//
// Upstream reference:
// tools/src/aden_tools/tools/{youtube,reddit,twitter,twilio,pushover}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - YouTube authenticates with an API key query parameter, Twitter with a
//     bearer token, Twilio with Basic auth over form-encoded bodies, and
//     Pushover with its app token as a form/query field.
//   - Reddit follows the OAuth2 client-credentials grant. WithCredential can
//     inject an already-acquired access token (tests, external token caches);
//     otherwise the token is fetched from REDDIT_TOKEN_URL (default
//     https://www.reddit.com/api/v1/access_token) using REDDIT_CLIENT_ID and
//     REDDIT_CLIENT_SECRET.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane7MaxBody  = 8 << 20
	batch7Lane7MaxError = 500
)

const (
	batch7Lane7YouTubeModule  = "youtube_tool"
	batch7Lane7RedditModule   = "reddit_tool"
	batch7Lane7TwitterModule  = "twitter_tool"
	batch7Lane7TwilioModule   = "twilio_tool"
	batch7Lane7PushoverModule = "pushover_tool"

	batch7Lane7RedditUserAgent      = "HiveAgent/1.0"
	batch7Lane7RedditDefaultToken   = "https://www.reddit.com/api/v1/access_token"
	batch7Lane7YouTubeDefaultAPI    = "https://www.googleapis.com/youtube/v3"
	batch7Lane7TwitterDefaultAPI    = "https://api.x.com/2"
	batch7Lane7TwilioDefaultAPI     = "https://api.twilio.com"
	batch7Lane7PushoverDefaultAPI   = "https://api.pushover.net/1"
	batch7Lane7RedditDefaultAPI     = "https://oauth.reddit.com"
	batch7Lane7TwilioAPIVersionPath = "/2010-04-01/Accounts/"
)

// InstallBatch7Lane7 publishes the batch-7 lane-7 tools into a shared registry
// and returns the number of tools registered.
func InstallBatch7Lane7(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	return newBatch7Lane7Registry().Install(target)
}

// newBatch7Lane7Registry builds a registry containing only the lane-7 tools,
// with the same Runtime surface as New() so base URLs, transports and
// credentials stay injectable.
func newBatch7Lane7Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane7Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch7Lane7YouTubeTool,
		batch7Lane7RedditTool,
		batch7Lane7TwitterTool,
		batch7Lane7TwilioTool,
		batch7Lane7PushoverTool,
	} {
		registry.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"youtube":  batch7Lane7YouTubeImpls(),
		"reddit":   batch7Lane7RedditImpls(),
		"twitter":  batch7Lane7TwitterImpls(),
		"twilio":   batch7Lane7TwilioImpls(),
		"pushover": batch7Lane7PushoverImpls(),
	} {
		for action, impl := range actions {
			registry.AddImpl(tool, action, impl)
		}
	}
	return registry
}

// batch7Lane7Modules declares the default endpoints and credential variables.
func batch7Lane7Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		batch7Lane7YouTubeModule: {
			ID: batch7Lane7YouTubeModule, BaseURL: batch7Lane7YouTubeDefaultAPI,
			EnvVars: []string{"YOUTUBE_API_KEY"},
		},
		batch7Lane7RedditModule: {
			ID: batch7Lane7RedditModule, BaseURL: batch7Lane7RedditDefaultAPI,
			EnvVars: []string{"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"},
		},
		batch7Lane7TwitterModule: {
			ID: batch7Lane7TwitterModule, BaseURL: batch7Lane7TwitterDefaultAPI,
			EnvVars: []string{"X_BEARER_TOKEN"},
		},
		batch7Lane7TwilioModule: {
			ID: batch7Lane7TwilioModule, BaseURL: batch7Lane7TwilioDefaultAPI,
			EnvVars: []string{"TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN"},
		},
		batch7Lane7PushoverModule: {
			ID: batch7Lane7PushoverModule, BaseURL: batch7Lane7PushoverDefaultAPI,
			EnvVars: []string{"PUSHOVER_API_TOKEN"},
		},
	}
}

// ------------------------------------------------------------------ catalog ---

var batch7Lane7YouTubeTool = &Tool{
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

var batch7Lane7RedditTool = &Tool{
	ID:          "reddit_tool",
	Name:        "reddit",
	DisplayName: "Reddit",
	Summary:     "Reddit OAuth2 integration (7 actions).",
	Actions: []Action{
		{
			Name:     "reddit_search",
			Summary:  "Search Reddit posts.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"subreddit": paramDefault("string", ""),
				"sort":      paramDefault("string", "relevance"),
				"time":      paramDefault("string", "all"),
				"limit":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "reddit_get_posts",
			Summary:  "Get posts from a subreddit.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"subreddit"},
			Params: map[string]any{
				"subreddit": param("string"),
				"sort":      paramDefault("string", "hot"),
				"time":      paramDefault("string", "day"),
				"limit":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "reddit_get_comments",
			Summary:  "Get comments on a Reddit post.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"post_id"},
			Params: map[string]any{
				"post_id":   param("string"),
				"subreddit": paramDefault("string", ""),
				"sort":      paramDefault("string", "confidence"),
				"limit":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "reddit_get_user",
			Summary:  "Get public info about a Reddit user.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"username"},
			Params: map[string]any{
				"username": param("string"),
			},
		},
		{
			Name:     "reddit_get_subreddit_info",
			Summary:  "Get information about a subreddit.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"subreddit"},
			Params: map[string]any{
				"subreddit": param("string"),
			},
		},
		{
			Name:     "reddit_get_post_detail",
			Summary:  "Get full details for a single Reddit post by ID.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"post_id"},
			Params: map[string]any{
				"post_id": param("string"),
			},
		},
		{
			Name:     "reddit_get_user_posts",
			Summary:  "Get recent posts submitted by a Reddit user.",
			Source:   "reddit_tool/reddit_tool.py",
			Required: []string{"username"},
			Params: map[string]any{
				"username": param("string"),
				"sort":     paramDefault("string", "new"),
				"time":     paramDefault("string", "all"),
				"limit":    paramDefault("integer", 25),
			},
		},
	},
}

var batch7Lane7TwitterTool = &Tool{
	ID:          "twitter_tool",
	Name:        "twitter",
	DisplayName: "Twitter/X",
	Summary:     "Twitter/X API v2 integration (7 actions).",
	Actions: []Action{
		{
			Name:     "twitter_search_tweets",
			Summary:  "Search recent tweets (last 7 days) on X/Twitter.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"max_results": paramDefault("integer", 10),
				"sort_order":  paramDefault("string", "recency"),
			},
		},
		{
			Name:     "twitter_get_user",
			Summary:  "Get a Twitter/X user profile by username.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"username"},
			Params: map[string]any{
				"username": param("string"),
			},
		},
		{
			Name:     "twitter_get_user_tweets",
			Summary:  "Get recent tweets from a user's timeline.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"user_id"},
			Params: map[string]any{
				"user_id":          param("string"),
				"max_results":      paramDefault("integer", 10),
				"exclude_replies":  paramDefault("boolean", true),
				"exclude_retweets": paramDefault("boolean", true),
			},
		},
		{
			Name:     "twitter_get_tweet",
			Summary:  "Get details of a specific tweet by ID.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"tweet_id"},
			Params: map[string]any{
				"tweet_id": param("string"),
			},
		},
		{
			Name:     "twitter_get_user_followers",
			Summary:  "Get followers of a Twitter/X user.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"user_id"},
			Params: map[string]any{
				"user_id":     param("string"),
				"max_results": paramDefault("integer", 25),
			},
		},
		{
			Name:     "twitter_get_tweet_replies",
			Summary:  "Get replies to a specific tweet using search.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"tweet_id"},
			Params: map[string]any{
				"tweet_id":    param("string"),
				"max_results": paramDefault("integer", 10),
			},
		},
		{
			Name:     "twitter_get_list_tweets",
			Summary:  "Get recent tweets from a Twitter/X list.",
			Source:   "twitter_tool/twitter_tool.py",
			Required: []string{"list_id"},
			Params: map[string]any{
				"list_id":     param("string"),
				"max_results": paramDefault("integer", 10),
			},
		},
	},
}

var batch7Lane7TwilioTool = &Tool{
	ID:          "twilio_tool",
	Name:        "twilio",
	DisplayName: "Twilio",
	Summary:     "Twilio SMS and WhatsApp integration (7 actions).",
	Actions: []Action{
		{
			Name:     "twilio_send_sms",
			Summary:  "Send an SMS message via Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"to", "from_number", "body"},
			Params: map[string]any{
				"to":          param("string"),
				"from_number": param("string"),
				"body":        param("string"),
			},
		},
		{
			Name:     "twilio_send_whatsapp",
			Summary:  "Send a WhatsApp message via Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"to", "from_number", "body"},
			Params: map[string]any{
				"to":          param("string"),
				"from_number": param("string"),
				"body":        param("string"),
			},
		},
		{
			Name:    "twilio_list_messages",
			Summary: "List recent messages from your Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params: map[string]any{
				"to":          paramDefault("string", ""),
				"from_number": paramDefault("string", ""),
				"page_size":   paramDefault("integer", 20),
			},
		},
		{
			Name:     "twilio_get_message",
			Summary:  "Get details about a specific Twilio message.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"message_sid"},
			Params: map[string]any{
				"message_sid": param("string"),
			},
		},
		{
			Name:    "twilio_list_phone_numbers",
			Summary: "List phone numbers owned by the Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:    "twilio_list_calls",
			Summary: "List recent calls from your Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params: map[string]any{
				"to":          paramDefault("string", ""),
				"from_number": paramDefault("string", ""),
				"status":      paramDefault("string", ""),
				"page_size":   paramDefault("integer", 20),
			},
		},
		{
			Name:     "twilio_delete_message",
			Summary:  "Delete a message from Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"message_sid"},
			Params: map[string]any{
				"message_sid": param("string"),
			},
		},
	},
}

var batch7Lane7PushoverTool = &Tool{
	ID:          "pushover_tool",
	Name:        "pushover",
	DisplayName: "Pushover",
	Summary:     "Pushover push-notification integration (7 actions).",
	Actions: []Action{
		{
			Name:     "pushover_send",
			Summary:  "Send a push notification via Pushover.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key", "message"},
			Params: map[string]any{
				"user_key":  param("string"),
				"message":   param("string"),
				"title":     paramDefault("string", ""),
				"priority":  paramDefault("integer", 0),
				"sound":     paramDefault("string", ""),
				"device":    paramDefault("string", ""),
				"url":       paramDefault("string", ""),
				"url_title": paramDefault("string", ""),
				"html":      paramDefault("boolean", false),
				"ttl":       paramDefault("integer", 0),
			},
		},
		{
			Name:     "pushover_validate_user",
			Summary:  "Validate a Pushover user or group key.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key"},
			Params: map[string]any{
				"user_key": param("string"),
				"device":   paramDefault("string", ""),
			},
		},
		{
			Name:    "pushover_list_sounds",
			Summary: "List available notification sounds.",
			Source:  "pushover_tool/pushover_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "pushover_check_receipt",
			Summary:  "Check the status of an emergency-priority notification receipt.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"receipt"},
			Params: map[string]any{
				"receipt": param("string"),
			},
		},
		{
			Name:     "pushover_cancel_receipt",
			Summary:  "Cancel emergency-priority notification retries for a receipt.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"receipt"},
			Params: map[string]any{
				"receipt": param("string"),
			},
		},
		{
			Name:     "pushover_send_glance",
			Summary:  "Update Pushover Glance data on a user's device widget.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key"},
			Params: map[string]any{
				"user_key": param("string"),
				"title":    paramDefault("string", ""),
				"text":     paramDefault("string", ""),
				"subtext":  paramDefault("string", ""),
				"count":    param("integer"),
				"percent":  param("integer"),
				"device":   paramDefault("string", ""),
			},
		},
		{
			Name:    "pushover_get_limits",
			Summary: "Get Pushover application message limits and usage.",
			Source:  "pushover_tool/pushover_tool.py",
			Params:  map[string]any{},
		},
	},
}

func batch7Lane7YouTubeImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"youtube_search_videos":        batch7Lane7YouTubeSearchVideos,
		"youtube_get_video_details":    batch7Lane7YouTubeGetVideoDetails,
		"youtube_get_channel":          batch7Lane7YouTubeGetChannel,
		"youtube_list_channel_videos":  batch7Lane7YouTubeListChannelVideos,
		"youtube_get_playlist":         batch7Lane7YouTubeGetPlaylist,
		"youtube_search_channels":      batch7Lane7YouTubeSearchChannels,
		"youtube_get_video_comments":   batch7Lane7YouTubeGetVideoComments,
		"youtube_get_video_categories": batch7Lane7YouTubeGetVideoCategories,
	}
}

func batch7Lane7RedditImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"reddit_search":             batch7Lane7RedditSearch,
		"reddit_get_posts":          batch7Lane7RedditGetPosts,
		"reddit_get_comments":       batch7Lane7RedditGetComments,
		"reddit_get_user":           batch7Lane7RedditGetUser,
		"reddit_get_subreddit_info": batch7Lane7RedditGetSubredditInfo,
		"reddit_get_post_detail":    batch7Lane7RedditGetPostDetail,
		"reddit_get_user_posts":     batch7Lane7RedditGetUserPosts,
	}
}

func batch7Lane7TwitterImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"twitter_search_tweets":      batch7Lane7TwitterSearchTweets,
		"twitter_get_user":           batch7Lane7TwitterGetUser,
		"twitter_get_user_tweets":    batch7Lane7TwitterGetUserTweets,
		"twitter_get_tweet":          batch7Lane7TwitterGetTweet,
		"twitter_get_user_followers": batch7Lane7TwitterGetUserFollowers,
		"twitter_get_tweet_replies":  batch7Lane7TwitterGetTweetReplies,
		"twitter_get_list_tweets":    batch7Lane7TwitterGetListTweets,
	}
}

func batch7Lane7TwilioImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"twilio_send_sms":           batch7Lane7TwilioSendSMS,
		"twilio_send_whatsapp":      batch7Lane7TwilioSendWhatsApp,
		"twilio_list_messages":      batch7Lane7TwilioListMessages,
		"twilio_get_message":        batch7Lane7TwilioGetMessage,
		"twilio_list_phone_numbers": batch7Lane7TwilioListPhoneNumbers,
		"twilio_list_calls":         batch7Lane7TwilioListCalls,
		"twilio_delete_message":     batch7Lane7TwilioDeleteMessage,
	}
}

func batch7Lane7PushoverImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pushover_send":           batch7Lane7PushoverSend,
		"pushover_validate_user":  batch7Lane7PushoverValidateUser,
		"pushover_list_sounds":    batch7Lane7PushoverListSounds,
		"pushover_check_receipt":  batch7Lane7PushoverCheckReceipt,
		"pushover_cancel_receipt": batch7Lane7PushoverCancelReceipt,
		"pushover_send_glance":    batch7Lane7PushoverSendGlance,
		"pushover_get_limits":     batch7Lane7PushoverGetLimits,
	}
}

// ------------------------------------------------------------- shared HTTP ---

// batch7Lane7Request is one provider call after auth and URL resolution.
type batch7Lane7Request struct {
	Method string
	URL    string
	Query  url.Values
	Header map[string]string
	Form   url.Values
	JSON   any
}

// batch7Lane7Response is a decoded provider response.
type batch7Lane7Response struct {
	Status int
	Body   []byte
	Parsed any
}

// batch7Lane7Do executes one HTTP request through the runtime transport.
func batch7Lane7Do(ctx context.Context, rt *Runtime, spec batch7Lane7Request) (batch7Lane7Response, error) {
	var out batch7Lane7Response
	if ctx == nil {
		ctx = context.Background()
	}
	target := spec.URL
	if encoded := spec.Query.Encode(); encoded != "" {
		if strings.Contains(target, "?") {
			target += "&" + encoded
		} else {
			target += "?" + encoded
		}
	}
	var body io.Reader
	contentType := ""
	switch {
	case spec.Form != nil:
		body = strings.NewReader(spec.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case spec.JSON != nil:
		encoded, err := json.Marshal(spec.JSON)
		if err != nil {
			return out, err
		}
		body = bytes.NewReader(encoded)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, spec.Method, target, body)
	if err != nil {
		return out, err
	}
	for name, value := range spec.Header {
		req.Header.Set(name, value)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := newHTTPClient(nil)
	if rt != nil && rt.HTTP != nil {
		client = rt.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane7MaxBody))
	if err != nil {
		return out, err
	}
	out.Status = resp.StatusCode
	out.Body = raw
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Parsed)
	}
	return out, nil
}

// batch7Lane7Getenv resolves the runtime environment lookup.
func batch7Lane7Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch7Lane7Env reads a trimmed environment value through the runtime lookup.
func batch7Lane7Env(rt *Runtime, name string) string {
	return strings.TrimSpace(batch7Lane7Getenv(rt)(name))
}

// batch7Lane7ModuleBase resolves a module base URL, honouring WithBaseURL.
func batch7Lane7ModuleBase(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch7Lane7ModuleToken resolves a single-value module credential.
func batch7Lane7ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(batch7Lane7Getenv(rt))
	return strings.TrimSpace(token)
}

// batch7Lane7InjectedToken returns only the WithCredential value, ignoring
// environment fallbacks (Reddit uses it as a pre-acquired OAuth token).
func batch7Lane7InjectedToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.ConfiguredToken())
}

// batch7Lane7TimedOut reports whether a transport failure was a timeout.
func batch7Lane7TimedOut(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch7Lane7NetworkFailure maps a transport failure onto the module wording.
func batch7Lane7NetworkFailure(moduleID string, ctx context.Context, err error) map[string]any {
	timedOut := batch7Lane7TimedOut(ctx, err)
	switch moduleID {
	case batch7Lane7YouTubeModule:
		if timedOut {
			return map[string]any{"error": "Request to YouTube API timed out"}
		}
		return map[string]any{"error": "YouTube API request failed: " + err.Error()}
	case batch7Lane7RedditModule:
		if timedOut {
			return map[string]any{"error": "Request to Reddit timed out"}
		}
		return map[string]any{"error": "Reddit request failed: " + err.Error()}
	case batch7Lane7TwitterModule:
		if timedOut {
			return map[string]any{"error": "Request to Twitter timed out"}
		}
		return map[string]any{"error": "Twitter request failed: " + err.Error()}
	case batch7Lane7TwilioModule:
		if timedOut {
			return map[string]any{"error": "Request to Twilio timed out"}
		}
		return map[string]any{"error": "Twilio request failed: " + err.Error()}
	default:
		if timedOut {
			return map[string]any{"error": "Request to Pushover timed out"}
		}
		return map[string]any{"error": "Pushover request failed: " + err.Error()}
	}
}

// ---------------------------------------------------------------- helpers ---

// batch7Lane7Map views a decoded JSON object.
func batch7Lane7Map(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch7Lane7Default mirrors Python's `dict.get(key, fallback)` for fields
// whose absence must surface as the upstream default rather than null.
func batch7Lane7Default(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

// batch7Lane7MapOrEmpty mirrors `result.get(key, {})`: absent objects are
// empty maps rather than JSON null.
func batch7Lane7MapOrEmpty(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	return map[string]any{}
}

// batch7Lane7Slice views a decoded JSON array.
func batch7Lane7Slice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch7Lane7SliceOrEmpty mirrors upstream's `data.get(key) or []`.
func batch7Lane7SliceOrEmpty(value any) []any {
	if items, ok := value.([]any); ok && items != nil {
		return items
	}
	return []any{}
}

// batch7Lane7String reads a decoded JSON string.
func batch7Lane7String(value any) string {
	text, _ := value.(string)
	return text
}

// batch7Lane7Bool reads a decoded JSON boolean.
func batch7Lane7Bool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch7Lane7Int reads a decoded JSON number as an int.
func batch7Lane7Int(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// batch7Lane7Dig walks nested JSON objects.
func batch7Lane7Dig(root any, path ...string) any {
	current := root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

// batch7Lane7Clamp bounds an integer to [low, high].
func batch7Lane7Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch7Lane7Truncate caps text by runes, matching Python slicing.
func batch7Lane7Truncate(text string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// batch7Lane7OptionalInt distinguishes an absent argument from zero.
func batch7Lane7OptionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	return intArg(input, name, 0), true
}

// batch7Lane7JoinErrors joins Pushover's `errors` array the upstream way.
func batch7Lane7JoinErrors(value any) string {
	parts := make([]string, 0)
	for _, item := range batch7Lane7Slice(value) {
		if text := batch7Lane7String(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ", ")
}

// ----------------------------------------------------------------- YouTube ---

// batch7Lane7YouTubeAuth resolves the API key or returns the upstream error.
func batch7Lane7YouTubeAuth(rt *Runtime) (string, map[string]any) {
	key := batch7Lane7ModuleToken(rt, batch7Lane7YouTubeModule)
	if key == "" {
		return "", map[string]any{
			"error": "YOUTUBE_API_KEY not set",
			"help":  "Get an API key at https://console.cloud.google.com/apis/credentials",
		}
	}
	return key, nil
}

// batch7Lane7YouTubeGET performs one Data API GET with the key attached.
func batch7Lane7YouTubeGET(
	ctx context.Context,
	rt *Runtime,
	key, endpoint string,
	params url.Values,
) (any, map[string]any) {
	base := batch7Lane7ModuleBase(rt, batch7Lane7YouTubeModule)
	if base == "" {
		base = batch7Lane7YouTubeDefaultAPI
	}
	params.Set("key", key)
	res, err := batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodGet,
		URL:    base + "/" + endpoint,
		Query:  params,
	})
	if err != nil {
		return nil, batch7Lane7NetworkFailure(batch7Lane7YouTubeModule, ctx, err)
	}
	if res.Status == http.StatusForbidden {
		reason := ""
		if errorsList := batch7Lane7Slice(batch7Lane7Dig(res.Parsed, "error", "errors")); len(errorsList) > 0 {
			reason = batch7Lane7String(batch7Lane7Map(errorsList[0])["reason"])
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
			"error": fmt.Sprintf("YouTube API error %d: %s", res.Status, batch7Lane7Truncate(string(res.Body), batch7Lane7MaxError)),
		}
	}
	return res.Parsed, nil
}

// batch7Lane7ParseDuration converts ISO 8601 durations to human text.
func batch7Lane7ParseDuration(duration string) string {
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

func batch7Lane7YouTubeSearchVideos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	maxResults := batch7Lane7Clamp(intArg(input, "max_results", 10), 1, 50)
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
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "search", params)
	if fail != nil {
		return fail, nil
	}
	results := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		snippet := batch7Lane7Map(item["snippet"])
		results = append(results, map[string]any{
			"videoId":      batch7Lane7String(batch7Lane7Dig(item, "id", "videoId")),
			"title":        batch7Lane7String(snippet["title"]),
			"channelTitle": batch7Lane7String(snippet["channelTitle"]),
			"channelId":    batch7Lane7String(snippet["channelId"]),
			"publishedAt":  batch7Lane7String(snippet["publishedAt"]),
			"description":  batch7Lane7String(snippet["description"]),
			"thumbnail":    batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{
		"query":         query,
		"results":       results,
		"total_results": batch7Lane7Int(batch7Lane7Dig(data, "pageInfo", "totalResults")),
	}, nil
}

func batch7Lane7YouTubeGetVideoDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	videoIDs := stringArg(input, "video_ids")
	if videoIDs == "" {
		return map[string]any{"error": "video_ids is required"}, nil
	}
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "videos", url.Values{
		"part": {"snippet,contentDetails,statistics"},
		"id":   {videoIDs},
	})
	if fail != nil {
		return fail, nil
	}
	videos := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		snippet := batch7Lane7Map(item["snippet"])
		stats := batch7Lane7Map(item["statistics"])
		content := batch7Lane7Map(item["contentDetails"])
		rawDuration := batch7Lane7String(content["duration"])
		videos = append(videos, map[string]any{
			"videoId":      batch7Lane7String(item["id"]),
			"title":        batch7Lane7String(snippet["title"]),
			"description":  batch7Lane7String(snippet["description"]),
			"channelTitle": batch7Lane7String(snippet["channelTitle"]),
			"channelId":    batch7Lane7String(snippet["channelId"]),
			"publishedAt":  batch7Lane7String(snippet["publishedAt"]),
			"tags":         batch7Lane7SliceOrEmpty(snippet["tags"]),
			"categoryId":   batch7Lane7String(snippet["categoryId"]),
			"duration":     batch7Lane7ParseDuration(rawDuration),
			"duration_raw": rawDuration,
			"viewCount":    batch7Lane7Int(stats["viewCount"]),
			"likeCount":    batch7Lane7Int(stats["likeCount"]),
			"commentCount": batch7Lane7Int(stats["commentCount"]),
			"thumbnail":    batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "high", "url")),
		})
	}
	return map[string]any{"videos": videos}, nil
}

func batch7Lane7YouTubeGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
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
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "channels", params)
	if fail != nil {
		return fail, nil
	}
	items := batch7Lane7Slice(batch7Lane7Map(data)["items"])
	if len(items) == 0 {
		return map[string]any{"error": "Channel not found"}, nil
	}
	item := batch7Lane7Map(items[0])
	snippet := batch7Lane7Map(item["snippet"])
	stats := batch7Lane7Map(item["statistics"])
	return map[string]any{
		"channelId":         batch7Lane7String(item["id"]),
		"title":             batch7Lane7String(snippet["title"]),
		"description":       batch7Lane7String(snippet["description"]),
		"customUrl":         batch7Lane7String(snippet["customUrl"]),
		"publishedAt":       batch7Lane7String(snippet["publishedAt"]),
		"subscriberCount":   batch7Lane7Int(stats["subscriberCount"]),
		"videoCount":        batch7Lane7Int(stats["videoCount"]),
		"viewCount":         batch7Lane7Int(stats["viewCount"]),
		"thumbnail":         batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "high", "url")),
		"uploadsPlaylistId": batch7Lane7String(batch7Lane7Dig(item, "contentDetails", "relatedPlaylists", "uploads")),
	}, nil
}

func batch7Lane7YouTubeListChannelVideos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	maxResults := batch7Lane7Clamp(intArg(input, "max_results", 20), 1, 50)
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "search", url.Values{
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
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		snippet := batch7Lane7Map(item["snippet"])
		videos = append(videos, map[string]any{
			"videoId":     batch7Lane7String(batch7Lane7Dig(item, "id", "videoId")),
			"title":       batch7Lane7String(snippet["title"]),
			"publishedAt": batch7Lane7String(snippet["publishedAt"]),
			"description": batch7Lane7String(snippet["description"]),
			"thumbnail":   batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{"channel_id": channelID, "videos": videos}, nil
}

func batch7Lane7YouTubeGetPlaylist(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	playlistID := stringArg(input, "playlist_id")
	if playlistID == "" {
		return map[string]any{"error": "playlist_id is required"}, nil
	}
	maxResults := batch7Lane7Clamp(intArg(input, "max_results", 20), 1, 50)
	playlistData, fail := batch7Lane7YouTubeGET(ctx, rt, key, "playlists", url.Values{
		"part": {"snippet,contentDetails"},
		"id":   {playlistID},
	})
	if fail != nil {
		return fail, nil
	}
	playlistItems := batch7Lane7Slice(batch7Lane7Map(playlistData)["items"])
	if len(playlistItems) == 0 {
		return map[string]any{"error": "Playlist not found"}, nil
	}
	playlist := batch7Lane7Map(playlistItems[0])
	playlistSnippet := batch7Lane7Map(playlist["snippet"])
	itemsData, fail := batch7Lane7YouTubeGET(ctx, rt, key, "playlistItems", url.Values{
		"part":       {"snippet,contentDetails"},
		"playlistId": {playlistID},
		"maxResults": {strconv.Itoa(maxResults)},
	})
	if fail != nil {
		return fail, nil
	}
	items := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(itemsData)["items"]) {
		item := batch7Lane7Map(raw)
		snippet := batch7Lane7Map(item["snippet"])
		items = append(items, map[string]any{
			"videoId":      batch7Lane7String(batch7Lane7Dig(snippet, "resourceId", "videoId")),
			"title":        batch7Lane7String(snippet["title"]),
			"position":     batch7Lane7Int(snippet["position"]),
			"channelTitle": batch7Lane7String(snippet["videoOwnerChannelTitle"]),
			"thumbnail":    batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{
		"playlistId":   playlistID,
		"title":        batch7Lane7String(playlistSnippet["title"]),
		"description":  batch7Lane7String(playlistSnippet["description"]),
		"channelTitle": batch7Lane7String(playlistSnippet["channelTitle"]),
		"itemCount":    batch7Lane7Int(batch7Lane7Dig(playlist, "contentDetails", "itemCount")),
		"items":        items,
	}, nil
}

func batch7Lane7YouTubeSearchChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	maxResults := batch7Lane7Clamp(intArg(input, "max_results", 10), 1, 50)
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "search", url.Values{
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
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		snippet := batch7Lane7Map(item["snippet"])
		results = append(results, map[string]any{
			"channelId":   batch7Lane7String(batch7Lane7Dig(item, "id", "channelId")),
			"title":       batch7Lane7String(snippet["title"]),
			"description": batch7Lane7String(snippet["description"]),
			"thumbnail":   batch7Lane7String(batch7Lane7Dig(snippet, "thumbnails", "medium", "url")),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func batch7Lane7YouTubeGetVideoComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	videoID := stringArg(input, "video_id")
	if videoID == "" {
		return map[string]any{"error": "video_id is required"}, nil
	}
	maxResults := batch7Lane7Clamp(intArg(input, "max_results", 20), 1, 100)
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "commentThreads", url.Values{
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
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		top := batch7Lane7Map(batch7Lane7Dig(item, "snippet", "topLevelComment", "snippet"))
		comments = append(comments, map[string]any{
			"author":      batch7Lane7String(top["authorDisplayName"]),
			"text":        batch7Lane7String(top["textDisplay"]),
			"likeCount":   batch7Lane7Int(top["likeCount"]),
			"publishedAt": batch7Lane7String(top["publishedAt"]),
			"replyCount":  batch7Lane7Int(batch7Lane7Dig(item, "snippet", "totalReplyCount")),
		})
	}
	return map[string]any{"video_id": videoID, "comments": comments}, nil
}

func batch7Lane7YouTubeGetVideoCategories(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, fail := batch7Lane7YouTubeAuth(rt)
	if fail != nil {
		return fail, nil
	}
	regionCode := stringArg(input, "region_code")
	data, fail := batch7Lane7YouTubeGET(ctx, rt, key, "videoCategories", url.Values{
		"part":       {"snippet"},
		"regionCode": {regionCode},
	})
	if fail != nil {
		return fail, nil
	}
	categories := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["items"]) {
		item := batch7Lane7Map(raw)
		categories = append(categories, map[string]any{
			"id":    batch7Lane7String(item["id"]),
			"title": batch7Lane7String(batch7Lane7Dig(item, "snippet", "title")),
		})
	}
	return map[string]any{"region_code": regionCode, "categories": categories}, nil
}

// ------------------------------------------------------------------ Reddit ---

// batch7Lane7RedditCredentials reads the OAuth client credentials.
func batch7Lane7RedditCredentials(rt *Runtime) (string, string) {
	return batch7Lane7Env(rt, "REDDIT_CLIENT_ID"), batch7Lane7Env(rt, "REDDIT_CLIENT_SECRET")
}

// batch7Lane7RedditReady checks injected-token or client-credential presence
// before any argument validation, matching the upstream order.
func batch7Lane7RedditReady(rt *Runtime) map[string]any {
	if batch7Lane7InjectedToken(rt, batch7Lane7RedditModule) != "" {
		return nil
	}
	clientID, clientSecret := batch7Lane7RedditCredentials(rt)
	if clientID == "" || clientSecret == "" {
		return map[string]any{
			"error": "REDDIT_CLIENT_ID and REDDIT_CLIENT_SECRET not set",
			"help":  "Create an app at https://www.reddit.com/prefs/apps",
		}
	}
	return nil
}

// batch7Lane7RedditTokenURL resolves the token endpoint override.
func batch7Lane7RedditTokenURL(rt *Runtime) string {
	if override := batch7Lane7Env(rt, "REDDIT_TOKEN_URL"); override != "" {
		return override
	}
	return batch7Lane7RedditDefaultToken
}

// batch7Lane7RedditAccessToken returns an injected token or performs the
// OAuth2 client-credentials grant.
func batch7Lane7RedditAccessToken(ctx context.Context, rt *Runtime) (string, map[string]any) {
	if injected := batch7Lane7InjectedToken(rt, batch7Lane7RedditModule); injected != "" {
		return injected, nil
	}
	clientID, clientSecret := batch7Lane7RedditCredentials(rt)
	failure := map[string]any{"error": "Failed to acquire Reddit access token"}
	if clientID == "" || clientSecret == "" {
		return "", map[string]any{
			"error": "REDDIT_CLIENT_ID and REDDIT_CLIENT_SECRET not set",
			"help":  "Create an app at https://www.reddit.com/prefs/apps",
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	res, err := batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodPost,
		URL:    batch7Lane7RedditTokenURL(rt),
		Header: map[string]string{
			"Authorization": "Basic " + basic,
			"User-Agent":    batch7Lane7RedditUserAgent,
		},
		Form: url.Values{"grant_type": {"client_credentials"}},
	})
	if err != nil || res.Status != http.StatusOK {
		return "", failure
	}
	token := batch7Lane7String(batch7Lane7Map(res.Parsed)["access_token"])
	if token == "" {
		return "", failure
	}
	return token, nil
}

// batch7Lane7RedditGET performs one authenticated OAuth API GET.
func batch7Lane7RedditGET(ctx context.Context, rt *Runtime, path string, params url.Values) (any, map[string]any) {
	token, fail := batch7Lane7RedditAccessToken(ctx, rt)
	if fail != nil {
		return nil, fail
	}
	base := batch7Lane7ModuleBase(rt, batch7Lane7RedditModule)
	if base == "" {
		base = batch7Lane7RedditDefaultAPI
	}
	res, err := batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodGet,
		URL:    base + path,
		Query:  params,
		Header: map[string]string{
			"Authorization": "bearer " + token,
			"User-Agent":    batch7Lane7RedditUserAgent,
		},
	})
	if err != nil {
		return nil, batch7Lane7NetworkFailure(batch7Lane7RedditModule, ctx, err)
	}
	switch res.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Reddit token may be expired."}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Forbidden. Check Reddit app permissions."}
	}
	if res.Status != http.StatusOK {
		return nil, map[string]any{
			"error": fmt.Sprintf("Reddit API error %d: %s", res.Status, batch7Lane7Truncate(string(res.Body), batch7Lane7MaxError)),
		}
	}
	return res.Parsed, nil
}

// batch7Lane7RedditPosts extracts t3 children from a Listing.
func batch7Lane7RedditPosts(listing map[string]any) []any {
	posts := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Dig(listing, "data", "children")) {
		child := batch7Lane7Map(raw)
		if batch7Lane7String(child["kind"]) != "t3" {
			continue
		}
		post := batch7Lane7Map(child["data"])
		posts = append(posts, map[string]any{
			"id":           batch7Lane7String(post["id"]),
			"title":        batch7Lane7String(post["title"]),
			"author":       batch7Lane7String(post["author"]),
			"subreddit":    batch7Lane7String(post["subreddit"]),
			"score":        batch7Lane7Int(post["score"]),
			"num_comments": batch7Lane7Int(post["num_comments"]),
			"url":          batch7Lane7String(post["url"]),
			"permalink":    batch7Lane7String(post["permalink"]),
			"selftext":     batch7Lane7Truncate(batch7Lane7String(post["selftext"]), 500),
			"created_utc":  batch7Lane7Default(post["created_utc"], 0),
			"is_self":      batch7Lane7Bool(post["is_self"]),
		})
	}
	return posts
}

func batch7Lane7RedditSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	subreddit := stringArg(input, "subreddit")
	path := "/search"
	restrict := "false"
	if subreddit != "" {
		path = "/r/" + subreddit + "/search"
		restrict = "true"
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, path, url.Values{
		"q":           {query},
		"sort":        {stringArg(input, "sort")},
		"t":           {stringArg(input, "time")},
		"limit":       {strconv.Itoa(batch7Lane7Clamp(intArg(input, "limit", 25), 1, 100))},
		"restrict_sr": {restrict},
	})
	if fail != nil {
		return fail, nil
	}
	posts := batch7Lane7RedditPosts(batch7Lane7Map(data))
	return map[string]any{"query": query, "posts": posts, "count": len(posts)}, nil
}

func batch7Lane7RedditGetPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	subreddit := stringArg(input, "subreddit")
	if subreddit == "" {
		return map[string]any{"error": "subreddit is required"}, nil
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, "/r/"+subreddit+"/"+stringArg(input, "sort"), url.Values{
		"limit": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "limit", 25), 1, 100))},
		"t":     {stringArg(input, "time")},
	})
	if fail != nil {
		return fail, nil
	}
	posts := batch7Lane7RedditPosts(batch7Lane7Map(data))
	return map[string]any{"subreddit": subreddit, "posts": posts, "count": len(posts)}, nil
}

func batch7Lane7RedditGetComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	postID := stringArg(input, "post_id")
	if postID == "" {
		return map[string]any{"error": "post_id is required"}, nil
	}
	subreddit := stringArg(input, "subreddit")
	path := "/comments/" + postID
	if subreddit != "" {
		path = "/r/" + subreddit + "/comments/" + postID
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, path, url.Values{
		"sort":  {stringArg(input, "sort")},
		"limit": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "limit", 25), 1, 100))},
	})
	if fail != nil {
		return fail, nil
	}
	listings := batch7Lane7Slice(data)
	if len(listings) < 2 {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	post := map[string]any{}
	postChildren := batch7Lane7Slice(batch7Lane7Dig(listings[0], "data", "children"))
	if len(postChildren) > 0 {
		if child := batch7Lane7Map(postChildren[0]); batch7Lane7String(child["kind"]) == "t3" {
			details := batch7Lane7Map(child["data"])
			post = map[string]any{
				"id":       batch7Lane7String(details["id"]),
				"title":    batch7Lane7String(details["title"]),
				"author":   batch7Lane7String(details["author"]),
				"score":    batch7Lane7Int(details["score"]),
				"selftext": batch7Lane7Truncate(batch7Lane7String(details["selftext"]), 500),
			}
		}
	}
	comments := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Dig(listings[1], "data", "children")) {
		child := batch7Lane7Map(raw)
		if batch7Lane7String(child["kind"]) != "t1" {
			continue
		}
		details := batch7Lane7Map(child["data"])
		comments = append(comments, map[string]any{
			"id":          batch7Lane7String(details["id"]),
			"author":      batch7Lane7String(details["author"]),
			"body":        batch7Lane7Truncate(batch7Lane7String(details["body"]), 500),
			"score":       batch7Lane7Int(details["score"]),
			"created_utc": batch7Lane7Default(details["created_utc"], 0),
		})
	}
	return map[string]any{"post": post, "comments": comments, "comment_count": len(comments)}, nil
}

func batch7Lane7RedditGetUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, "/user/"+username+"/about", nil)
	if fail != nil {
		return fail, nil
	}
	details := batch7Lane7Map(batch7Lane7Map(data)["data"])
	return map[string]any{
		"name":          batch7Lane7String(details["name"]),
		"link_karma":    batch7Lane7Int(details["link_karma"]),
		"comment_karma": batch7Lane7Int(details["comment_karma"]),
		"total_karma":   batch7Lane7Int(details["total_karma"]),
		"created_utc":   batch7Lane7Default(details["created_utc"], 0),
		"is_gold":       batch7Lane7Bool(details["is_gold"]),
	}, nil
}

func batch7Lane7RedditGetSubredditInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	subreddit := stringArg(input, "subreddit")
	if subreddit == "" {
		return map[string]any{"error": "subreddit is required"}, nil
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, "/r/"+subreddit+"/about", nil)
	if fail != nil {
		return fail, nil
	}
	details := batch7Lane7Map(batch7Lane7Map(data)["data"])
	return map[string]any{
		"name":            batch7Lane7String(details["display_name"]),
		"title":           batch7Lane7String(details["title"]),
		"description":     batch7Lane7Truncate(batch7Lane7String(details["public_description"]), 500),
		"subscribers":     batch7Lane7Int(details["subscribers"]),
		"active_users":    batch7Lane7Int(details["accounts_active"]),
		"created_utc":     batch7Lane7Default(details["created_utc"], 0),
		"over18":          batch7Lane7Bool(details["over18"]),
		"subreddit_type":  batch7Lane7String(details["subreddit_type"]),
		"submission_type": batch7Lane7String(details["submission_type"]),
	}, nil
}

func batch7Lane7RedditGetPostDetail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	postID := stringArg(input, "post_id")
	if postID == "" {
		return map[string]any{"error": "post_id is required"}, nil
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, "/by_id/t3_"+postID, nil)
	if fail != nil {
		return fail, nil
	}
	children := batch7Lane7Slice(batch7Lane7Dig(data, "data", "children"))
	if len(children) == 0 || batch7Lane7String(batch7Lane7Map(children[0])["kind"]) != "t3" {
		return map[string]any{"error": "Post not found"}, nil
	}
	details := batch7Lane7Map(batch7Lane7Map(children[0])["data"])
	return map[string]any{
		"id":              batch7Lane7String(details["id"]),
		"title":           batch7Lane7String(details["title"]),
		"author":          batch7Lane7String(details["author"]),
		"subreddit":       batch7Lane7String(details["subreddit"]),
		"score":           batch7Lane7Int(details["score"]),
		"upvote_ratio":    batch7Lane7Default(details["upvote_ratio"], 0),
		"num_comments":    batch7Lane7Int(details["num_comments"]),
		"url":             batch7Lane7String(details["url"]),
		"permalink":       batch7Lane7String(details["permalink"]),
		"selftext":        batch7Lane7Truncate(batch7Lane7String(details["selftext"]), 2000),
		"link_flair_text": batch7Lane7String(details["link_flair_text"]),
		"created_utc":     batch7Lane7Default(details["created_utc"], 0),
		"is_self":         batch7Lane7Bool(details["is_self"]),
		"over_18":         batch7Lane7Bool(details["over_18"]),
		"locked":          batch7Lane7Bool(details["locked"]),
		"archived":        batch7Lane7Bool(details["archived"]),
	}, nil
}

func batch7Lane7RedditGetUserPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := batch7Lane7RedditReady(rt); fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := batch7Lane7RedditGET(ctx, rt, "/user/"+username+"/submitted", url.Values{
		"sort":  {stringArg(input, "sort")},
		"t":     {stringArg(input, "time")},
		"limit": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "limit", 25), 1, 100))},
	})
	if fail != nil {
		return fail, nil
	}
	posts := batch7Lane7RedditPosts(batch7Lane7Map(data))
	return map[string]any{"username": username, "posts": posts, "count": len(posts)}, nil
}

// ----------------------------------------------------------------- Twitter ---

// batch7Lane7TwitterAuth resolves the bearer token or the upstream error.
func batch7Lane7TwitterAuth(rt *Runtime) (string, map[string]any) {
	token := batch7Lane7ModuleToken(rt, batch7Lane7TwitterModule)
	if token == "" {
		return "", map[string]any{
			"error": "X_BEARER_TOKEN is required",
			"help":  "Set X_BEARER_TOKEN environment variable",
		}
	}
	return token, nil
}

// batch7Lane7TwitterGET performs one v2 GET.
func batch7Lane7TwitterGET(
	ctx context.Context,
	rt *Runtime,
	token, path string,
	params url.Values,
) (any, map[string]any) {
	base := batch7Lane7ModuleBase(rt, batch7Lane7TwitterModule)
	if base == "" {
		base = batch7Lane7TwitterDefaultAPI
	}
	res, err := batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodGet,
		URL:    base + path,
		Query:  params,
		Header: map[string]string{"Authorization": "Bearer " + token},
	})
	if err != nil {
		return nil, batch7Lane7NetworkFailure(batch7Lane7TwitterModule, ctx, err)
	}
	if res.Status >= http.StatusBadRequest {
		return nil, map[string]any{
			"error": fmt.Sprintf("HTTP %d: %s", res.Status, batch7Lane7Truncate(string(res.Body), batch7Lane7MaxError)),
		}
	}
	return res.Parsed, nil
}

// batch7Lane7TwitterTweet extracts the upstream tweet projection.
func batch7Lane7TwitterTweet(tweet map[string]any) map[string]any {
	metrics := batch7Lane7Map(tweet["public_metrics"])
	return map[string]any{
		"id":               tweet["id"],
		"text":             tweet["text"],
		"author_id":        tweet["author_id"],
		"created_at":       tweet["created_at"],
		"lang":             tweet["lang"],
		"retweet_count":    batch7Lane7Int(metrics["retweet_count"]),
		"reply_count":      batch7Lane7Int(metrics["reply_count"]),
		"like_count":       batch7Lane7Int(metrics["like_count"]),
		"impression_count": batch7Lane7Int(metrics["impression_count"]),
	}
}

// batch7Lane7TwitterUsers indexes includes.users by id.
func batch7Lane7TwitterUsers(data map[string]any) map[string]map[string]any {
	users := map[string]map[string]any{}
	for _, raw := range batch7Lane7Slice(batch7Lane7Dig(data, "includes", "users")) {
		user := batch7Lane7Map(raw)
		users[batch7Lane7String(user["id"])] = user
	}
	return users
}

func batch7Lane7TwitterSearchTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/tweets/search/recent", url.Values{
		"query":        {query},
		"max_results":  {strconv.Itoa(batch7Lane7Clamp(intArg(input, "max_results", 10), 10, 100))},
		"sort_order":   {stringArg(input, "sort_order")},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := batch7Lane7Map(data)
	users := batch7Lane7TwitterUsers(object)
	results := make([]any, 0)
	for _, raw := range batch7Lane7Slice(object["data"]) {
		tweet := batch7Lane7TwitterTweet(batch7Lane7Map(raw))
		author := users[batch7Lane7String(tweet["author_id"])]
		tweet["author_name"] = author["name"]
		tweet["author_username"] = author["username"]
		results = append(results, tweet)
	}
	count := len(results)
	if resultCount, present := batch7Lane7Map(object["meta"])["result_count"]; present {
		count = batch7Lane7Int(resultCount)
	}
	return map[string]any{"count": count, "tweets": results}, nil
}

func batch7Lane7TwitterGetUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/users/by/username/"+username, url.Values{
		"user.fields": {"created_at,description,public_metrics,profile_image_url,verified"},
	})
	if fail != nil {
		return fail, nil
	}
	user := batch7Lane7Map(batch7Lane7Map(data)["data"])
	metrics := batch7Lane7Map(user["public_metrics"])
	return map[string]any{
		"id":                user["id"],
		"name":              user["name"],
		"username":          user["username"],
		"description":       user["description"],
		"created_at":        user["created_at"],
		"profile_image_url": user["profile_image_url"],
		"verified":          user["verified"],
		"followers_count":   batch7Lane7Int(metrics["followers_count"]),
		"following_count":   batch7Lane7Int(metrics["following_count"]),
		"tweet_count":       batch7Lane7Int(metrics["tweet_count"]),
	}, nil
}

func batch7Lane7TwitterGetUserTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		return map[string]any{"error": "user_id is required"}, nil
	}
	params := url.Values{
		"max_results":  {strconv.Itoa(batch7Lane7Clamp(intArg(input, "max_results", 10), 5, 100))},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
	}
	excludes := make([]string, 0, 2)
	if boolArg(input, "exclude_replies", true) {
		excludes = append(excludes, "replies")
	}
	if boolArg(input, "exclude_retweets", true) {
		excludes = append(excludes, "retweets")
	}
	if len(excludes) > 0 {
		params.Set("exclude", strings.Join(excludes, ","))
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/users/"+userID+"/tweets", params)
	if fail != nil {
		return fail, nil
	}
	tweets := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["data"]) {
		tweets = append(tweets, batch7Lane7TwitterTweet(batch7Lane7Map(raw)))
	}
	return map[string]any{"count": len(tweets), "tweets": tweets}, nil
}

func batch7Lane7TwitterGetTweet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	tweetID := stringArg(input, "tweet_id")
	if tweetID == "" {
		return map[string]any{"error": "tweet_id is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/tweets/"+tweetID, url.Values{
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := batch7Lane7Map(data)
	tweet := batch7Lane7TwitterTweet(batch7Lane7Map(object["data"]))
	users := batch7Lane7Slice(batch7Lane7Dig(object, "includes", "users"))
	if len(users) > 0 {
		user := batch7Lane7Map(users[0])
		tweet["author_name"] = user["name"]
		tweet["author_username"] = user["username"]
	}
	return tweet, nil
}

func batch7Lane7TwitterGetUserFollowers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		return map[string]any{"error": "user_id is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/users/"+userID+"/followers", url.Values{
		"max_results": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "max_results", 25), 1, 100))},
		"user.fields": {"created_at,description,public_metrics,profile_image_url,verified"},
	})
	if fail != nil {
		return fail, nil
	}
	followers := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(data)["data"]) {
		user := batch7Lane7Map(raw)
		metrics := batch7Lane7Map(user["public_metrics"])
		followers = append(followers, map[string]any{
			"id":              user["id"],
			"name":            user["name"],
			"username":        user["username"],
			"description":     batch7Lane7Truncate(batch7Lane7String(user["description"]), 200),
			"followers_count": batch7Lane7Int(metrics["followers_count"]),
			"following_count": batch7Lane7Int(metrics["following_count"]),
			"verified":        user["verified"],
		})
	}
	return map[string]any{"count": len(followers), "followers": followers}, nil
}

func batch7Lane7TwitterGetTweetReplies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	tweetID := stringArg(input, "tweet_id")
	if tweetID == "" {
		return map[string]any{"error": "tweet_id is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/tweets/search/recent", url.Values{
		"query":        {"conversation_id:" + tweetID + " is:reply"},
		"max_results":  {strconv.Itoa(batch7Lane7Clamp(intArg(input, "max_results", 10), 10, 100))},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := batch7Lane7Map(data)
	users := batch7Lane7TwitterUsers(object)
	replies := make([]any, 0)
	for _, raw := range batch7Lane7Slice(object["data"]) {
		reply := batch7Lane7TwitterTweet(batch7Lane7Map(raw))
		author := users[batch7Lane7String(reply["author_id"])]
		reply["author_name"] = author["name"]
		reply["author_username"] = author["username"]
		replies = append(replies, reply)
	}
	return map[string]any{"tweet_id": tweetID, "count": len(replies), "replies": replies}, nil
}

func batch7Lane7TwitterGetListTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7TwitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	listID := stringArg(input, "list_id")
	if listID == "" {
		return map[string]any{"error": "list_id is required"}, nil
	}
	data, fail := batch7Lane7TwitterGET(ctx, rt, token, "/lists/"+listID+"/tweets", url.Values{
		"max_results":  {strconv.Itoa(batch7Lane7Clamp(intArg(input, "max_results", 10), 1, 100))},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := batch7Lane7Map(data)
	users := batch7Lane7TwitterUsers(object)
	tweets := make([]any, 0)
	for _, raw := range batch7Lane7Slice(object["data"]) {
		tweet := batch7Lane7TwitterTweet(batch7Lane7Map(raw))
		author := users[batch7Lane7String(tweet["author_id"])]
		tweet["author_name"] = author["name"]
		tweet["author_username"] = author["username"]
		tweets = append(tweets, tweet)
	}
	return map[string]any{"list_id": listID, "count": len(tweets), "tweets": tweets}, nil
}

// ------------------------------------------------------------------ Twilio ---

// batch7Lane7TwilioAuth resolves the account SID and auth token.
func batch7Lane7TwilioAuth(rt *Runtime) (string, string, map[string]any) {
	sid := batch7Lane7Env(rt, "TWILIO_ACCOUNT_SID")
	token := batch7Lane7Env(rt, "TWILIO_AUTH_TOKEN")
	if sid == "" || token == "" {
		return "", "", map[string]any{
			"error": "TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN not set",
			"help":  "Get credentials from https://console.twilio.com/",
		}
	}
	return sid, token, nil
}

// batch7Lane7TwilioRequest performs one Basic-authenticated API call.
func batch7Lane7TwilioRequest(
	ctx context.Context,
	rt *Runtime,
	sid, token, method, suffix string,
	query, form url.Values,
) (batch7Lane7Response, map[string]any) {
	base := batch7Lane7ModuleBase(rt, batch7Lane7TwilioModule)
	if base == "" {
		base = batch7Lane7TwilioDefaultAPI
	}
	basic := base64.StdEncoding.EncodeToString([]byte(sid + ":" + token))
	spec := batch7Lane7Request{
		Method: method,
		URL:    base + batch7Lane7TwilioAPIVersionPath + url.PathEscape(sid) + suffix,
		Header: map[string]string{"Authorization": "Basic " + basic},
	}
	if method == http.MethodGet {
		spec.Query = query
	} else {
		spec.Form = form
	}
	res, err := batch7Lane7Do(ctx, rt, spec)
	if err != nil {
		return res, batch7Lane7NetworkFailure(batch7Lane7TwilioModule, ctx, err)
	}
	return res, nil
}

// batch7Lane7TwilioChecked maps the shared status contract.
func batch7Lane7TwilioChecked(res batch7Lane7Response) (any, map[string]any) {
	switch res.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Check your Twilio credentials."}
	case http.StatusNotFound:
		return nil, map[string]any{"error": "Resource not found."}
	case http.StatusTooManyRequests:
		return nil, map[string]any{"error": "Rate limited. Try again shortly."}
	}
	if res.Status != http.StatusOK && res.Status != http.StatusCreated {
		return nil, map[string]any{
			"error": fmt.Sprintf("Twilio API error %d: %s", res.Status, batch7Lane7Truncate(string(res.Body), batch7Lane7MaxError)),
		}
	}
	return res.Parsed, nil
}

// batch7Lane7TwilioMessage extracts the upstream message projection.
func batch7Lane7TwilioMessage(message map[string]any) map[string]any {
	return map[string]any{
		"sid":           batch7Lane7String(message["sid"]),
		"to":            batch7Lane7String(message["to"]),
		"from":          batch7Lane7String(message["from"]),
		"body":          batch7Lane7String(message["body"]),
		"status":        batch7Lane7String(message["status"]),
		"direction":     batch7Lane7String(message["direction"]),
		"date_sent":     message["date_sent"],
		"price":         message["price"],
		"error_code":    message["error_code"],
		"error_message": message["error_message"],
	}
}

func batch7Lane7TwilioSendSMS(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	to, from, body := stringArg(input, "to"), stringArg(input, "from_number"), stringArg(input, "body")
	if to == "" || from == "" || body == "" {
		return map[string]any{"error": "to, from_number, and body are required"}, nil
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodPost, "/Messages.json", nil, url.Values{
		"To":   {to},
		"From": {from},
		"Body": {body},
	})
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return batch7Lane7TwilioMessage(batch7Lane7Map(decoded)), nil
}

func batch7Lane7TwilioSendWhatsApp(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	to, from, body := stringArg(input, "to"), stringArg(input, "from_number"), stringArg(input, "body")
	if to == "" || from == "" || body == "" {
		return map[string]any{"error": "to, from_number, and body are required"}, nil
	}
	if !strings.HasPrefix(to, "whatsapp:") {
		to = "whatsapp:" + to
	}
	if !strings.HasPrefix(from, "whatsapp:") {
		from = "whatsapp:" + from
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodPost, "/Messages.json", nil, url.Values{
		"To":   {to},
		"From": {from},
		"Body": {body},
	})
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return batch7Lane7TwilioMessage(batch7Lane7Map(decoded)), nil
}

func batch7Lane7TwilioListMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := url.Values{
		"PageSize": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "page_size", 20), 1, 1000))},
	}
	if to := stringArg(input, "to"); to != "" {
		query.Set("To", to)
	}
	if from := stringArg(input, "from_number"); from != "" {
		query.Set("From", from)
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodGet, "/Messages.json", query, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	messages := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(decoded)["messages"]) {
		messages = append(messages, batch7Lane7TwilioMessage(batch7Lane7Map(raw)))
	}
	return map[string]any{"messages": messages, "count": len(messages)}, nil
}

func batch7Lane7TwilioGetMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	messageSID := stringArg(input, "message_sid")
	if messageSID == "" {
		return map[string]any{"error": "message_sid is required"}, nil
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodGet, "/Messages/"+url.PathEscape(messageSID)+".json", nil, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return batch7Lane7TwilioMessage(batch7Lane7Map(decoded)), nil
}

func batch7Lane7TwilioListPhoneNumbers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodGet, "/IncomingPhoneNumbers.json", url.Values{"PageSize": {"100"}}, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	numbers := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(decoded)["incoming_phone_numbers"]) {
		number := batch7Lane7Map(raw)
		capabilities := batch7Lane7Map(number["capabilities"])
		numbers = append(numbers, map[string]any{
			"sid":           batch7Lane7String(number["sid"]),
			"phone_number":  batch7Lane7String(number["phone_number"]),
			"friendly_name": batch7Lane7String(number["friendly_name"]),
			"sms_enabled":   batch7Lane7Bool(capabilities["sms"]),
			"voice_enabled": batch7Lane7Bool(capabilities["voice"]),
			"mms_enabled":   batch7Lane7Bool(capabilities["mms"]),
			"date_created":  number["date_created"],
		})
	}
	return map[string]any{"phone_numbers": numbers, "count": len(numbers)}, nil
}

func batch7Lane7TwilioListCalls(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := url.Values{
		"PageSize": {strconv.Itoa(batch7Lane7Clamp(intArg(input, "page_size", 20), 1, 1000))},
	}
	if to := stringArg(input, "to"); to != "" {
		query.Set("To", to)
	}
	if from := stringArg(input, "from_number"); from != "" {
		query.Set("From", from)
	}
	if status := stringArg(input, "status"); status != "" {
		query.Set("Status", status)
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodGet, "/Calls.json", query, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := batch7Lane7TwilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	calls := make([]any, 0)
	for _, raw := range batch7Lane7Slice(batch7Lane7Map(decoded)["calls"]) {
		call := batch7Lane7Map(raw)
		calls = append(calls, map[string]any{
			"sid":        batch7Lane7String(call["sid"]),
			"to":         batch7Lane7String(call["to"]),
			"from":       batch7Lane7String(call["from"]),
			"status":     batch7Lane7String(call["status"]),
			"direction":  batch7Lane7String(call["direction"]),
			"duration":   call["duration"],
			"price":      call["price"],
			"start_time": call["start_time"],
			"end_time":   call["end_time"],
		})
	}
	return map[string]any{"calls": calls, "count": len(calls)}, nil
}

func batch7Lane7TwilioDeleteMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := batch7Lane7TwilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	messageSID := stringArg(input, "message_sid")
	if messageSID == "" {
		return map[string]any{"error": "message_sid is required"}, nil
	}
	res, fail := batch7Lane7TwilioRequest(ctx, rt, sid, token, http.MethodDelete, "/Messages/"+url.PathEscape(messageSID)+".json", nil, nil)
	if fail != nil {
		return fail, nil
	}
	switch res.Status {
	case http.StatusNoContent:
		return map[string]any{"sid": messageSID, "status": "deleted"}, nil
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Twilio credentials."}, nil
	case http.StatusNotFound:
		return map[string]any{"error": "Message not found."}, nil
	}
	return map[string]any{
		"error": fmt.Sprintf("Twilio API error %d: %s", res.Status, batch7Lane7Truncate(string(res.Body), batch7Lane7MaxError)),
	}, nil
}

// ---------------------------------------------------------------- Pushover ---

// batch7Lane7PushoverAuth resolves the application token.
func batch7Lane7PushoverAuth(rt *Runtime) (string, map[string]any) {
	token := batch7Lane7ModuleToken(rt, batch7Lane7PushoverModule)
	if token == "" {
		return "", map[string]any{
			"error": "PUSHOVER_API_TOKEN not set",
			"help":  "Create an app at https://pushover.net/apps/build to get a token",
		}
	}
	return token, nil
}

// batch7Lane7PushoverPost performs one form-encoded POST.
func batch7Lane7PushoverPost(
	ctx context.Context,
	rt *Runtime,
	suffix string,
	form url.Values,
) (batch7Lane7Response, error) {
	base := batch7Lane7ModuleBase(rt, batch7Lane7PushoverModule)
	if base == "" {
		base = batch7Lane7PushoverDefaultAPI
	}
	return batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodPost,
		URL:    base + suffix,
		Form:   form,
	})
}

// batch7Lane7PushoverGet performs one token-query GET.
func batch7Lane7PushoverGet(
	ctx context.Context,
	rt *Runtime,
	suffix string,
	query url.Values,
) (batch7Lane7Response, error) {
	base := batch7Lane7ModuleBase(rt, batch7Lane7PushoverModule)
	if base == "" {
		base = batch7Lane7PushoverDefaultAPI
	}
	return batch7Lane7Do(ctx, rt, batch7Lane7Request{
		Method: http.MethodGet,
		URL:    base + suffix,
		Query:  query,
	})
}

func batch7Lane7PushoverSend(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey, message := stringArg(input, "user_key"), stringArg(input, "message")
	if userKey == "" || message == "" {
		return map[string]any{"error": "user_key and message are required"}, nil
	}
	if utf8.RuneCountInString(message) > 1024 {
		return map[string]any{"error": "message must be 1024 characters or fewer"}, nil
	}
	priority := intArg(input, "priority", 0)
	switch priority {
	case -2, -1, 0, 1, 2:
	default:
		return map[string]any{"error": "priority must be -2, -1, 0, 1, or 2"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}, "message": {message}}
	if title := stringArg(input, "title"); title != "" {
		form.Set("title", batch7Lane7Truncate(title, 250))
	}
	if priority != 0 {
		form.Set("priority", strconv.Itoa(priority))
	}
	if priority == 2 {
		form.Set("retry", "60")
		form.Set("expire", "3600")
	}
	if sound := stringArg(input, "sound"); sound != "" {
		form.Set("sound", sound)
	}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	if link := stringArg(input, "url"); link != "" {
		form.Set("url", batch7Lane7Truncate(link, 512))
	}
	if title := stringArg(input, "url_title"); title != "" {
		form.Set("url_title", batch7Lane7Truncate(title, 100))
	}
	if boolArg(input, "html", false) {
		form.Set("html", "1")
	}
	if ttl := intArg(input, "ttl", 0); ttl > 0 {
		form.Set("ttl", strconv.Itoa(ttl))
	}
	res, err := batch7Lane7PushoverPost(ctx, rt, "/messages.json", form)
	if err != nil {
		if batch7Lane7TimedOut(ctx, err) {
			return map[string]any{"error": "Request to Pushover timed out"}, nil
		}
		return map[string]any{"error": "Pushover request failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		detail := batch7Lane7JoinErrors(result["errors"])
		if detail == "" {
			detail = batch7Lane7Truncate(string(res.Body), 300)
		}
		return map[string]any{"error": "Pushover error: " + detail}, nil
	}
	out := map[string]any{"status": "sent", "request": batch7Lane7String(result["request"])}
	if receipt, present := result["receipt"]; present {
		out["receipt"] = receipt
	}
	return out, nil
}

func batch7Lane7PushoverValidateUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey := stringArg(input, "user_key")
	if userKey == "" {
		return map[string]any{"error": "user_key is required"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	res, err := batch7Lane7PushoverPost(ctx, rt, "/users/validate.json", form)
	if err != nil {
		return map[string]any{"error": "Validation failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	return map[string]any{
		"is_valid": batch7Lane7Int(result["status"]) == 1,
		"devices":  batch7Lane7SliceOrEmpty(result["devices"]),
		"is_group": batch7Lane7Int(result["group"]) == 1,
	}, nil
}

func batch7Lane7PushoverListSounds(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, err := batch7Lane7PushoverGet(ctx, rt, "/sounds.json", url.Values{"token": {token}})
	if err != nil {
		return map[string]any{"error": "List sounds failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		return map[string]any{"error": "Failed to list sounds: " + batch7Lane7Truncate(string(res.Body), 300)}, nil
	}
	return map[string]any{"sounds": batch7Lane7MapOrEmpty(result["sounds"])}, nil
}

func batch7Lane7PushoverCheckReceipt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	receipt := stringArg(input, "receipt")
	if receipt == "" {
		return map[string]any{"error": "receipt is required"}, nil
	}
	res, err := batch7Lane7PushoverGet(ctx, rt, "/receipts/"+receipt+".json", url.Values{"token": {token}})
	if err != nil {
		return map[string]any{"error": "Receipt check failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		return map[string]any{"error": "Receipt check failed: " + batch7Lane7Truncate(string(res.Body), 300)}, nil
	}
	return map[string]any{
		"acknowledged":      batch7Lane7Int(result["acknowledged"]) == 1,
		"acknowledged_by":   batch7Lane7String(result["acknowledged_by"]),
		"acknowledged_at":   batch7Lane7Default(result["acknowledged_at"], 0),
		"last_delivered_at": batch7Lane7Default(result["last_delivered_at"], 0),
		"expired":           batch7Lane7Int(result["expired"]) == 1,
		"called_back":       batch7Lane7Int(result["called_back"]) == 1,
	}, nil
}

func batch7Lane7PushoverCancelReceipt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	receipt := stringArg(input, "receipt")
	if receipt == "" {
		return map[string]any{"error": "receipt is required"}, nil
	}
	res, err := batch7Lane7PushoverPost(ctx, rt, "/receipts/"+receipt+"/cancel.json", url.Values{"token": {token}})
	if err != nil {
		if batch7Lane7TimedOut(ctx, err) {
			return map[string]any{"error": "Cancel request timed out"}, nil
		}
		return map[string]any{"error": "Cancel failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		return map[string]any{"error": "Cancel failed: " + batch7Lane7Truncate(string(res.Body), 300)}, nil
	}
	return map[string]any{"status": "cancelled", "receipt": receipt}, nil
}

func batch7Lane7PushoverSendGlance(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey := stringArg(input, "user_key")
	if userKey == "" {
		return map[string]any{"error": "user_key is required"}, nil
	}
	title, text, subtext := stringArg(input, "title"), stringArg(input, "text"), stringArg(input, "subtext")
	count, hasCount := batch7Lane7OptionalInt(input, "count")
	percent, hasPercent := batch7Lane7OptionalInt(input, "percent")
	if title == "" && text == "" && subtext == "" && !hasCount && !hasPercent {
		return map[string]any{"error": "At least one of title, text, subtext, count, or percent is required"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}}
	if title != "" {
		form.Set("title", batch7Lane7Truncate(title, 100))
	}
	if text != "" {
		form.Set("text", batch7Lane7Truncate(text, 100))
	}
	if subtext != "" {
		form.Set("subtext", batch7Lane7Truncate(subtext, 100))
	}
	if hasCount {
		form.Set("count", strconv.Itoa(batch7Lane7Clamp(count, -999, 999)))
	}
	if hasPercent {
		form.Set("percent", strconv.Itoa(batch7Lane7Clamp(percent, 0, 100)))
	}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	res, err := batch7Lane7PushoverPost(ctx, rt, "/glances.json", form)
	if err != nil {
		if batch7Lane7TimedOut(ctx, err) {
			return map[string]any{"error": "Glance request timed out"}, nil
		}
		return map[string]any{"error": "Glance update failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		detail := batch7Lane7JoinErrors(result["errors"])
		if detail == "" {
			detail = batch7Lane7Truncate(string(res.Body), 300)
		}
		return map[string]any{"error": "Glance error: " + detail}, nil
	}
	return map[string]any{"status": "updated", "request": batch7Lane7String(result["request"])}, nil
}

func batch7Lane7PushoverGetLimits(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := batch7Lane7PushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, err := batch7Lane7PushoverGet(ctx, rt, "/apps/limits.json", url.Values{"token": {token}})
	if err != nil {
		if batch7Lane7TimedOut(ctx, err) {
			return map[string]any{"error": "Limits request timed out"}, nil
		}
		return map[string]any{"error": "Limits check failed: " + err.Error()}, nil
	}
	result := batch7Lane7Map(res.Parsed)
	if batch7Lane7Int(result["status"]) != 1 {
		return map[string]any{"error": "Limits check failed: " + batch7Lane7Truncate(string(res.Body), 300)}, nil
	}
	return map[string]any{
		"limit":     batch7Lane7Int(result["limit"]),
		"remaining": batch7Lane7Int(result["remaining"]),
		"reset":     batch7Lane7Default(result["reset"], 0),
	}, nil
}
