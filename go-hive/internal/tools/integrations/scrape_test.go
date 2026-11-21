package integrations

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func parseIPForTest(address string) net.IP { return net.ParseIP(address) }

func batch3ScrapeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_STORAGE_PATH", "")
	return home
}

func batch3SavedBody(t *testing.T, result map[string]any) string {
	t.Helper()
	path, _ := result["saved_to"].(string)
	if path == "" {
		t.Fatalf("missing saved_to: %v", result)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved body: %v", err)
	}
	return string(body)
}

func batch3HTMLServer(t *testing.T, html string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(server.Close)
	return server
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeTool::test_body_written_to_disk, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_block_level_newlines_preserved, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_headings_outline_returned, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_structured_data_json_ld, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_structured_data_open_graph
func TestBatch3WebScrapeStaticArticle(t *testing.T) {
	home := batch3ScrapeEnv(t)
	html := `<html><head>
		<title>Doc Title</title>
		<meta name="description" content="A description">
		<meta property="og:title" content="OG Title">
		<script type="application/ld+json">{"@type": "Article", "headline": "Hello"}</script>
		</head><body>
		<nav>Home | About</nav>
		<main>
			<h1>Top</h1>
			<p>First paragraph.</p>
			<p>Second paragraph.</p>
			<ul><li>Item one</li><li>Item two</li></ul>
		</main>
		<footer>Footer text</footer>
		</body></html>`
	server := batch3HTMLServer(t, html)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/page"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if result["title"] != "Doc Title" || result["description"] != "A description" {
		t.Fatalf("title/description = %v", result)
	}
	if result["page_type"] != "article" {
		t.Fatalf("page_type = %v", result["page_type"])
	}
	body := batch3SavedBody(t, result)
	for _, want := range []string{"Top", "First paragraph.", "Second paragraph.", "Item one", "Item two"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "Home | About") || strings.Contains(body, "Footer text") {
		t.Fatalf("boilerplate leaked into body: %q", body)
	}
	if result["total_length"] != len(body) {
		t.Fatalf("total_length = %v, len = %d", result["total_length"], len(body))
	}
	if _, inline := result["content"]; inline {
		t.Fatalf("body must not be inlined: %v", result)
	}
	saved := result["saved_to"].(string)
	if !strings.HasPrefix(saved, home) {
		t.Fatalf("saved_to outside HIVE_HOME: %v", saved)
	}
	headings, _ := result["headings"].([]map[string]any)
	if len(headings) != 1 || headings[0]["level"] != 1 || headings[0]["text"] != "Top" {
		t.Fatalf("headings = %v", result["headings"])
	}
	structured, _ := result["structured_data"].(map[string]any)
	jsonLD, _ := structured["json_ld"].([]any)
	if len(jsonLD) != 1 {
		t.Fatalf("json_ld = %v", structured["json_ld"])
	}
	openGraph, _ := structured["open_graph"].(map[string]any)
	if openGraph["title"] != "OG Title" {
		t.Fatalf("open_graph = %v", openGraph)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolLinkConversion::test_relative_links_converted_to_absolute, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolLinkConversion::test_root_relative_links_converted, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolLinkConversion::test_fragment_links_preserved, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolLinkConversion::test_query_parameters_preserved, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolLinkConversion::test_empty_href_skipped, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_inline_links_when_include_links
func TestBatch3WebScrapeLinks(t *testing.T) {
	batch3ScrapeEnv(t)
	html := `<html><body><article>
		<p>See <a href="/docs">our docs</a> for details.</p>
		<a href="../home">Home</a>
		<a href="page.html">Next Page</a>
		<a href="/search?q=test&sort=date">Search</a>
		<a href="#section">Jump</a>
		<a href="/empty">   </a>
	</article></body></html>`
	server := batch3HTMLServer(t, html)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":           "https://example.com/blog/post",
		"include_links": true,
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	links, _ := result["links"].([]map[string]any)
	hrefs := map[string]string{}
	for _, link := range links {
		hrefs[fmt.Sprint(link["text"])] = fmt.Sprint(link["href"])
	}
	want := map[string]string{
		"our docs":  "https://example.com/docs",
		"Home":      "https://example.com/home",
		"Next Page": "https://example.com/blog/page.html",
		"Search":    "https://example.com/search?q=test&sort=date",
		"Jump":      "https://example.com/blog/post#section",
	}
	for text, href := range want {
		if hrefs[text] != href {
			t.Fatalf("link %q = %q, want %q (all: %v)", text, hrefs[text], href, hrefs)
		}
	}
	if _, present := hrefs[""]; present {
		t.Fatalf("empty link text should be skipped: %v", hrefs)
	}
	body := batch3SavedBody(t, result)
	if !strings.Contains(body, "[our docs](https://example.com/docs)") {
		t.Fatalf("inline link missing: %q", body)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeTool::test_selector_option
func TestBatch3WebScrapeSelector(t *testing.T) {
	batch3ScrapeEnv(t)
	html := `<html><body><div class="outside">Outside text</div>
		<div class="content"><p>Content here</p></div></body></html>`
	server := batch3HTMLServer(t, html)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":      "https://example.com/page",
		"selector": ".content",
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	body := batch3SavedBody(t, result)
	if !strings.Contains(body, "Content here") || strings.Contains(body, "Outside text") {
		t.Fatalf("selector body = %q", body)
	}

	missing := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":      "https://example.com/page",
		"selector": ".does-not-exist",
	})
	if !strings.Contains(fmt.Sprint(missing["error"]), "selector") {
		t.Fatalf("missing selector = %v", missing)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolErrorHandling::test_non_html_content_type_skipped
func TestBatch3WebScrapeNonHTMLSkipped(t *testing.T) {
	batch3ScrapeEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/file.pdf"})
	if result["skipped"] != true {
		t.Fatalf("skipped = %v (%v)", result["skipped"], result)
	}
	if !strings.Contains(fmt.Sprint(result["error"]), "non-HTML") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolErrorHandling::test_http_error_returns_without_waiting
func TestBatch3WebScrapeHTTPError(t *testing.T) {
	batch3ScrapeEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/missing"})
	if result["error"] != "HTTP 404: Failed to fetch URL" {
		t.Fatalf("error = %v", result["error"])
	}
	if result["status"] != 404 || result["hint"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeTool::test_url_auto_prefixed_with_https
func TestBatch3WebScrapeURLSchemePrefixed(t *testing.T) {
	batch3ScrapeEnv(t)
	server := batch3HTMLServer(t, `<html><body><p>Hello world</p></body></html>`)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "example.com/page"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if result["url"] != "https://example.com/page" {
		t.Fatalf("url = %v", result["url"])
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_page_type_listing
func TestBatch3WebScrapePageTypeListing(t *testing.T) {
	batch3ScrapeEnv(t)
	html := `<html><body>
		<article><h2>Post 1</h2></article>
		<article><h2>Post 2</h2></article>
		<article><h2>Post 3</h2></article>
	</body></html>`
	server := batch3HTMLServer(t, html)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/blog"})
	if result["page_type"] != "listing" {
		t.Fatalf("page_type = %v", result["page_type"])
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolAIFriendlyOutput::test_full_body_persisted_regardless_of_size
func TestBatch3WebScrapePersistsFullBody(t *testing.T) {
	batch3ScrapeEnv(t)
	payload := strings.Repeat("a", 5000)
	server := batch3HTMLServer(t, "<html><body>"+payload+"</body></html>")
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/big"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	body := batch3SavedBody(t, result)
	if body != payload {
		t.Fatalf("body length = %d, want %d", len(body), len(payload))
	}
	if result["total_length"] != 5000 {
		t.Fatalf("total_length = %v", result["total_length"])
	}
}

// Parity-exempt: the upstream tool cannot return an empty body because
// Playwright would have rendered something; the Go port must refuse.
func TestBatch3WebScrapeEmptyPageErrors(t *testing.T) {
	batch3ScrapeEnv(t)
	server := batch3HTMLServer(t, `<html><body>   </body></html>`)
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/blank"})
	if result["error"] == nil {
		t.Fatalf("empty page must error: %v", result)
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(result["error"])), "empty") {
		t.Fatalf("error = %v", result["error"])
	}
	if _, saved := result["saved_to"]; saved {
		t.Fatalf("empty page must not write an artifact: %v", result)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolRobotsTxt::test_blocked_by_robots_txt, tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeToolRobotsTxt::test_robots_txt_disabled
func TestBatch3WebScrapeRobotsTxt(t *testing.T) {
	batch3ScrapeEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>Content</p></body></html>`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	blocked := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":                "https://example.com/private/page",
		"respect_robots_txt": true,
	})
	if blocked["skipped"] != true || !strings.Contains(fmt.Sprint(blocked["error"]), "robots.txt") {
		t.Fatalf("blocked = %v", blocked)
	}

	allowed := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":                "https://example.com/public",
		"respect_robots_txt": true,
	})
	if _, failed := allowed["error"]; failed {
		t.Fatalf("allowed = %v", allowed)
	}

	disabled := batch3Execute(t, registry, "web_scrape", map[string]any{
		"url":                "https://example.com/private/page",
		"respect_robots_txt": false,
	})
	if _, failed := disabled["error"]; failed {
		t.Fatalf("disabled = %v", disabled)
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeSSRF::test_blocks_internal_targets
func TestBatch3WebScrapeBlocksInternalTargets(t *testing.T) {
	registry := batch3TestRegistry(t, "")
	for _, target := range []string{
		"http://192.168.1.1/admin",
		"http://127.0.0.1/secret",
		"http://169.254.169.254/latest/meta-data/",
	} {
		result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": target})
		if result["blocked_by_ssrf_protection"] != true {
			t.Fatalf("%s -> %v", target, result)
		}
	}
}

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestCheckUrlTarget::test_public_hostname_allowed, tools/tests/tools/test_web_scrape_tool.py::TestCheckUrlTarget::test_private_hostname_blocked, tools/tests/tools/test_web_scrape_tool.py::TestCheckUrlTarget::test_raw_private_ip_blocked, tools/tests/tools/test_web_scrape_tool.py::TestCheckUrlTarget::test_dns_failure_returns_error, tools/tests/tools/test_web_scrape_tool.py::TestIsInternalAddress::test_classification
func TestBatch3CheckURLTarget(t *testing.T) {
	public := func(ctx context.Context, host string) ([]string, error) {
		return []string{"93.184.216.34"}, nil
	}
	private := func(ctx context.Context, host string) ([]string, error) {
		return []string{"10.0.0.1"}, nil
	}
	dnsFailure := func(ctx context.Context, host string) ([]string, error) {
		return nil, fmt.Errorf("NXDOMAIN")
	}

	if problem := batch3CheckURLTarget(context.Background(), "https://example.com/page", public); problem != "" {
		t.Fatalf("public = %q", problem)
	}
	if problem := batch3CheckURLTarget(context.Background(), "https://evil.com/steal", private); !strings.Contains(strings.ToLower(problem), "internal") {
		t.Fatalf("private = %q", problem)
	}
	if problem := batch3CheckURLTarget(context.Background(), "http://127.0.0.1/admin", public); problem == "" {
		t.Fatal("raw private IP must be blocked")
	}
	if problem := batch3CheckURLTarget(context.Background(), "https://nonexistent.invalid/", dnsFailure); !strings.Contains(problem, "DNS") {
		t.Fatalf("dns = %q", problem)
	}

	cases := map[string]bool{
		"127.0.0.1": true, "10.0.0.1": true, "192.168.1.1": true,
		"169.254.169.254": true, "8.8.8.8": false,
		"2607:f8b0:4004:800::200e": false, "not-an-ip": true,
	}
	for address, want := range cases {
		ip := parseIPForTest(address)
		got := ip == nil || batch3IsInternalIP(ip)
		if got != want {
			t.Fatalf("internal(%q) = %v, want %v", address, got, want)
		}
	}
}

// Parity-exempt: upstream blocks redirect navigation inside Playwright; the
// Go port follows redirects manually and must apply the same check.
func TestBatch3WebScrapeBlocksInternalRedirect(t *testing.T) {
	batch3ScrapeEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/secret", http.StatusFound)
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/redirect"})
	if result["blocked_by_ssrf_protection"] != true {
		t.Fatalf("result = %v", result)
	}
}
