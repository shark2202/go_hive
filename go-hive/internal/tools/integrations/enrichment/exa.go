package enrichment

// Exa: catalog, seven neural-search/content actions and the 429-retry
// call helper.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

var exaTool = &Tool{
	ID:          "exa_search_tool",
	Name:        "exa_search",
	DisplayName: "Exa Search",
	Summary:     "Exa AI-powered web search, similar-page discovery and content extraction (7 actions).",
	Actions: []Action{
		{
			Name:     "exa_search",
			Summary:  "Search the web using Exa's AI-powered search engine.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":                integrationkit.Param("string"),
				"num_results":          integrationkit.ParamDefault("integer", 10),
				"search_type":          integrationkit.ParamEnumDefault("string", "auto", []string{"auto", "neural", "keyword"}),
				"include_domains":      integrationkit.ParamItems("array", "string"),
				"exclude_domains":      integrationkit.ParamItems("array", "string"),
				"start_published_date": integrationkit.Param("string"),
				"end_published_date":   integrationkit.Param("string"),
				"include_text":         integrationkit.ParamDefault("boolean", true),
				"include_highlights":   integrationkit.ParamDefault("boolean", false),
				"category":             integrationkit.Param("string"),
			},
		},
		{
			Name:     "exa_find_similar",
			Summary:  "Find web pages similar to a given URL.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"url"},
			Params: map[string]any{
				"url":             integrationkit.Param("string"),
				"num_results":     integrationkit.ParamDefault("integer", 10),
				"include_domains": integrationkit.ParamItems("array", "string"),
				"exclude_domains": integrationkit.ParamItems("array", "string"),
				"include_text":    integrationkit.ParamDefault("boolean", true),
			},
		},
		{
			Name:     "exa_get_contents",
			Summary:  "Extract content from one or more URLs using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"urls"},
			Params: map[string]any{
				"urls":               integrationkit.ParamItems("array", "string"),
				"include_text":       integrationkit.ParamDefault("boolean", true),
				"include_highlights": integrationkit.ParamDefault("boolean", false),
			},
		},
		{
			Name:     "exa_answer",
			Summary:  "Get an answer to a question with citations from web sources.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":             integrationkit.Param("string"),
				"include_citations": integrationkit.ParamDefault("boolean", true),
			},
		},
		{
			Name:     "exa_search_news",
			Summary:  "Search recent news articles using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        integrationkit.Param("string"),
				"num_results":  integrationkit.ParamDefault("integer", 10),
				"days_back":    integrationkit.ParamDefault("integer", 7),
				"include_text": integrationkit.ParamDefault("boolean", true),
			},
		},
		{
			Name:     "exa_search_papers",
			Summary:  "Search for research papers and academic content using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        integrationkit.Param("string"),
				"num_results":  integrationkit.ParamDefault("integer", 10),
				"year_start":   integrationkit.Param("integer"),
				"include_text": integrationkit.ParamDefault("boolean", false),
			},
		},
		{
			Name:     "exa_search_companies",
			Summary:  "Search for companies and startups using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        integrationkit.Param("string"),
				"num_results":  integrationkit.ParamDefault("integer", 10),
				"include_text": integrationkit.ParamDefault("boolean", true),
			},
		},
	},
}

func exaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"exa_search":           exaSearch,
		"exa_find_similar":     exaFindSimilar,
		"exa_get_contents":     exaGetContents,
		"exa_answer":           exaAnswer,
		"exa_search_news":      exaSearchNews,
		"exa_search_papers":    exaSearchPapers,
		"exa_search_companies": exaSearchCompanies,
	}
}

// exaAuthError mirrors the upstream missing-key payload.
func exaAuthError() map[string]any {
	return map[string]any{
		"error": "Exa credentials not configured",
		"help":  "Set EXA_API_KEY environment variable",
	}
}

// exaBaseURL resolves the API root.
func exaBaseURL(rt *Runtime) string {
	return baseURL(rt, "exa_search_tool", "https://api.exa.ai")
}

// exaCall performs one Exa call with the upstream 429 backoff.
func exaCall(
	ctx context.Context,
	rt *Runtime,
	endpoint string,
	payload map[string]any,
	timeoutLabel string,
) (map[string]any, bool) {
	token := moduleToken(rt, "exa_search_tool")
	if token == "" {
		return exaAuthError(), false
	}
	target := exaBaseURL(rt) + endpoint
	headers := map[string]string{
		"x-api-key":    token,
		"Content-Type": "application/json",
	}
	for attempt := 0; attempt <= 3; attempt++ {
		reply, err := doJSON(ctx, rt, http.MethodPost, target, headers, payload)
		if err != nil {
			if isTimeout(ctx, err) {
				return map[string]any{"error": timeoutLabel}, false
			}
			return map[string]any{"error": "Network error: " + err.Error()}, false
		}
		if reply.Status == http.StatusTooManyRequests && attempt < 3 {
			RetrySleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		switch reply.Status {
		case http.StatusUnauthorized:
			return map[string]any{"error": "Invalid Exa API key"}, false
		case http.StatusTooManyRequests:
			return map[string]any{"error": "Exa rate limit exceeded. Try again later."}, false
		}
		if reply.Status != http.StatusOK {
			return map[string]any{
				"error": fmt.Sprintf("Exa API request failed: HTTP %d", reply.Status),
			}, false
		}
		value, ok := decodeObject(reply.Body)
		if !ok {
			return map[string]any{"error": "Exa API request failed: invalid JSON response"}, false
		}
		return value, true
	}
	return map[string]any{"error": "Exa rate limit exceeded. Try again later."}, false
}

// exaResult flattens one Exa result with optional text/highlights.
func exaResult(item map[string]any, includeText, includeHighlights bool) map[string]any {
	result := map[string]any{
		"title":          asString(item["title"]),
		"url":            asString(item["url"]),
		"published_date": asString(item["publishedDate"]),
		"author":         asString(item["author"]),
	}
	if includeText {
		if text, present := item["text"]; present {
			result["text"] = text
		}
	}
	if includeHighlights {
		if highlights, present := item["highlights"]; present {
			result["highlights"] = highlights
		}
	}
	return result
}

// exaResults flattens a result page with the standard envelope.
func exaResults(data map[string]any, includeText, includeHighlights bool) ([]map[string]any, int) {
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		results = append(results, exaResult(asMap(raw), includeText, includeHighlights))
	}
	return results, len(results)
}

func exaSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	numResults := integrationkit.ClampInt(integrationkit.IntArg(input, "num_results", 10), 1, 20)
	includeText := integrationkit.BoolArg(input, "include_text", true)
	includeHighlights := integrationkit.BoolArg(input, "include_highlights", false)
	payload := map[string]any{
		"query":      query,
		"numResults": numResults,
		"contents":   map[string]any{},
	}
	if searchType := integrationkit.StringArg(input, "search_type"); searchType != "" && searchType != "auto" {
		payload["type"] = searchType
	}
	if domains := integrationkit.StringSliceArg(input, "include_domains"); len(domains) > 0 {
		payload["includeDomains"] = interfaceSlice(domains)
	}
	if domains := integrationkit.StringSliceArg(input, "exclude_domains"); len(domains) > 0 {
		payload["excludeDomains"] = interfaceSlice(domains)
	}
	if date := integrationkit.StringArg(input, "start_published_date"); date != "" {
		payload["startPublishedDate"] = date
	}
	if date := integrationkit.StringArg(input, "end_published_date"); date != "" {
		payload["endPublishedDate"] = date
	}
	if category := integrationkit.StringArg(input, "category"); category != "" {
		payload["category"] = category
	}
	contents := asMap(payload["contents"])
	if includeText {
		contents["text"] = true
	}
	if includeHighlights {
		contents["highlights"] = true
	}
	data, ok := exaCall(ctx, rt, "/search", payload, "Exa search request timed out")
	if !ok {
		return data, nil
	}
	results, total := exaResults(data, includeText, includeHighlights)
	return map[string]any{
		"query":    query,
		"results":  results,
		"total":    total,
		"provider": "exa",
	}, nil
}

func exaFindSimilar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sourceURL := integrationkit.StringArg(input, "url")
	if sourceURL == "" {
		return map[string]any{"error": "URL is required"}, nil
	}
	includeText := integrationkit.BoolArg(input, "include_text", true)
	payload := map[string]any{
		"url":        sourceURL,
		"numResults": integrationkit.ClampInt(integrationkit.IntArg(input, "num_results", 10), 1, 20),
		"contents":   map[string]any{},
	}
	if domains := integrationkit.StringSliceArg(input, "include_domains"); len(domains) > 0 {
		payload["includeDomains"] = interfaceSlice(domains)
	}
	if domains := integrationkit.StringSliceArg(input, "exclude_domains"); len(domains) > 0 {
		payload["excludeDomains"] = interfaceSlice(domains)
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := exaCall(ctx, rt, "/findSimilar", payload, "Exa find similar request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"title":          asString(item["title"]),
			"url":            asString(item["url"]),
			"published_date": asString(item["publishedDate"]),
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		results = append(results, result)
	}
	return map[string]any{
		"source_url": sourceURL,
		"results":    results,
		"total":      len(results),
		"provider":   "exa",
	}, nil
}

func exaGetContents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	urls := integrationkit.StringSliceArg(input, "urls")
	if len(urls) == 0 {
		return map[string]any{"error": "At least one URL is required"}, nil
	}
	if len(urls) > 10 {
		return map[string]any{"error": "Maximum 10 URLs per request"}, nil
	}
	includeText := integrationkit.BoolArg(input, "include_text", true)
	includeHighlights := integrationkit.BoolArg(input, "include_highlights", false)
	payload := map[string]any{
		"ids":      interfaceSlice(urls),
		"contents": map[string]any{},
	}
	contents := asMap(payload["contents"])
	if includeText {
		contents["text"] = true
	}
	if includeHighlights {
		contents["highlights"] = true
	}
	data, ok := exaCall(ctx, rt, "/contents", payload, "Exa content extraction request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"url":   asString(item["url"]),
			"title": asString(item["title"]),
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		if includeHighlights {
			if highlights, present := item["highlights"]; present {
				result["highlights"] = highlights
			}
		}
		results = append(results, result)
	}
	return map[string]any{"results": results, "total": len(results), "provider": "exa"}, nil
}

func exaAnswer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	data, ok := exaCall(ctx, rt, "/answer", map[string]any{"query": query}, "Exa answer request timed out")
	if !ok {
		return data, nil
	}
	result := map[string]any{
		"query":    query,
		"answer":   asString(data["answer"]),
		"provider": "exa",
	}
	if integrationkit.BoolArg(input, "include_citations", true) {
		citations := make([]map[string]any, 0)
		for _, raw := range asSlice(data["citations"]) {
			source := asMap(raw)
			citations = append(citations, map[string]any{
				"title":          asString(source["title"]),
				"url":            asString(source["url"]),
				"published_date": asString(source["publishedDate"]),
			})
		}
		result["citations"] = citations
	}
	return result, nil
}

func exaSearchNews(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	daysBack := integrationkit.IntArg(input, "days_back", 7)
	now := time.Now
	if rt != nil && rt.Now != nil {
		now = rt.Now
	}
	startDate := now().UTC().AddDate(0, 0, -daysBack).Format("2006-01-02") + "T00:00:00.000Z"
	includeText := integrationkit.BoolArg(input, "include_text", true)
	payload := map[string]any{
		"query":              query,
		"numResults":         integrationkit.ClampInt(integrationkit.IntArg(input, "num_results", 10), 1, 20),
		"category":           "news",
		"startPublishedDate": startDate,
		"contents":           map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := exaCall(ctx, rt, "/search", payload, "Exa news search timed out")
	if !ok {
		return data, nil
	}
	results, total := exaResults(data, includeText, true)
	return map[string]any{
		"query":     query,
		"days_back": daysBack,
		"results":   results,
		"total":     total,
		"provider":  "exa",
	}, nil
}

func exaSearchPapers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	includeText := integrationkit.BoolArg(input, "include_text", false)
	payload := map[string]any{
		"query":      query,
		"numResults": integrationkit.ClampInt(integrationkit.IntArg(input, "num_results", 10), 1, 20),
		"category":   "research paper",
		"contents":   map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	if year, present := integrationkit.OptionalInt(input, "year_start"); present && year != 0 {
		payload["startPublishedDate"] = fmt.Sprintf("%d-01-01T00:00:00.000Z", year)
	}
	data, ok := exaCall(ctx, rt, "/search", payload, "Exa paper search timed out")
	if !ok {
		return data, nil
	}
	results, total := exaResults(data, includeText, true)
	return map[string]any{"query": query, "results": results, "total": total, "provider": "exa"}, nil
}

func exaSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	includeText := integrationkit.BoolArg(input, "include_text", true)
	payload := map[string]any{
		"query":      query,
		"numResults": integrationkit.ClampInt(integrationkit.IntArg(input, "num_results", 10), 1, 20),
		"category":   "company",
		"contents":   map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := exaCall(ctx, rt, "/search", payload, "Exa company search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"title":          asString(item["title"]),
			"url":            asString(item["url"]),
			"published_date": asString(item["publishedDate"]),
		}
		if highlights, present := item["highlights"]; present {
			result["highlights"] = highlights
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		results = append(results, result)
	}
	return map[string]any{"query": query, "results": results, "total": len(results), "provider": "exa"}, nil
}
