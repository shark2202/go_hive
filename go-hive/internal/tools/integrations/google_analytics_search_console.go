// Google Search Console tool: the google_search_console_tool catalog entry,
// search analytics/sitemap/URL-inspection helpers and the eight GSC actions
// (split from google_analytics.go).

package integrations

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// ------------------------------------------------------- google search console ---

var searchConsoleTool = &Tool{
	ID:          "google_search_console_tool",
	Name:        "google_search_console",
	DisplayName: "Google Search Console",
	Summary:     "Search analytics, sitemaps and URL inspection for Google Search Console (8 actions).",
	Actions: []Action{
		{
			Name:     "gsc_search_analytics",
			Summary:  "Query search analytics data from Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"dimensions":  paramDefault("string", "query"),
				"row_limit":   paramDefault("integer", 100),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:    "gsc_list_sites",
			Summary: "List all sites in the Google Search Console account.",
			Source:  "google_search_console_tool/google_search_console_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "gsc_list_sitemaps",
			Summary:  "List sitemaps for a site in Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url"},
			Params: map[string]any{
				"site_url": param("string"),
			},
		},
		{
			Name:     "gsc_inspect_url",
			Summary:  "Inspect a URL's indexing status in Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "inspection_url"},
			Params: map[string]any{
				"site_url":       param("string"),
				"inspection_url": param("string"),
			},
		},
		{
			Name:     "gsc_submit_sitemap",
			Summary:  "Submit a sitemap to Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "sitemap_url"},
			Params: map[string]any{
				"site_url":    param("string"),
				"sitemap_url": param("string"),
			},
		},
		{
			Name:     "gsc_top_queries",
			Summary:  "Get the top search queries for a site sorted by clicks.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"row_limit":   paramDefault("integer", 25),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:     "gsc_top_pages",
			Summary:  "Get the top-performing pages for a site sorted by clicks.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"row_limit":   paramDefault("integer", 25),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:     "gsc_delete_sitemap",
			Summary:  "Delete a sitemap from Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "sitemap_url"},
			Params: map[string]any{
				"site_url":    param("string"),
				"sitemap_url": param("string"),
			},
		},
	},
}

func searchConsoleImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gsc_search_analytics": gscSearchAnalytics,
		"gsc_list_sites":       gscListSites,
		"gsc_list_sitemaps":    gscListSitemaps,
		"gsc_inspect_url":      gscInspectURL,
		"gsc_submit_sitemap":   gscSubmitSitemap,
		"gsc_top_queries":      gscTopQueries,
		"gsc_top_pages":        gscTopPages,
		"gsc_delete_sitemap":   gscDeleteSitemap,
	}
}

// gscAuthError mirrors upstream _auth_error.
func gscAuthError() map[string]any {
	return map[string]any{
		"error": "GOOGLE_SEARCH_CONSOLE_TOKEN not set",
		"help":  "Generate an OAuth2 access token with webmasters.readonly scope",
	}
}

// gscToken resolves the bearer token for Search Console, minting one
// from GOOGLE_APPLICATION_CREDENTIALS when no direct token is configured.
func gscToken(ctx context.Context, rt *Runtime) (string, map[string]any) {
	token, failure := googleToken(ctx, rt, "google_search_console_tool", gscReadScope)
	if token != "" {
		return token, nil
	}
	if failure != nil {
		return "", failure
	}
	return "", gscAuthError()
}

// gscCall performs one Search Console request. The accepted success
// codes differ per verb (PUT/DELETE answer 200 or 204).
func gscCall(ctx context.Context, rt *Runtime, token, method, endpoint string, payload any) (map[string]any, map[string]any) {
	headers := marketingBearer(token)
	resp, err := marketingDoJSON(ctx, rt, method, endpoint, headers, payload, 30*time.Second)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request to Google Search Console timed out"}
		}
		return nil, map[string]any{"error": "Request failed: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Check your GOOGLE_SEARCH_CONSOLE_TOKEN."}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Forbidden: " + truncateText(string(resp.Body), 300)}
	}
	success := resp.Status == http.StatusOK || resp.Status == http.StatusCreated
	if method == http.MethodPut || method == http.MethodDelete {
		success = resp.Status == http.StatusOK || resp.Status == http.StatusNoContent
	}
	if !success {
		return nil, map[string]any{
			"error": "Google API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{}, nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Google API error: invalid JSON response"}
	}
	return data, nil
}

// gscAnalyticsBody builds the searchAnalytics/query payload.
func gscAnalyticsBody(input map[string]any, dimensions []any) map[string]any {
	rowLimit := clampInt(intArg(input, "row_limit", 100), 1, 25000)
	searchType := stringArg(input, "search_type")
	if searchType == "" {
		searchType = "web"
	}
	return map[string]any{
		"startDate":  stringArg(input, "start_date"),
		"endDate":    stringArg(input, "end_date"),
		"dimensions": dimensions,
		"rowLimit":   rowLimit,
		"type":       searchType,
	}
}

// gscRows maps analytics rows. A non-empty keyName flattens the
// first key into that field (the top-query/top-page wrappers), otherwise the
// full keys array is kept.
func gscRows(rows []any, keyName string) []any {
	out := make([]any, 0, len(rows))
	for _, raw := range rows {
		row := asMap(raw)
		keys := asSlice(row["keys"])
		entry := map[string]any{
			"clicks":      marketingIntOr(row, "clicks", 0),
			"impressions": marketingIntOr(row, "impressions", 0),
			"ctr":         marketingRound(marketingFloatOr(row, "ctr", 0), 4),
			"position":    marketingRound(marketingFloatOr(row, "position", 0), 1),
		}
		if keyName != "" {
			first := ""
			if len(keys) > 0 {
				first = asString(keys[0])
			}
			entry[keyName] = first
		} else {
			entry["keys"] = keys
		}
		out = append(out, entry)
	}
	return out
}

// gscSortByClicks sorts rows by clicks descending (stable).
func gscSortByClicks(rows []any) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftRow := asMap(rows[left])
		rightRow := asMap(rows[right])
		return marketingIntOr(leftRow, "clicks", 0) > marketingIntOr(rightRow, "clicks", 0)
	})
}

// gscSitePath escapes one site URL into a path segment.
func gscSitePath(siteURL string) string {
	return "sites/" + marketingStrictEscape(siteURL)
}

func gscSearchAnalytics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	startDate, endDate := stringArg(input, "start_date"), stringArg(input, "end_date")
	if siteURL == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "site_url, start_date, and end_date are required"}, nil
	}
	dimensions := stringSliceArg(input, "dimensions")
	if len(dimensions) == 0 {
		dimensions = []string{"query"}
	}
	dimensionList := make([]any, 0, len(dimensions))
	for _, dimension := range dimensions {
		dimensionList = append(dimensionList, dimension)
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := gscCall(ctx, rt, token, http.MethodPost, endpoint, gscAnalyticsBody(input, dimensionList))
	if failure != nil {
		return failure, nil
	}
	rows := gscRows(asSlice(data["rows"]), "")
	return map[string]any{"site_url": siteURL, "rows": rows, "count": len(rows)}, nil
}

func gscListSites(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	data, failure := gscCall(ctx, rt, token, http.MethodGet, base+"/sites", nil)
	if failure != nil {
		return failure, nil
	}
	sites := make([]any, 0)
	for _, raw := range asSlice(data["siteEntry"]) {
		entry := asMap(raw)
		sites = append(sites, map[string]any{
			"site_url":         stringOr(entry, "siteUrl", ""),
			"permission_level": stringOr(entry, "permissionLevel", ""),
		})
	}
	return map[string]any{"sites": sites}, nil
}

func gscListSitemaps(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	if siteURL == "" {
		return map[string]any{"error": "site_url is required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps"
	data, failure := gscCall(ctx, rt, token, http.MethodGet, endpoint, nil)
	if failure != nil {
		return failure, nil
	}
	sitemaps := make([]any, 0)
	for _, raw := range asSlice(data["sitemap"]) {
		entry := asMap(raw)
		sitemaps = append(sitemaps, map[string]any{
			"path":           stringOr(entry, "path", ""),
			"last_submitted": stringOr(entry, "lastSubmitted", ""),
			"is_pending":     asBool(entry["isPending"]),
			"is_index":       asBool(entry["isSitemapsIndex"]),
			"warnings":       marketingIntOr(entry, "warnings", 0),
			"errors":         marketingIntOr(entry, "errors", 0),
		})
	}
	return map[string]any{"site_url": siteURL, "sitemaps": sitemaps}, nil
}

func gscInspectURL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, inspectionURL := stringArg(input, "site_url"), stringArg(input, "inspection_url")
	if siteURL == "" || inspectionURL == "" {
		return map[string]any{"error": "site_url and inspection_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_inspection_tool", gscInspectionBase)
	body := map[string]any{"inspectionUrl": inspectionURL, "siteUrl": siteURL}
	data, failure := gscCall(ctx, rt, token, http.MethodPost, base+"/urlInspection/index:inspect", body)
	if failure != nil {
		return failure, nil
	}
	result := asMap(data["inspectionResult"])
	indexStatus := asMap(result["indexStatusResult"])
	mobile := asMap(result["mobileUsabilityResult"])
	return map[string]any{
		"inspection_url":   inspectionURL,
		"verdict":          stringOr(indexStatus, "verdict", ""),
		"coverage_state":   stringOr(indexStatus, "coverageState", ""),
		"indexing_state":   stringOr(indexStatus, "indexingState", ""),
		"last_crawl_time":  stringOr(indexStatus, "lastCrawlTime", ""),
		"crawled_as":       stringOr(indexStatus, "crawledAs", ""),
		"page_fetch_state": stringOr(indexStatus, "pageFetchState", ""),
		"robots_txt_state": stringOr(indexStatus, "robotsTxtState", ""),
		"mobile_verdict":   stringOr(mobile, "verdict", ""),
	}, nil
}

func gscSubmitSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps/" + marketingStrictEscape(sitemapURL)
	if _, failure := gscCall(ctx, rt, token, http.MethodPut, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "submitted"}, nil
}

// gscTopDimension runs the shared top-query/top-page wrapper.
func gscTopDimension(ctx context.Context, rt *Runtime, input map[string]any, dimension, key string, defaultLimit int) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	startDate, endDate := stringArg(input, "start_date"), stringArg(input, "end_date")
	if siteURL == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "site_url, start_date, and end_date are required"}, nil
	}
	if _, present := input["row_limit"]; !present {
		input = googleCloneWith(input, "row_limit", defaultLimit)
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := gscCall(ctx, rt, token, http.MethodPost, endpoint,
		gscAnalyticsBody(input, []any{dimension}))
	if failure != nil {
		return failure, nil
	}
	rows := gscRows(asSlice(data["rows"]), dimension)
	gscSortByClicks(rows)
	return map[string]any{"site_url": siteURL, key: rows, "count": len(rows)}, nil
}

// googleCloneWith returns a shallow copy of input with one extra key.
func googleCloneWith(input map[string]any, name string, value any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, existing := range input {
		out[key] = existing
	}
	out[name] = value
	return out
}

func gscTopQueries(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gscTopDimension(ctx, rt, input, "query", "queries", 25)
}

func gscTopPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gscTopDimension(ctx, rt, input, "page", "pages", 25)
}

func gscDeleteSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps/" + marketingStrictEscape(sitemapURL)
	if _, failure := gscCall(ctx, rt, token, http.MethodDelete, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "deleted"}, nil
}
