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

const batch3BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// ---------------------------------------------------------- dependencies ----

type batch3Resolver func(ctx context.Context, host string) ([]string, error)

// batch3Renderer returns the post-JavaScript DOM for a URL. Production runs
// the shared headless browser (batch3RenderWithBrowser); tests inject a fake
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
		scrapeRenderer: batch3BrowserRenderer,
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

	status, header, body, finalURL, fetchErr := batch3Fetch(ctx, rt, target, resolver)
	if fetchErr != nil {
		var blocked *batch3SSRFError
		if errors.As(fetchErr, &blocked) {
			return map[string]any{"error": blocked.Error(), "blocked_by_ssrf_protection": true, "url": blocked.url}, nil
		}
		if ctx.Err() != nil || errTimeout(fetchErr) {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Scraping failed: " + fetchErr.Error()}, nil
	}
	if status != http.StatusOK {
		hint := "Resource may not exist or server may be down."
		if status == 401 || status == 403 || status == 429 {
			hint = "Site likely requires auth, blocks bots, or is rate-limiting."
		}
		return map[string]any{
			"error":  fmt.Sprintf("HTTP %d: Failed to fetch URL", status),
			"url":    target,
			"status": status,
			"hint":   hint,
		}, nil
	}
	contentType := strings.ToLower(header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return map[string]any{
			"error":   "Skipping non-HTML content (Content-Type: " + contentType + ")",
			"url":     target,
			"skipped": true,
		}, nil
	}

	// Render the final URL in the shared headless browser. Upstream does this
	// inside Playwright (page.goto + page.content()); the pre-flight fetch above
	// already validated the target and the redirect chain, so the browser
	// navigates straight to the resolved URL.
	rendered, renderErr := renderer(ctx, finalURL, string(body))
	if renderErr != nil {
		var unavailable *batch3BrowserUnavailableError
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
	jsonLD := batch3JSONLD(doc)
	openGraph := batch3OpenGraph(doc)

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
		request.Header.Set("User-Agent", batch3BrowserUserAgent)
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

// batch3JSONLD collects <script type="application/ld+json"> payloads before
// boilerplate removal: JSON-LD lives in a script tag the text walker ignores.
func batch3JSONLD(doc *batch3Node) []any {
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

// batch3OpenGraph maps og:* meta tags to their bare names.
func batch3OpenGraph(doc *batch3Node) map[string]any {
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
	status, body, err := batch2Get(ctx, rt, robotsURL, map[string]string{"User-Agent": batch3BrowserUserAgent})
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
			} else if agent != "" && strings.Contains(strings.ToLower(batch3BrowserUserAgent), agent) {
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

// ------------------------------------------------------------ HTML model ----

type batch3Node struct {
	tag      string
	attrs    map[string]string
	text     string
	raw      bool
	children []*batch3Node
	parent   *batch3Node
}

var batch3VoidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var batch3RawTextTags = map[string]bool{"script": true, "style": true, "textarea": true}

var batch3SkipTags = map[string]bool{
	"script": true, "style": true, "nav": true, "footer": true, "header": true,
	"aside": true, "noscript": true, "iframe": true, "head": true,
	"template": true, "svg": true, "canvas": true,
}

var batch3BlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "dd": true, "div": true, "dl": true, "dt": true,
	"fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hr": true, "li": true, "main": true,
	"nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true,
	"thead": true, "tr": true, "ul": true, "title": true,
}

func batch3ParseHTML(source string) *batch3Node {
	root := &batch3Node{tag: "#document"}
	stack := []*batch3Node{root}
	i := 0
	for i < len(source) {
		if source[i] != '<' {
			next := strings.IndexByte(source[i:], '<')
			chunk := ""
			if next < 0 {
				chunk = source[i:]
				i = len(source)
			} else {
				chunk = source[i : i+next]
				i += next
			}
			if chunk != "" {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, &batch3Node{tag: "#text", text: batch3Unescape(chunk), parent: parent})
			}
			continue
		}
		rest := source[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				i = len(source)
			} else {
				i += 4 + end + 3
			}
			continue
		case strings.HasPrefix(rest, "<!") || strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				i = len(source)
			} else {
				i += end + 1
			}
			continue
		case strings.HasPrefix(rest, "</"):
			end := strings.IndexByte(rest, '>')
			name := ""
			if end < 0 {
				name = rest[2:]
				i = len(source)
			} else {
				name = rest[2:end]
				i += end + 1
			}
			name = strings.ToLower(strings.TrimSpace(name))
			for depth := len(stack) - 1; depth >= 1; depth-- {
				if stack[depth].tag == name {
					stack = stack[:depth]
					break
				}
			}
			continue
		}

		// Start tag.
		cursor := i + 1
		if cursor >= len(source) || !batch3IsTagNameStart(source[cursor]) {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, &batch3Node{tag: "#text", text: "<", parent: parent})
			i++
			continue
		}
		for cursor < len(source) && batch3IsTagNameChar(source[cursor]) {
			cursor++
		}
		name := strings.ToLower(source[i+1 : cursor])
		attrs, selfClosing, next := batch3ParseAttrs(source, cursor)
		node := &batch3Node{tag: name, attrs: attrs, parent: stack[len(stack)-1]}
		stack[len(stack)-1].children = append(stack[len(stack)-1].children, node)
		i = next

		if batch3RawTextTags[name] {
			closing := "</" + name
			index := batch3IndexFold(source[i:], closing)
			raw := ""
			if index < 0 {
				raw = source[i:]
				i = len(source)
			} else {
				raw = source[i : i+index]
				end := strings.IndexByte(source[i+index:], '>')
				if end < 0 {
					i = len(source)
				} else {
					i += index + end + 1
				}
			}
			node.children = append(node.children, &batch3Node{tag: "#text", text: raw, raw: true, parent: node})
			continue
		}
		if !selfClosing && !batch3VoidTags[name] {
			stack = append(stack, node)
		}
	}
	return root
}

func batch3IsTagNameStart(char byte) bool {
	return char == '!' || char == '?' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func batch3IsTagNameChar(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '-' || char == ':' || char == '_'
}

func batch3ParseAttrs(source string, pos int) (map[string]string, bool, int) {
	attrs := map[string]string{}
	selfClosing := false
	for pos < len(source) {
		for pos < len(source) && batch3IsSpaceByte(source[pos]) {
			pos++
		}
		if pos >= len(source) {
			return attrs, selfClosing, pos
		}
		if source[pos] == '/' {
			selfClosing = true
			pos++
			continue
		}
		if source[pos] == '>' {
			return attrs, selfClosing, pos + 1
		}
		start := pos
		for pos < len(source) && !batch3IsSpaceByte(source[pos]) &&
			source[pos] != '=' && source[pos] != '>' && source[pos] != '/' {
			pos++
		}
		name := strings.ToLower(source[start:pos])
		for pos < len(source) && batch3IsSpaceByte(source[pos]) {
			pos++
		}
		value := ""
		if pos < len(source) && source[pos] == '=' {
			pos++
			for pos < len(source) && batch3IsSpaceByte(source[pos]) {
				pos++
			}
			if pos < len(source) && (source[pos] == '"' || source[pos] == '\'') {
				quote := source[pos]
				pos++
				end := strings.IndexByte(source[pos:], quote)
				if end < 0 {
					value = source[pos:]
					pos = len(source)
				} else {
					value = source[pos : pos+end]
					pos += end + 1
				}
			} else {
				valueStart := pos
				for pos < len(source) && !batch3IsSpaceByte(source[pos]) && source[pos] != '>' {
					pos++
				}
				value = source[valueStart:pos]
			}
		}
		if name != "" {
			attrs[name] = batch3Unescape(value)
		}
	}
	return attrs, selfClosing, pos
}

func batch3IsSpaceByte(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\f'
}

func batch3IndexFold(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if strings.EqualFold(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// --------------------------------------------------------------- entities ---

var batch3NamedEntities = map[string]string{
	"nbsp": " ", "copy": "©", "reg": "®", "trade": "™", "hellip": "…",
	"mdash": "—", "ndash": "–", "rsquo": "’", "lsquo": "‘", "ldquo": "“",
	"rdquo": "”", "middot": "·", "bull": "•", "deg": "°", "plusmn": "±",
	"sup2": "²", "sup3": "³", "frac12": "½", "frac14": "¼", "frac34": "¾",
	"times": "×", "divide": "÷", "euro": "€", "pound": "£", "yen": "¥",
	"cent": "¢", "sect": "§", "para": "¶", "laquo": "«", "raquo": "»",
	"dagger": "†", "Dagger": "‡", "permil": "‰", "larr": "←", "rarr": "→",
	"uarr": "↑", "darr": "↓", "harr": "↔", "ensp": " ", "emsp": " ",
	"thinsp": " ", "shy": "", "apos": "'", "quot": "\"", "amp": "&",
	"lt": "<", "gt": ">",
}

func batch3Unescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	text = htmlUnescape(text)
	return batch2EntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := batch2EntityPattern.FindStringSubmatch(match)
		if parts[3] == "" {
			return match
		}
		if value, ok := batch3NamedEntities[parts[3]]; ok {
			return value
		}
		if value, ok := batch3NamedEntities[strings.ToLower(parts[3])]; ok {
			return value
		}
		return match
	})
}

// ------------------------------------------------------------ node utils ----

func batch3RawText(node *batch3Node) string {
	var builder strings.Builder
	var walk func(*batch3Node)
	walk = func(current *batch3Node) {
		if current.tag == "#text" {
			builder.WriteString(current.text)
			return
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func batch3PlainText(node *batch3Node) string {
	var builder strings.Builder
	var walk func(*batch3Node)
	walk = func(current *batch3Node) {
		if current.tag == "#text" {
			builder.WriteString(current.text)
			return
		}
		if current.tag == "script" || current.tag == "style" {
			return
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func batch3FindAll(root *batch3Node, tag string) []*batch3Node {
	found := []*batch3Node{}
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if node.tag == tag {
			found = append(found, node)
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return found
}

func batch3FirstTag(root *batch3Node, tag string) *batch3Node {
	var found *batch3Node
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if found != nil {
			return
		}
		if node.tag == tag {
			found = node
			return
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return found
}

func batch3WalkVisible(root *batch3Node, visit func(*batch3Node)) {
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if batch3SkipTags[node.tag] {
			return
		}
		visit(node)
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
}

func batch3InsideSkip(node *batch3Node) bool {
	for current := node; current != nil; current = current.parent {
		if batch3SkipTags[current.tag] {
			return true
		}
	}
	return false
}

func batch3FindByID(root *batch3Node, id string) *batch3Node {
	var found *batch3Node
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if found != nil {
			return
		}
		if node.attrs["id"] == id {
			found = node
			return
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return found
}

func batch3HasClass(node *batch3Node, class string) bool {
	for _, candidate := range strings.Fields(node.attrs["class"]) {
		if candidate == class {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------- selector ----

func batch3SelectOne(root *batch3Node, selector string) *batch3Node {
	for _, alternative := range strings.Split(selector, ",") {
		compoundParts := strings.Fields(strings.TrimSpace(alternative))
		if len(compoundParts) == 0 {
			continue
		}
		if node := batch3SelectChain(root, compoundParts); node != nil {
			return node
		}
	}
	return nil
}

func batch3SelectChain(root *batch3Node, compounds []string) *batch3Node {
	matches := []*batch3Node{}
	batch3WalkVisible(root, func(node *batch3Node) {
		if batch3MatchCompound(node, compounds[len(compounds)-1]) {
			matches = append(matches, node)
		}
	})
	for _, match := range matches {
		if batch3MatchAncestors(match.parent, compounds[:len(compounds)-1]) {
			return match
		}
	}
	return nil
}

func batch3MatchAncestors(node *batch3Node, compounds []string) bool {
	if len(compounds) == 0 {
		return true
	}
	for current := node; current != nil; current = current.parent {
		if batch3MatchCompound(current, compounds[len(compounds)-1]) {
			if batch3MatchAncestors(current.parent, compounds[:len(compounds)-1]) {
				return true
			}
		}
	}
	return false
}

func batch3MatchCompound(node *batch3Node, compound string) bool {
	index := 0
	for index < len(compound) && compound[index] != '.' && compound[index] != '#' {
		index++
	}
	tag := compound[:index]
	if tag != "" && tag != "*" && !strings.EqualFold(node.tag, tag) {
		return false
	}
	for index < len(compound) {
		marker := compound[index]
		index++
		start := index
		for index < len(compound) && compound[index] != '.' && compound[index] != '#' {
			index++
		}
		value := compound[start:index]
		if value == "" {
			return false
		}
		switch marker {
		case '#':
			if node.attrs["id"] != value {
				return false
			}
		case '.':
			if !batch3HasClass(node, value) {
				return false
			}
		}
	}
	return true
}

func batch3DefaultContentRoot(doc *batch3Node) *batch3Node {
	if node := batch3FirstTag(doc, "main"); node != nil && !batch3InsideSkip(node) {
		return node
	}
	if node := batch3FindAttr(doc, "role", "main"); node != nil && !batch3InsideSkip(node) {
		return node
	}
	if node := batch3FirstVisibleTag(doc, "article"); node != nil {
		return node
	}
	for _, class := range []string{"content", "post", "entry", "article-body"} {
		if node := batch3FindByClass(doc, class); node != nil {
			return node
		}
	}
	if body := batch3FirstTag(doc, "body"); body != nil {
		return body
	}
	return doc
}

func batch3FirstVisibleTag(root *batch3Node, tag string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && node.tag == tag {
			found = node
		}
	})
	return found
}

func batch3FindByClass(root *batch3Node, class string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && batch3HasClass(node, class) {
			found = node
		}
	})
	return found
}

func batch3FindAttr(root *batch3Node, name, value string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && strings.EqualFold(node.attrs[name], value) {
			found = node
		}
	})
	return found
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
