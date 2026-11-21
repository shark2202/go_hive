package social

// Reddit OAuth2: token acquisition, catalog entry and seven read actions.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	redditUserAgent       = "HiveAgent/1.0"
	redditDefaultTokenURL = "https://www.reddit.com/api/v1/access_token"
)

var redditTool = &Tool{
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

func redditImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"reddit_search":             redditSearch,
		"reddit_get_posts":          redditGetPosts,
		"reddit_get_comments":       redditGetComments,
		"reddit_get_user":           redditGetUser,
		"reddit_get_subreddit_info": redditGetSubredditInfo,
		"reddit_get_post_detail":    redditGetPostDetail,
		"reddit_get_user_posts":     redditGetUserPosts,
	}
}

// ------------------------------------------------------------------ Reddit ---

// redditCredentials reads the OAuth client credentials.
func redditCredentials(rt *Runtime) (string, string) {
	return envValue(rt, "REDDIT_CLIENT_ID"), envValue(rt, "REDDIT_CLIENT_SECRET")
}

// redditReady checks injected-token or client-credential presence
// before any argument validation, matching the upstream order.
func redditReady(rt *Runtime) map[string]any {
	if injectedToken(rt, redditModuleID) != "" {
		return nil
	}
	clientID, clientSecret := redditCredentials(rt)
	if clientID == "" || clientSecret == "" {
		return map[string]any{
			"error": "REDDIT_CLIENT_ID and REDDIT_CLIENT_SECRET not set",
			"help":  "Create an app at https://www.reddit.com/prefs/apps",
		}
	}
	return nil
}

// redditTokenURL resolves the token endpoint override.
func redditTokenURL(rt *Runtime) string {
	if override := envValue(rt, "REDDIT_TOKEN_URL"); override != "" {
		return override
	}
	return redditDefaultTokenURL
}

// redditAccessToken returns an injected token or performs the
// OAuth2 client-credentials grant.
func redditAccessToken(ctx context.Context, rt *Runtime) (string, map[string]any) {
	if injected := injectedToken(rt, redditModuleID); injected != "" {
		return injected, nil
	}
	clientID, clientSecret := redditCredentials(rt)
	failure := map[string]any{"error": "Failed to acquire Reddit access token"}
	if clientID == "" || clientSecret == "" {
		return "", map[string]any{
			"error": "REDDIT_CLIENT_ID and REDDIT_CLIENT_SECRET not set",
			"help":  "Create an app at https://www.reddit.com/prefs/apps",
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	res, err := doRequest(ctx, rt, providerRequest{
		Method: http.MethodPost,
		URL:    redditTokenURL(rt),
		Header: map[string]string{
			"Authorization": "Basic " + basic,
			"User-Agent":    redditUserAgent,
		},
		Form: url.Values{"grant_type": {"client_credentials"}},
	})
	if err != nil || res.Status != http.StatusOK {
		return "", failure
	}
	token := asString(asMap(res.Parsed)["access_token"])
	if token == "" {
		return "", failure
	}
	return token, nil
}

// redditGET performs one authenticated OAuth API GET.
func redditGET(ctx context.Context, rt *Runtime, path string, params url.Values) (any, map[string]any) {
	token, fail := redditAccessToken(ctx, rt)
	if fail != nil {
		return nil, fail
	}
	base := moduleBaseURL(rt, redditModuleID)
	if base == "" {
		base = redditDefaultBase
	}
	res, err := doRequest(ctx, rt, providerRequest{
		Method: http.MethodGet,
		URL:    base + path,
		Query:  params,
		Header: map[string]string{
			"Authorization": "bearer " + token,
			"User-Agent":    redditUserAgent,
		},
	})
	if err != nil {
		return nil, networkFailure(redditModuleID, ctx, err)
	}
	switch res.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Reddit token may be expired."}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Forbidden. Check Reddit app permissions."}
	}
	if res.Status != http.StatusOK {
		return nil, map[string]any{
			"error": fmt.Sprintf("Reddit API error %d: %s", res.Status, truncateTextClamped(string(res.Body), maxErrorBytes)),
		}
	}
	return res.Parsed, nil
}

// redditPosts extracts t3 children from a Listing.
func redditPosts(listing map[string]any) []any {
	posts := make([]any, 0)
	for _, raw := range asSlice(digValue(listing, "data", "children")) {
		child := asMap(raw)
		if asString(child["kind"]) != "t3" {
			continue
		}
		post := asMap(child["data"])
		posts = append(posts, map[string]any{
			"id":           asString(post["id"]),
			"title":        asString(post["title"]),
			"author":       asString(post["author"]),
			"subreddit":    asString(post["subreddit"]),
			"score":        intValue(post["score"]),
			"num_comments": intValue(post["num_comments"]),
			"url":          asString(post["url"]),
			"permalink":    asString(post["permalink"]),
			"selftext":     truncateTextClamped(asString(post["selftext"]), 500),
			"created_utc":  valueOrDefault(post["created_utc"], 0),
			"is_self":      asBool(post["is_self"]),
		})
	}
	return posts
}

func redditSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
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
	data, fail := redditGET(ctx, rt, path, url.Values{
		"q":           {query},
		"sort":        {stringArg(input, "sort")},
		"t":           {stringArg(input, "time")},
		"limit":       {strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 100))},
		"restrict_sr": {restrict},
	})
	if fail != nil {
		return fail, nil
	}
	posts := redditPosts(asMap(data))
	return map[string]any{"query": query, "posts": posts, "count": len(posts)}, nil
}

func redditGetPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
		return fail, nil
	}
	subreddit := stringArg(input, "subreddit")
	if subreddit == "" {
		return map[string]any{"error": "subreddit is required"}, nil
	}
	data, fail := redditGET(ctx, rt, "/r/"+subreddit+"/"+stringArg(input, "sort"), url.Values{
		"limit": {strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 100))},
		"t":     {stringArg(input, "time")},
	})
	if fail != nil {
		return fail, nil
	}
	posts := redditPosts(asMap(data))
	return map[string]any{"subreddit": subreddit, "posts": posts, "count": len(posts)}, nil
}

func redditGetComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
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
	data, fail := redditGET(ctx, rt, path, url.Values{
		"sort":  {stringArg(input, "sort")},
		"limit": {strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 100))},
	})
	if fail != nil {
		return fail, nil
	}
	listings := asSlice(data)
	if len(listings) < 2 {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	post := map[string]any{}
	postChildren := asSlice(digValue(listings[0], "data", "children"))
	if len(postChildren) > 0 {
		if child := asMap(postChildren[0]); asString(child["kind"]) == "t3" {
			details := asMap(child["data"])
			post = map[string]any{
				"id":       asString(details["id"]),
				"title":    asString(details["title"]),
				"author":   asString(details["author"]),
				"score":    intValue(details["score"]),
				"selftext": truncateTextClamped(asString(details["selftext"]), 500),
			}
		}
	}
	comments := make([]any, 0)
	for _, raw := range asSlice(digValue(listings[1], "data", "children")) {
		child := asMap(raw)
		if asString(child["kind"]) != "t1" {
			continue
		}
		details := asMap(child["data"])
		comments = append(comments, map[string]any{
			"id":          asString(details["id"]),
			"author":      asString(details["author"]),
			"body":        truncateTextClamped(asString(details["body"]), 500),
			"score":       intValue(details["score"]),
			"created_utc": valueOrDefault(details["created_utc"], 0),
		})
	}
	return map[string]any{"post": post, "comments": comments, "comment_count": len(comments)}, nil
}

func redditGetUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := redditGET(ctx, rt, "/user/"+username+"/about", nil)
	if fail != nil {
		return fail, nil
	}
	details := asMap(asMap(data)["data"])
	return map[string]any{
		"name":          asString(details["name"]),
		"link_karma":    intValue(details["link_karma"]),
		"comment_karma": intValue(details["comment_karma"]),
		"total_karma":   intValue(details["total_karma"]),
		"created_utc":   valueOrDefault(details["created_utc"], 0),
		"is_gold":       asBool(details["is_gold"]),
	}, nil
}

func redditGetSubredditInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
		return fail, nil
	}
	subreddit := stringArg(input, "subreddit")
	if subreddit == "" {
		return map[string]any{"error": "subreddit is required"}, nil
	}
	data, fail := redditGET(ctx, rt, "/r/"+subreddit+"/about", nil)
	if fail != nil {
		return fail, nil
	}
	details := asMap(asMap(data)["data"])
	return map[string]any{
		"name":            asString(details["display_name"]),
		"title":           asString(details["title"]),
		"description":     truncateTextClamped(asString(details["public_description"]), 500),
		"subscribers":     intValue(details["subscribers"]),
		"active_users":    intValue(details["accounts_active"]),
		"created_utc":     valueOrDefault(details["created_utc"], 0),
		"over18":          asBool(details["over18"]),
		"subreddit_type":  asString(details["subreddit_type"]),
		"submission_type": asString(details["submission_type"]),
	}, nil
}

func redditGetPostDetail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
		return fail, nil
	}
	postID := stringArg(input, "post_id")
	if postID == "" {
		return map[string]any{"error": "post_id is required"}, nil
	}
	data, fail := redditGET(ctx, rt, "/by_id/t3_"+postID, nil)
	if fail != nil {
		return fail, nil
	}
	children := asSlice(digValue(data, "data", "children"))
	if len(children) == 0 || asString(asMap(children[0])["kind"]) != "t3" {
		return map[string]any{"error": "Post not found"}, nil
	}
	details := asMap(asMap(children[0])["data"])
	return map[string]any{
		"id":              asString(details["id"]),
		"title":           asString(details["title"]),
		"author":          asString(details["author"]),
		"subreddit":       asString(details["subreddit"]),
		"score":           intValue(details["score"]),
		"upvote_ratio":    valueOrDefault(details["upvote_ratio"], 0),
		"num_comments":    intValue(details["num_comments"]),
		"url":             asString(details["url"]),
		"permalink":       asString(details["permalink"]),
		"selftext":        truncateTextClamped(asString(details["selftext"]), 2000),
		"link_flair_text": asString(details["link_flair_text"]),
		"created_utc":     valueOrDefault(details["created_utc"], 0),
		"is_self":         asBool(details["is_self"]),
		"over_18":         asBool(details["over_18"]),
		"locked":          asBool(details["locked"]),
		"archived":        asBool(details["archived"]),
	}, nil
}

func redditGetUserPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if fail := redditReady(rt); fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := redditGET(ctx, rt, "/user/"+username+"/submitted", url.Values{
		"sort":  {stringArg(input, "sort")},
		"t":     {stringArg(input, "time")},
		"limit": {strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 100))},
	})
	if fail != nil {
		return fail, nil
	}
	posts := redditPosts(asMap(data))
	return map[string]any{"username": username, "posts": posts, "count": len(posts)}, nil
}
