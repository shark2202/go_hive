package charts

import (
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stubRasterizer makes the PNG path deterministic: it pretends rsvg-convert is
// installed and writes a real PNG at the requested size.
func stubRasterizer(t *testing.T) {
	t.Helper()
	oldLook, oldRun := chartLookPath, chartRunCommand
	t.Cleanup(func() {
		chartLookPath = oldLook
		chartRunCommand = oldRun
	})
	chartLookPath = func(name string) (string, error) {
		if name == "rsvg-convert" {
			return "/stub/rsvg-convert", nil
		}
		return "", os.ErrNotExist
	}
	chartRunCommand = func(name string, args ...string) ([]byte, error) {
		// rsvg-convert args: -w W -h H -o PNG SVG
		width, _ := strconv.Atoi(args[1])
		height, _ := strconv.Atoi(args[3])
		pngPath := args[5]
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(10, 10, width/2, height/2), &image.Uniform{C: color.RGBA{R: 219, G: 111, B: 2, A: 255}}, image.Point{}, draw.Src)
		file, err := os.Create(pngPath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if err := png.Encode(file, img); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func chartsTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_DESKTOP_THEME", "light")
	return home
}

func pngDimensions(t *testing.T, path string) (int, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return config.Width, config.Height
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_bar_chart
func TestRenderEChartsBarChart(t *testing.T) {
	home := chartsTestHome(t)
	stubRasterizer(t)

	spec := map[string]any{
		"title":  map[string]any{"text": "Smoke test"},
		"xAxis":  map[string]any{"type": "category", "data": []any{"A", "B", "C"}},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": "bar", "data": []any{12, 24, 6}}},
	}
	result := Envelope(Options{Kind: "echarts", Spec: spec, Title: "smoke", Width: 600, Height: 400, DPI: 96})
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
	if result["bytes"].(int) <= 200 {
		t.Fatalf("bytes = %v", result["bytes"])
	}
	if result["format"] != "png" {
		t.Fatalf("format = %v", result["format"])
	}

	path := result["file_path"].(string)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(home, "charts") {
		t.Fatalf("dir = %s", filepath.Dir(path))
	}
	if !strings.Contains(filepath.Base(path), "smoke") {
		t.Fatalf("filename = %s", filepath.Base(path))
	}
	width, height := pngDimensions(t, path)
	if width != 600 || height != 400 {
		t.Fatalf("png = %dx%d", width, height)
	}
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_invalid_kind_returns_error
func TestRenderEChartsInvalidKindReturnsError(t *testing.T) {
	chartsTestHome(t)
	result := Envelope(Options{Kind: "bogus", Spec: map[string]any{}, Title: "x"})
	if result["error"] == nil {
		t.Fatal("expected an error")
	}
	if result["kind"] != "bogus" {
		t.Fatalf("kind = %v", result["kind"])
	}
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_accepts_string_spec
func TestRenderEChartsAcceptsStringSpec(t *testing.T) {
	chartsTestHome(t)
	stubRasterizer(t)

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
	result := Envelope(Options{Kind: "echarts", Spec: string(encoded), Title: "string-spec", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
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
	stubRasterizer(t)

	src := "flowchart LR\nA[Start] --> B{ok?}\nB -- yes --> C[Done]\nB -- no --> D[Retry]\n"
	result := Envelope(Options{Kind: "mermaid", Spec: src, Title: "flow", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["spec"] != src {
		t.Fatalf("spec echo = %#v", result["spec"])
	}
	path := result["file_path"].(string)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	graph, err := parseMermaid(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 4 || len(graph.Edges) != 3 {
		t.Fatalf("graph = %d nodes, %d edges", len(graph.Nodes), len(graph.Edges))
	}
	if graph.Direction != "LR" {
		t.Fatalf("direction = %q", graph.Direction)
	}
	if edge := graph.Edges[1]; edge.Label != "yes" || edge.To != "C" {
		t.Fatalf("edge = %+v", edge)
	}
}

// Parity: tools/tests/test_chart_tools_smoke.py::test_render_echarts_bar_chart
func TestOutputPathDefaultsUnderChartsDir(t *testing.T) {
	home := chartsTestHome(t)
	path, err := ResolveOutputPath("", "Quarterly Revenue")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(home, "charts") {
		t.Fatalf("dir = %s", filepath.Dir(path))
	}
	if !strings.HasSuffix(filepath.Base(path), "-quarterly-revenue.png") {
		t.Fatalf("filename = %s", filepath.Base(path))
	}
}

// Parity-exempt: no Python test covers the Go-only SVG fallback; the renderer
// always has Chromium upstream.
func TestRenderFallsBackToSVG(t *testing.T) {
	chartsTestHome(t)
	oldLook := chartLookPath
	t.Cleanup(func() { chartLookPath = oldLook })
	chartLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	spec := map[string]any{
		"xAxis":  map[string]any{"data": []any{"A", "B"}},
		"series": []any{map[string]any{"type": "line", "data": []any{1, 4}}},
	}
	result := Envelope(Options{Kind: "echarts", Spec: spec, Title: "fallback", Width: 600, Height: 400, DPI: 96})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["format"] != "svg" {
		t.Fatalf("format = %v", result["format"])
	}
	if result["backend"] != "svg" {
		t.Fatalf("backend = %v", result["backend"])
	}
	if note, _ := result["note"].(string); !strings.Contains(note, "rasterizer") {
		t.Fatalf("note = %q", note)
	}
	path := result["file_path"].(string)
	if !strings.HasSuffix(path, ".svg") {
		t.Fatalf("path = %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<svg") || !strings.Contains(string(body), "</svg>") {
		t.Fatalf("not an svg: %s", body[:min(len(body), 80)])
	}
}

// Parity-exempt: line/pie drawing coverage has no direct Python test; the
// shapes are exercised through the same renderer entry point as the bar test.
func TestRenderEChartsLineAndPie(t *testing.T) {
	chartsTestHome(t)
	stubRasterizer(t)

	line := map[string]any{
		"xAxis": map[string]any{"data": []any{"Q1", "Q2", "Q3"}},
		"series": []any{map[string]any{
			"type": "line", "name": "Revenue", "data": []any{3, 8, 5},
			"areaStyle": map[string]any{},
		}},
		"legend": map[string]any{},
	}
	result := RenderSVGOnly(line, "line")
	if !strings.Contains(result, "polyline") || !strings.Contains(result, "<path") || !strings.Contains(result, "Revenue") {
		t.Fatalf("line svg missing primitives: %s", result[:min(len(result), 400)])
	}

	pieSpec := map[string]any{
		"series": []any{map[string]any{
			"type": "pie", "radius": []any{"40%", "70%"},
			"data": []any{
				map[string]any{"name": "A", "value": 3},
				map[string]any{"name": "B", "value": 7},
			},
		}},
		"legend": map[string]any{},
	}
	svg := RenderSVGOnly(pieSpec, "pie")
	if !strings.Contains(svg, ">A</text>") || !strings.Contains(svg, "M ") {
		t.Fatalf("pie svg missing arcs: %s", svg[:min(len(svg), 400)])
	}
	if strings.Count(svg, "<path") < 2 {
		t.Fatalf("pie svg has too few slices")
	}
}

// RenderSVGOnly renders directly to markup, bypassing the filesystem and the
// rasterizer. Test-only helper kept beside its users.
func RenderSVGOnly(spec any, title string) string {
	svg, err := buildSVG("echarts", spec, 600, 400, 600, 400, "light")
	if err != nil {
		panic(err)
	}
	return svg
}

// Parity-exempt: helper behavior (slugging, dpi scale) has no Python unit test.
func TestSlugifyAndPixelSize(t *testing.T) {
	cases := map[string]string{
		"":                  "chart",
		"Quarterly Revenue": "quarterly-revenue",
		"  -- weird!!  ":    "weird",
	}
	for input, want := range cases {
		if got := Slugify(input); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
	if width, height := pixelSize(1600, 900, 300); width != 5000 || height != 2813 {
		t.Fatalf("pixelSize(300) = %dx%d", width, height)
	}
	if width, height := pixelSize(600, 400, 48); width != 600 || height != 400 {
		t.Fatalf("pixelSize(48) should clamp to 1x, got %dx%d", width, height)
	}
}

// Parity-exempt: rejection messages are Go-specific guards for specs outside
// the documented subset.
func TestRenderRejectsUnsupportedSpecs(t *testing.T) {
	chartsTestHome(t)
	stubRasterizer(t)

	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"unknown series", Options{Kind: "echarts", Spec: map[string]any{
			"xAxis":  map[string]any{"data": []any{"A"}},
			"series": []any{map[string]any{"type": "radar", "data": []any{1}}},
		}}, "unsupported series type"},
		{"missing categories", Options{Kind: "echarts", Spec: map[string]any{
			"series": []any{map[string]any{"type": "bar", "data": []any{1}}},
		}}, "xAxis.data is required"},
		{"not an object", Options{Kind: "echarts", Spec: "not-json"}, "not valid JSON"},
		{"unsupported diagram", Options{Kind: "mermaid", Spec: "sequenceDiagram\nA->>B: hi"}, "unsupported mermaid diagram"},
	}
	for _, test := range cases {
		result := Envelope(test.opts)
		message, _ := result["error"].(string)
		if !strings.Contains(message, test.want) {
			t.Fatalf("%s: error = %q, want %q", test.name, message, test.want)
		}
	}
}
