package charts

// Headless Chrome backend for chart_render.
//
// The Python tool drives Chromium through Playwright: set_content, inject the
// bundled ECharts/Mermaid script, evaluate the render call, wait for
// window.__chartReady, then screenshot #chart at device_scale_factor dpi/96.
// This port reproduces that with the Chrome binary's own headless mode: a
// temporary page next to the embedded library, --window-size for the logical
// viewport, --force-device-scale-factor for the DPR, --virtual-time-budget so
// the page finishes rendering before the shot, and --dump-dom so the page's
// success/error marker travels back with the screenshot.
//
// No Chrome means an explicit error — never a fabricated PNG.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// chromeRenderTimeout bounds one Chrome invocation. The default
	// 1600x900 @ 300 dpi render is a 5000x2813 PNG; two minutes is far
	// above the measured ~1s cold start.
	chromeRenderTimeout = 120 * time.Second
	// virtualTimeBudgetMS lets timers/rAF advance instantly before the
	// screenshot, so charts are never captured mid-layout.
	virtualTimeBudgetMS = 10000
	// chromeStderrTail is how much Chrome stderr an error message keeps.
	chromeStderrTail = 600
)

// Hooks so tests can drive the Chrome path without depending on the host.
var (
	chartLookPath  = exec.LookPath
	chartRunChrome = func(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
)

// chromeCandidates are the executable paths probed in order after
// $CHROME_PATH. The macOS bundle path matches upstream Hive's desktop install.
var chromeCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// chromePathNames are probed through PATH for Linux/Windows-style installs.
var chromePathNames = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"chrome",
}

// findChrome resolves the browser binary, honouring $CHROME_PATH first.
func findChrome() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CHROME_PATH")); override != "" {
		path, err := chartLookPath(override)
		if err != nil {
			return "", fmt.Errorf("CHROME_PATH %q is not an executable file: %w", override, err)
		}
		return path, nil
	}
	for _, candidate := range chromeCandidates {
		if path, err := chartLookPath(candidate); err == nil && path != "" {
			return path, nil
		}
	}
	for _, name := range chromePathNames {
		if path, err := chartLookPath(name); err == nil && path != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found: set CHROME_PATH or install Google Chrome "+
		"(looked for %s and PATH entries %s)",
		strings.Join(chromeCandidates, ", "), strings.Join(chromePathNames, ", "))
}

// renderWithChrome renders one spec and returns the PNG bytes.
func renderWithChrome(ctx context.Context, kind string, spec any, width, height, dpi int, theme string) ([]byte, error) {
	chrome, err := findChrome()
	if err != nil {
		return nil, &RenderError{Message: err.Error()}
	}
	page, err := buildPage(kind, spec, width, height, theme)
	if err != nil {
		return nil, err
	}

	workDir, err := os.MkdirTemp("", "hive-chart-")
	if err != nil {
		return nil, &RenderError{Message: "could not create render temp dir: " + err.Error()}
	}
	defer os.RemoveAll(workDir)

	htmlPath := filepath.Join(workDir, "chart.html")
	if err := os.WriteFile(htmlPath, []byte(page), 0o644); err != nil {
		return nil, &RenderError{Message: "could not write render page: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(workDir, libraryFileName(kind)), libraryBytes(kind), 0o644); err != nil {
		return nil, &RenderError{Message: "could not write bundled " + kind + " library: " + err.Error()}
	}
	pngPath := filepath.Join(workDir, "chart.png")

	args := chromeArgs(workDir, pngPath, htmlPath, width, height, dpi)
	runCtx, cancel := context.WithTimeout(ctx, chromeRenderTimeout)
	defer cancel()
	stdout, stderr, runErr := chartRunChrome(runCtx, chrome, args)

	if runErr != nil {
		// Chrome's teardown watchdog can exit nonzero *after* the page already
		// reported its outcome (macOS flakes: "Teardown watchdog expired").
		// When the page itself reported a render failure, that deterministic
		// message is what upstream's RendererError surfaces; otherwise this
		// stays a chrome-level failure.
		if status, message := pageOutcome(stdout); status == "error" {
			return nil, &RenderError{Message: chartKindLabel(kind) + " render failed: " + message}
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, &RenderError{Message: fmt.Sprintf("chrome render timed out after %s", chromeRenderTimeout)}
		}
		return nil, &RenderError{Message: "chrome render failed: " + runErr.Error() + stderrSuffix(stderr)}
	}

	status, message := pageOutcome(stdout)
	switch status {
	case "ok":
	case "error":
		return nil, &RenderError{Message: chartKindLabel(kind) + " render failed: " + message}
	default:
		return nil, &RenderError{Message: "chrome page did not report a render status" + stderrSuffix(stderr)}
	}

	data, readErr := os.ReadFile(pngPath)
	if readErr != nil {
		return nil, &RenderError{Message: "chrome reported success but wrote no screenshot: " + readErr.Error() + stderrSuffix(stderr)}
	}
	return data, nil
}

// chromeArgs builds the headless flag set. Kept separate so tests can assert
// the viewport/dpi contract without launching a browser.
func chromeArgs(workDir, pngPath, htmlPath string, width, height, dpi int) []string {
	scale := float64(dpi) / 96.0
	if scale < 1 {
		scale = 1
	}
	return []string{
		"--headless=new",
		"--disable-gpu",
		// Playwright launches with these two for root/container safety;
		// harmless on a desktop box.
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--hide-scrollbars",
		"--mute-audio",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--user-data-dir=" + filepath.Join(workDir, "chrome-profile"),
		"--window-size=" + strconv.Itoa(width) + "," + strconv.Itoa(height),
		"--force-device-scale-factor=" + strconv.FormatFloat(scale, 'f', -1, 64),
		"--virtual-time-budget=" + strconv.Itoa(virtualTimeBudgetMS),
		"--dump-dom",
		"--screenshot=" + pngPath,
		FileURL(htmlPath),
	}
}

var (
	chartStatusPattern = regexp.MustCompile(`data-chart-status="([^"]*)"`)
	chartErrorPattern  = regexp.MustCompile(`data-chart-error="([^"]*)"`)
	// The dump starts with <!DOCTYPE html><html ...>, so the markers are in
	// the first few KB even when the rendered SVG makes the DOM huge.
	chartDOMScanLimit = 32 * 1024
)

// pageOutcome reads the success/error marker the page script set on <html>.
func pageOutcome(stdout []byte) (status, message string) {
	prefix := stdout
	if len(prefix) > chartDOMScanLimit {
		prefix = prefix[:chartDOMScanLimit]
	}
	if match := chartStatusPattern.FindSubmatch(prefix); match != nil {
		status = html.UnescapeString(string(match[1]))
	}
	if match := chartErrorPattern.FindSubmatch(prefix); match != nil {
		message = html.UnescapeString(string(match[1]))
		// A render error is final: ECharts' 'finished' handler can race the
		// catch and leave status "ok" next to the error text, but upstream's
		// evaluate returns {ok:false} regardless of what happens later.
		if status == "" || status == "ok" {
			status = "error"
		}
	}
	if status == "" && message != "" {
		status = "error"
	}
	return status, message
}

func stderrSuffix(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	if len(text) > chromeStderrTail {
		text = text[len(text)-chromeStderrTail:]
	}
	return "; chrome stderr: " + text
}

func chartKindLabel(kind string) string {
	if kind == "mermaid" {
		return "Mermaid"
	}
	return "ECharts"
}
