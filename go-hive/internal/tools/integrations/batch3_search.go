// Batch 3 web search: one web_search tool over two providers. Brave is tried
// first in "auto" mode for backward compatibility; Google Custom Search is the
// fallback. Both requests run on the runtime's injectable http.Client so tests
// can stand in a fake upstream for either provider.

package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	batch3BraveSearchPath  = "/res/v1/web/search"
	batch3GoogleSearchPath = "/customsearch/v1"

	batch3SearchTimeout    = 30 * time.Second
	batch3SearchMaxRetries = 3
	batch3SearchMaxQuery   = 500
)

// batch3WebSearchTool declares web_search, one action with the upstream
// parameter list and defaults.
func batch3WebSearchTool() *Tool {
	return &Tool{
		ID:          "web_search_tool",
		Name:        "web_search",
		DisplayName: "Web Search",
		Summary:     "Search the web with Brave Search or Google Custom Search.",
		Actions: []Action{
			{
				Name:     "web_search",
				Summary:  "Search the web for information. \"auto\" prefers Brave, then falls back to Google Custom Search.",
				Source:   "web_search_tool/web_search_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query":       param("string"),
					"num_results": paramDefault("integer", 10),
					"country":     paramDefault("string", "us"),
					"language":    paramDefault("string", "en"),
					"provider":    paramDefault("string", "auto"),
				},
			},
		},
	}
}

// webSearch dispatches one query to the selected provider.
func webSearch(ctx context.Context, rt *Runtime, input map[string]any, deps batch3Deps) (any, error) {
	query := stringArg(input, "query")
	if query == "" || utf8.RuneCountInString(query) > batch3SearchMaxQuery {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	numResults := intArg(input, "num_results", 10)
	country := strings.TrimSpace(stringArg(input, "country"))
	if country == "" {
		country = "us"
	}
	language := strings.TrimSpace(stringArg(input, "language"))
	if language == "" {
		language = "en"
	}
	provider := strings.ToLower(strings.TrimSpace(stringArg(input, "provider")))
	if provider == "" {
		provider = "auto"
	}

	braveKey := strings.TrimSpace(batch3Env(rt, "BRAVE_SEARCH_API_KEY"))
	googleKey := strings.TrimSpace(batch3Env(rt, "GOOGLE_API_KEY"))
	googleCSE := strings.TrimSpace(batch3Env(rt, "GOOGLE_CSE_ID"))
	braveAvailable := braveKey != ""
	googleAvailable := googleKey != "" && googleCSE != ""

	switch provider {
	case "google":
		if !googleAvailable {
			return map[string]any{
				"error": "Google credentials not configured",
				"help":  "Set GOOGLE_API_KEY and GOOGLE_CSE_ID environment variables",
			}, nil
		}
		return batch3SearchGoogle(ctx, rt, deps, query, numResults, country, language, googleKey, googleCSE)
	case "brave":
		if !braveAvailable {
			return map[string]any{
				"error": "Brave credentials not configured",
				"help":  "Set BRAVE_SEARCH_API_KEY environment variable",
			}, nil
		}
		return batch3SearchBrave(ctx, rt, deps, query, numResults, country, braveKey)
	default: // auto - Brave first for backward compatibility
		if braveAvailable {
			return batch3SearchBrave(ctx, rt, deps, query, numResults, country, braveKey)
		}
		if googleAvailable {
			return batch3SearchGoogle(ctx, rt, deps, query, numResults, country, language, googleKey, googleCSE)
		}
		return map[string]any{
			"error": "No search credentials configured",
			"help":  "Set either GOOGLE_API_KEY+GOOGLE_CSE_ID or BRAVE_SEARCH_API_KEY",
		}, nil
	}
}

// batch3SearchBrave runs one Brave Search API query.
func batch3SearchBrave(ctx context.Context, rt *Runtime, deps batch3Deps, query string, numResults int, country, apiKey string) (any, error) {
	params := url.Values{}
	params.Set("q", query)
	count := numResults
	if count > 20 {
		count = 20
	}
	params.Set("count", strconv.Itoa(count))
	params.Set("country", country)
	base := batch3ModuleBaseURL(rt, "brave_search", "https://api.search.brave.com")
	target := base + batch3BraveSearchPath + "?" + params.Encode()

	data, status, err := batch3SearchGet(ctx, rt, deps, target, map[string]string{
		"X-Subscription-Token": apiKey,
		"Accept":               "application/json",
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return batch3SearchRequestError(err), nil
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Brave API key"}, nil
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Brave rate limit exceeded. Try again later."}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("Brave API request failed: HTTP %d", status)}, nil
	}
	if data == nil {
		return map[string]any{"error": "Search failed: invalid JSON response from Brave"}, nil
	}

	items := []any{}
	if web, ok := data["web"].(map[string]any); ok {
		if results, ok := web["results"].([]any); ok {
			items = results
		}
	}
	results := make([]map[string]any, 0, len(items))
	for _, entry := range items {
		if len(results) >= numResults {
			break
		}
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		results = append(results, map[string]any{
			"title":   batch3Text(item["title"]),
			"url":     batch3Text(item["url"]),
			"snippet": batch3Text(item["description"]),
		})
	}
	return map[string]any{
		"query":    query,
		"results":  results,
		"total":    len(results),
		"provider": "brave",
	}, nil
}

// batch3SearchGoogle runs one Google Custom Search JSON API query.
func batch3SearchGoogle(ctx context.Context, rt *Runtime, deps batch3Deps, query string, numResults int, country, language, apiKey, cseID string) (any, error) {
	params := url.Values{}
	params.Set("key", apiKey)
	params.Set("cx", cseID)
	params.Set("q", query)
	num := numResults
	if num > 10 {
		num = 10
	}
	params.Set("num", strconv.Itoa(num))
	params.Set("lr", "lang_"+language)
	params.Set("gl", country)
	base := batch3ModuleBaseURL(rt, "google_cse", "https://www.googleapis.com")
	target := base + batch3GoogleSearchPath + "?" + params.Encode()

	data, status, err := batch3SearchGet(ctx, rt, deps, target, nil)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return batch3SearchRequestError(err), nil
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Google API key"}, nil
	case http.StatusForbidden:
		return map[string]any{"error": "Google API key not authorized or quota exceeded"}, nil
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Google rate limit exceeded. Try again later."}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("Google API request failed: HTTP %d", status)}, nil
	}
	if data == nil {
		return map[string]any{"error": "Search failed: invalid JSON response from Google"}, nil
	}

	items, _ := data["items"].([]any)
	results := make([]map[string]any, 0, len(items))
	for _, entry := range items {
		if len(results) >= numResults {
			break
		}
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		results = append(results, map[string]any{
			"title":   batch3Text(item["title"]),
			"url":     batch3Text(item["link"]),
			"snippet": batch3Text(item["snippet"]),
		})
	}
	return map[string]any{
		"query":    query,
		"results":  results,
		"total":    len(results),
		"provider": "google",
	}, nil
}

// batch3SearchGet performs one GET with the upstream 429 retry ladder
// (1s, 2s, 4s) and returns the parsed body plus the HTTP status.
func batch3SearchGet(ctx context.Context, rt *Runtime, deps batch3Deps, target string, headers map[string]string) (map[string]any, int, error) {
	client := batch3HTTPClient(rt, batch3SearchTimeout)
	requestCtx, cancel := context.WithTimeout(ctx, batch3SearchTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
		if err != nil {
			return nil, 0, err
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, readErr
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < batch3SearchMaxRetries {
			if err := deps.sleep(requestCtx, time.Duration(1<<uint(attempt))*time.Second); err != nil {
				return nil, resp.StatusCode, err
			}
			continue
		}
		var parsed map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &parsed)
		}
		return parsed, resp.StatusCode, nil
	}
}

// batch3SearchRequestError maps transport errors onto the upstream messages.
func batch3SearchRequestError(err error) map[string]any {
	if batch3IsTimeout(err) {
		return map[string]any{"error": "Search request timed out"}
	}
	return map[string]any{"error": "Network error: " + err.Error()}
}

// batch3Text renders a provider field as text.
func batch3Text(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
