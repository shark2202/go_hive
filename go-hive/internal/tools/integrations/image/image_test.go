package image

// Behavior tests moved with the image_generate implementation from the
// integrations root package. They exercise the image-only registry; the
// assembly-level install/preview tests stay in the root package.

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
)

// Smallest valid 1x1 PNG (8-bit grayscale), reused as generated output and as
// a reference-image fixture.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func tinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	return raw
}

// publicResolver is the DNS stub for tests that serve reference images from
// loopback: the SSRF policy sees a public address, the transport still reaches
// the fake server.
func publicResolver(context.Context, string) ([]string, error) {
	return []string{"93.184.216.34"}, nil
}

// testRegistry points the image tool at one fake upstream and injects the HTTP
// client, credentials, clock and resolver.
func testRegistry(t *testing.T, server *httptest.Server, env map[string]string, opts ...Option) *Registry {
	t.Helper()
	base := []Option{
		WithHTTPClient(server.Client()),
		WithBaseURL(server.URL),
		WithGetenv(func(name string) string { return env[name] }),
		WithClock(func() time.Time { return time.UnixMilli(1700000000000) }),
		WithResolver(publicResolver),
	}
	return NewRegistry(append(base, opts...)...)
}

// runImage executes image_generate and asserts it returned a map without a Go
// error.
func runImage(t *testing.T, registry *Registry, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), "image_generate", input)
	if err != nil {
		t.Fatalf("image_generate: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("image_generate returned %T, want map", result)
	}
	return object
}

// imageSummary decodes image_generate's `_text` payload.
func imageSummary(t *testing.T, result map[string]any) map[string]any {
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

// rewriteTransport keeps the logical host (example.com) while routing the
// actual connection at an httptest server, so reference-URL tests need no DNS.
type rewriteTransport struct {
	target string
}

func (t *rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	clone.Host = target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_missing_token_returns_error
func TestImageMissingTokenReturns503(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	result := runImage(t, registry, map[string]any{"prompt": "a cat"})
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
func TestImageGenerationSavesFile(t *testing.T) {
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
			"data": []any{map[string]any{"b64_json": tinyPNGBase64}},
			"usage": map[string]any{
				"input_tokens": 12, "output_tokens": 300, "total_tokens": 312, "credits": 8.5,
			},
		})
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": storage,
	})

	result := runImage(t, registry, map[string]any{"prompt": "a friendly robot logo"})
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

	// Output: one file under <storage>/data and the usage echoed verbatim in
	// the text summary; the preview block rides beside it.
	summary := imageSummary(t, result)
	if summary["model"] != "gpt-image-2" || summary["n"] != float64(1) {
		t.Fatalf("summary = %v", summary)
	}
	usage, _ := summary["usage"].(map[string]any)
	if usage["credits"] != 8.5 {
		t.Fatalf("usage = %v", usage)
	}
	images, _ := summary["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("images = %v", summary["images"])
	}
	entry, _ := images[0].(map[string]any)
	pathOnDisk, _ := entry["path"].(string)
	if !strings.HasPrefix(pathOnDisk, filepath.Join(storage, "data")) {
		t.Fatalf("path outside storage/data: %q", pathOnDisk)
	}
	if entry["format"] != "png" || entry["quality"] != "low" {
		t.Fatalf("image entry = %v", entry)
	}
	raw, err := os.ReadFile(pathOnDisk)
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if len(raw) == 0 || entry["bytes"] != float64(len(raw)) {
		t.Fatalf("bytes = %v, file = %d", entry["bytes"], len(raw))
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		t.Fatalf("saved file is not a PNG: %v", err)
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_n_and_quality_are_clamped, tools/tests/tools/test_image_gen_tool.py::test_only_low_quality_is_enabled
func TestImageClampsInputs(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	runImage(t, registry, map[string]any{
		"prompt":        "variations",
		"n":             99,
		"quality":       "ultra",
		"output_format": "bmp",
	})
	for _, requested := range []string{"high", "auto", "medium", "low"} {
		runImage(t, registry, map[string]any{"prompt": "x", "quality": requested})
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
func TestImageReferenceFileSentAsBase64(t *testing.T) {
	reference := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(reference, tinyPNGBytes(t), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})

	result := runImage(t, registry, map[string]any{
		"prompt":           "restyle this",
		"reference_images": []string{reference},
	})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	encoded, _ := body["image"].([]any)
	if len(encoded) != 1 || encoded[0] != tinyPNGBase64 {
		t.Fatalf("image = %v", body["image"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_too_many_reference_images
func TestImageTooManyReferenceImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("proxy should not be called")
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	})
	refs := make([]string, 11)
	for i := range refs {
		refs[i] = filepath.Join(t.TempDir(), "missing.png")
	}
	result := runImage(t, registry, map[string]any{
		"prompt":           "x",
		"reference_images": refs,
	})
	if message, _ := result["error"].(string); !strings.Contains(message, "Too many reference images") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_url_reference_image_fetched
func TestImageURLReferenceFetched(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ref.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNGBytes(t))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}, WithHTTPClient(&http.Client{Transport: &rewriteTransport{target: server.URL}}))

	result := runImage(t, registry, map[string]any{
		"prompt":           "edit",
		"reference_images": []string{"https://ref.example/ref.png"},
	})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	encoded, _ := body["image"].([]any)
	if len(encoded) != 1 || encoded[0] != tinyPNGBase64 {
		t.Fatalf("image = %v", body["image"])
	}
}

// Parity: tools/tests/tools/test_image_gen_tool.py::test_error_status_mapping
func TestImageErrorStatusMapping(t *testing.T) {
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
			registry := testRegistry(t, server, map[string]string{
				"HIVE_API_KEY":      "stream-token",
				"HIVE_STORAGE_PATH": t.TempDir(),
			})
			result := runImage(t, registry, map[string]any{"prompt": "x"})
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
func TestImageTimeoutMapsToError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	fast := server.Client()
	fast.Timeout = 10 * time.Millisecond
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}, WithHTTPClient(fast))

	result := runImage(t, registry, map[string]any{"prompt": "x"})
	message, _ := result["error"].(string)
	if !strings.Contains(strings.ToLower(message), "timed out") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: upstream's fixture deletes HIVE_LLM_BASE_URL, so the proxy
// base override path has no Python test; the tool documents and supports it.
func TestImageHonorsProxyBaseURLOverride(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	env := map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_LLM_BASE_URL": server.URL + "/",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}
	// No module override: only HIVE_LLM_BASE_URL may redirect the request.
	registry := NewRegistry(
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
		WithClock(func() time.Time { return time.UnixMilli(1700000000000) }),
	)
	result := runImage(t, registry, map[string]any{"prompt": "x"})
	if _, present := result["error"]; present {
		t.Fatalf("error = %v", result["error"])
	}
	if path != "/v1/images/generations" {
		t.Fatalf("path = %q", path)
	}
}

// Parity-exempt: the upstream tests never cover malformed proxy bodies; these
// branches mirror the Python handler's guards.
func TestImageMalformedProxyResponses(t *testing.T) {
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
			registry := testRegistry(t, server, map[string]string{
				"HIVE_API_KEY":      "stream-token",
				"HIVE_STORAGE_PATH": t.TempDir(),
			})
			result := runImage(t, registry, map[string]any{"prompt": "x"})
			message, _ := result["error"].(string)
			if !strings.Contains(message, testCase.needle) {
				t.Fatalf("error = %q, want %q", message, testCase.needle)
			}
		})
	}
}

// Parity-exempt: SSRF guard is Go-side hardening; upstream monkeypatches the
// guard away in its URL-reference test.
func TestImageBlocksNonPublicReferenceURL(t *testing.T) {
	var referenceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ref.png" {
			referenceHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNGBytes(t))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGBase64 + `"}]}`))
	}))
	defer server.Close()
	registry := testRegistry(t, server, map[string]string{
		"HIVE_API_KEY":      "stream-token",
		"HIVE_STORAGE_PATH": t.TempDir(),
	}, WithHTTPClient(&http.Client{Transport: &rewriteTransport{target: server.URL}}),
		WithResolver(func(context.Context, string) ([]string, error) {
			return []string{"127.0.0.1"}, nil
		}))

	result := runImage(t, registry, map[string]any{
		"prompt":           "edit",
		"reference_images": []string{"https://ref.example/ref.png"},
	})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "Blocked non-public") {
		t.Fatalf("error = %q", message)
	}
	if referenceHits.Load() != 0 {
		t.Fatal("reference URL was fetched despite the SSRF guard")
	}
}

// TestPythonJSONDumpsMatchesPythonPunctuation pins the serializer the model
// sees: sorted keys, ", " and ": " separators, integer/float spelling. The
// root package used tools.PythonJSONDumps; the subpackage copy must stay
// byte-identical.
// Parity-exempt: pins the Go-side serializer copy; no Python serializer test.
func TestPythonJSONDumpsMatchesPythonPunctuation(t *testing.T) {
	payload := map[string]any{
		"images": []map[string]any{{"bytes": 3, "path": "/tmp/a.png"}},
		"n":      1,
		"usage":  map[string]any{"credits": 8.5, "total": 312},
	}
	want := `{"images": [{"bytes": 3, "path": "/tmp/a.png"}], "n": 1, "usage": {"credits": 8.5, "total": 312}}`
	if got := pythonJSONDumps(payload); got != want {
		t.Fatalf("pythonJSONDumps:\n got %s\nwant %s", got, want)
	}
	if got := pythonJSONDumps(map[string]any{"tiny": 1e-7}); got != `{"tiny": 1e-07}` {
		t.Fatalf("float spelling = %s", got)
	}
}
