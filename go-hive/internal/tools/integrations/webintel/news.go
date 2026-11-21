package webintel

// News module: the upstream two-provider model — NewsData is primary and
// Finlight is the fallback (and the only provider for sentiment). 429
// responses retry with exponential backoff up to three times, then the other
// provider is tried; when both fail the upstream "All providers failed"
// envelope is returned. The backoff sleeper is a package variable so tests
// run without real delays.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ----------------------------------------------------------------- News ---

// newsTool mirrors the upstream module: seven actions over the
// NewsData and Finlight providers.
var newsTool = &Tool{
	ID:          "news_tool",
	Name:        "news",
	DisplayName: "News",
	Summary:     "News integration (7 actions).",
	Actions: []Action{
		{
			Name:     "news_search",
			Summary:  "Search news articles with filters.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"from_date": paramDefault("string", ""),
				"to_date":   paramDefault("string", ""),
				"language":  paramDefault("string", "en"),
				"limit":     paramDefault("integer", 10),
				"sources":   paramDefault("string", ""),
				"category":  paramDefault("string", ""),
				"country":   paramDefault("string", ""),
			},
		},
		{
			Name:     "news_headlines",
			Summary:  "Get top news headlines by category and country.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"category", "country"},
			Params: map[string]any{
				"category": param("string"),
				"country":  param("string"),
				"limit":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_company",
			Summary:  "Get news mentioning a specific company.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"company_name"},
			Params: map[string]any{
				"company_name": param("string"),
				"days_back":    paramDefault("integer", 7),
				"limit":        paramDefault("integer", 10),
				"language":     paramDefault("string", "en"),
			},
		},
		{
			Name:     "news_sentiment",
			Summary:  "Get news with sentiment analysis (Finlight provider).",
			Source:   "news_tool/news_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"from_date": paramDefault("string", ""),
				"to_date":   paramDefault("string", ""),
			},
		},
		{
			Name:    "news_latest",
			Summary: "Get the latest breaking news without a search query.",
			Source:  "news_tool/news_tool.py",
			Params: map[string]any{
				"language": paramDefault("string", "en"),
				"country":  paramDefault("string", ""),
				"category": paramDefault("string", ""),
				"limit":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_source",
			Summary:  "Get news from specific sources.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"sources"},
			Params: map[string]any{
				"sources":   param("string"),
				"query":     paramDefault("string", ""),
				"days_back": paramDefault("integer", 7),
				"language":  paramDefault("string", "en"),
				"limit":     paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_topic",
			Summary:  "Get news articles about a broad topic or industry.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"topic"},
			Params: map[string]any{
				"topic":     param("string"),
				"days_back": paramDefault("integer", 3),
				"language":  paramDefault("string", "en"),
				"country":   paramDefault("string", ""),
				"limit":     paramDefault("integer", 10),
			},
		},
	},
}

// newsArgs is the shared normalized search request.
type newsArgs struct {
	Query    string
	FromDate string
	ToDate   string
	Language string
	Sources  string
	Category string
	Country  string
	Limit    int
}

// newsImpls binds every action name to its implementation.
func newsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"news_search":     newsSearch,
		"news_headlines":  newsHeadlines,
		"news_by_company": newsByCompany,
		"news_sentiment":  newsSentiment,
		"news_latest":     newsLatest,
		"news_by_source":  newsBySource,
		"news_by_topic":   newsByTopic,
	}
}

// newsKeys resolves the two provider credentials independently.
func newsKeys(rt *Runtime) (string, string) {
	return moduleToken(rt, "news_tool"), moduleToken(rt, "finlight_tool")
}

// newsNoCredentials is the upstream missing-pair error.
func newsNoCredentials() map[string]any {
	return map[string]any{
		"error": "No news credentials configured",
		"help":  "Set NEWSDATA_API_KEY or FINLIGHT_API_KEY environment variable",
	}
}

// newsLimit normalizes an optional limit like upstream
// _normalize_limit: None -> 10, otherwise at least 1.
func newsLimit(input map[string]any) int {
	limit := intArg(input, "limit", 10)
	if limit < 1 {
		return 1
	}
	return limit
}

// newsDateRange builds the upstream from/to pair for the past N
// days (both inclusive).
func newsDateRange(rt *Runtime, days int) (string, string) {
	now := runtimeNow(rt)
	end := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -days).Format("2006-01-02")
	return start, end
}

// newsArticle normalizes one article payload. Every upstream key is
// present, defaulting to "".
func newsArticle(title, source, published, link, snippet any) map[string]any {
	return map[string]any{
		"title":   asString(title),
		"source":  asString(source),
		"date":    asString(published),
		"url":     asString(link),
		"snippet": asString(snippet),
	}
}

// newsDataArticles projects the NewsData results array.
func newsDataArticles(parsed any) []any {
	results := make([]any, 0)
	for _, raw := range asSlice(asMap(parsed)["results"]) {
		item := asMap(raw)
		results = append(results, newsArticle(
			item["title"], item["source_id"], item["pubDate"], item["link"], item["description"],
		))
	}
	return results
}

// newsDataError maps NewsData non-200 responses onto the upstream
// friendly messages.
func newsDataError(status int, parsed any, body []byte) map[string]any {
	switch status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid NewsData API key"}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "NewsData rate limit exceeded. Try again later."}
	case http.StatusUnprocessableEntity:
		detail := string(body)
		if results, ok := asMap(parsed)["results"].(map[string]any); ok {
			if message, present := results["message"]; present {
				detail = fmt.Sprint(message)
			}
		}
		return map[string]any{"error": "Invalid NewsData parameters: " + detail}
	default:
		return map[string]any{"error": fmt.Sprintf("NewsData request failed: HTTP %d", status)}
	}
}

// finlightError maps Finlight non-200 responses onto the upstream
// friendly messages.
func finlightError(status int, parsed any, body []byte) map[string]any {
	switch status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Finlight API key"}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Finlight rate limit exceeded. Try again later."}
	case http.StatusUnprocessableEntity:
		detail := string(body)
		if message, present := asMap(parsed)["message"]; present {
			detail = fmt.Sprint(message)
		}
		return map[string]any{"error": "Invalid Finlight parameters: " + detail}
	default:
		return map[string]any{"error": fmt.Sprintf("Finlight request failed: HTTP %d", status)}
	}
}

// newsRetry sends one request, retrying 429 with exponential
// backoff exactly like upstream (three retries, then the final response).
func newsRetry(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any) (*httpResponse, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := doHTTP(ctx, rt, method, endpoint, headers, payload)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusTooManyRequests && attempt < maxRetries {
			backoffSleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("news: retry loop exhausted")
}

// newsDataSearch queries NewsData, switching to the archive
// endpoint when any date filter is present.
func newsDataSearch(ctx context.Context, rt *Runtime, args newsArgs, key string) map[string]any {
	path := "/news"
	useArchive := args.FromDate != "" || args.ToDate != ""
	if useArchive {
		path = "/archive"
	}
	query := url.Values{}
	query.Set("apikey", key)
	if args.Query != "" {
		query.Set("q", args.Query)
	}
	if useArchive {
		if args.FromDate != "" {
			query.Set("from_date", args.FromDate)
		}
		if args.ToDate != "" {
			query.Set("to_date", args.ToDate)
		}
	}
	if args.Language != "" {
		query.Set("language", args.Language)
	}
	if args.Category != "" {
		query.Set("category", args.Category)
	}
	if args.Country != "" {
		query.Set("country", args.Country)
	}
	query.Set("size", strconv.Itoa(args.Limit))
	if args.Sources != "" {
		query.Set("sources", args.Sources)
	}
	target := joinQuery(moduleBaseURL(rt, "news_tool")+path, query)
	resp, err := newsRetry(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return newsDataError(resp.Status, resp.Parsed, resp.Body)
	}
	results := newsDataArticles(resp.Parsed)
	return map[string]any{
		"results":  results,
		"total":    len(results),
		"provider": "newsdata",
	}
}

// newsSentimentValue normalizes an upstream sentiment value to a
// float in [-1, 1], or reports that no value is available.
func newsSentimentValue(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case nil:
		return 0, false
	case float64:
		return clampFloat(typed, -1, 1), true
	case int:
		return clampFloat(float64(typed), -1, 1), true
	case int64:
		return clampFloat(float64(typed), -1, 1), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return clampFloat(parsed, -1, 1), true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "positive":
			return 1, true
		case "negative":
			return -1, true
		case "neutral":
			return 0, true
		default:
			return 0, false
		}
	default:
		return 0, false
	}
}

// newsFalsy mirrors Python truthiness for the sentiment fallback
// (`item.get("sentiment") or item.get("sentiment_score")`).
func newsFalsy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case float64:
		return typed == 0
	case int:
		return typed == 0
	case bool:
		return !typed
	default:
		return false
	}
}

// finlightSearch queries the Finlight articles endpoint.
func finlightSearch(ctx context.Context, rt *Runtime, args newsArgs, key string, includeSentiment bool) map[string]any {
	query := args.Query
	if query == "" && args.Category != "" {
		query = args.Category
	}
	body := map[string]any{"page": 1, "pageSize": args.Limit}
	if query != "" {
		body["query"] = query
	}
	if args.FromDate != "" {
		body["from"] = args.FromDate
	}
	if args.ToDate != "" {
		body["to"] = args.ToDate
	}
	if args.Language != "" {
		body["language"] = args.Language
	}
	if args.Sources != "" {
		sources := make([]any, 0)
		for _, part := range strings.Split(args.Sources, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				sources = append(sources, trimmed)
			}
		}
		if len(sources) > 0 {
			body["sources"] = sources
		}
	}
	if args.Country != "" {
		body["countries"] = []any{strings.ToUpper(args.Country)}
	}
	target := moduleBaseURL(rt, "finlight_tool") + "/articles"
	resp, err := newsRetry(ctx, rt, http.MethodPost, target, map[string]string{
		"X-API-KEY": key,
		"Accept":    "application/json",
	}, body)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return finlightError(resp.Status, resp.Parsed, resp.Body)
	}
	payload := asMap(resp.Parsed)
	rawResults := payload["articles"]
	if rawResults == nil {
		rawResults = payload["data"]
	}
	if rawResults == nil {
		rawResults = payload["results"]
	}
	results := make([]any, 0)
	for _, raw := range asSlice(rawResults) {
		item := asMap(raw)
		article := newsArticle(
			item["title"],
			item["source"],
			stringOr(item["publishDate"], item["published_at"]),
			stringOr(item["link"], item["url"]),
			stringOr(item["summary"], item["description"]),
		)
		if includeSentiment {
			rawSentiment := item["sentiment"]
			if newsFalsy(rawSentiment) {
				if alternative, present := item["sentiment_score"]; present {
					rawSentiment = alternative
				}
			}
			if sentiment, ok := newsSentimentValue(rawSentiment); ok {
				article["sentiment"] = sentiment
			}
		}
		results = append(results, article)
	}
	return map[string]any{
		"results":  results,
		"total":    len(results),
		"provider": "finlight",
	}
}

// newsWithFallback tries NewsData first and falls back to Finlight
// only when the primary reports an error, exactly like the upstream helper.
func newsWithFallback(ctx context.Context, rt *Runtime, args newsArgs) map[string]any {
	newsKey, finlightKey := newsKeys(rt)
	var primary map[string]any
	if newsKey != "" {
		primary = newsDataSearch(ctx, rt, args, newsKey)
	} else {
		primary = map[string]any{"error": "NewsData credentials not configured"}
	}
	if _, failed := primary["error"]; !failed {
		return primary
	}
	if finlightKey == "" {
		return primary
	}
	fallback := finlightSearch(ctx, rt, args, finlightKey, false)
	if _, failed := fallback["error"]; !failed {
		return fallback
	}
	return map[string]any{
		"error":     "All providers failed",
		"providers": map[string]any{"primary": primary, "fallback": fallback},
	}
}

// newsSearch searches articles with filters.
func newsSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "Query is required"}, nil
	}
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	result := newsWithFallback(ctx, rt, newsArgs{
		Query:    query,
		FromDate: stringArg(input, "from_date"),
		ToDate:   stringArg(input, "to_date"),
		Language: stringArg(input, "language"),
		Sources:  stringArg(input, "sources"),
		Category: stringArg(input, "category"),
		Country:  stringArg(input, "country"),
		Limit:    newsLimit(input),
	})
	result["query"] = query
	return result, nil
}

// newsHeadlines lists top headlines by category and country.
func newsHeadlines(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	category := stringArg(input, "category")
	if category == "" {
		return map[string]any{"error": "Category is required"}, nil
	}
	country := stringArg(input, "country")
	if country == "" {
		return map[string]any{"error": "Country is required"}, nil
	}
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	result := newsWithFallback(ctx, rt, newsArgs{
		Category: category,
		Country:  country,
		Limit:    newsLimit(input),
	})
	result["category"] = category
	result["country"] = country
	return result, nil
}

// newsByCompany searches news mentioning a company over the past
// N days.
func newsByCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	companyName := stringArg(input, "company_name")
	if companyName == "" {
		return map[string]any{"error": "Company name is required"}, nil
	}
	daysBack := intArg(input, "days_back", 7)
	if daysBack < 0 {
		return map[string]any{"error": "days_back must be 0 or greater"}, nil
	}
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	fromDate, toDate := newsDateRange(rt, daysBack)
	result := newsWithFallback(ctx, rt, newsArgs{
		Query:    `"` + companyName + `"`,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Limit:    newsLimit(input),
	})
	result["company_name"] = companyName
	result["days_back"] = daysBack
	return result, nil
}

// newsSentiment queries Finlight and normalizes each article's
// sentiment score.
func newsSentiment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "Query is required"}, nil
	}
	_, finlightKey := newsKeys(rt)
	if finlightKey == "" {
		return map[string]any{
			"error": "Finlight credentials not configured",
			"help":  "Set FINLIGHT_API_KEY environment variable",
		}, nil
	}
	result := finlightSearch(ctx, rt, newsArgs{
		Query:    query,
		FromDate: stringArg(input, "from_date"),
		ToDate:   stringArg(input, "to_date"),
		Limit:    10,
	}, finlightKey, true)
	result["query"] = query
	return result, nil
}

// newsLatest fetches breaking news, falling back to a broad search
// when the NewsData latest endpoint fails.
func newsLatest(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	language := stringArg(input, "language")
	country := stringArg(input, "country")
	category := stringArg(input, "category")
	limit := newsLimit(input)
	if newsKey != "" {
		query := url.Values{}
		query.Set("apikey", newsKey)
		if language != "" {
			query.Set("language", language)
		}
		if category != "" {
			query.Set("category", category)
		}
		if country != "" {
			query.Set("country", country)
		}
		query.Set("size", strconv.Itoa(limit))
		target := joinQuery(moduleBaseURL(rt, "news_tool")+"/news", query)
		resp, err := newsRetry(ctx, rt, http.MethodGet, target, nil, nil)
		if err == nil && resp.Status == http.StatusOK {
			results := newsDataArticles(resp.Parsed)
			return map[string]any{
				"results":  results,
				"total":    len(results),
				"provider": "newsdata",
			}, nil
		}
	}
	broadQuery := category
	if broadQuery == "" {
		broadQuery = "breaking news"
	}
	return newsWithFallback(ctx, rt, newsArgs{
		Query:    broadQuery,
		Language: language,
		Category: category,
		Country:  country,
		Limit:    limit,
	}), nil
}

// newsBySource lists news from specific source IDs.
func newsBySource(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sources := stringArg(input, "sources")
	if sources == "" {
		return map[string]any{"error": "sources is required (comma-separated source IDs)"}, nil
	}
	daysBack := intArg(input, "days_back", 7)
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	fromDate, toDate := newsDateRange(rt, daysBack)
	query := stringArg(input, "query")
	result := newsWithFallback(ctx, rt, newsArgs{
		Query:    query,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Sources:  sources,
		Limit:    newsLimit(input),
	})
	result["sources"] = sources
	if query != "" {
		result["query"] = query
	}
	return result, nil
}

// newsByTopic discovers articles about a broad topic.
func newsByTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	topic := stringArg(input, "topic")
	if topic == "" {
		return map[string]any{"error": "topic is required"}, nil
	}
	daysBack := intArg(input, "days_back", 3)
	newsKey, finlightKey := newsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return newsNoCredentials(), nil
	}
	fromDate, toDate := newsDateRange(rt, daysBack)
	result := newsWithFallback(ctx, rt, newsArgs{
		Query:    topic,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Country:  stringArg(input, "country"),
		Limit:    newsLimit(input),
	})
	result["topic"] = topic
	result["days_back"] = daysBack
	return result, nil
}
