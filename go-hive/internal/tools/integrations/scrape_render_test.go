package integrations

// Rendering tests for web_scrape. The default test registry substitutes the
// browser with the pre-flight HTML (see batch3TestRegistry); the tests here
// exercise the renderer contract itself: the DOM the browser returns is what
// gets extracted, the no-browser and render-failure envelopes are explicit,
// and the real-Chrome check is opt-in because it launches a browser process.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher/flags"
)

// Parity: tools/tests/tools/test_web_scrape_tool.py::TestWebScrapeTool::test_body_written_to_disk
// The Python fixture mocks page.content() to return the rendered DOM; this
// test injects the same seam and feeds a JavaScript-only shell through it.
func TestBatch3WebScrapeRendersJavaScript(t *testing.T) {
	batch3ScrapeEnv(t)
	shell := `<html><body><div id="root"></div><script src="/app.js"></script>` +
		`<noscript>You need to enable JavaScript to run this app.</noscript></body></html>`
	server := batch3HTMLServer(t, shell)

	var gotTarget, gotStatic string
	rendered := `<html><head><title>Rendered App</title>` +
		`<meta name="description" content="After JS"></head><body><main>` +
		`<h1>Dashboard</h1><p>Client-rendered content is here.</p></main></body></html>`
	renderer := func(_ context.Context, target, staticHTML string) (string, error) {
		gotTarget, gotStatic = target, staticHTML
		return rendered, nil
	}
	registry := batch3TestRegistry(t, server.URL, withBatch3Renderer(renderer))

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/app"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if gotTarget != "https://example.com/app" {
		t.Fatalf("renderer target = %q, want the final URL", gotTarget)
	}
	if !strings.Contains(gotStatic, `id="root"`) || strings.Contains(gotStatic, "Client-rendered content") {
		t.Fatalf("pre-flight body must be the JS shell, got %q", gotStatic)
	}
	if result["title"] != "Rendered App" || result["description"] != "After JS" {
		t.Fatalf("rendered metadata = %v", result)
	}
	body := batch3SavedBody(t, result)
	if !strings.Contains(body, "Client-rendered content is here.") || !strings.Contains(body, "Dashboard") {
		t.Fatalf("extracted body missing rendered text: %q", body)
	}
	if result["total_length"] != len(body) {
		t.Fatalf("total_length = %v, len = %d", result["total_length"], len(body))
	}
}

// Parity-exempt: upstream does not register web_scrape at all when Playwright
// is missing, so there is no Python case to mirror; a Go catalogue cannot drop
// a tool at call time, so the call returns this explicit envelope instead.
func TestBatch3WebScrapeBrowserUnavailable(t *testing.T) {
	batch3ScrapeEnv(t)
	closeBrowser()
	oldFind := findScrapeChrome
	t.Cleanup(func() {
		findScrapeChrome = oldFind
		closeBrowser()
	})
	findScrapeChrome = func() (string, error) {
		return "", errors.New("no Chrome/Chromium found: set CHROME_PATH or install Google Chrome")
	}
	server := batch3HTMLServer(t, `<html><body><p>Static text</p></body></html>`)
	registry := batch3TestRegistry(t, server.URL, withBatch3Renderer(browserRenderer))

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/page"})
	if result["browser_unavailable"] != true {
		t.Fatalf("browser_unavailable = %v (%v)", result["browser_unavailable"], result)
	}
	message := fmt.Sprint(result["error"])
	if !strings.Contains(message, "Browser unavailable") || !strings.Contains(message, "CHROME_PATH") {
		t.Fatalf("error = %q", message)
	}
	if !strings.Contains(fmt.Sprint(result["hint"]), "Install Google Chrome") {
		t.Fatalf("hint = %v", result["hint"])
	}
	if _, saved := result["saved_to"]; saved {
		t.Fatalf("missing browser must not fabricate an artifact: %v", result)
	}
}

// Parity-exempt: upstream maps PlaywrightError to "Browser error: ..."; this
// injects a renderer failure so the Go envelope is pinned without a browser.
func TestBatch3WebScrapeRenderFailure(t *testing.T) {
	batch3ScrapeEnv(t)
	server := batch3HTMLServer(t, `<html><body><p>Static text</p></body></html>`)
	registry := batch3TestRegistry(t, server.URL, withBatch3Renderer(
		func(context.Context, string, string) (string, error) {
			return "", errors.New("chrome crashed")
		}))

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/page"})
	if result["error"] != "Browser error: chrome crashed" {
		t.Fatalf("error = %v", result["error"])
	}
	if _, saved := result["saved_to"]; saved {
		t.Fatalf("render failure must not write an artifact: %v", result)
	}
}

// Parity-exempt: upstream maps PlaywrightTimeout to "Request timed out"; the
// renderer deadline must produce the same envelope, never an empty body.
func TestBatch3WebScrapeRenderTimeout(t *testing.T) {
	batch3ScrapeEnv(t)
	server := batch3HTMLServer(t, `<html><body><p>Static text</p></body></html>`)
	registry := batch3TestRegistry(t, server.URL, withBatch3Renderer(
		func(context.Context, string, string) (string, error) {
			return "", fmt.Errorf("page render timed out: %w", context.DeadlineExceeded)
		}))

	result := batch3Execute(t, registry, "web_scrape", map[string]any{"url": "https://example.com/page"})
	if result["error"] != "Request timed out" {
		t.Fatalf("error = %v", result["error"])
	}
	if _, saved := result["saved_to"]; saved {
		t.Fatalf("timeout must not write an artifact: %v", result)
	}
}

// Parity-exempt: the launcher flags mirror upstream's browser.new_context
// (user agent, locale, 1920x1080 viewport) and Playwright's launch args; the
// Python suite has no unit test for launch configuration.
func TestBatch3ScrapeChromeLauncherFlags(t *testing.T) {
	path := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	launch := newChromeLauncher(path)
	if got := launch.Get(flags.Bin); got != path {
		t.Fatalf("launcher bin = %q, want the system Chrome path", got)
	}
	args := launch.FormatArgs()
	wants := []string{
		"--headless=new",
		"--no-sandbox",
		"--disable-setuid-sandbox",
		"--disable-dev-shm-usage",
		"--disable-blink-features=AutomationControlled",
		"--user-agent=" + browserUserAgent,
		"--window-size=1920,1080",
		"--lang=en-US",
	}
	for _, want := range wants {
		found := false
		for _, arg := range args {
			if arg == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("launcher args missing %q: %v", want, args)
		}
	}
}

// Parity-exempt: chrome discovery is infrastructure, not upstream tool logic;
// the upstream equivalent is Playwright bundling its own browser.
func TestBatch3ScrapeChromeDiscovery(t *testing.T) {
	t.Setenv("CHROME_PATH", "/bin/sh")
	if path, err := findScrapeChrome(); err != nil || path != "/bin/sh" {
		t.Fatalf("CHROME_PATH override = %q, %v", path, err)
	}
	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	if _, err := findScrapeChrome(); err == nil || !strings.Contains(err.Error(), "CHROME_PATH") {
		t.Fatalf("missing CHROME_PATH must fail with guidance, got %v", err)
	}
}

// Parity-exempt: the reference is the frozen Python capture
// (go_hive/contracts/tool-definitions.json), not a Python unit test; the
// capture itself is generated from the live Python registry.
func TestBatch3WebScrapeDefinitionMatchesPythonContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v) — this check needs the committed artifact", err)
	}
	type contractTool struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	var manifest struct {
		Tools []contractTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("contract manifest: %v", err)
	}
	var reference *contractTool
	for index := range manifest.Tools {
		if manifest.Tools[index].Name == "web_scrape" {
			reference = &manifest.Tools[index]
			break
		}
	}
	if reference == nil {
		t.Fatal("web_scrape missing from contracts/tool-definitions.json")
	}

	description := batch3ScrapeTool.Description()
	sum := sha256.Sum256([]byte(description))
	t.Logf("web_scrape description: %d bytes, sha256 %x", len(description), sum)
	if description != reference.Description {
		t.Fatalf("description is not the Python contract: got %d bytes, want %d bytes",
			len(description), len(reference.Description))
	}
	for _, param := range []string{"url", "selector", "include_links", "respect_robots_txt"} {
		if _, ok := reference.InputSchema["properties"].(map[string]any)[param]; !ok {
			t.Fatalf("reference schema lost %q: %v", param, reference.InputSchema)
		}
	}
	gotSchema, err := json.Marshal(batch3ScrapeTool.Schema())
	if err != nil {
		t.Fatalf("marshal Go schema: %v", err)
	}
	wantSchema, err := json.Marshal(reference.InputSchema)
	if err != nil {
		t.Fatalf("marshal Python schema: %v", err)
	}
	if string(gotSchema) != string(wantSchema) {
		t.Fatalf("input schema differs from Python contract\n got: %s\nwant: %s", gotSchema, wantSchema)
	}
}

// Parity-exempt: upstream's Playwright is mocked in every Python test; this is
// the opt-in environment check that a real system Chrome renders JavaScript
// through the shared browser. Set HIVE_SCRAPE_CHROME_E2E=1 to run it.
func TestBatch3WebScrapeRealChromeRendersJS(t *testing.T) {
	if os.Getenv("HIVE_SCRAPE_CHROME_E2E") != "1" {
		t.Skip("set HIVE_SCRAPE_CHROME_E2E=1 to run the real-Chrome check")
	}
	if _, err := findScrapeChrome(); err != nil {
		t.Skipf("real Chrome render skipped: %v", err)
	}
	chromePath, _ := findScrapeChrome()
	t.Logf("system browser: %s", chromePath)
	closeBrowser()
	t.Cleanup(closeBrowser)

	staticPage := `<html><body><main><h1>Static</h1><p>Server-rendered text.</p></main></body></html>`
	jsShell := `<html><body><div id="root"></div><noscript>You need to enable JavaScript.</noscript>` +
		`<script>document.getElementById("root").innerHTML = ` +
		`"<main><h1>Rendered</h1><p>Client-side marker: rendered-by-chrome.</p></main>";</script></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/static", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, staticPage)
	})
	mux.HandleFunc("/js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, jsShell)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	staticDOM, err := renderWithBrowser(ctx, server.URL+"/static")
	if err != nil {
		t.Fatalf("render static page: %v", err)
	}
	if body := batch3RenderedBody(t, staticDOM); !strings.Contains(body, "Server-rendered text.") {
		t.Fatalf("static render body = %q", body)
	}

	// JS off: the same shell through the tokenizer has no readable body.
	jsOffBody := batch3RenderedBody(t, jsShell)
	t.Logf("JS off (static tokenizer): body=%q", jsOffBody)
	if jsOffBody != "" {
		t.Fatalf("JS shell must be empty without rendering, got %q", jsOffBody)
	}
	// JS on: the real browser must execute the inline script.
	jsDOM, err := renderWithBrowser(ctx, server.URL+"/js")
	if err != nil {
		t.Fatalf("render JS page: %v", err)
	}
	body := batch3RenderedBody(t, jsDOM)
	t.Logf("JS on (rod + Chrome): body=%q", body)
	if !strings.Contains(body, "Client-side marker: rendered-by-chrome.") {
		t.Fatalf("rendered JS body = %q", body)
	}

	// The shared browser process is reused, not relaunched per call.
	chromePool.mu.Lock()
	first := chromePool.browser
	chromePool.mu.Unlock()
	if first == nil {
		t.Fatal("shared browser missing after a successful render")
	}
	if _, err := first.Version(); err != nil {
		t.Fatalf("shared browser is not reusable: %v", err)
	}
}

// batch3RenderedBody runs the production extraction pipeline over a DOM
// string, which is what web_scrape does after the renderer returns.
func batch3RenderedBody(t *testing.T, dom string) string {
	t.Helper()
	doc := batch3ParseHTML(dom)
	root := batch3DefaultContentRoot(doc)
	if root == nil {
		root = doc
	}
	raw, _ := batch3ExtractText(root, nil, false)
	return batch3CleanText(raw)
}
