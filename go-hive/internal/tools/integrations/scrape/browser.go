// Browser rendering for web_scrape.
//
// The upstream tool renders every page through Playwright + playwright_stealth
// and parses page.content(): page.goto(wait_until="domcontentloaded",
// timeout=30000), a best-effort wait_for_load_state("networkidle", timeout=3000)
// whose timeout is swallowed, then the rendered DOM. This port reproduces that
// pipeline with go-rod/rod over the Chrome DevTools Protocol against the
// system Chrome/Chromium (CGO-free, pure Go): the browser is launched from the
// same executable candidates chart_render probes, one process is reused
// across calls, and go-rod/stealth injects the anti-detection JS on every new
// document.
//
// Stealth coverage. playwright_stealth is a patch bundle; this table is the
// honest comparison against go-rod/stealth v0.4.9 (bundle v2.7.3) plus the
// launcher flags below. "not covered" entries are best-effort gaps, not
// claims of parity:
//
//	patch point                     this port
//	navigator.webdriver             stealth JS
//	chrome.app/runtime/csi/loadTimes stealth JS
//	navigator.plugins / mimeTypes   stealth JS
//	navigator.languages             stealth JS
//	navigator.hardwareConcurrency   stealth JS (4)
//	Notification.permission         stealth JS
//	WebGL vendor/renderer           stealth JS (getParameter proxy)
//	window.outerdimensions          stealth JS
//	iframe.contentWindow            stealth JS
//	media codecs (canPlayType)      stealth JS
//	navigator.vendor                not patched — headless Chrome already
//	                                reports "Google Inc." like headful
//	navigator.permissions.query     NOT covered (only Notification.permission)
//	userAgent                       launcher --user-agent (same string as
//	                                upstream's context.user_agent)
//	locale / Accept-Language        launcher --lang / --accept-lang
//	viewport 1920x1080              launcher --window-size (headless window ==
//	                                Playwright's CSS viewport here)
//	deviceScaleFactor               default 1 on both sides
//	timezone                        host default on both sides (upstream does
//	                                not override it either)
//	WebRTC IP handling              NOT covered (playwright_stealth does not
//	                                patch it either)
//	canvas noise                    NOT covered (same as upstream)
//
// Failure shape: no Chrome/Chromium is an explicit browser_unavailable error
// with install guidance — never a fabricated body. A render that times out or
// fails recycles the browser process and returns an error; the caller maps it
// to the upstream "Request timed out" / "Browser error: ..." envelopes.

package scrape

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/stealth"
)

const (
	// browserUserAgent is the upstream context.user_agent value.
	browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// chromeNavTimeout mirrors upstream page.goto(timeout=30000).
	chromeNavTimeout = 30 * time.Second
	// chromeIdleWait mirrors wait_for_load_state("networkidle",
	// timeout=3000); its timeout is swallowed upstream too.
	chromeIdleWait = 3 * time.Second
	// chromeIdleWindow is the quiet period rod needs to call the network
	// idle. Playwright's networkidle uses the same 500ms convention.
	chromeIdleWindow = 500 * time.Millisecond
	// chromeRenderTimeout bounds launch + navigation + DOM read for one
	// scrape. It sits above goto's 30s so the navigation timeout wins inside
	// the call, like upstream's 30s goto inside the agent-loop budget.
	chromeRenderTimeout = 45 * time.Second
	// chromeHealthCheck bounds the CDP round-trip that decides whether
	// the shared browser is still alive, so a wedged process cannot block the
	// pool mutex indefinitely.
	chromeHealthCheck = 2 * time.Second
)

// chromeCandidates mirrors internal/charts/chrome.go's discovery order:
// $CHROME_PATH, the macOS bundles, then PATH names. The charts package owns
// that list; web_scrape repeats it instead of importing a sibling lane's
// package (see the lane report).
var chromeCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

var chromePathNames = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"chrome",
}

// findScrapeChrome resolves the browser binary. Tests replace it to drive the
// no-browser path without touching the host.
var findScrapeChrome = locateChrome

func locateChrome() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CHROME_PATH")); override != "" {
		path, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("CHROME_PATH %q is not an executable file: %w", override, err)
		}
		return path, nil
	}
	for _, candidate := range chromeCandidates {
		if path, err := exec.LookPath(candidate); err == nil && path != "" {
			return path, nil
		}
	}
	for _, name := range chromePathNames {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found: set CHROME_PATH or install Google Chrome "+
		"(looked for %s and PATH entries %s)",
		strings.Join(chromeCandidates, ", "), strings.Join(chromePathNames, ", "))
}

// browserUnavailableError marks "no usable Chrome/Chromium". Upstream's
// equivalent is Playwright being absent, where the tool is simply not
// registered; a Go catalogue cannot drop a tool at call time, so the call
// returns this explicit, recognizable error instead of faking a result.
type browserUnavailableError struct{ reason string }

func (e *browserUnavailableError) Error() string {
	return "Browser unavailable: " + e.reason
}

// browserPool owns the one headless browser process shared by every
// scrape call (lazy start, mutex-guarded, recycled on timeout/failure).
type browserPool struct {
	mu       sync.Mutex
	browser  *rod.Browser
	launcher *launcher.Launcher
}

var chromePool browserPool

// newChromeLauncher builds the launcher for a system Chrome binary.
// Every flag here is a deliberate Playwright-parity or stealth choice; the
// binary is always set explicitly so launcher never falls back to rod's
// browser download.
func newChromeLauncher(chromePath string) *launcher.Launcher {
	return launcher.New().
		Bin(chromePath).
		HeadlessNew(true).
		Leakless(true).
		Set("no-sandbox").
		Set("disable-setuid-sandbox").
		Set("disable-dev-shm-usage").
		Set("disable-blink-features", "AutomationControlled").
		Set("user-agent", browserUserAgent).
		Set("window-size", "1920", "1080").
		Set("lang", "en-US").
		Set("accept-lang", "en-US,en")
}

func (pool *browserPool) acquire(ctx context.Context) (*rod.Browser, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.browser != nil {
		// A cheap CDP round-trip tells us whether the process is still alive;
		// a dead browser is replaced instead of being handed out.
		healthCtx, cancel := context.WithTimeout(ctx, chromeHealthCheck)
		_, err := pool.browser.Context(healthCtx).Version()
		cancel()
		if err == nil {
			return pool.browser, nil
		}
		pool.closeLocked()
	}
	chrome, err := findScrapeChrome()
	if err != nil {
		return nil, &browserUnavailableError{reason: err.Error()}
	}
	launch := newChromeLauncher(chrome).Context(ctx)
	controlURL, err := launch.Launch()
	if err != nil {
		launch.Kill()
		return nil, &browserUnavailableError{reason: "Chrome could not be launched: " + err.Error()}
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		launch.Kill()
		launch.Cleanup()
		return nil, &browserUnavailableError{reason: "Chrome could not be reached over CDP: " + err.Error()}
	}
	pool.browser = browser
	pool.launcher = launch
	return browser, nil
}

// closeLocked shuts the shared browser down and removes its profile. Callers
// hold pool.mu.
func (pool *browserPool) closeLocked() {
	if pool.browser != nil {
		_ = pool.browser.Close()
		pool.browser = nil
	}
	if pool.launcher != nil {
		pool.launcher.Kill()
		pool.launcher.Cleanup()
		pool.launcher = nil
	}
}

// recycle drops the shared browser so the next call starts a fresh process.
func (pool *browserPool) recycle() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.closeLocked()
}

// closeBrowser is the test/shutdown hook for the shared browser.
func closeBrowser() {
	chromePool.recycle()
}

// browserRenderer adapts the shared headless browser to the scrape
// renderer signature; staticHTML exists only for the test seam.
func browserRenderer(ctx context.Context, target, _ string) (string, error) {
	return renderWithBrowser(ctx, target)
}

// renderWithBrowser renders target and returns the post-JavaScript DOM.
func renderWithBrowser(ctx context.Context, target string) (string, error) {
	renderCtx, cancel := context.WithTimeout(ctx, chromeRenderTimeout)
	defer cancel()
	if err := renderCtx.Err(); err != nil {
		return "", fmt.Errorf("render budget already exhausted: %w", context.DeadlineExceeded)
	}
	browser, err := chromePool.acquire(renderCtx)
	if err != nil {
		return "", err
	}
	html, err := renderHTML(renderCtx, browser, target)
	if err != nil {
		if renderCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errTimeout(err) {
			chromePool.recycle()
			return "", fmt.Errorf("page render timed out: %w", context.DeadlineExceeded)
		}
		return "", err
	}
	return html, nil
}

// renderHTML drives one navigation on a fresh page and reads the DOM.
func renderHTML(ctx context.Context, browser *rod.Browser, target string) (string, error) {
	page, err := stealth.Page(browser)
	if err != nil {
		return "", fmt.Errorf("could not open a browser page: %w", err)
	}
	defer func() {
		// Close with a fresh context: navCtx is cancelled by the defer below,
		// and a CDP call on a cancelled context would leave the tab open in
		// the shared browser.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), chromeHealthCheck)
		defer closeCancel()
		_ = page.Context(closeCtx).Close()
	}()

	navCtx, cancel := context.WithTimeout(ctx, chromeNavTimeout)
	defer cancel()
	page = page.Context(navCtx)

	// Playwright: page.goto(url, wait_until="domcontentloaded"). rod's
	// Navigate returns once the response headers arrive, so wait for
	// readyState to leave "loading" — the DOMContentLoaded boundary.
	if err := page.Navigate(target); err != nil {
		return "", fmt.Errorf("navigation failed: %w", err)
	}
	if err := page.Wait(rod.Eval(`() => document.readyState !== "loading"`)); err != nil {
		if navCtx.Err() != nil {
			return "", fmt.Errorf("page did not reach domcontentloaded in %s: %w", chromeNavTimeout, context.DeadlineExceeded)
		}
		return "", fmt.Errorf("waiting for domcontentloaded failed: %w", err)
	}

	// Playwright: wait_for_load_state("networkidle", timeout=3000) with the
	// timeout swallowed. The DOM is taken as-is when the quiet window expires.
	idleCtx, cancelIdle := context.WithTimeout(ctx, chromeIdleWait)
	page.Context(idleCtx).WaitRequestIdle(chromeIdleWindow, nil, nil, nil)()
	cancelIdle()

	html, err := page.Context(ctx).HTML()
	if err != nil {
		return "", fmt.Errorf("could not read the rendered DOM: %w", err)
	}
	return html, nil
}
