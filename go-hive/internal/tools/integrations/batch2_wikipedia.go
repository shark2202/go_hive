package integrations

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// wikipediaUserAgent mirrors the upstream client identification.
const wikipediaUserAgent = "AdenAgentFramework/1.0 (https://adenhq.com)"

func wikipediaSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(input, "query"))
	if query == "" {
		return map[string]any{"error": "Query cannot be empty"}, nil
	}
	lang := strings.TrimSpace(stringArg(input, "lang"))
	if lang == "" {
		lang = "en"
	}
	numResults := intArg(input, "num_results", 3)
	if numResults < 1 {
		numResults = 1
	}
	if numResults > 10 {
		numResults = 10
	}
	base := batch2BaseURL(rt, "wikipedia_tool", map[string]string{"lang": lang})
	if base == "" {
		base = "https://" + lang + ".wikipedia.org"
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(numResults))
	target := base + "/w/rest.php/v1/search/page?" + params.Encode()

	status, body, err := batch2Get(ctx, rt, target, map[string]string{"User-Agent": wikipediaUserAgent})
	if err != nil {
		if ctx.Err() != nil {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	if status != 200 {
		return map[string]any{"error": fmt.Sprintf("Wikipedia API error: %d", status), "query": query}, nil
	}
	parsed, problem := batch2DecodeJSONObject(body)
	if problem != nil {
		return map[string]any{"error": "Search failed: " + stringArg(problem, "error")}, nil
	}
	pages, _ := parsed["pages"].([]any)
	results := make([]map[string]any, 0, len(pages))
	for _, raw := range pages {
		page, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		title := stringArg(page, "title")
		key := stringArg(page, "key")
		description := stringArg(page, "description")
		if description == "" {
			description = "No description available."
		}
		results = append(results, map[string]any{
			"title":       title,
			"url":         base + "/wiki/" + key,
			"description": description,
			"snippet":     cleanHTMLText(stringArg(page, "excerpt")),
		})
	}
	return map[string]any{"query": query, "lang": lang, "count": len(results), "results": results}, nil
}
