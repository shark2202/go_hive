package charts

// chart_render: render a chart or diagram to a high-DPI PNG and return the
// rich envelope the chat needs to embed the chart live and link the file.
//
// Upstream (tools/src/chart_tools/tools.py) drives Chromium through Playwright.
// This port drives the local Chrome binary in headless mode against a temporary
// page that loads the same bundled ECharts/Mermaid builds, so the PNG comes
// from the same engine. The success envelope carries the upstream fields plus
// `theme`, the HIVE_DESKTOP_THEME value the render used.
//
// Behavioural contract kept from upstream:
//   - kind must be echarts or mermaid;
//   - an ECharts spec passed as a JSON string is parsed, not handed to
//     setOption as a string;
//   - output defaults to $HIVE_HOME/charts/<UTC-timestamp>-<slug>.png;
//   - one retry for transient renderer failures, then {"error", "kind",
//     "spec"} (plus "retried": true);
//   - a PNG smaller than 200 bytes counts as a failed render;
//   - no Chrome means an explicit error, never a fabricated PNG.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	// ToolName is the single agent-facing tool of the chart-tools group.
	ToolName = "chart_render"

	DefaultWidth  = 1600
	DefaultHeight = 900
	DefaultDPI    = 300
	// MinPNGBytes is the sanity floor for "did the renderer actually draw
	// anything".
	MinPNGBytes = 200
)

// Options are the tool's arguments. Home is the HIVE_HOME root used for the
// default charts directory (empty means resolve the process home).
type Options struct {
	Kind       string
	Spec       any
	Title      string
	Width      int
	Height     int
	DPI        int
	OutputPath string
	Home       string
}

// Result is the success envelope (upstream fields plus theme).
type Result struct {
	Kind      string `json:"kind"`
	Spec      any    `json:"spec"`
	FilePath  string `json:"file_path"`
	FileURL   string `json:"file_url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	DPI       int    `json:"dpi"`
	Bytes     int    `json:"bytes"`
	Title     string `json:"title"`
	Theme     string `json:"theme"`
	RuntimeMS int64  `json:"runtime_ms"`
}

// RenderError carries the message the chat surfaces as a "spec invalid" pill.
// Retried records that the one allowed retry also failed, mirroring upstream's
// "retried": true marker.
type RenderError struct {
	Message string
	Retried bool
}

func (e *RenderError) Error() string { return e.Message }

// ToolDef advertises chart_render with the upstream argument surface.
func ToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: ToolName,
		Description: "Render a chart or diagram to a high-DPI PNG and return a rich envelope so the " +
			"chat can also render the chart live.\n\n" +
			"This single tool drives both the live in-chat embedding (chat reads result.spec and " +
			"mounts the same library in the bubble) and the downloadable file (chat shows " +
			"result.file_url as a link). Calling this tool IS the embedding — there is no separate " +
			"\"show in chat\" step.\n\n" +
			"Args:\n" +
			"  kind: \"echarts\" for ECharts JSON specs (bar, line, area, scatter, candlestick, " +
			"heatmap, treemap, sankey, gauge, pie, sunburst, geo maps); \"mermaid\" for diagrams " +
			"(flowchart, sequence, gantt, ERD, state, mindmap).\n" +
			"  spec: echarts — a dict matching the ECharts option schema; mermaid — the raw " +
			"mermaid source string. A JSON-encoded echarts spec string is accepted too.\n" +
			"  title: short slug used in the auto-generated filename.\n" +
			"  width, height: logical viewport size in CSS pixels (default 1600x900).\n" +
			"  dpi: device pixel ratio for the screenshot (300 is print quality, 96 screen).\n" +
			"  output_path: override where the PNG lands; default $HIVE_HOME/charts/" +
			"<UTC-timestamp>-<title>.png.\n\n" +
			"Returns kind, spec, title, file_path, file_url, width, height, dpi, bytes, theme, " +
			"runtime_ms. On error returns {error, kind, spec} so the chat can surface a " +
			"\"spec invalid\" pill instead of swallowing the failure. The theme is resolved from " +
			"HIVE_DESKTOP_THEME, not from an argument.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": `"echarts" for ECharts JSON specs, "mermaid" for raw mermaid diagram source.`,
				},
				"spec": map[string]any{
					"description": "For kind=\"echarts\", a dict matching the ECharts option schema " +
						"(https://echarts.apache.org/en/option.html); a JSON-encoded dict string is also " +
						"accepted. For kind=\"mermaid\", the raw mermaid source string.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Short slug used in the auto-generated filename (lowercased and hyphenated).",
					"default":     "",
				},
				"width": map[string]any{
					"type":        "integer",
					"description": "Logical viewport width in CSS pixels. Default 1600.",
					"default":     DefaultWidth,
				},
				"height": map[string]any{
					"type":        "integer",
					"description": "Logical viewport height in CSS pixels. Default 900.",
					"default":     DefaultHeight,
				},
				"dpi": map[string]any{
					"type":        "integer",
					"description": "Device pixel ratio for the screenshot: 300 print quality, 150 web-retina, 96 screen.",
					"default":     DefaultDPI,
				},
				"output_path": map[string]any{
					"type":        "string",
					"description": "Override where the PNG lands. Default $HIVE_HOME/charts/<UTC-timestamp>-<title>.png.",
				},
			},
			"required": []string{"kind", "spec"},
		},
		ConcurrencySafe: false,
	}
}

// ExecuteTool parses one tool call, renders, and returns the JSON-encoded
// envelope. Tool-level failures are reported in the envelope (isError true),
// never as a transport error.
func ExecuteTool(ctx context.Context, input map[string]any, home string) (string, bool) {
	opts, err := optionsFromInput(input, home)
	var envelope map[string]any
	if err != nil {
		envelope = map[string]any{"error": err.Error(), "kind": input["kind"], "spec": input["spec"]}
	} else {
		envelope = Envelope(ctx, opts)
	}
	payload, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		// The spec echo is the only value that can be unencodable (for
		// example a NaN float smuggled in by a test harness).
		return `{"error":"chart envelope could not be encoded: ` + escapeJSON(marshalErr.Error()) + `"}`, true
	}
	_, isError := envelope["error"]
	return string(payload), isError
}

// Envelope renders and returns the dict-shaped result the tool exposes.
// Errors never escape: they become {"error", "kind", "spec"} like upstream.
func Envelope(ctx context.Context, opts Options) map[string]any {
	result, err := Render(ctx, opts)
	if err != nil {
		spec := result.Spec
		if spec == nil {
			spec = opts.Spec
		}
		out := map[string]any{"error": err.Error(), "kind": opts.Kind, "spec": spec}
		if renderErr, ok := err.(*RenderError); ok && renderErr.Retried {
			out["retried"] = true
		}
		return out
	}
	return map[string]any{
		"kind":       result.Kind,
		"spec":       result.Spec,
		"title":      result.Title,
		"file_path":  result.FilePath,
		"file_url":   result.FileURL,
		"width":      result.Width,
		"height":     result.Height,
		"dpi":        result.DPI,
		"bytes":      result.Bytes,
		"theme":      result.Theme,
		"runtime_ms": result.RuntimeMS,
	}
}

// Render resolves the output path, renders through Chrome and writes the PNG.
func Render(ctx context.Context, opts Options) (Result, error) {
	start := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	width, height, dpi := opts.Width, opts.Height, opts.DPI
	if width == 0 {
		width = DefaultWidth
	}
	if height == 0 {
		height = DefaultHeight
	}
	if dpi == 0 {
		dpi = DefaultDPI
	}
	result := Result{Kind: opts.Kind, Spec: opts.Spec, Title: opts.Title, Width: width, Height: height, DPI: dpi}
	if width < 0 || height < 0 || dpi < 0 {
		return result, &RenderError{Message: fmt.Sprintf(
			"width, height and dpi must be positive (got %d x %d @ %d dpi)", opts.Width, opts.Height, opts.DPI)}
	}
	if opts.Kind != "echarts" && opts.Kind != "mermaid" {
		return result, &RenderError{Message: fmt.Sprintf("kind must be 'echarts' or 'mermaid', got %s", quote(opts.Kind))}
	}

	// Coerce a JSON-string ECharts spec to a dict. Common LLM mistake: the
	// agent serializes the option to JSON before passing it; without this
	// setOption("...") blows up exactly as it did upstream on 2026-05-01.
	if opts.Kind == "echarts" {
		if text, isString := opts.Spec.(string); isString {
			var parsed any
			if err := json.Unmarshal([]byte(text), &parsed); err != nil {
				return result, &RenderError{Message: "spec was a string but not valid JSON: " + err.Error() +
					". Pass the spec as a dict, not a JSON-encoded string."}
			}
			result.Spec = parsed
		}
	}
	if result.Spec == nil {
		return result, &RenderError{Message: "spec is required"}
	}

	path, err := ResolveOutputPath(opts.OutputPath, opts.Title, opts.Home)
	if err != nil {
		return result, &RenderError{Message: "could not create output dir: " + err.Error()}
	}

	// One retry on transient errors (browser cold-start race, font loading
	// flake). Persistent spec errors fail on the second attempt.
	theme := ResolveThemeName()
	var pngBytes []byte
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		pngBytes, lastErr = renderWithChrome(ctx, opts.Kind, result.Spec, width, height, dpi, theme)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			return result, &RenderError{Message: lastErr.Error()}
		}
		if attempt == 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return result, &RenderError{Message: lastErr.Error(), Retried: true}
	}
	if len(pngBytes) < MinPNGBytes {
		return result, &RenderError{Message: fmt.Sprintf("render produced suspiciously small PNG (%d bytes)", len(pngBytes))}
	}
	if pngWidth, pngHeight, ok := pngDimensions(pngBytes); !ok || pngWidth <= 0 || pngHeight <= 0 {
		return result, &RenderError{Message: fmt.Sprintf("chrome wrote %d bytes that are not a usable PNG", len(pngBytes))}
	}
	if writeErr := os.WriteFile(path, pngBytes, 0o644); writeErr != nil {
		return result, &RenderError{Message: "could not write " + path + ": " + writeErr.Error()}
	}

	result.FilePath = path
	result.FileURL = FileURL(path)
	result.Bytes = len(pngBytes)
	result.Theme = theme
	result.RuntimeMS = time.Since(start).Milliseconds()
	return result, nil
}

// optionsFromInput coerces the raw tool input into Options.
func optionsFromInput(input map[string]any, home string) (Options, error) {
	opts := Options{Home: home, Width: DefaultWidth, Height: DefaultHeight, DPI: DefaultDPI}
	if raw, exists := input["kind"]; exists && raw != nil {
		kind, isString := raw.(string)
		if !isString {
			return opts, fmt.Errorf("kind must be a string, got %T", raw)
		}
		opts.Kind = kind
	}
	opts.Spec = input["spec"]
	if opts.Spec == nil {
		return opts, fmt.Errorf("spec is required")
	}
	if raw, exists := input["title"]; exists && raw != nil {
		title, isString := raw.(string)
		if !isString {
			return opts, fmt.Errorf("title must be a string, got %T", raw)
		}
		opts.Title = title
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"width", &opts.Width}, {"height", &opts.Height}, {"dpi", &opts.DPI},
	} {
		if raw, exists := input[field.key]; exists && raw != nil {
			value, err := coerceInt(raw)
			if err != nil {
				return opts, fmt.Errorf("%s: %w", field.key, err)
			}
			*field.target = value
		}
	}
	if raw, exists := input["output_path"]; exists && raw != nil {
		path, isString := raw.(string)
		if !isString {
			return opts, fmt.Errorf("output_path must be a string, got %T", raw)
		}
		opts.OutputPath = path
	}
	return opts, nil
}

// coerceInt accepts JSON numbers and numeric strings, the two shapes tool
// arguments arrive in.
func coerceInt(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("must be an integer, got %v", typed)
		}
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("must be an integer, got %q", typed.String())
		}
		return int(parsed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("must be an integer, got %q", typed)
		}
		return parsed, nil
	}
	return 0, fmt.Errorf("must be an integer, got %T", value)
}

// ── Output paths ─────────────────────────────────────────────────────

var chartSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases, hyphenates and strips everything else; empty → "chart".
//
// Parity: tools/src/chart_tools/tools.py::_slugify
func Slugify(title string) string {
	slug := strings.Trim(chartSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-"), "-")
	if slug == "" {
		return "chart"
	}
	return slug
}

// ResolveOutputPath picks $HIVE_HOME/charts/<UTC-timestamp>-<slug>.png unless
// the caller supplied an override.
//
// Parity: tools/src/chart_tools/tools.py::_resolve_output_path
func ResolveOutputPath(override, title, home string) (string, error) {
	if override != "" {
		path := expandHome(override)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	dir := filepath.Join(chartsHome(home), "charts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("2006-01-02T15-04-05")
	return filepath.Join(dir, ts+"-"+Slugify(title)+".png"), nil
}

func chartsHome(home string) string {
	if strings.TrimSpace(home) != "" {
		return expandHome(home)
	}
	if resolved, err := hivehome.Resolve(); err == nil {
		return resolved.Root
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

// ── Small helpers ────────────────────────────────────────────────────

// pixelSize applies the renderer's scale rule: the logical viewport stays
// width × height CSS pixels; the PNG is width*dpi/96 × height*dpi/96.
func pixelSize(width, height, dpi int) (int, int) {
	scale := float64(dpi) / 96.0
	if scale < 1 {
		scale = 1
	}
	return int(float64(width)*scale + 0.5), int(float64(height)*scale + 0.5)
}

// pngDimensions reads the IHDR header without decoding pixels.
func pngDimensions(data []byte) (int, int, bool) {
	if len(data) < 8 || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return 0, 0, false
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}

func escapeJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		return ""
	}
	return string(encoded[1 : len(encoded)-1])
}
