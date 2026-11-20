package integrations

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The Python module uses the duckduckgo_search library. That library talks to
// DuckDuckGo's public endpoints: the HTML endpoint for web results and the
// vqd-guarded JSON endpoints for news and images. This port speaks the same
// wire protocol so a fake upstream can exercise every action.

const duckduckgoUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// duckduckgoRegion maps the upstream default and any explicit code onto the
// "kl" parameter.
func duckduckgoRegion(input map[string]any) string {
	region := strings.TrimSpace(stringArg(input, "region"))
	if region == "" {
		return "us-en"
	}
	return region
}

func duckduckgoSafeSearch(input map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(stringArg(input, "safesearch"))) {
	case "on", "strict":
		return "1"
	case "off":
		return "-2"
	default:
		return "-1"
	}
}

func duckduckgoResultLimit(input map[string]any) int {
	limit := intArg(input, "max_results", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	return limit
}

// parseHTMLAttributes extracts attributes from an opening tag body. It accepts
// both quoted and bare values, which is enough for the anchors DDG emits.
func parseHTMLAttributes(tag string) map[string]string {
	attrs := map[string]string{}
	index := 0
	for index < len(tag) {
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\n' || tag[index] == '\r' || tag[index] == '/') {
			index++
		}
		start := index
		for index < len(tag) && tag[index] != '=' && tag[index] != ' ' && tag[index] != '\t' && tag[index] != '\n' && tag[index] != '\r' {
			index++
		}
		name := strings.ToLower(strings.TrimSpace(tag[start:index]))
		if name == "" {
			break
		}
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t') {
			index++
		}
		if index >= len(tag) || tag[index] != '=' {
			attrs[name] = ""
			continue
		}
		index++
		for index < len(tag) && (tag[index] == ' ' || tag[index] == '\t') {
			index++
		}
		if index < len(tag) && (tag[index] == '"' || tag[index] == '\'') {
			quote := tag[index]
			index++
			valueStart := index
			for index < len(tag) && tag[index] != quote {
				index++
			}
			attrs[name] = htmlUnescape(tag[valueStart:index])
			if index < len(tag) {
				index++
			}
			continue
		}
		valueStart := index
		for index < len(tag) && tag[index] != ' ' && tag[index] != '\t' && tag[index] != '\n' && tag[index] != '\r' {
			index++
		}
		attrs[name] = htmlUnescape(tag[valueStart:index])
	}
	return attrs
}

type htmlAnchor struct {
	attrs map[string]string
	inner string
}

// scanAnchors returns every <a> element's attributes and inner HTML.
func scanAnchors(body string) []htmlAnchor {
	var anchors []htmlAnchor
	index := 0
	lower := strings.ToLower(body)
	for {
		start := strings.Index(lower[index:], "<a ")
		if start < 0 {
			break
		}
		start += index
		tagEnd := strings.Index(body[start:], ">")
		if tagEnd < 0 {
			break
		}
		tagEnd += start
		innerStart := tagEnd + 1
		closeAt := strings.Index(lower[innerStart:], "</a>")
		if closeAt < 0 {
			break
		}
		closeAt += innerStart
		anchors = append(anchors, htmlAnchor{
			attrs: parseHTMLAttributes(body[start+2 : tagEnd]),
			inner: body[innerStart:closeAt],
		})
		index = closeAt + len("</a>")
	}
	return anchors
}

// duckduckgoDecodeHref unwraps DDG's /l/?uddg= redirector.
func duckduckgoDecodeHref(href string) string {
	raw := strings.TrimSpace(htmlUnescape(href))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return href
	}
	if target := parsed.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}

// duckduckgoParseWebResults reads the result__a / result__snippet anchors.
func duckduckgoParseWebResults(body string, limit int) []map[string]any {
	results := make([]map[string]any, 0, limit)
	current := map[string]any{}
	for _, anchor := range scanAnchors(body) {
		class := anchor.attrs["class"]
		switch {
		case strings.Contains(class, "result__a"):
			if current != nil && current["title"] != nil {
				results = append(results, current)
				if len(results) >= limit {
					return results
				}
			}
			current = map[string]any{
				"title":   cleanHTMLText(anchor.inner),
				"url":     duckduckgoDecodeHref(anchor.attrs["href"]),
				"snippet": "",
			}
		case strings.Contains(class, "result__snippet"):
			if current == nil {
				current = map[string]any{"title": "", "url": "", "snippet": ""}
			}
			if snippet := cleanHTMLText(anchor.inner); snippet != "" {
				current["snippet"] = snippet
			}
		}
	}
	if current != nil && current["title"] != nil && current["title"] != "" {
		results = append(results, current)
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// duckduckgoSearch mirrors duckduckgo_search: HTML endpoint, form POST.
func duckduckgoSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(input, "query"))
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", duckduckgoRegion(input))
	form.Set("kp", duckduckgoSafeSearch(input))
	form.Set("l", "wt-wt")
	if timelimit := strings.TrimSpace(stringArg(input, "timelimit")); timelimit != "" {
		form.Set("df", timelimit)
	}
	target := batch2BaseURL(rt, "duckduckgo_tool", nil) + "/html/"
	status, body, err := batch2PostForm(ctx, rt, target, form, map[string]string{
		"User-Agent": duckduckgoUserAgent,
		"Accept":     "text/html",
	})
	if err != nil {
		return map[string]any{"error": "DuckDuckGo search failed: " + err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		return map[string]any{"error": fmt.Sprintf("DuckDuckGo search failed: HTTP %d", status)}, nil
	}
	items := duckduckgoParseWebResults(string(body), duckduckgoResultLimit(input))
	return map[string]any{"query": query, "results": items, "count": len(items)}, nil
}

var duckduckgoVQDPattern = regexp.MustCompile(`vqd\s*=\s*["']?([0-9-]+)`)

// duckduckgoVQD fetches the anti-bot token required by news.js/i.js.
func duckduckgoVQD(ctx context.Context, rt *Runtime, query string) (string, error) {
	target := batch2BaseURL(rt, "duckduckgo_tool", nil) + "/?q=" + url.QueryEscape(query)
	status, body, err := batch2Get(ctx, rt, target, map[string]string{"User-Agent": duckduckgoUserAgent})
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("HTTP %d", status)
	}
	if match := duckduckgoVQDPattern.FindSubmatch(body); match != nil {
		return string(match[1]), nil
	}
	return "", nil
}

// duckduckgoJSONResults fetches one of the vqd-guarded JSON endpoints.
func duckduckgoJSONResults(ctx context.Context, rt *Runtime, endpoint string, params url.Values) ([]map[string]any, map[string]any) {
	target := batch2BaseURL(rt, "duckduckgo_tool", nil) + endpoint
	parsed, problem := batch2GetJSON(ctx, rt, target+"?"+params.Encode(), map[string]string{
		"User-Agent": duckduckgoUserAgent,
		"Accept":     "application/json",
	})
	if problem != nil {
		return nil, problem
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, map[string]any{"error": "Unexpected DuckDuckGo response shape"}
	}
	raw, ok := object["results"].([]any)
	if !ok {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out, nil
}

func duckduckgoNews(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(input, "query"))
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	vqd, err := duckduckgoVQD(ctx, rt, query)
	if err != nil {
		return map[string]any{"error": "DuckDuckGo news search failed: " + err.Error()}, nil
	}
	params := url.Values{}
	params.Set("l", duckduckgoRegion(input))
	params.Set("o", "json")
	params.Set("q", query)
	params.Set("vqd", vqd)
	if timelimit := strings.TrimSpace(stringArg(input, "timelimit")); timelimit != "" {
		params.Set("df", timelimit)
	}
	entries, problem := duckduckgoJSONResults(ctx, rt, "/news.js", params)
	if problem != nil {
		return map[string]any{"error": "DuckDuckGo news search failed: " + stringArg(problem, "error")}, nil
	}
	limit := duckduckgoResultLimit(input)
	items := make([]map[string]any, 0, limit)
	for _, entry := range entries {
		if len(items) >= limit {
			break
		}
		items = append(items, map[string]any{
			"title":   stringArg(entry, "title"),
			"url":     stringArg(entry, "url"),
			"source":  stringArg(entry, "source"),
			"date":    stringArg(entry, "date"),
			"snippet": stringArg(entry, "body"),
		})
	}
	return map[string]any{"query": query, "results": items, "count": len(items)}, nil
}

func duckduckgoImages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(input, "query"))
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	vqd, err := duckduckgoVQD(ctx, rt, query)
	if err != nil {
		return map[string]any{"error": "DuckDuckGo image search failed: " + err.Error()}, nil
	}
	params := url.Values{}
	params.Set("l", duckduckgoRegion(input))
	params.Set("o", "json")
	params.Set("q", query)
	params.Set("vqd", vqd)
	params.Set("kp", duckduckgoSafeSearch(input))
	if size := strings.TrimSpace(stringArg(input, "size")); size != "" {
		params.Set("f", ",,,"+size)
	}
	entries, problem := duckduckgoJSONResults(ctx, rt, "/i.js", params)
	if problem != nil {
		return map[string]any{"error": "DuckDuckGo image search failed: " + stringArg(problem, "error")}, nil
	}
	limit := duckduckgoResultLimit(input)
	items := make([]map[string]any, 0, limit)
	for _, entry := range entries {
		if len(items) >= limit {
			break
		}
		width, _ := batch2Number(entry["width"])
		height, _ := batch2Number(entry["height"])
		items = append(items, map[string]any{
			"title":         stringArg(entry, "title"),
			"image_url":     stringArg(entry, "image"),
			"thumbnail_url": stringArg(entry, "thumbnail"),
			"source":        stringArg(entry, "url"),
			"width":         int(width),
			"height":        int(height),
		})
	}
	return map[string]any{"query": query, "results": items, "count": len(items)}, nil
}
