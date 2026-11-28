package scrape

// Action implementation: pre-flight fetch, SSRF/robots checks, browser
// rendering hand-off, extraction and artifact persistence.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------- action ----

func webScrapeAction(ctx context.Context, rt *Runtime, input map[string]any, deps Deps) (any, error) {
	renderer := deps.Renderer
	if renderer == nil {
		renderer = DefaultDeps().Renderer
	}
	resolver := deps.Resolver
	rawURL := strings.TrimSpace(stringArg(input, "url"))
	if rawURL == "" {
		return map[string]any{"error": "url is required"}, nil
	}
	target := rawURL
	if !strings.HasPrefix(strings.ToLower(target), "http://") && !strings.HasPrefix(strings.ToLower(target), "https://") {
		target = "https://" + target
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" {
		return map[string]any{"error": "Invalid URL: missing hostname", "url": target}, nil
	}
	if block := (urlPolicy{resolver: resolver}).check(ctx, target); block != "" {
		return map[string]any{"error": block, "blocked_by_ssrf_protection": true, "url": target}, nil
	}

	if boolArg(input, "respect_robots_txt", false) {
		allowed, reason := robotsAllows(ctx, rt, target)
		if !allowed {
			return map[string]any{
				"error":   "Blocked by robots.txt: " + target,
				"url":     target,
				"skipped": true,
				"hint":    "Pass respect_robots_txt=false if you have authorization to scrape this site.",
				"reason":  reason,
			}, nil
		}
	}

	body, finalURL, fetchBlock := scrapeFetchPreflight(ctx, rt, target, resolver)
	if fetchBlock != nil {
		return fetchBlock, nil
	}

	// Render the final URL in the shared headless browser. Upstream does this
	// inside Playwright (page.goto + page.content()); the pre-flight fetch above
	// already validated the target and the redirect chain, so the browser
	// navigates straight to the resolved URL.
	rendered, renderErr := renderer(ctx, finalURL, string(body))
	if renderErr != nil {
		var unavailable *browserUnavailableError
		if errors.As(renderErr, &unavailable) {
			return map[string]any{
				"error":               unavailable.Error(),
				"browser_unavailable": true,
				"url":                 target,
				"hint":                "Install Google Chrome (or Chromium), or set CHROME_PATH to the browser binary, then retry.",
			}, nil
		}
		if ctx.Err() != nil || errors.Is(renderErr, context.DeadlineExceeded) || errTimeout(renderErr) {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Browser error: " + renderErr.Error()}, nil
	}

	doc := parseHTML(rendered)

	// Structured data is extracted before boilerplate removal because JSON-LD
	// lives in <script>, which the text walker ignores.
	jsonLD := jsonLDBlocks(doc)
	openGraph := openGraphMeta(doc)

	title := ""
	if node := firstTag(doc, "title"); node != nil {
		title = strings.Join(strings.Fields(plainText(node)), " ")
	}
	description := ""
	for _, meta := range findAll(doc, "meta") {
		if strings.EqualFold(meta.attrs["name"], "description") {
			description = strings.TrimSpace(meta.attrs["content"])
			break
		}
	}
	if description == "" {
		if value, ok := openGraph["description"].(string); ok {
			description = value
		}
	}

	headings := make([]map[string]any, 0, 16)
	walkVisible(doc, func(node *htmlNode) {
		if len(headings) >= 100 {
			return
		}
		if len(node.tag) == 2 && node.tag[0] == 'h' && node.tag[1] >= '1' && node.tag[1] <= '6' {
			text := strings.Join(strings.Fields(plainText(node)), " ")
			if text != "" {
				headings = append(headings, map[string]any{"level": int(node.tag[1] - '0'), "text": text})
			}
		}
	})

	articleCount := 0
	walkVisible(doc, func(node *htmlNode) {
		if node.tag == "article" {
			articleCount++
		}
	})
	hasMain := false
	if node := firstTag(doc, "main"); node != nil && !insideSkip(node) {
		hasMain = true
	}
	pageType := "page"
	if articleCount >= 3 {
		pageType = "listing"
	} else if articleCount == 1 || hasMain {
		pageType = "article"
	}

	selector := strings.TrimSpace(stringArg(input, "selector"))
	var contentRoot *htmlNode
	if selector != "" {
		contentRoot = selectOne(doc, selector)
		if contentRoot == nil {
			return map[string]any{
				"error": fmt.Sprintf("No elements found matching selector: %s", selector),
				"url":   target,
				"hint":  "Try a broader selector or omit selector to use auto-detection.",
			}, nil
		}
	} else {
		contentRoot = defaultContentRoot(doc)
	}
	if contentRoot == nil {
		contentRoot = doc
	}

	base, err := url.Parse(finalURL)
	if err != nil {
		base, _ = url.Parse(target)
	}
	includeLinks := boolArg(input, "include_links", false)
	rawText, links := extractText(contentRoot, base, includeLinks)
	text := cleanText(rawText)

	if text == "" {
		return map[string]any{
			"error":     "Empty page: no readable text was found in the rendered HTML after removing scripts, navigation and boilerplate. Refusing to return an empty result.",
			"url":       target,
			"final_url": finalURL,
		}, nil
	}

	host := ""
	if base != nil {
		host = base.Hostname()
	}
	savedTo, writeErr := writeScrapeArtifact(rt, deps, host, text)
	if writeErr != nil {
		return map[string]any{"error": "Failed to write scrape artifact: " + writeErr.Error(), "url": target}, nil
	}

	structured := map[string]any{}
	if len(jsonLD) > 0 {
		structured["json_ld"] = jsonLD
	}
	if len(openGraph) > 0 {
		structured["open_graph"] = openGraph
	}
	result := map[string]any{
		"url":          target,
		"final_url":    finalURL,
		"title":        title,
		"description":  description,
		"page_type":    pageType,
		"total_length": len(text),
		"saved_to":     savedTo,
		"headings":     headings,
	}
	if len(structured) > 0 {
		result["structured_data"] = structured
	}
	if includeLinks {
		result["links"] = links
	}
	return result, nil
}

// -------------------------------------------------------------- fetching ----

type ssrfError struct {
	url     string
	message string
}

func (e *ssrfError) Error() string { return e.message }

// fetch performs a bounded GET with manual redirect handling so every
// redirect target goes through the same SSRF check.
func fetch(ctx context.Context, rt *Runtime, target string, resolver Resolver) (int, http.Header, []byte, string, error) {
	client := &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	if rt.HTTP != nil && rt.HTTP.Inner != nil {
		clone := *rt.HTTP.Inner
		clone.CheckRedirect = client.CheckRedirect
		client = &clone
	}
	current := target
	for redirects := 0; redirects <= 5; redirects++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return 0, nil, nil, current, err
		}
		request.Header.Set("User-Agent", browserUserAgent)
		request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		request.Header.Set("Accept-Language", "en-US,en;q=0.9")
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, nil, current, err
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return response.StatusCode, response.Header, nil, current, nil
			}
			next, err := url.Parse(current)
			if err != nil {
				return 0, nil, nil, current, err
			}
			relative, err := url.Parse(location)
			if err != nil {
				return 0, nil, nil, current, err
			}
			current = next.ResolveReference(relative).String()
			if block := (urlPolicy{resolver: resolver}).check(ctx, current); block != "" {
				return 0, nil, nil, current, &ssrfError{url: current, message: block}
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		response.Body.Close()
		if readErr != nil {
			return response.StatusCode, response.Header, nil, current, readErr
		}
		return response.StatusCode, response.Header, body, current, nil
	}
	return 0, nil, nil, current, errors.New("too many redirects")
}

// scrapeFetchPreflight runs the pre-flight GET and its HTTP-level checks:
// fetch already re-validates every redirect target, and this wrapper
// maps fetch failures, non-200 responses and non-HTML content to the
// action's early-return payload, byte-identical to the inline branches it
// replaced. A non-nil result means webScrapeAction must return it as-is;
// body/finalURL are only consumed when the result is nil.
func scrapeFetchPreflight(ctx context.Context, rt *Runtime, target string, resolver Resolver) ([]byte, string, map[string]any) {
	status, header, body, finalURL, fetchErr := fetch(ctx, rt, target, resolver)
	if fetchErr != nil {
		var blocked *ssrfError
		if errors.As(fetchErr, &blocked) {
			return nil, "", map[string]any{"error": blocked.Error(), "blocked_by_ssrf_protection": true, "url": blocked.url}
		}
		if ctx.Err() != nil || errTimeout(fetchErr) {
			return nil, "", map[string]any{"error": "Request timed out"}
		}
		return nil, "", map[string]any{"error": "Scraping failed: " + fetchErr.Error()}
	}
	if status != http.StatusOK {
		hint := "Resource may not exist or server may be down."
		if status == 401 || status == 403 || status == 429 {
			hint = "Site likely requires auth, blocks bots, or is rate-limiting."
		}
		return nil, "", map[string]any{
			"error":  fmt.Sprintf("HTTP %d: Failed to fetch URL", status),
			"url":    target,
			"status": status,
			"hint":   hint,
		}
	}
	contentType := strings.ToLower(header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return nil, "", map[string]any{
			"error":   "Skipping non-HTML content (Content-Type: " + contentType + ")",
			"url":     target,
			"skipped": true,
		}
	}
	return body, finalURL, nil
}

// jsonLDBlocks collects <script type="application/ld+json"> payloads before
// boilerplate removal: JSON-LD lives in a script tag the text walker ignores.
func jsonLDBlocks(doc *htmlNode) []any {
	out := []any{}
	for _, script := range findAll(doc, "script") {
		if !strings.EqualFold(strings.TrimSpace(script.attrs["type"]), "application/ld+json") {
			continue
		}
		payload := strings.TrimSpace(rawText(script))
		if payload == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			out = append(out, decoded)
		}
	}
	return out
}

// openGraphMeta maps og:* meta tags to their bare names.
func openGraphMeta(doc *htmlNode) map[string]any {
	out := map[string]any{}
	for _, meta := range findAll(doc, "meta") {
		property := strings.TrimSpace(meta.attrs["property"])
		if !strings.HasPrefix(strings.ToLower(property), "og:") {
			continue
		}
		value := strings.TrimSpace(meta.attrs["content"])
		if value != "" {
			out[strings.TrimPrefix(strings.ToLower(property), "og:")] = value
		}
	}
	return out
}

// -------------------------------------------------------------- robots ------

func robotsAllows(ctx context.Context, rt *Runtime, target string) (bool, string) {
	parsed, err := url.Parse(target)
	if err != nil {
		return true, ""
	}
	robotsURL := parsed.Scheme + "://" + parsed.Host + "/robots.txt"
	status, body, err := rt.GetBytes(ctx, robotsURL, map[string]string{"User-Agent": browserUserAgent})
	if err != nil || status >= 400 {
		return true, "" // robots.txt unavailable → proceed, matching upstream
	}
	allowed := robotsPathAllowed(string(body), parsed.Path)
	if allowed {
		return true, ""
	}
	return false, "path disallowed for user-agent"
}

func robotsPathAllowed(text, path string) bool {
	star := &robotsRules{}
	ours := &robotsRules{}
	current := []*robotsRules{}
	for _, line := range strings.Split(text, "\n") {
		if index := strings.IndexByte(line, '#'); index >= 0 {
			line = line[:index]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			current = current[:0]
			agent := strings.ToLower(value)
			if agent == "*" {
				current = append(current, star)
			} else if agent != "" && strings.Contains(strings.ToLower(browserUserAgent), agent) {
				current = append(current, ours)
			}
		case "disallow":
			if value != "" {
				for _, rules := range current {
					rules.disallow = append(rules.disallow, value)
				}
			}
		case "allow":
			if value != "" {
				for _, rules := range current {
					rules.allow = append(rules.allow, value)
				}
			}
		}
	}
	if len(ours.allow) > 0 || len(ours.disallow) > 0 {
		return robotsRulesAllow(ours, path)
	}
	return robotsRulesAllow(star, path)
}

type robotsRules struct {
	allow    []string
	disallow []string
}

// robotsRulesAllow applies longest-match precedence between Allow and
// Disallow, which is what robots.txt consumers (and RobotFileParser) do.
func robotsRulesAllow(rules *robotsRules, path string) bool {
	best := -1
	allowed := true
	for _, pattern := range rules.disallow {
		if strings.HasPrefix(path, pattern) && len(pattern) > best {
			best = len(pattern)
			allowed = false
		}
	}
	for _, pattern := range rules.allow {
		if strings.HasPrefix(path, pattern) && len(pattern) >= best {
			best = len(pattern)
			allowed = true
		}
	}
	return allowed
}

// ------------------------------------------------------------- artifacts ----

var artifactSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// writeScrapeArtifact persists the extracted body under the shared
// batch-3 artifact directory and returns its absolute path.
func writeScrapeArtifact(rt *Runtime, deps Deps, host, body string) (string, error) {
	dir, err := resolveArtifactDir(storageEnv{
		getenv:      func(name string) string { return envValue(rt, name) },
		userHomeDir: deps.UserHomeDir,
	})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	slug := artifactSlug.ReplaceAllString(host, "_")
	if len(slug) > 60 {
		slug = slug[:60]
	}
	if slug == "" {
		slug = "unknown"
	}
	path := filepath.Join(dir, fmt.Sprintf("web_scrape_%d_%s.txt", time.Now().UnixMilli(), slug))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// ------------------------------------------------------------ extraction ----

func extractText(root *htmlNode, base *url.URL, includeLinks bool) (string, []map[string]any) {
	links := []map[string]any{}
	var builder strings.Builder
	var walk func(*htmlNode)
	walk = func(node *htmlNode) {
		if node.tag == "#text" {
			builder.WriteString(node.text)
			return
		}
		if skipTags[node.tag] {
			return
		}
		if node.tag == "br" {
			builder.WriteByte('\n')
			return
		}
		if includeLinks && node.tag == "a" {
			href := node.attrs["href"]
			text := strings.Join(strings.Fields(plainText(node)), " ")
			if href != "" && text != "" {
				absolute := resolveURL(base, href)
				builder.WriteString("[" + text + "](" + absolute + ")")
				if len(links) < 50 {
					links = append(links, map[string]any{"text": text, "href": absolute})
				}
				return
			}
		}
		block := blockTags[node.tag]
		if block {
			builder.WriteByte('\n')
		}
		for _, child := range node.children {
			walk(child)
		}
		if block {
			builder.WriteByte('\n')
		}
	}
	if root != nil {
		walk(root)
	}
	return builder.String(), links
}

func resolveURL(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	relative, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(relative).String()
}

func cleanText(raw string) string {
	lines := []string{}
	blank := true
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
			blank = false
		} else if !blank {
			lines = append(lines, "")
			blank = true
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
