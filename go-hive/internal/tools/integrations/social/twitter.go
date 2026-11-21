package social

// Twitter/X API v2: catalog entry and seven read actions.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var twitterTool = &Tool{
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

func twitterImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"twitter_search_tweets":      twitterSearchTweets,
		"twitter_get_user":           twitterGetUser,
		"twitter_get_user_tweets":    twitterGetUserTweets,
		"twitter_get_tweet":          twitterGetTweet,
		"twitter_get_user_followers": twitterGetUserFollowers,
		"twitter_get_tweet_replies":  twitterGetTweetReplies,
		"twitter_get_list_tweets":    twitterGetListTweets,
	}
}

// ----------------------------------------------------------------- Twitter ---

// twitterAuth resolves the bearer token or the upstream error.
func twitterAuth(rt *Runtime) (string, map[string]any) {
	token := moduleToken(rt, twitterModuleID)
	if token == "" {
		return "", map[string]any{
			"error": "X_BEARER_TOKEN is required",
			"help":  "Set X_BEARER_TOKEN environment variable",
		}
	}
	return token, nil
}

// twitterGET performs one v2 GET.
func twitterGET(
	ctx context.Context,
	rt *Runtime,
	token, path string,
	params url.Values,
) (any, map[string]any) {
	base := moduleBaseURL(rt, twitterModuleID)
	if base == "" {
		base = twitterDefaultBase
	}
	res, err := doRequest(ctx, rt, providerRequest{
		Method: http.MethodGet,
		URL:    base + path,
		Query:  params,
		Header: map[string]string{"Authorization": "Bearer " + token},
	})
	if err != nil {
		return nil, networkFailure(twitterModuleID, ctx, err)
	}
	if res.Status >= http.StatusBadRequest {
		return nil, map[string]any{
			"error": fmt.Sprintf("HTTP %d: %s", res.Status, truncateTextClamped(string(res.Body), maxErrorBytes)),
		}
	}
	return res.Parsed, nil
}

// twitterTweet extracts the upstream tweet projection.
func twitterTweet(tweet map[string]any) map[string]any {
	metrics := asMap(tweet["public_metrics"])
	return map[string]any{
		"id":               tweet["id"],
		"text":             tweet["text"],
		"author_id":        tweet["author_id"],
		"created_at":       tweet["created_at"],
		"lang":             tweet["lang"],
		"retweet_count":    intValue(metrics["retweet_count"]),
		"reply_count":      intValue(metrics["reply_count"]),
		"like_count":       intValue(metrics["like_count"]),
		"impression_count": intValue(metrics["impression_count"]),
	}
}

// twitterUsers indexes includes.users by id.
func twitterUsers(data map[string]any) map[string]map[string]any {
	users := map[string]map[string]any{}
	for _, raw := range asSlice(digValue(data, "includes", "users")) {
		user := asMap(raw)
		users[asString(user["id"])] = user
	}
	return users
}

func twitterSearchTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/tweets/search/recent", url.Values{
		"query":        {query},
		"max_results":  {strconv.Itoa(clampInt(intArg(input, "max_results", 10), 10, 100))},
		"sort_order":   {stringArg(input, "sort_order")},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := asMap(data)
	users := twitterUsers(object)
	results := make([]any, 0)
	for _, raw := range asSlice(object["data"]) {
		tweet := twitterTweet(asMap(raw))
		author := users[asString(tweet["author_id"])]
		tweet["author_name"] = author["name"]
		tweet["author_username"] = author["username"]
		results = append(results, tweet)
	}
	count := len(results)
	if resultCount, present := asMap(object["meta"])["result_count"]; present {
		count = intValue(resultCount)
	}
	return map[string]any{"count": count, "tweets": results}, nil
}

func twitterGetUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	username := stringArg(input, "username")
	if username == "" {
		return map[string]any{"error": "username is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/users/by/username/"+username, url.Values{
		"user.fields": {"created_at,description,public_metrics,profile_image_url,verified"},
	})
	if fail != nil {
		return fail, nil
	}
	user := asMap(asMap(data)["data"])
	metrics := asMap(user["public_metrics"])
	return map[string]any{
		"id":                user["id"],
		"name":              user["name"],
		"username":          user["username"],
		"description":       user["description"],
		"created_at":        user["created_at"],
		"profile_image_url": user["profile_image_url"],
		"verified":          user["verified"],
		"followers_count":   intValue(metrics["followers_count"]),
		"following_count":   intValue(metrics["following_count"]),
		"tweet_count":       intValue(metrics["tweet_count"]),
	}, nil
}

func twitterGetUserTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		return map[string]any{"error": "user_id is required"}, nil
	}
	params := url.Values{
		"max_results":  {strconv.Itoa(clampInt(intArg(input, "max_results", 10), 5, 100))},
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
	data, fail := twitterGET(ctx, rt, token, "/users/"+userID+"/tweets", params)
	if fail != nil {
		return fail, nil
	}
	tweets := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["data"]) {
		tweets = append(tweets, twitterTweet(asMap(raw)))
	}
	return map[string]any{"count": len(tweets), "tweets": tweets}, nil
}

func twitterGetTweet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	tweetID := stringArg(input, "tweet_id")
	if tweetID == "" {
		return map[string]any{"error": "tweet_id is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/tweets/"+tweetID, url.Values{
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := asMap(data)
	tweet := twitterTweet(asMap(object["data"]))
	users := asSlice(digValue(object, "includes", "users"))
	if len(users) > 0 {
		user := asMap(users[0])
		tweet["author_name"] = user["name"]
		tweet["author_username"] = user["username"]
	}
	return tweet, nil
}

func twitterGetUserFollowers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		return map[string]any{"error": "user_id is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/users/"+userID+"/followers", url.Values{
		"max_results": {strconv.Itoa(clampInt(intArg(input, "max_results", 25), 1, 100))},
		"user.fields": {"created_at,description,public_metrics,profile_image_url,verified"},
	})
	if fail != nil {
		return fail, nil
	}
	followers := make([]any, 0)
	for _, raw := range asSlice(asMap(data)["data"]) {
		user := asMap(raw)
		metrics := asMap(user["public_metrics"])
		followers = append(followers, map[string]any{
			"id":              user["id"],
			"name":            user["name"],
			"username":        user["username"],
			"description":     truncateTextClamped(asString(user["description"]), 200),
			"followers_count": intValue(metrics["followers_count"]),
			"following_count": intValue(metrics["following_count"]),
			"verified":        user["verified"],
		})
	}
	return map[string]any{"count": len(followers), "followers": followers}, nil
}

func twitterGetTweetReplies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	tweetID := stringArg(input, "tweet_id")
	if tweetID == "" {
		return map[string]any{"error": "tweet_id is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/tweets/search/recent", url.Values{
		"query":        {"conversation_id:" + tweetID + " is:reply"},
		"max_results":  {strconv.Itoa(clampInt(intArg(input, "max_results", 10), 10, 100))},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := asMap(data)
	users := twitterUsers(object)
	replies := make([]any, 0)
	for _, raw := range asSlice(object["data"]) {
		reply := twitterTweet(asMap(raw))
		author := users[asString(reply["author_id"])]
		reply["author_name"] = author["name"]
		reply["author_username"] = author["username"]
		replies = append(replies, reply)
	}
	return map[string]any{"tweet_id": tweetID, "count": len(replies), "replies": replies}, nil
}

func twitterGetListTweets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := twitterAuth(rt)
	if fail != nil {
		return fail, nil
	}
	listID := stringArg(input, "list_id")
	if listID == "" {
		return map[string]any{"error": "list_id is required"}, nil
	}
	data, fail := twitterGET(ctx, rt, token, "/lists/"+listID+"/tweets", url.Values{
		"max_results":  {strconv.Itoa(clampInt(intArg(input, "max_results", 10), 1, 100))},
		"tweet.fields": {"created_at,public_metrics,author_id,lang"},
		"expansions":   {"author_id"},
		"user.fields":  {"name,username"},
	})
	if fail != nil {
		return fail, nil
	}
	object := asMap(data)
	users := twitterUsers(object)
	tweets := make([]any, 0)
	for _, raw := range asSlice(object["data"]) {
		tweet := twitterTweet(asMap(raw))
		author := users[asString(tweet["author_id"])]
		tweet["author_name"] = author["name"]
		tweet["author_username"] = author["username"]
		tweets = append(tweets, tweet)
	}
	return map[string]any{"list_id": listID, "count": len(tweets), "tweets": tweets}, nil
}
