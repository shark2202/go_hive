package charts

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ── test helpers ─────────────────────────────────────────────────────

func chartsTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_DESKTOP_THEME", "light")
	t.Setenv("CHROME_PATH", "")
	return home
}

func requireChrome(t *testing.T) {
	t.Helper()
	if _, err := findChrome(); err != nil {
		t.Skipf("real Chrome render skipped: %v", err)
	}
}

// stubChrome replaces the lookup and run hooks. The handler receives the argv
// and is responsible for writing the screenshot file and returning the DOM
// dump Chrome would have printed.
func stubChrome(t *testing.T, handler func(args []string) (stdout, stderr []byte, err error)) *[]string {
	t.Helper()
	oldLook, oldRun := chartLookPath, chartRunChrome
	var captured []string
	t.Cleanup(func() {
		chartLookPath = oldLook
		chartRunChrome = oldRun
	})
	chartLookPath = func(name string) (string, error) { return "/stub/chrome", nil }
	chartRunChrome = func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		captured = append([]string{}, args...)
		return handler(args)
	}
	return &captured
}

func writeStubPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(10, 10, width/2, height/2), &image.Uniform{C: color.RGBA{R: 219, G: 111, B: 2, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func okDOM() []byte {
	return []byte(`<!DOCTYPE html><html data-chart-status="ok"><body><div id="chart"></div></body></html>`)
}

// pngStats decodes a PNG and reports the pixel histogram, enough for a
// text-first visual check: a chart renders as many distinct colors and a large
// share of non-background pixels.
func pngStats(t *testing.T, path string) (width, height, distinct, nonBackground int, counts map[color.RGBA]int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	bounds := img.Bounds()
	counts = map[color.RGBA]int{}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			counts[color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}]++
		}
	}
	background := 0
	for _, count := range counts {
		if count > background {
			background = count
		}
	}
	return bounds.Dx(), bounds.Dy(), len(counts), bounds.Dx()*bounds.Dy() - background, counts
}

func mustRGBA(t *testing.T, hex string) color.RGBA {
	t.Helper()
	value, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		t.Fatalf("bad color %q: %v", hex, err)
	}
	return color.RGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 255}
}

// ── theme / path rules ───────────────────────────────────────────────

// Parity: tools/src/chart_tools/theme.py::build_theme
func TestThemeMatchesUpstreamJSON(t *testing.T) {
	for _, name := range []string{"light", "dark"} {
		fixture, err := os.ReadFile(filepath.Join("testdata", "theme-"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want, got any
		if err := json.Unmarshal(fixture, &want); err != nil {
			t.Fatalf("%s fixture: %v", name, err)
		}
		if err := json.Unmarshal([]byte(ThemeJSON(name)), &got); err != nil {
			t.Fatalf("%s ThemeJSON: %v", name, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("%s theme differs from upstream theme.py:\nwant %#v\ngot  %#v", name, want, got)
		}
	}
}

// Parity: tools/src/chart_tools/tools.py::_system_theme
func TestResolveThemeNameFollowsDesktopEnv(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", "light"},
		{"dark", "dark"},
		{"  DARK ", "dark"},
		{"Dark", "dark"},
		{"light", "light"},
		{"solarized", "light"},
	}
	for _, test := range cases {
		t.Setenv("HIVE_DESKTOP_THEME", test.env)
		if got := ResolveThemeName(); got != test.want {
			t.Fatalf("HIVE_DESKTOP_THEME=%q => %q, want %q", test.env, got, test.want)
		}
	}
}

// Parity: tools/src/chart_tools/tools.py::_slugify
func TestSlugifyMirrorsUpstream(t *testing.T) {
	cases := map[string]string{
		"":                  "chart",
		"   ":               "chart",
		"Quarterly Revenue": "quarterly-revenue",
		"  -- weird!!  ":    "weird",
		"Chart 2024 (v2)":   "chart-2024-v2",
	}
	for input, want := range cases {
		if got := Slugify(input); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity: tools/src/chart_tools/tools.py::_resolve_output_path
func TestOutputPathDefaultsUnderChartsDir(t *testing.T) {
	home := chartsTestHome(t)
	path, err := ResolveOutputPath("", "Quarterly Revenue", "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(home, "charts") {
		t.Fatalf("dir = %s", filepath.Dir(path))
	}
	if !strings.HasSuffix(filepath.Base(path), "-quarterly-revenue.png") {
		t.Fatalf("filename = %s", filepath.Base(path))
	}
	// <UTC-timestamp>-<slug>.png, e.g. 2026-11-20T10-30-00-quarterly-revenue.png
	if len(filepath.Base(path)) != len("2006-01-02T15-04-05")+1+len("quarterly-revenue")+4 {
		t.Fatalf("filename shape = %s", filepath.Base(path))
	}
	if _, err := checkOverridePath(t, home); err != nil {
		t.Fatal(err)
	}
}

// checkOverridePath checks the override branch creates the parent dir.
func checkOverridePath(t *testing.T, home string) (string, error) {
	t.Helper()
	target := filepath.Join(home, "nested", "custom.png")
	path, err := ResolveOutputPath(target, "ignored", "")
	if err != nil {
		return "", err
	}
	if path != target {
		t.Fatalf("override path = %s, want %s", path, target)
	}
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Fatalf("override parent dir missing: %v", err)
	}
	return path, nil
}

// Parity-exempt: the pixel-size rule (dpi/96, clamped at 1x) has no direct
// upstream unit test; it is exercised through the Chrome flag tests.
func TestPixelSizeScalesByDPI(t *testing.T) {
	if width, height := pixelSize(1600, 900, 300); width != 5000 || height != 2813 {
		t.Fatalf("pixelSize(1600x900@300) = %dx%d", width, height)
	}
	if width, height := pixelSize(600, 400, 96); width != 600 || height != 400 {
		t.Fatalf("pixelSize(600x400@96) = %dx%d", width, height)
	}
	if width, height := pixelSize(600, 400, 48); width != 600 || height != 400 {
		t.Fatalf("pixelSize(600x400@48) should clamp to 1x, got %dx%d", width, height)
	}
}

// ── argument validation / error envelopes ────────────────────────────

// jsonKeyOrder decodes a JSON object and returns its keys in document order.
func jsonKeyOrder(t *testing.T, raw string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		t.Fatalf("not a JSON object: %q", raw)
	}
	keys := []string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("non-string key %v in %q", keyToken, raw)
		}
		keys = append(keys, key)
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestRenderEChartsInvalidKindReturnsError(t *testing.T) {
	chartsTestHome(t)
	result := Envelope(context.Background(), Options{Kind: "bogus", Spec: map[string]any{}, Title: "x"})
	if result["error"] == nil {
		t.Fatalf("expected an error, got %#v", result)
	}
	if result["kind"] != "bogus" {
		t.Fatalf("kind = %v", result["kind"])
	}
	want := "kind must be 'echarts' or 'mermaid', got 'bogus'"
	if message, _ := result["error"].(string); message != want {
		t.Fatalf("error = %q, want %q", message, want)
	}
	if result["retried"] != nil {
		t.Fatalf("validation errors must not report retried: %#v", result)
	}
	if len(result) != 3 {
		t.Fatalf("error envelope fields = %d, want 3 (error, kind, spec): %#v", len(result), result)
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestInvalidKindMessageUsesPythonRepr(t *testing.T) {
	chartsTestHome(t)
	prefix := "kind must be 'echarts' or 'mermaid', got "
	cases := []struct {
		kind string
		want string
	}{
		{"bogus", "'bogus'"},
		{"it's", `"it's"`},
		{`a'b"c`, `'a\'b"c'`},
		{"a\nb", `'a\nb'`},
		{"", "''"},
	}
	for _, test := range cases {
		result := Envelope(context.Background(), Options{Kind: test.kind, Spec: map[string]any{}})
		if message, _ := result["error"].(string); message != prefix+test.want {
			t.Fatalf("kind %q => %q, want %q", test.kind, message, prefix+test.want)
		}
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestInvalidKindWinsOverBadSizes(t *testing.T) {
	chartsTestHome(t)
	// Upstream checks kind first, before it ever coerces width/height/dpi.
	result := Envelope(context.Background(), Options{Kind: "bogus", Spec: map[string]any{}, Width: -5, Height: -5, DPI: -5})
	want := "kind must be 'echarts' or 'mermaid', got 'bogus'"
	if message, _ := result["error"].(string); message != want {
		t.Fatalf("error = %q, want the kind error %q", message, want)
	}
}

// TestRenderEChartsStringSpecInvalidJSONReturnsError pins the upstream frame
// (`spec was a string but not valid JSON: … Pass the spec as a dict …`); the
// decoder's own detail text differs between json.loads and encoding/json.
//
// Parity: tools/src/chart_tools/tools.py::chart_render
func TestRenderEChartsStringSpecInvalidJSONReturnsError(t *testing.T) {
	chartsTestHome(t)
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: "not-json", Title: "x"})
	message, _ := result["error"].(string)
	if !strings.HasPrefix(message, "spec was a string but not valid JSON: ") ||
		!strings.HasSuffix(message, ". Pass the spec as a dict, not a JSON-encoded string.") {
		t.Fatalf("error = %q", message)
	}
	if result["spec"] != "not-json" {
		t.Fatalf("spec echo = %#v", result["spec"])
	}
	if result["retried"] != nil {
		t.Fatalf("validation errors must not report retried: %#v", result)
	}
	if len(result) != 3 {
		t.Fatalf("error envelope fields = %d, want 3: %#v", len(result), result)
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestSmallPNGErrorMatchesUpstreamWording(t *testing.T) {
	chartsTestHome(t)
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screenshot=") {
				if err := os.WriteFile(strings.TrimPrefix(arg, "--screenshot="), []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return okDOM(), nil, nil
	})
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: map[string]any{}, Title: "tiny"})
	want := "render produced suspiciously small PNG (8 bytes)"
	if message, _ := result["error"].(string); message != want {
		t.Fatalf("error = %q, want %q", message, want)
	}
	if result["retried"] != nil {
		t.Fatalf("small-PNG errors must not report retried: %#v", result)
	}
}

// Parity-exempt: Go-only coercion guard; FastMCP rejects these shapes before
// the Python tool body runs.
func TestRenderRejectsBadDimensions(t *testing.T) {
	chartsTestHome(t)
	result := ExecuteToolJSON(t, map[string]any{"kind": "echarts", "spec": map[string]any{}, "width": "wide"})
	if !strings.Contains(result["error"].(string), "width: must be an integer") {
		t.Fatalf("error = %v", result["error"])
	}
	if result["kind"] != "echarts" {
		t.Fatalf("kind = %v", result["kind"])
	}
}

// ExecuteToolJSON runs ExecuteTool and decodes the envelope.
func ExecuteToolJSON(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	content, isError := ExecuteTool(context.Background(), input, t.TempDir())
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("tool content is not JSON (%q): %v", content, err)
	}
	_, hasError := envelope["error"]
	if hasError != isError {
		t.Fatalf("isError=%v but error field present=%v (%s)", isError, hasError, content)
	}
	return envelope
}

// Parity-exempt: Go-specific guard for a missing Chrome; upstream Playwright
// raises the same class of error but there is no Python test for it.
func TestNoChromeReturnsExplicitError(t *testing.T) {
	home := chartsTestHome(t)
	oldLook := chartLookPath
	t.Cleanup(func() { chartLookPath = oldLook })
	chartLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	result := Envelope(context.Background(), Options{
		Kind: "echarts", Spec: map[string]any{"series": []any{}}, Title: "nochrome",
	})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "no Chrome/Chromium found") || !strings.Contains(message, "CHROME_PATH") {
		t.Fatalf("error = %q", message)
	}
	if result["retried"] != true {
		t.Fatalf("failed render should mark retried: %#v", result)
	}
	// No fake PNG may be left behind.
	entries, err := os.ReadDir(filepath.Join(home, "charts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no-chrome run wrote %d files: %v", len(entries), entries)
	}
}

// Parity-exempt: browser-side message passthrough has no Python unit test;
// the markup channel (data-chart-error) is a Go-renderer detail.
func TestPageErrorSurfacesInEnvelope(t *testing.T) {
	home := chartsTestHome(t)
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		return []byte(`<!DOCTYPE html><html data-chart-status="error" ` +
			`data-chart-error="Cannot read property &quot;series&quot; of undefined">`), nil, nil
	})
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: map[string]any{}, Title: "boom"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, `ECharts render failed: Cannot read property "series" of undefined`) {
		t.Fatalf("error = %q", message)
	}
	if result["retried"] != true {
		t.Fatalf("expected retried marker, got %#v", result)
	}
	entries, err := os.ReadDir(filepath.Join(home, "charts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed render wrote %d files", len(entries))
	}
}

// Parity: tools/src/chart_tools/renderer.py::_render_in_page
func TestPageOutcomeErrorWinsOverRacingOkStatus(t *testing.T) {
	// ECharts can fire 'finished' after the catch recorded the failure; the
	// dump then carries status="ok" next to the error text. Upstream's
	// evaluate returns {ok:false} regardless, so the error must win.
	status, message := pageOutcome([]byte(`<!DOCTYPE html><html data-chart-status="ok" data-chart-error="TypeError: boom">`))
	if status != "error" || message != "TypeError: boom" {
		t.Fatalf("pageOutcome = (%q, %q), want (error, TypeError: boom)", status, message)
	}
	// A clean success is untouched.
	status, message = pageOutcome(okDOM())
	if status != "ok" || message != "" {
		t.Fatalf("clean success = (%q, %q)", status, message)
	}
}

// Parity: tools/src/chart_tools/renderer.py::_render_in_page
func TestRacedPageErrorSurfacesAsRenderFailure(t *testing.T) {
	chartsTestHome(t)
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		return []byte(`<!DOCTYPE html><html data-chart-status="ok" ` +
			`data-chart-error="TypeError: Cannot read properties of undefined (reading &quot;get&quot;)">`), nil, nil
	})
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: map[string]any{}, Title: "raced"})
	want := `ECharts render failed: TypeError: Cannot read properties of undefined (reading "get")`
	if message, _ := result["error"].(string); message != want {
		t.Fatalf("error = %q, want %q", message, want)
	}
	if result["retried"] != true {
		t.Fatalf("expected retried marker: %#v", result)
	}
}

// ── Chrome invocation contract ───────────────────────────────────────

// Parity-exempt: the flag set is the Go replacement for Playwright's
// new_context(viewport, device_scale_factor); no Python test asserts CLI flags.
func TestChromeArgsCarryViewportScaleAndBudget(t *testing.T) {
	home := chartsTestHome(t)
	var captured *[]string
	captured = stubChrome(t, func(args []string) ([]byte, []byte, error) {
		screenshot := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screenshot=") {
				screenshot = strings.TrimPrefix(arg, "--screenshot=")
			}
		}
		if screenshot == "" {
			t.Error("no --screenshot flag")
		}
		writeStubPNG(t, screenshot, 600, 400)
		return okDOM(), nil, nil
	})

	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: map[string]any{}, Title: "flags", Width: 600, Height: 400, DPI: 300})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	args := strings.Join(*captured, " ")
	for _, want := range []string{
		"--headless=new",
		"--window-size=600,400",
		"--force-device-scale-factor=3.125",
		"--virtual-time-budget=10000",
		"--dump-dom",
		"--screenshot=",
		"file://",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("chrome args missing %q: %s", want, args)
		}
	}
	// dpi below 96 clamps to 1x, matching renderer.py's max(1.0, dpi/96).
	result = Envelope(context.Background(), Options{Kind: "echarts", Spec: map[string]any{}, Title: "clamp", Width: 600, Height: 400, DPI: 48})
	if result["error"] == nil && !strings.Contains(strings.Join(*captured, " "), "--force-device-scale-factor=1 ") {
		t.Fatalf("48 dpi should clamp to 1x: %s", strings.Join(*captured, " "))
	}
	// The rendered page must sit next to the embedded library.
	if !strings.Contains(strings.Join(*captured, " "), "chart.html") {
		t.Fatalf("chrome args missing page path: %s", strings.Join(*captured, " "))
	}
	_ = home
}

// Parity-exempt: envelope shape guard for the Go tool channel; upstream
// returns a dict, Go returns its JSON encoding.
func TestExecuteToolSerializesEnvelope(t *testing.T) {
	chartsTestHome(t)
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screenshot=") {
				writeStubPNG(t, strings.TrimPrefix(arg, "--screenshot="), 600, 400)
			}
		}
		return okDOM(), nil, nil
	})
	envelope := ExecuteToolJSON(t, map[string]any{
		"kind": "echarts", "spec": map[string]any{"series": []any{}}, "title": "Envelope Check", "dpi": float64(96),
	})
	for _, key := range []string{"kind", "spec", "title", "file_path", "file_url", "width", "height", "dpi", "bytes", "runtime_ms"} {
		if _, ok := envelope[key]; !ok {
			t.Fatalf("envelope missing %q: %#v", key, envelope)
		}
	}
	if _, hasTheme := envelope["theme"]; hasTheme {
		t.Fatalf("theme is renderer input, not an envelope field: %#v", envelope)
	}
	if envelope["title"] != "Envelope Check" {
		t.Fatalf("title = %v", envelope["title"])
	}
	if !strings.HasSuffix(envelope["file_path"].(string), "-envelope-check.png") {
		t.Fatalf("file_path = %v", envelope["file_path"])
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestSuccessEnvelopeMatchesUpstreamFieldOrder(t *testing.T) {
	chartsTestHome(t)
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screenshot=") {
				writeStubPNG(t, strings.TrimPrefix(arg, "--screenshot="), 600, 400)
			}
		}
		return okDOM(), nil, nil
	})
	content, isError := ExecuteTool(context.Background(), map[string]any{
		"kind": "echarts", "spec": map[string]any{"series": []any{}}, "title": "order", "dpi": float64(96),
	}, t.TempDir())
	if isError {
		t.Fatalf("unexpected tool error: %s", content)
	}
	// Python's chart_render returns this dict, and JSON-RPC preserves its order.
	want := []string{"kind", "spec", "file_path", "file_url", "width", "height", "dpi", "bytes", "title", "runtime_ms"}
	if got := jsonKeyOrder(t, content); !reflect.DeepEqual(got, want) {
		t.Fatalf("success field order = %v, want %v", got, want)
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestErrorEnvelopeMatchesUpstreamFieldOrder(t *testing.T) {
	chartsTestHome(t)
	// Validation error: {"error", "kind", "spec"} with no retried marker.
	content, isError := ExecuteTool(context.Background(), map[string]any{
		"kind": "bogus", "spec": map[string]any{"series": []any{}},
	}, t.TempDir())
	if !isError {
		t.Fatalf("invalid kind reported success: %s", content)
	}
	if got := jsonKeyOrder(t, content); !reflect.DeepEqual(got, []string{"error", "kind", "spec"}) {
		t.Fatalf("error field order = %v", got)
	}
	if !strings.Contains(content, `"error":"kind must be 'echarts' or 'mermaid', got 'bogus'"`) {
		t.Fatalf("error text = %s", content)
	}

	// Render failure after the retry: retried is appended last, as upstream does.
	stubChrome(t, func(args []string) ([]byte, []byte, error) {
		return []byte(`<!DOCTYPE html><html data-chart-status="error" data-chart-error="boom">`), nil, nil
	})
	content, isError = ExecuteTool(context.Background(), map[string]any{
		"kind": "echarts", "spec": map[string]any{}, "title": "retry",
	}, t.TempDir())
	if !isError {
		t.Fatalf("render failure reported success: %s", content)
	}
	if got := jsonKeyOrder(t, content); !reflect.DeepEqual(got, []string{"error", "kind", "spec", "retried"}) {
		t.Fatalf("retried error field order = %v (%s)", got, content)
	}
}

// Parity: tools/src/chart_tools/tools.py::chart_render
func TestToolDefMatchesUpstreamContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var manifest struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	var reference *struct {
		Description string
		InputSchema map[string]any
	}
	for _, tool := range manifest.Tools {
		if tool.Name == ToolName {
			reference = &struct {
				Description string
				InputSchema map[string]any
			}{tool.Description, tool.InputSchema}
			break
		}
	}
	if reference == nil {
		t.Fatalf("%s missing from the contract manifest", ToolName)
	}

	def := ToolDef()
	if def.Name != ToolName {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Description != reference.Description {
		t.Fatalf("description differs from the upstream reference:\nwant %q\ngot  %q", reference.Description, def.Description)
	}
	if got, want := canonicalJSON(t, def.Parameters), canonicalJSON(t, reference.InputSchema); !reflect.DeepEqual(got, want) {
		t.Fatalf("input_schema differs from the upstream reference:\nwant %#v\ngot  %#v", want, got)
	}
}

// canonicalJSON round-trips through encoding/json so Go ints and the JSON
// decoder's float64 compare as the same JSON value.
func canonicalJSON(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// ── real Chrome renders ──────────────────────────────────────────────

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_bar_chart
func TestRenderEChartsBarChart(t *testing.T) {
	home := chartsTestHome(t)
	requireChrome(t)

	spec := map[string]any{
		"title":  map[string]any{"text": "Smoke test"},
		"xAxis":  map[string]any{"type": "category", "data": []any{"A", "B", "C"}},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": "bar", "data": []any{12, 24, 6}}},
	}
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: spec, Title: "smoke", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["kind"] != "echarts" {
		t.Fatalf("kind = %v", result["kind"])
	}
	echo, ok := result["spec"].(map[string]any)
	if !ok || echo["title"] == nil {
		t.Fatalf("spec echo = %#v", result["spec"])
	}
	if result["width"] != 600 || result["height"] != 400 {
		t.Fatalf("dims = %v x %v", result["width"], result["height"])
	}
	if bytesValue, _ := result["bytes"].(int); bytesValue <= 1000 {
		t.Fatalf("bytes = %v, want > 1000", result["bytes"])
	}
	if result["theme"] != nil {
		t.Fatalf("envelope must not carry theme: %#v", result)
	}

	path := result["file_path"].(string)
	if filepath.Dir(path) != filepath.Join(home, "charts") {
		t.Fatalf("dir = %s", filepath.Dir(path))
	}
	if !strings.Contains(filepath.Base(path), "smoke") {
		t.Fatalf("filename = %s", filepath.Base(path))
	}
	if result["file_url"].(string) != FileURL(path) {
		t.Fatalf("file_url = %v", result["file_url"])
	}

	width, height, distinct, nonBackground, counts := pngStats(t, path)
	if width != 600 || height != 400 {
		t.Fatalf("png = %dx%d, want 600x400", width, height)
	}
	if distinct < 20 || nonBackground < 2000 {
		t.Fatalf("png looks blank: %d colors, %d non-background pixels", distinct, nonBackground)
	}
	// The primary palette color must actually be painted (the bars).
	if got := counts[mustRGBA(t, "#db6f02")]; got < 1000 {
		t.Fatalf("brand color pixels = %d, want > 1000", got)
	}
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_accepts_string_spec
func TestRenderEChartsAcceptsStringSpec(t *testing.T) {
	chartsTestHome(t)
	requireChrome(t)

	spec := map[string]any{
		"title":  map[string]any{"text": "String-spec regression"},
		"xAxis":  map[string]any{"type": "category", "data": []any{"A", "B", "C"}},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": "bar", "data": []any{1, 2, 3}}},
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: string(encoded), Title: "string-spec", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if bytesValue, _ := result["bytes"].(int); bytesValue <= 1000 {
		t.Fatalf("bytes = %v", result["bytes"])
	}
	echo, ok := result["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec echoed as %T, want object", result["spec"])
	}
	if echo["title"] == nil {
		t.Fatalf("spec = %#v", echo)
	}
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_mermaid_flowchart
func TestRenderMermaidFlowchart(t *testing.T) {
	chartsTestHome(t)
	requireChrome(t)

	src := "flowchart LR\nA[Start] --> B{ok?}\nB -- yes --> C[Done]\nB -- no --> D[Retry]\n"
	result := Envelope(context.Background(), Options{Kind: "mermaid", Spec: src, Title: "flow", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["spec"] != src {
		t.Fatalf("spec echo = %#v", result["spec"])
	}
	path := result["file_path"].(string)
	_, _, distinct, nonBackground, _ := pngStats(t, path)
	if distinct < 20 || nonBackground < 2000 {
		t.Fatalf("mermaid png looks blank: %d colors, %d non-background pixels", distinct, nonBackground)
	}
}

// Parity-exempt: theme selection through HIVE_DESKTOP_THEME is covered by the
// Python smoke only indirectly (the fixture pins light); the dark PNG is
// verified here so the env plumbing has real evidence.
func TestRenderMermaidDarkTheme(t *testing.T) {
	chartsTestHome(t)
	requireChrome(t)
	t.Setenv("HIVE_DESKTOP_THEME", "dark")

	src := "flowchart TD\nX[Alpha] --> Y[Beta]\n"
	result := Envelope(context.Background(), Options{Kind: "mermaid", Spec: src, Title: "dark-flow", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	path := result["file_path"].(string)
	_, _, _, _, counts := pngStats(t, path)
	if counts[mustRGBA(t, "#0e0e0d")] < 100000 {
		t.Fatalf("dark background missing: %d px of #0e0e0d", counts[mustRGBA(t, "#0e0e0d")])
	}
}

// Parity-exempt: dpi scaling is asserted upstream as "roughly viewport-sized"
// at 1x; the 2x case is exercised here so the device-scale flag has evidence.
func TestRenderHonorsDPIScale(t *testing.T) {
	chartsTestHome(t)
	requireChrome(t)

	spec := map[string]any{
		"xAxis":  map[string]any{"type": "category", "data": []any{"A", "B"}},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": "line", "data": []any{1, 3}}},
	}
	result := Envelope(context.Background(), Options{Kind: "echarts", Spec: spec, Title: "hidpi", Width: 300, Height: 200, DPI: 192})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["width"] != 300 || result["height"] != 200 {
		t.Fatalf("logical dims = %v x %v", result["width"], result["height"])
	}
	width, height, _, _, _ := pngStats(t, result["file_path"].(string))
	if width != 600 || height != 400 {
		t.Fatalf("png = %dx%d, want 600x400 at 2x dpi scaling", width, height)
	}
}

// Parity-exempt: upstream raises RendererError for bad mermaid source; there
// is no Python test, so this checks the real browser message passthrough.
func TestRenderMermaidInvalidSourceReturnsError(t *testing.T) {
	chartsTestHome(t)
	requireChrome(t)

	result := Envelope(context.Background(), Options{
		Kind: "mermaid", Spec: "hello world not a diagram", Title: "bad-mermaid", Width: 600, Height: 400, DPI: 96,
	})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "Mermaid render failed:") {
		t.Fatalf("error = %q", message)
	}
	if result["retried"] != true {
		t.Fatalf("expected retried marker: %#v", result)
	}
}

// ── page markup ──────────────────────────────────────────────────────

// Parity-exempt: the HTML shell is renderer.py's _build_html rendered to a
// file; template assertions are Go-side.
func TestBuildPageMatchesRendererShell(t *testing.T) {
	light, err := buildPage("echarts", map[string]any{"series": []any{}}, 800, 600, "light")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`background: #ffffff`,
		`width: 800px`,
		`height: 600px`,
		`<script src="echarts.min.js"></script>`,
		`animation: false`,
		`containLabel: true`,
		`data-chart-status`,
		`registerTheme('openhive'`,
		`legend.top:"8%"`,
	} {
		if !strings.Contains(light, want) {
			t.Fatalf("light page missing %q", want)
		}
	}
	// A mis-escaped Sprintf verb would surface as %! in the page.
	if strings.Contains(light, "%!") {
		t.Fatalf("page has a formatting error: %s", light[:min(len(light), 400)])
	}
	dark, err := buildPage("mermaid", "flowchart LR\nA --> B\n", 400, 300, "dark")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`background: #0e0e0d`,
		`<script src="mermaid.min.js"></script>`,
		`theme: "dark"`,
		`securityLevel: 'loose'`,
	} {
		if !strings.Contains(dark, want) {
			t.Fatalf("dark page missing %q", want)
		}
	}
	// A spec string must never be handed to setOption, and mermaid needs source.
	if _, err := buildPage("echarts", "{}", 100, 100, "light"); err == nil {
		t.Fatal("string echarts spec should be rejected at page build")
	}
	if _, err := buildPage("mermaid", map[string]any{}, 100, 100, "light"); err == nil {
		t.Fatal("non-string mermaid spec should be rejected")
	}
}

// Parity-exempt: embeds guard; the bundles must ship in the binary.
func TestEmbeddedLibrariesPresent(t *testing.T) {
	echarts := libraryBytes("echarts")
	mermaid := libraryBytes("mermaid")
	if len(echarts) < 500_000 || !bytes.Contains(echarts, []byte("echarts")) {
		t.Fatalf("echarts bundle looks wrong: %d bytes", len(echarts))
	}
	if len(mermaid) < 1_000_000 || !bytes.Contains(mermaid, []byte("mermaid")) {
		t.Fatalf("mermaid bundle looks wrong: %d bytes", len(mermaid))
	}
	if libraryFileName("echarts") != "echarts.min.js" || libraryFileName("mermaid") != "mermaid.min.js" {
		t.Fatalf("library file names = %q, %q", libraryFileName("echarts"), libraryFileName("mermaid"))
	}
}
