package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// Smallest valid 1x1 PNG (8-bit grayscale), reused as generated output and as
// a reference-image fixture.
const batch3TinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func batch3TinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(batch3TinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	return raw
}

// batch3TestRegistry points both batch-3 tools at one fake upstream, injects
// the HTTP client, credentials and clock, and keeps retry backoff at zero.
func batch3TestRegistry(t *testing.T, server *httptest.Server, env map[string]string, opts ...batch3Option) *Registry {
	t.Helper()
	base := []batch3Option{
		withBatch3HTTPClient(server.Client()),
		withBatch3BaseURL(server.URL),
		withBatch3Getenv(func(name string) string { return env[name] }),
		withBatch3Clock(func() time.Time { return time.UnixMilli(1700000000000) }),
		withBatch3Sleep(func(context.Context, time.Duration) error { return nil }),
		withBatch3PublicURL(func(*url.URL) bool { return true }),
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

// Parity: tools/tests/tools/test_image_gen_tool.py::test_missing_token_returns_error
func TestBatch3ImageMissingTokenReturns503(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "a cat"})
	if result["status"] != http.StatusServiceUnavailable {
		t.Fatalf("status = %v, want 503", result["status"])
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "missing Hive proxy token") {
		t.Fatalf("error = %v", result["error"])
	}
	if hits.Load() != 0 {
		t.Fatalf("proxy called %d times without a token", hits.Load())
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_generation_success_saves_and_previews
func TestBatch3ImageGenerationSavesFile(t *testing.T) {
	storage := t.TempDir()
	var method, path, authorization, contentType string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		authorization = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{"b64_json": batch3TinyPNGBase64}},
			"usage": map[string]any{
				"input_tokens": 12, "output_tokens": 300, "total_tokens": 312, "credits": 8.5,
			},
		})
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": storage,
	})

	result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "a friendly robot logo"})
	if errMessage, present := result["error"]; present {
		t.Fatalf("error = %v", errMessage)
	}

	// Request shape: proxy path, bearer token, defaults applied, no image.
	if method != http.MethodPost || path != "/v1/images/generations" {
		t.Fatalf("request = %s %s", method, path)
	}
	if authorization != "Bearer stream-token" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if body["model"] != "gpt-image-2" || body["quality"] != "low" || body["size"] != "1024x1024" {
		t.Fatalf("body = %v", body)
	}
	if n, _ := body["n"].(float64); n != 1 {
		t.Fatalf("n = %v", body["n"])
	}
	if _, present := body["image"]; present {
		t.Fatalf("unexpected image field: %v", body["image"])
	}

	// Output: one file under <storage>/data and the usage echoed verbatim.
	if result["model"] != "gpt-image-2" || result["n"] != 1 {
		t.Fatalf("summary = %v", result)
	}
	usage, _ := result["usage"].(map[string]any)
	if usage["credits"] != 8.5 {
		t.Fatalf("usage = %v", usage)
	}
	images, _ := result["images"].([]map[string]any)
	if len(images) != 1 {
		t.Fatalf("images = %v", result["images"])
	}
	pathOnDisk, _ := images[0]["path"].(string)
	if !strings.HasPrefix(pathOnDisk, filepath.Join(storage, "data")) {
		t.Fatalf("path outside storage/data: %q", pathOnDisk)
	}
	if images[0]["format"] != "png" || images[0]["quality"] != "low" {
		t.Fatalf("image entry = %v", images[0])
	}
	raw, err := os.ReadFile(pathOnDisk)
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if len(raw) == 0 || images[0]["bytes"] != len(raw) {
		t.Fatalf("bytes = %v, file = %d", images[0]["bytes"], len(raw))
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		t.Fatalf("saved file is not a PNG: %v", err)
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_n_and_quality_are_clamped, tools/tests/tools/test_image_gen_tool.py::test_only_low_quality_is_enabled
func TestBatch3ImageClampsInputs(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	batch3Run(t, registry, "image_generate", map[string]any{
		"prompt":        "variations",
		"n":             99,
		"quality":       "ultra",
		"output_format": "bmp",
	})
	for _, requested := range []string{"high", "auto", "medium", "low"} {
		batch3Run(t, registry, "image_generate", map[string]any{"prompt": "x", "quality": requested})
	}
	if len(bodies) != 5 {
		t.Fatalf("requests = %d, want 5", len(bodies))
	}
	first := bodies[0]
	if n, _ := first["n"].(float64); n != 4 {
		t.Fatalf("n = %v, want 4", first["n"])
	}
	if first["quality"] != "low" || first["output_format"] != "png" {
		t.Fatalf("first body = %v", first)
	}
	for _, body := range bodies {
		if body["quality"] != "low" {
			t.Fatalf("quality not clamped: %v", body)
		}
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_reference_image_sent_as_base64
func TestBatch3ImageReferenceFileSentAsBase64(t *testing.T) {
	reference := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(reference, batch3TinyPNGBytes(t), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	result := batch3Run(t, registry, "image_generate", map[string]any{
		"prompt":           "restyle this",
		"reference_images": []string{reference},
	})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	encoded, _ := body["image"].([]any)
	if len(encoded) != 1 || encoded[0] != batch3TinyPNGBase64 {
		t.Fatalf("image = %v", body["image"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_too_many_reference_images
func TestBatch3ImageTooManyReferenceImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("proxy should not be called")
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})
	refs := make([]string, 11)
	for i := range refs {
		refs[i] = filepath.Join(t.TempDir(), "missing.png")
	}
	result := batch3Run(t, registry, "image_generate", map[string]any{
		"prompt":           "x",
		"reference_images": refs,
	})
	if message, _ := result["error"].(string); !strings.Contains(message, "Too many reference images") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_url_reference_image_fetched
func TestBatch3ImageURLReferenceFetched(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ref.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(batch3TinyPNGBytes(t))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	result := batch3Run(t, registry, "image_generate", map[string]any{
		"prompt":           "edit",
		"reference_images": []string{server.URL + "/ref.png"},
	})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	encoded, _ := body["image"].([]any)
	if len(encoded) != 1 || encoded[0] != batch3TinyPNGBase64 {
		t.Fatalf("image = %v", body["image"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_error_status_mapping
func TestBatch3ImageErrorStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		needle string
	}{
		{http.StatusPaymentRequired, "credit"},
		{http.StatusUnauthorized, "authentication failed"},
		{http.StatusForbidden, "unavailable"},
		{http.StatusTooManyRequests, "rate-limited"},
		{http.StatusBadRequest, "rejected"},
		{http.StatusInternalServerError, "error"},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			}))
			defer server.Close()
			registry := batch3TestRegistry(t, server, map[string]string{
				"HIVE_API_KEY":      "stream-token",
				"HIVE_STORAGE_PATH": t.TempDir(),
			})
			result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "x"})
			message, _ := result["error"].(string)
			if !strings.Contains(strings.ToLower(message), testCase.needle) {
				t.Fatalf("error = %q, want %q", message, testCase.needle)
			}
			if result["status"] != testCase.status {
				t.Fatalf("status = %v, want %d", result["status"], testCase.status)
			}
		})
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_timeout_maps_to_error_without_raising
func TestBatch3ImageTimeoutMapsToError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	fast := server.Client()
	fast.Timeout = 10 * time.Millisecond
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}, withBatch3HTTPClient(fast))

	result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "x"})
	message, _ := result["error"].(string)
	if !strings.Contains(strings.ToLower(message), "timed out") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: upstream's fixture deletes HIVE_LLM_BASE_URL, so the proxy
// base override path has no Python test; the tool documents and supports it.
func TestBatch3ImageHonorsProxyBaseURLOverride(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	env := map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_LLM_BASE_URL": server.URL + "/",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}
	// No module override: only HIVE_LLM_BASE_URL may redirect the request.
	registry := newBatch3MediaRegistry(
		withBatch3HTTPClient(server.Client()),
		withBatch3Getenv(func(name string) string { return env[name] }),
		withBatch3Clock(func() time.Time { return time.UnixMilli(1700000000000) }),
	)
	result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "x"})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	if path != "/v1/images/generations" {
		t.Fatalf("path = %q", path)
	}
}

// Parity-exempt: the upstream tests never cover malformed proxy bodies; these
// branches mirror the Python handler's guards.
func TestBatch3ImageMalformedProxyResponses(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		needle  string
	}{
		{"non-json", "not json", "non-JSON"},
		{"no-data", `{"data":[]}`, "no image data"},
		{"undecodable", `{"data":[{"b64_json":"!!!"}]}`, "no decodable image"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.payload))
			}))
			defer server.Close()
			registry := batch3TestRegistry(t, server, map[string]string{
				"HIVE_API_KEY":      "stream-token",
				"HIVE_STORAGE_PATH": t.TempDir(),
			})
			result := batch3Run(t, registry, "image_generate", map[string]any{"prompt": "x"})
			message, _ := result["error"].(string)
			if !strings.Contains(message, testCase.needle) {
				t.Fatalf("error = %q, want %q", message, testCase.needle)
			}
		})
	}
}

// Parity-exempt: SSRF guard is Go-side hardening; upstream monkeypatches the
// guard away in its URL-reference test.
func TestBatch3ImageBlocksNonPublicReferenceURL(t *testing.T) {
	var referenceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ref.png" {
			referenceHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(batch3TinyPNGBytes(t))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}, withBatch3PublicURL(nil))

	result := batch3Run(t, registry, "image_generate", map[string]any{
		"prompt":           "edit",
		"reference_images": []string{server.URL + "/ref.png"},
	})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "Blocked non-public") {
		t.Fatalf("error = %q", message)
	}
	if referenceHits.Load() != 0 {
		t.Fatal("reference URL was fetched despite the SSRF guard")
	}
}
