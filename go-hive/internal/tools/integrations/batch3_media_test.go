package integrations

// Cross-tool helpers and assembly-level tests for the batch-3 media pair.
// The image_generate implementation and its behavior tests live in the image
// subpackage; these helpers stay here because web_search_test.go and the
// cross-tool artifact test also use them.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Smallest valid 1x1 PNG (8-bit grayscale), reused as generated output and as
// a reference-image fixture.
const batch3TinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

// batch3MediaPublicResolver is the DNS stub for tests that serve reference
// images from loopback: the SSRF policy sees a public address, the transport
// still reaches the fake server.
func batch3MediaPublicResolver(context.Context, string) ([]string, error) {
	return []string{"93.184.216.34"}, nil
}

// batch3MediaTestRegistry points both batch-3 tools at one fake upstream, injects
// the HTTP client, credentials and clock, and keeps retry backoff at zero.
func batch3MediaTestRegistry(t *testing.T, server *httptest.Server, env map[string]string, opts ...batch3MediaOption) *Registry {
	t.Helper()
	base := []batch3MediaOption{
		withBatch3HTTPClient(server.Client()),
		withBatch3BaseURL(server.URL),
		withBatch3Getenv(func(name string) string { return env[name] }),
		withBatch3Clock(func() time.Time { return time.UnixMilli(1700000000000) }),
		withBatch3Sleep(func(context.Context, time.Duration) error { return nil }),
		withBatch3MediaResolver(batch3MediaPublicResolver),
	}
	return newBatch3MediaRegistry(append(base, opts...)...)
}

// batch3Run executes one action and asserts it returned a map without a Go
// error.
func batch3Run(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

// batch3ImageSummary decodes image_generate's `_text` payload: a successful
// generation returns {_text: <summary JSON>, _images: [preview]} so the shared
// registry can split it into TextContent + ImageContent, exactly like the
// Python tool's content list.
func batch3ImageSummary(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	text, _ := result["_text"].(string)
	if text == "" {
		t.Fatalf("image_generate returned no _text summary: %v", result)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		t.Fatalf("_text is not a JSON summary: %v (%q)", err, text)
	}
	return summary
}

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise the MCP tool functions, not a registry surface.
func TestBatch3CatalogAndInstall(t *testing.T) {
	registry := newBatch3MediaRegistry()
	if got := len(registry.Names()); got != 2 {
		t.Fatalf("tools = %d, want 2", got)
	}
	if got := registry.ActionCount(); got != 2 {
		t.Fatalf("actions = %d, want 2", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	shared := tools.NewToolRegistry()
	if installed := InstallBatch3Media(shared); installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	for _, name := range []string{"image_generate", "web_search"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	image, _ := shared.GetTool("image_generate")
	props, _ := image.Parameters["properties"].(map[string]any)
	if _, present := props["action"]; present {
		t.Fatalf("single-action tool exposed action selector: %v", props)
	}
	if _, present := props["prompt"]; !present {
		t.Fatalf("image_generate missing prompt: %v", props)
	}
	search, _ := shared.GetTool("web_search")
	searchProps, _ := search.Parameters["properties"].(map[string]any)
	if _, present := searchProps["provider"]; !present {
		t.Fatalf("web_search missing provider: %v", searchProps)
	}
	// The batch-3 registry must not leak batch-1 or batch-2 tools.
	if _, err := registry.Execute(context.Background(), "arxiv", nil); err == nil {
		t.Fatal("batch-3 registry exposed arxiv")
	}
}

// batch3PNGFixture encodes a solid 2x2 PNG, so two generated images have
// distinct bytes and a preview can be matched to exactly one of them.
func batch3PNGFixture(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png fixture: %v", err)
	}
	return buf.Bytes()
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_generation_success_saves_and_previews
// (upstream returns [TextContent(summary), ImageContent(first image)]; the Go
// tool must hand the same pair to the shared registry through _text/_images).
func TestBatch3ImagePreviewReachesToolResult(t *testing.T) {
	first := base64.StdEncoding.EncodeToString(batch3PNGFixture(t, 0xff, 0, 0))
	second := base64.StdEncoding.EncodeToString(batch3PNGFixture(t, 0, 0, 0xff))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{
				map[string]any{"b64_json": first},
				map[string]any{"b64_json": second},
			},
		})
	}))
	defer server.Close()
	t.Setenv("HIVE_API_KEY", "stream-token")
	t.Setenv("HIVE_LLM_BASE_URL", server.URL)
	t.Setenv("HIVE_STORAGE_PATH", t.TempDir())

	shared := tools.NewToolRegistry()
	if installed := InstallBatch3Media(shared); installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	result, err := shared.GetExecutor().Execute(context.Background(), tools.ToolUse{
		ID:    "call-image",
		Name:  "image_generate",
		Input: map[string]any{"prompt": "two robots", "n": 2},
	})
	if err != nil {
		t.Fatalf("execute image_generate: %v", err)
	}

	var summary map[string]any
	if err := json.Unmarshal([]byte(result.Content), &summary); err != nil {
		t.Fatalf("Content is not the JSON summary: %v (%q)", err, result.Content)
	}
	images, _ := summary["images"].([]any)
	if summary["n"] != float64(2) || len(images) != 2 {
		t.Fatalf("summary = %v", summary)
	}
	if summary["note"] == nil {
		t.Fatalf("multi-image note missing: %v", summary)
	}

	if len(result.ImageContent) != 1 {
		t.Fatalf("preview blocks = %d, want exactly 1 (the first image)", len(result.ImageContent))
	}
	block := result.ImageContent[0]
	if block["type"] != "image_url" {
		t.Fatalf("preview block = %#v", block)
	}
	imageURL, _ := block["image_url"].(map[string]any)
	dataURL, _ := imageURL["url"].(string)
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("preview url = %q", dataURL)
	}
	preview, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}

	firstSaved, _ := images[0].(map[string]any)["path"].(string)
	secondSaved, _ := images[1].(map[string]any)["path"].(string)
	firstBytes, err := os.ReadFile(firstSaved)
	if err != nil {
		t.Fatalf("read first image: %v", err)
	}
	secondBytes, err := os.ReadFile(secondSaved)
	if err != nil {
		t.Fatalf("read second image: %v", err)
	}
	if !bytes.Equal(preview, firstBytes) {
		t.Fatal("preview bytes are not the first saved image")
	}
	if bytes.Equal(preview, secondBytes) {
		t.Fatal("preview bytes are the second image, want the first")
	}
}

// Negative control for the preview wiring: the same summary without the
// `_images` key yields no image block, so the positive test cannot pass by
// accident if the key is dropped.
// Parity-exempt: negative control for the Go _images wiring; the upstream MCP
// client always carries the ImageContent block, so it has no equivalent test.
func TestBatch3ImagePreviewWiringIsRequired(t *testing.T) {
	preview := attachFileImageBlock("image/png", batch3PNGFixture(t, 1, 2, 3))
	summary := map[string]any{
		"images": []map[string]any{{"path": "/tmp/image_gen_1_0.png"}},
		"model":  "gpt-image-2",
		"n":      1,
	}
	text := tools.PythonJSONDumps(summary)

	shared := tools.NewToolRegistry()
	shared.Register("summary_only", tools.Tool{Name: "summary_only"}, func(map[string]any) (any, error) {
		return summary, nil
	})
	shared.Register("summary_with_preview", tools.Tool{Name: "summary_with_preview"}, func(map[string]any) (any, error) {
		return map[string]any{"_text": text, "_images": []map[string]any{preview}}, nil
	})
	executor := shared.GetExecutor()

	without, err := executor.Execute(context.Background(), tools.ToolUse{ID: "a", Name: "summary_only"})
	if err != nil {
		t.Fatalf("summary_only: %v", err)
	}
	if len(without.ImageContent) != 0 {
		t.Fatalf("summary without _images produced %d image blocks", len(without.ImageContent))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(without.Content), &decoded); err != nil {
		t.Fatalf("summary-only Content is not JSON: %v", err)
	}

	with, err := executor.Execute(context.Background(), tools.ToolUse{ID: "b", Name: "summary_with_preview"})
	if err != nil {
		t.Fatalf("summary_with_preview: %v", err)
	}
	if len(with.ImageContent) != 1 {
		t.Fatalf("_images produced %d blocks, want 1", len(with.ImageContent))
	}
}
