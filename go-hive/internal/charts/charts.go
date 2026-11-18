package charts

// chart_render: render a chart or diagram to a local image and return a
// reference the caller can open or embed.
//
// The Python tool drives headless Chromium through Playwright so the PNG is
// pixel-identical to the live in-chat ECharts mount. This port is pure Go and
// offline, so it does the layout itself and emits SVG, then hands that SVG to
// whichever local rasterizer is installed (rsvg-convert, resvg, inkscape,
// sips, ImageMagick) to produce a real PNG. When no rasterizer exists the tool
// still succeeds, writing the SVG and saying so in the envelope
// (format/backend/note) instead of pretending a PNG was produced.
//
// Supported spec surface (documented, deliberately narrower than ECharts):
//   echarts — bar, line (incl. area) and pie series; category x-axis; one
//             value y-axis; title, legend, names, explicit min/max.
//   mermaid — flowchart/graph diagrams (TD/TB/LR/RL/BT) with rectangle,
//             rounded and diamond nodes plus labeled edges.
// Anything else returns an error naming what is supported rather than
// silently drawing the wrong chart.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const (
	DefaultWidth  = 1600
	DefaultHeight = 900
	DefaultDPI    = 300
	// Sanity floor for "did the renderer actually draw anything".
	MinPNGBytes = 200
)

// Options are the tool's arguments.
type Options struct {
	Kind       string
	Spec       any
	Title      string
	Width      int
	Height     int
	DPI        int
	OutputPath string
}

// Result is the success envelope. Format is "png" when a rasterizer ran and
// "svg" for the documented fallback; Backend names the tool that produced it.
type Result struct {
	Kind      string `json:"kind"`
	Spec      any    `json:"spec"`
	FilePath  string `json:"file_path"`
	FileURL   string `json:"file_url"`
	Format    string `json:"format"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	DPI       int    `json:"dpi"`
	Bytes     int    `json:"bytes"`
	Title     string `json:"title"`
	RuntimeMS int64  `json:"runtime_ms"`
	Backend   string `json:"backend"`
	Note      string `json:"note,omitempty"`
}

// RenderError carries the message the chat surfaces as a "spec invalid" pill.
type RenderError struct {
	Message string
}

func (e *RenderError) Error() string { return e.Message }

// Render resolves the output path, draws the spec to SVG and rasterizes it
// when a local renderer is available.
func Render(opts Options) (Result, error) {
	start := time.Now()
	width, height, dpi := opts.Width, opts.Height, opts.DPI
	if width <= 0 {
		width = DefaultWidth
	}
	if height <= 0 {
		height = DefaultHeight
	}
	if dpi <= 0 {
		dpi = DefaultDPI
	}

	echo := opts.Spec
	if opts.Kind != "echarts" && opts.Kind != "mermaid" {
		return Result{}, &RenderError{Message: fmt.Sprintf("kind must be 'echarts' or 'mermaid', got %s", quote(opts.Kind))}
	}
	if opts.Kind == "echarts" {
		if text, ok := opts.Spec.(string); ok {
			var parsed any
			if err := json.Unmarshal([]byte(text), &parsed); err != nil {
				return Result{}, &RenderError{Message: "spec was a string but not valid JSON: " + err.Error() +
					". Pass the spec as a dict, not a JSON-encoded string."}
			}
			echo = parsed
		}
	}

	pngPath, err := ResolveOutputPath(opts.OutputPath, opts.Title)
	if err != nil {
		return Result{}, &RenderError{Message: "could not create output dir: " + err.Error()}
	}
	svgPath := pngPath
	if !strings.EqualFold(filepath.Ext(svgPath), ".svg") {
		svgPath = strings.TrimSuffix(svgPath, filepath.Ext(svgPath)) + ".svg"
	}

	targetW, targetH := pixelSize(width, height, dpi)
	svgBody, err := buildSVG(opts.Kind, echo, width, height, targetW, targetH, ResolveThemeName())
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Kind:      opts.Kind,
		Spec:      echo,
		Width:     width,
		Height:    height,
		DPI:       dpi,
		Title:     opts.Title,
		RuntimeMS: int64(time.Since(start).Milliseconds()),
	}

	// Write the SVG first: it is both the rasterizer input and the fallback.
	if err := os.WriteFile(svgPath, []byte(svgBody), 0o644); err != nil {
		return Result{}, &RenderError{Message: "could not write " + svgPath + ": " + err.Error()}
	}

	backend, pngBytes, rasterErr := rasterizeSVG(svgPath, pngPath, targetW, targetH)
	if rasterErr == nil {
		if len(pngBytes) < MinPNGBytes {
			return Result{}, &RenderError{Message: fmt.Sprintf("render produced suspiciously small PNG (%d bytes)", len(pngBytes))}
		}
		if err := os.WriteFile(pngPath, pngBytes, 0o644); err != nil {
			return Result{}, &RenderError{Message: "could not write " + pngPath + ": " + err.Error()}
		}
		_ = os.Remove(svgPath)
		result.FilePath = pngPath
		result.FileURL = FileURL(pngPath)
		result.Format = "png"
		result.Bytes = len(pngBytes)
		result.Backend = backend
		result.RuntimeMS = int64(time.Since(start).Milliseconds())
		return result, nil
	}

	// No rasterizer (or every candidate failed): keep the SVG and be explicit
	// about the difference instead of failing the call.
	svgBytes := []byte(svgBody)
	result.FilePath = svgPath
	result.FileURL = FileURL(svgPath)
	result.Format = "svg"
	result.Bytes = len(svgBytes)
	result.Backend = "svg"
	result.Note = "no working PNG rasterizer found (looked for " + rasterizerNames() +
		"); wrote SVG instead. The layout is identical, but the file is vector rather than raster PNG and text is drawn by the viewer's fonts. Install rsvg-convert/resvg/inkscape/ImageMagick to get PNG."
	if rasterErr != nil && !errors.Is(rasterErr, errNoRasterizer) {
		result.Note = "PNG rasterization failed (" + rasterErr.Error() + "); wrote SVG instead."
	}
	result.RuntimeMS = int64(time.Since(start).Milliseconds())
	return result, nil
}

// Envelope renders and returns the dict-shaped result the MCP tool exposes.
// Errors never escape: they become {"error", "kind", "spec"} like upstream.
func Envelope(opts Options) map[string]any {
	result, err := Render(opts)
	if err != nil {
		return map[string]any{
			"error": err.Error(),
			"kind":  opts.Kind,
			"spec":  opts.Spec,
		}
	}
	out := map[string]any{
		"kind":       result.Kind,
		"spec":       result.Spec,
		"file_path":  result.FilePath,
		"file_url":   result.FileURL,
		"format":     result.Format,
		"width":      result.Width,
		"height":     result.Height,
		"dpi":        result.DPI,
		"bytes":      result.Bytes,
		"title":      result.Title,
		"runtime_ms": result.RuntimeMS,
		"backend":    result.Backend,
	}
	if result.Note != "" {
		out["note"] = result.Note
	}
	return out
}

// ── Output paths ─────────────────────────────────────────────────────

var chartSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases, hyphenates and strips everything else; empty → "chart".
func Slugify(title string) string {
	slug := strings.Trim(chartSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-"), "-")
	if slug == "" {
		return "chart"
	}
	return slug
}

// ResolveOutputPath picks $HIVE_HOME/charts/<UTC-timestamp>-<slug>.png unless
// the caller supplied an override.
func ResolveOutputPath(override, title string) (string, error) {
	if override != "" {
		path := expandHome(override)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	dir := filepath.Join(chartsHome(), "charts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("2006-01-02T15-04-05")
	return filepath.Join(dir, ts+"-"+Slugify(title)+".png"), nil
}

func chartsHome() string {
	if home, err := hivehome.Resolve(); err == nil {
		return home.Root
	}
	usr, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(usr, ".hive")
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		usr, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(usr, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		}
	}
	return path
}

// FileURL renders an absolute file:/// URL, percent-encoding as needed.
func FileURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// ── Rasterizers ──────────────────────────────────────────────────────

var errNoRasterizer = errors.New("no rasterizer available")

type rasterizer struct {
	name string
	args func(svg, png string, width, height int) []string
}

var rasterizerCandidates = []rasterizer{
	{"rsvg-convert", func(svg, png string, width, height int) []string {
		return []string{"-w", strconv.Itoa(width), "-h", strconv.Itoa(height), "-o", png, svg}
	}},
	{"resvg", func(svg, png string, width, height int) []string {
		return []string{"--width", strconv.Itoa(width), "--height", strconv.Itoa(height), svg, png}
	}},
	{"inkscape", func(svg, png string, width, height int) []string {
		return []string{"--export-type=png", "--export-filename=" + png,
			"--export-width=" + strconv.Itoa(width), "--export-height=" + strconv.Itoa(height), svg}
	}},
	{"sips", func(svg, png string, width, height int) []string {
		// sips takes height before width.
		return []string{"-s", "format", "png", "-z", strconv.Itoa(height), strconv.Itoa(width), svg, "--out", png}
	}},
	{"magick", func(svg, png string, width, height int) []string {
		return []string{"-background", "none", svg, "-resize", strconv.Itoa(width) + "x" + strconv.Itoa(height), png}
	}},
	{"convert", func(svg, png string, width, height int) []string {
		return []string{"-background", "none", svg, "-resize", strconv.Itoa(width) + "x" + strconv.Itoa(height), png}
	}},
}

// Hooks kept as vars so tests can drive the rasterizer path without depending
// on the host's installed tooling.
var (
	chartLookPath   = exec.LookPath
	chartRunCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

func rasterizerNames() string {
	names := make([]string, 0, len(rasterizerCandidates))
	for _, candidate := range rasterizerCandidates {
		names = append(names, candidate.name)
	}
	return strings.Join(names, ", ")
}

// rasterizeSVG tries each installed converter in order. It returns the
// converter's name and the PNG bytes.
func rasterizeSVG(svgPath, pngPath string, width, height int) (string, []byte, error) {
	var lastErr error
	found := false
	for _, candidate := range rasterizerCandidates {
		binary, err := chartLookPath(candidate.name)
		if err != nil || binary == "" {
			continue
		}
		found = true
		_ = os.Remove(pngPath)
		output, runErr := chartRunCommand(binary, candidate.args(svgPath, pngPath, width, height)...)
		if runErr != nil {
			lastErr = fmt.Errorf("%s: %v: %s", candidate.name, runErr, strings.TrimSpace(string(output)))
			continue
		}
		data, readErr := os.ReadFile(pngPath)
		if readErr != nil {
			lastErr = fmt.Errorf("%s: %v", candidate.name, readErr)
			continue
		}
		if !isPNG(data) || len(data) < MinPNGBytes {
			lastErr = fmt.Errorf("%s: output is not a usable PNG (%d bytes)", candidate.name, len(data))
			continue
		}
		return candidate.name, data, nil
	}
	if !found {
		return "", nil, errNoRasterizer
	}
	if lastErr == nil {
		lastErr = errNoRasterizer
	}
	return "", nil, lastErr
}

func isPNG(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
}

// pixelSize applies the renderer's scale rule: the logical viewport stays
// width × height pixels; the file is width*dpi/96 × height*dpi/96.
func pixelSize(width, height, dpi int) (int, int) {
	scale := float64(dpi) / 96.0
	if scale < 1 {
		scale = 1
	}
	return int(float64(width)*scale + 0.5), int(float64(height)*scale + 0.5)
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}
