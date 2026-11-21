// Batch 3: web_scrape with real browser rendering.
//
// The upstream tool drives Playwright + playwright_stealth so it can render
// JavaScript. This port keeps the same model-visible contract (description and
// input schema byte-for-byte from the Python capture) and the same extraction
// pipeline — dependency-free HTML tokenizer, body written to disk — but renders
// through go-rod/rod + go-rod/stealth against the system Chrome/Chromium. See
// scrape_browser.go for the browser lifecycle, the Playwright wait semantics
// (domcontentloaded, then a best-effort networkidle(3s)) and the stealth
// coverage table.
//
// The pre-flight net/http fetch is deliberate: it applies the SSRF guard, the
// optional robots.txt check, redirect re-validation and the HTTP status /
// Content-Type checks before any navigation. An empty rendered page is still an
// error, never a success.
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// ---------------------------------------------------------- dependencies ----

type batch3Resolver func(ctx context.Context, host string) ([]string, error)

// batch3Renderer returns the post-JavaScript DOM for a URL. Production runs
// the shared headless browser (renderWithBrowser); tests inject a fake
// so extraction fixtures never need a browser. staticHTML is the pre-flight
// body — production ignores it, tests may return it to stand in for
// page.content() on a static fixture.
type batch3Renderer func(ctx context.Context, target, staticHTML string) (string, error)

type batch3Deps struct {
	scrapeResolver batch3Resolver
	scrapeRenderer batch3Renderer
}

type batch3Option func(*batch3Deps)

func withBatch3Resolver(resolver batch3Resolver) batch3Option {
	return func(deps *batch3Deps) { deps.scrapeResolver = resolver }
}

func withBatch3Renderer(renderer batch3Renderer) batch3Option {
	return func(deps *batch3Deps) { deps.scrapeRenderer = renderer }
}

func defaultBatch3Deps() batch3Deps {
	return batch3Deps{
		scrapeResolver: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		},
		scrapeRenderer: browserRenderer,
	}
}

// batch3ScrapeDescription is the upstream web_scrape docstring, byte for byte:
// go_hive/contracts/tool-definitions.json, 1495 bytes, sha256
// a479a54a2eb01fe84a58028c23dcd3ba2cfe45ddd4d02f4ad1e3bf9511e04cc1.
const batch3ScrapeDescription = "Scrape and extract text content from a webpage.\n" +
	"\n" +
	"Uses a headless browser to render JavaScript and bypass bot detection.\n" +
	"Use when you need to read the content of a specific URL,\n" +
	"extract data from a website, or read articles/documentation.\n" +
	"\n" +
	"The extracted text body is **written to a file on disk** rather\n" +
	"than returned inline — JSON-wrapping multi-KB page text escapes\n" +
	"every newline and quote, which poisons the agent's context. The\n" +
	"response carries only metadata (title, description, headings,\n" +
	"structured data, links) plus ``saved_to`` pointing at the raw\n" +
	"text file. Use ``terminal_exec(\"cat <saved_to>\")`` (page large\n" +
	"output via ``terminal_output_get``) or ``terminal_rg`` on\n" +
	"``saved_to`` to inspect the body. Length is reported via\n" +
	"``total_length``.\n" +
	"\n" +
	"Args:\n" +
	"    url: URL of the webpage to scrape\n" +
	"    selector: CSS selector to target specific content (e.g., 'article', '.main-content')\n" +
	"    include_links: When True, links are inlined as `[text](url)` in\n" +
	"        the saved body and also returned as a `links` list\n" +
	"    respect_robots_txt: Whether to respect robots.txt rules (default False —\n" +
	"        operator has authorization for these one-off, low-volume fetches;\n" +
	"        pass True to re-enable the check for a given call)\n" +
	"\n" +
	"Returns:\n" +
	"    Dict with: url, final_url, title, description, page_type\n" +
	"    (article|listing|page), total_length, saved_to, headings,\n" +
	"    structured_data (json_ld + open_graph), and optionally links.\n" +
	"    On error, returns {\"error\": str, ...} with a hint when applicable."

var batch3ScrapeTool = &Tool{
	ID:          "web_scrape_tool",
	Name:        "web_scrape",
	DisplayName: "Web Scrape",
	Summary:     batch3ScrapeDescription,
	Actions: []Action{
		{
			Name:     "web_scrape",
			Summary:  batch3ScrapeDescription,
			Source:   "web_scrape_tool/web_scrape_tool.py",
			Required: []string{"url"},
			Params: map[string]any{
				"url": param("string"),
				// Python: selector: str | None = None → anyOf[string, null]
				// with an explicit null default.
				"selector": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "null"},
					},
					"default": nil,
				},
				"include_links":      paramDefault("boolean", false),
				"respect_robots_txt": paramDefault("boolean", false),
			},
		},
	},
}

// ---------------------------------------------------------------- action ----

func webScrapeAction(ctx context.Context, rt *Runtime, input map[string]any, resolver batch3Resolver, renderer batch3Renderer) (any, error) {
	if renderer == nil {
		renderer = defaultBatch3Deps().scrapeRenderer
	}
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
	if block := batch3CheckURLTarget(ctx, target, resolver); block != "" {
		return map[string]any{"error": block, "blocked_by_ssrf_protection": true, "url": target}, nil
	}

	if boolArg(input, "respect_robots_txt", false) {
		allowed, reason := batch3RobotsAllows(ctx, rt, target)
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

	doc := batch3ParseHTML(rendered)

	// Structured data is extracted before boilerplate removal because JSON-LD
	// lives in <script>, which the text walker ignores.
	jsonLD := jsonLDBlocks(doc)
	openGraph := openGraphMeta(doc)

	title := ""
	if node := batch3FirstTag(doc, "title"); node != nil {
		title = strings.Join(strings.Fields(batch3PlainText(node)), " ")
	}
	description := ""
	for _, meta := range batch3FindAll(doc, "meta") {
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
	batch3WalkVisible(doc, func(node *batch3Node) {
		if len(headings) >= 100 {
			return
		}
		if len(node.tag) == 2 && node.tag[0] == 'h' && node.tag[1] >= '1' && node.tag[1] <= '6' {
			text := strings.Join(strings.Fields(batch3PlainText(node)), " ")
			if text != "" {
				headings = append(headings, map[string]any{"level": int(node.tag[1] - '0'), "text": text})
			}
		}
	})

	articleCount := 0
	batch3WalkVisible(doc, func(node *batch3Node) {
		if node.tag == "article" {
			articleCount++
		}
	})
	hasMain := false
	if node := batch3FirstTag(doc, "main"); node != nil && !batch3InsideSkip(node) {
		hasMain = true
	}
	pageType := "page"
	if articleCount >= 3 {
		pageType = "listing"
	} else if articleCount == 1 || hasMain {
		pageType = "article"
	}

	selector := strings.TrimSpace(stringArg(input, "selector"))
	var contentRoot *batch3Node
	if selector != "" {
		contentRoot = batch3SelectOne(doc, selector)
		if contentRoot == nil {
			return map[string]any{
				"error": fmt.Sprintf("No elements found matching selector: %s", selector),
				"url":   target,
				"hint":  "Try a broader selector or omit selector to use auto-detection.",
			}, nil
		}
	} else {
		contentRoot = batch3DefaultContentRoot(doc)
	}
	if contentRoot == nil {
		contentRoot = doc
	}

	base, err := url.Parse(finalURL)
	if err != nil {
		base, _ = url.Parse(target)
	}
	includeLinks := boolArg(input, "include_links", false)
	rawText, links := batch3ExtractText(contentRoot, base, includeLinks)
	text := batch3CleanText(rawText)

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
	savedTo, writeErr := batch3WriteScrapeArtifact(host, text)
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

type batch3SSRFError struct {
	url     string
	message string
}

func (e *batch3SSRFError) Error() string { return e.message }

// batch3CheckURLTarget resolves a URL's host and refuses internal targets.
// An empty string means the URL is allowed.
func batch3CheckURLTarget(ctx context.Context, target string, resolver batch3Resolver) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" {
		return "Invalid URL: missing hostname"
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if batch3IsInternalIP(ip) {
			return fmt.Sprintf("Blocked: direct request to internal address (%s)", host)
		}
		return ""
	}
	if resolver == nil {
		resolver = defaultBatch3Deps().scrapeResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := resolver(lookupCtx, host)
	if err != nil {
		return "DNS resolution failed for host: " + host
	}
	if len(addresses) == 0 {
		return "No DNS records found for host: " + host
	}
	for _, address := range addresses {
		candidate := address
		if index := strings.IndexByte(candidate, '%'); index >= 0 {
			candidate = candidate[:index]
		}
		ip := net.ParseIP(candidate)
		if ip == nil || batch3IsInternalIP(ip) {
			return fmt.Sprintf("Blocked: %s resolves to internal address", host)
		}
	}
	return ""
}

func batch3IsInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT (100.64.0.0/10) is not global either.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// batch3Fetch performs a bounded GET with manual redirect handling so every
// redirect target goes through the same SSRF check.
func batch3Fetch(ctx context.Context, rt *Runtime, target string, resolver batch3Resolver) (int, http.Header, []byte, string, error) {
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
			if block := batch3CheckURLTarget(ctx, current, resolver); block != "" {
				return 0, nil, nil, current, &batch3SSRFError{url: current, message: block}
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
// batch3Fetch already re-validates every redirect target, and this wrapper
// maps fetch failures, non-200 responses and non-HTML content to the
// action's early-return payload, byte-identical to the inline branches it
// replaced. A non-nil result means webScrapeAction must return it as-is;
// body/finalURL are only consumed when the result is nil.
func scrapeFetchPreflight(ctx context.Context, rt *Runtime, target string, resolver batch3Resolver) ([]byte, string, map[string]any) {
	status, header, body, finalURL, fetchErr := batch3Fetch(ctx, rt, target, resolver)
	if fetchErr != nil {
		var blocked *batch3SSRFError
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
func jsonLDBlocks(doc *batch3Node) []any {
	out := []any{}
	for _, script := range batch3FindAll(doc, "script") {
		if !strings.EqualFold(strings.TrimSpace(script.attrs["type"]), "application/ld+json") {
			continue
		}
		payload := strings.TrimSpace(batch3RawText(script))
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
func openGraphMeta(doc *batch3Node) map[string]any {
	out := map[string]any{}
	for _, meta := range batch3FindAll(doc, "meta") {
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

func batch3RobotsAllows(ctx context.Context, rt *Runtime, target string) (bool, string) {
	parsed, err := url.Parse(target)
	if err != nil {
		return true, ""
	}
	robotsURL := parsed.Scheme + "://" + parsed.Host + "/robots.txt"
	status, body, err := batch2Get(ctx, rt, robotsURL, map[string]string{"User-Agent": browserUserAgent})
	if err != nil || status >= 400 {
		return true, "" // robots.txt unavailable → proceed, matching upstream
	}
	allowed := batch3RobotsPathAllowed(string(body), parsed.Path)
	if allowed {
		return true, ""
	}
	return false, "path disallowed for user-agent"
}

func batch3RobotsPathAllowed(text, path string) bool {
	star := &batch3RobotsRules{}
	ours := &batch3RobotsRules{}
	current := []*batch3RobotsRules{}
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
		return batch3RobotsRulesAllow(ours, path)
	}
	return batch3RobotsRulesAllow(star, path)
}

type batch3RobotsRules struct {
	allow    []string
	disallow []string
}

// batch3RobotsRulesAllow applies longest-match precedence between Allow and
// Disallow, which is what robots.txt consumers (and RobotFileParser) do.
func batch3RobotsRulesAllow(rules *batch3RobotsRules, path string) bool {
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

var batch3ArtifactSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func batch3ScrapeArtifactDir() string {
	if storage := strings.TrimSpace(os.Getenv("HIVE_STORAGE_PATH")); storage != "" {
		return filepath.Join(storage, "data")
	}
	home := strings.TrimSpace(os.Getenv("HIVE_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".hive")
		}
	}
	return filepath.Join(home, "tool-artifacts")
}

func batch3WriteScrapeArtifact(host, body string) (string, error) {
	dir := batch3ScrapeArtifactDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	slug := batch3ArtifactSlug.ReplaceAllString(host, "_")
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

func batch3ExtractText(root *batch3Node, base *url.URL, includeLinks bool) (string, []map[string]any) {
	links := []map[string]any{}
	var builder strings.Builder
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if node.tag == "#text" {
			builder.WriteString(node.text)
			return
		}
		if batch3SkipTags[node.tag] {
			return
		}
		if node.tag == "br" {
			builder.WriteByte('\n')
			return
		}
		if includeLinks && node.tag == "a" {
			href := node.attrs["href"]
			text := strings.Join(strings.Fields(batch3PlainText(node)), " ")
			if href != "" && text != "" {
				absolute := batch3ResolveURL(base, href)
				builder.WriteString("[" + text + "](" + absolute + ")")
				if len(links) < 50 {
					links = append(links, map[string]any{"text": text, "href": absolute})
				}
				return
			}
		}
		block := batch3BlockTags[node.tag]
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

func batch3ResolveURL(base *url.URL, href string) string {
	if base == nil {
		return href
	}
	relative, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(relative).String()
}

func batch3CleanText(raw string) string {
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
