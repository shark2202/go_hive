// Batch 3 of the integration port: image generation through the Hive image
// proxy, plus the multi-provider web search tool. Everything lives in
// batch3_*.go so the batch-1 and batch-2 catalogs and registrations stay
// untouched.
//
// Image generation never carries a provider key of its own: it reuses the
// runtime's Hive proxy token (HIVE_API_KEY) and the proxy base URL
// (HIVE_LLM_BASE_URL, defaulting to the hosted proxy), exactly like the
// Python tool. Without a token the tool reports an explicit 503 instead of
// pretending an image was produced.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	// batch3ImageProxyBase mirrors framework.config.HIVE_LLM_ENDPOINT.
	batch3ImageProxyBase = "https://llm.open-hive.com"
	batch3ImagesPath     = "/v1/images/generations"

	batch3ImageDefaultModel = "gpt-image-2"
	batch3ImageDefaultSize  = "1024x1024"

	// batch3ImageMaxN caps runaway spend: every image is billed separately.
	batch3ImageMaxN = 4
	// batch3ImageMaxReferences matches OpenAI's edits payload ceiling.
	batch3ImageMaxReferences = 10
	batch3ImageMaxRefBytes   = 10 * 1024 * 1024
	// batch3ImageMaxTotalRefBytes bounds the whole base64 payload.
	batch3ImageMaxTotalRefBytes = 20 * 1024 * 1024

	// Image generation can take minutes; keep the same ceiling as upstream
	// (under the background-tool and MCP-client timeouts).
	batch3ImageRequestTimeout  = 225 * time.Second
	batch3ImageDownloadTimeout = 30 * time.Second
)

// batch3Deps carries the injectable side effects shared by the batch-3 tools.
// Tests point the HTTP client at an httptest server and replace the clock and
// sleep so retries stay fast.
type batch3Deps struct {
	http      *http.Client
	getenv    func(string) string
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	publicURL func(*url.URL) bool
	baseURL   string
	// scrapeResolver is the DNS lookup used by web_scrape's SSRF guard.
	// Declared here because batch-3 shares one deps struct across its tools.
	scrapeResolver batch3Resolver
}

type batch3Option func(*batch3Deps)

// withBatch3HTTPClient swaps the transport used by both tools.
func withBatch3HTTPClient(client *http.Client) batch3Option {
	return func(deps *batch3Deps) { deps.http = client }
}

// withBatch3Getenv overrides environment lookup (credential tests).
func withBatch3Getenv(fn func(string) string) batch3Option {
	return func(deps *batch3Deps) { deps.getenv = fn }
}

// withBatch3Clock overrides the clock used for artifact filenames.
func withBatch3Clock(now func() time.Time) batch3Option {
	return func(deps *batch3Deps) { deps.now = now }
}

// withBatch3Sleep swaps the retry backoff used by web_search.
func withBatch3Sleep(sleep func(context.Context, time.Duration) error) batch3Option {
	return func(deps *batch3Deps) { deps.sleep = sleep }
}

// withBatch3PublicURL swaps the reference-image SSRF check (tests that serve
// reference images from loopback).
func withBatch3PublicURL(check func(*url.URL) bool) batch3Option {
	return func(deps *batch3Deps) { deps.publicURL = check }
}

// withBatch3BaseURL points every batch-3 module at one fake upstream.
func withBatch3BaseURL(base string) batch3Option {
	return func(deps *batch3Deps) { deps.baseURL = strings.TrimRight(base, "/") }
}

func defaultBatch3Deps() batch3Deps {
	return batch3Deps{
		getenv: os.Getenv,
		now:    time.Now,
		sleep:  batch3Sleep,
		scrapeResolver: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		},
	}
}

// batch3Sleep waits for d unless the context ends first.
func batch3Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// InstallBatch3Media publishes the batch-3 media tools into a shared registry
// and returns the number of tools registered.
func InstallBatch3Media(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	registry := newBatch3MediaRegistry()
	for _, tool := range registry.Tools() {
		name := tool.Name
		target.Register(name, tools.Tool{
			Name:            name,
			Description:     tool.Description(),
			Parameters:      tool.Schema(),
			ConcurrencySafe: false,
		}, func(inputs map[string]any) (any, error) {
			return registry.Execute(context.Background(), name, inputs)
		})
	}
	return len(registry.Names())
}

// newBatch3MediaRegistry builds a registry containing only the batch-3
// tools, with a Runtime wired the same way as New() so transports, base URLs
// and credentials stay injectable.
func newBatch3MediaRegistry(opts ...batch3Option) *Registry {
	deps := defaultBatch3Deps()
	for _, opt := range opts {
		opt(&deps)
	}
	if deps.getenv == nil {
		deps.getenv = os.Getenv
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.sleep == nil {
		deps.sleep = batch3Sleep
	}
	modules := batch3Modules()
	if deps.baseURL != "" {
		for _, cfg := range modules {
			cfg.BaseURL = deps.baseURL
		}
	}
	runtime := &Runtime{
		HTTP:    newHTTPClient(deps.http),
		Now:     deps.now,
		Modules: modules,
		Getenv:  deps.getenv,
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{batch3ImageTool(), batch3WebSearchTool()} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	registry.addImpl("image_generate", "image_generate", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return imageGenerate(ctx, rt, input, deps)
	})
	registry.addImpl("web_search", "web_search", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return webSearch(ctx, rt, input, deps)
	})
	return registry
}

// batch3Modules declares the default endpoints. The image module's BaseURL is
// overridden by HIVE_LLM_BASE_URL at call time; tests use withBatch3BaseURL.
func batch3Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"image_gen_tool": {ID: "image_gen_tool", BaseURL: batch3ImageProxyBase},
		"brave_search":   {ID: "brave_search", BaseURL: "https://api.search.brave.com"},
		"google_cse":     {ID: "google_cse", BaseURL: "https://www.googleapis.com"},
	}
}

// batch3ModuleBaseURL resolves a module endpoint, falling back to the
// compiled-in default when no configuration is present.
func batch3ModuleBaseURL(rt *Runtime, moduleID, fallback string) string {
	base := ""
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			base = cfg.BaseURL
		}
	}
	if strings.TrimSpace(base) == "" {
		base = fallback
	}
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// batch3Env reads one environment value through the runtime seam.
func batch3Env(rt *Runtime, name string) string {
	if rt == nil || rt.Getenv == nil {
		return os.Getenv(name)
	}
	return rt.Getenv(name)
}

// batch3HTTPClient returns a client bounded by timeout, keeping a stricter
// timeout already configured on the injected client.
func batch3HTTPClient(rt *Runtime, timeout time.Duration) *http.Client {
	base := http.DefaultClient
	if rt != nil && rt.HTTP != nil && rt.HTTP.inner != nil {
		base = rt.HTTP.inner
	}
	if base.Timeout > 0 && base.Timeout <= timeout {
		return base
	}
	clone := *base
	clone.Timeout = timeout
	return &clone
}

// batch3IsTimeout reports whether err is a deadline/timeout failure.
func batch3IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return os.IsTimeout(err)
}

// batch3ImageTool declares image_generate, one action with the upstream
// parameter list and defaults.
func batch3ImageTool() *Tool {
	return &Tool{
		ID:          "image_gen_tool",
		Name:        "image_generate",
		DisplayName: "Image Gen",
		Summary:     "Generate or edit an image from a text prompt. Billed to Hive credits.",
		Actions: []Action{
			{
				Name:    "image_generate",
				Summary: "Generate or edit an image from a text prompt, billed to Hive credits. Provide reference images to edit or compose from existing pictures.",
				Source:  "image_gen_tool/image_gen_tool.py",
				Required: []string{
					"prompt",
				},
				Params: map[string]any{
					"prompt":           param("string"),
					"reference_images": param("array"),
					"size":             paramDefault("string", batch3ImageDefaultSize),
					"quality":          paramDefault("string", "low"),
					"n":                paramDefault("integer", 1),
					"output_format":    paramDefault("string", "png"),
					"model":            paramDefault("string", batch3ImageDefaultModel),
				},
			},
		},
	}
}

// imageGenerate posts one generation/edit request to the Hive image proxy and
// saves every returned image under the tool-artifact directory.
func imageGenerate(ctx context.Context, rt *Runtime, input map[string]any, deps batch3Deps) (any, error) {
	token := strings.TrimSpace(batch3Env(rt, "HIVE_API_KEY"))
	if token == "" {
		// The Hive proxy bills generation to the user's credits and is the
		// only key path; without it the capability is unavailable (503), not
		// merely misconfigured.
		return map[string]any{
			"error":  "Image generation unavailable: missing Hive proxy token. Sign in to refresh credentials, then retry.",
			"status": http.StatusServiceUnavailable,
		}, nil
	}

	model := strings.TrimSpace(stringArg(input, "model"))
	if model == "" {
		model = batch3ImageDefaultModel
	}
	size := strings.TrimSpace(stringArg(input, "size"))
	if size == "" {
		size = batch3ImageDefaultSize
	}
	// Only "low" quality is enabled for cost control; every higher tier is
	// forced down, matching the upstream clamp.
	quality := strings.TrimSpace(stringArg(input, "quality"))
	if quality != "low" {
		quality = "low"
	}
	n := intArg(input, "n", 1)
	if n < 1 {
		n = 1
	}
	if n > batch3ImageMaxN {
		n = batch3ImageMaxN
	}
	format := strings.ToLower(strings.TrimSpace(stringArg(input, "output_format")))
	if format != "png" && format != "jpeg" && format != "webp" {
		format = "png"
	}

	body := map[string]any{
		"model":         model,
		"prompt":        stringArg(input, "prompt"),
		"size":          size,
		"quality":       quality,
		"n":             n,
		"output_format": format,
	}

	references := stringSliceArg(input, "reference_images")
	if len(references) > batch3ImageMaxReferences {
		return map[string]any{"error": fmt.Sprintf("Too many reference images: %d (max %d).", len(references), batch3ImageMaxReferences)}, nil
	}
	if len(references) > 0 {
		encoded := make([]string, 0, len(references))
		total := 0
		for _, source := range references {
			payload, sizeBytes, err := batch3LoadReference(ctx, rt, deps, source)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			total += sizeBytes
			if total > batch3ImageMaxTotalRefBytes {
				return map[string]any{"error": fmt.Sprintf("Reference images total exceeds %dMB.", batch3ImageMaxTotalRefBytes/(1024*1024))}, nil
			}
			encoded = append(encoded, payload)
		}
		body["image"] = encoded
	}

	base := strings.TrimRight(strings.TrimSpace(batch3Env(rt, "HIVE_LLM_BASE_URL")), "/")
	if base == "" {
		base = batch3ModuleBaseURL(rt, "image_gen_tool", batch3ImageProxyBase)
	}
	target := base + batch3ImagesPath

	payload, err := json.Marshal(body)
	if err != nil {
		return map[string]any{"error": "Failed to encode image request: " + err.Error()}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, batch3ImageRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return map[string]any{"error": "Failed to build image request: " + err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := batch3HTTPClient(rt, batch3ImageRequestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if batch3IsTimeout(err) {
			// Do not auto-retry: the image may have been generated and billed.
			return map[string]any{"error": "Image generation timed out. Try again; do not retry repeatedly."}, nil
		}
		return map[string]any{"error": "Network error reaching image service: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if readErr != nil {
		return map[string]any{"error": "Failed to read image service response: " + readErr.Error()}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return batch3ImageProxyError(resp.StatusCode, raw), nil
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return map[string]any{"error": "Image service returned a non-JSON response."}, nil
	}
	items, _ := data["data"].([]any)
	if len(items) == 0 {
		return map[string]any{"error": "Image service returned no image data."}, nil
	}

	outDir, err := batch3ImageArtifactDir(rt)
	if err != nil {
		return map[string]any{"error": "Failed to resolve output directory: " + err.Error()}, nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return map[string]any{"error": "Failed to create output directory: " + err.Error()}, nil
	}

	ext := batch3ImageExtension(format)
	stamp := deps.now().UnixMilli()
	saved := make([]map[string]any, 0, len(items))
	for index, entry := range items {
		item, _ := entry.(map[string]any)
		encoded, _ := item["b64_json"].(string)
		if encoded == "" {
			continue
		}
		imageBytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		// Re-encode from pixels so ancillary chunks (the C2PA provenance
		// manifest) never reach disk. Best-effort: raw bytes survive any
		// decode/encode failure.
		imageBytes = batch3PostprocessImage(imageBytes, format)
		path := filepath.Join(outDir, fmt.Sprintf("image_gen_%d_%d.%s", stamp, index, ext))
		if err := os.WriteFile(path, imageBytes, 0o644); err != nil {
			return map[string]any{"error": "Failed to write generated image: " + err.Error()}, nil
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		saved = append(saved, map[string]any{
			"path":    absolute,
			"size":    size,
			"quality": quality,
			"format":  format,
			"bytes":   len(imageBytes),
		})
	}
	if len(saved) == 0 {
		return map[string]any{"error": "Image service returned data but no decodable image."}, nil
	}

	summary := map[string]any{
		"images":    saved,
		"model":     model,
		"n":         len(saved),
		"usage":     data["usage"],
		"next_step": "Call attach_file(path) on an image path to show the user a downloadable chip.",
	}
	if len(saved) > 1 {
		summary["note"] = "Only the first image is previewed inline; the rest are on disk — attach_file them to show the user."
	}
	return summary, nil
}

// batch3ImageProxyError maps a non-200 proxy response onto the upstream error
// dict, keeping the HTTP status for callers that surface it.
func batch3ImageProxyError(status int, raw []byte) map[string]any {
	detail := batch3ImageErrorDetail(raw)
	switch status {
	case http.StatusPaymentRequired:
		return map[string]any{
			"error":  "Out of Hive credits (or subscription inactive): " + detail,
			"status": http.StatusPaymentRequired,
		}
	case http.StatusUnauthorized:
		return map[string]any{
			"error":  "Authentication failed reaching the image service (proxy token rejected).",
			"status": http.StatusUnauthorized,
		}
	case http.StatusForbidden:
		return map[string]any{
			"error":  "Image model unavailable (provider access / org verification): " + detail,
			"status": http.StatusForbidden,
		}
	case http.StatusTooManyRequests:
		return map[string]any{
			"error":  "Image service rate-limited. Wait a moment, then retry once.",
			"status": http.StatusTooManyRequests,
		}
	case http.StatusBadRequest:
		return map[string]any{
			"error":  "Image request rejected: " + detail + ". Rephrase the prompt if it was moderated.",
			"status": http.StatusBadRequest,
		}
	default:
		return map[string]any{
			"error":  fmt.Sprintf("Image service error (HTTP %d): %s", status, detail),
			"status": status,
		}
	}
}

// batch3ImageErrorDetail extracts the human-readable message from a proxy or
// OpenAI error body, both of which nest it under "error".
func batch3ImageErrorDetail(raw []byte) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err == nil {
		if object, ok := body["error"].(map[string]any); ok {
			if message, ok := object["message"].(string); ok && message != "" {
				return message
			}
			if kind, ok := object["type"].(string); ok && kind != "" {
				return kind
			}
		}
		if message, ok := body["error"].(string); ok && message != "" {
			return message
		}
	}
	detail := strings.TrimSpace(string(raw))
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return detail
}

// batch3ImageExtension maps the requested format onto the saved file suffix.
func batch3ImageExtension(format string) string {
	switch format {
	case "jpeg":
		return "jpg"
	case "webp":
		return "webp"
	default:
		return "png"
	}
}

// batch3PostprocessImage re-encodes a generated PNG/JPEG from its pixels.
// Pillow's denoise/unsharp passes have no stdlib equivalent, but the re-encode
// still drops ancillary metadata (C2PA/EXIF/XMP) exactly like upstream's
// "save from pixels" step. WebP has no stdlib codec, so its bytes are kept.
func batch3PostprocessImage(raw []byte, format string) []byte {
	switch format {
	case "png":
		decoded, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		var out bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := encoder.Encode(&out, decoded); err != nil {
			return raw
		}
		return out.Bytes()
	case "jpeg":
		decoded, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&out, decoded, &jpeg.Options{Quality: 95}); err != nil {
			return raw
		}
		return out.Bytes()
	default:
		return raw
	}
}

// batch3ImageArtifactDir mirrors the upstream resolution order:
// <HIVE_STORAGE_PATH>/data, then <HIVE_HOME>/tool-artifacts, then
// ~/.hive/tool-artifacts.
func batch3ImageArtifactDir(rt *Runtime) (string, error) {
	if storage := strings.TrimSpace(batch3Env(rt, "HIVE_STORAGE_PATH")); storage != "" {
		return filepath.Join(batch3ExpandHome(storage), "data"), nil
	}
	if home := strings.TrimSpace(batch3Env(rt, "HIVE_HOME")); home != "" {
		return filepath.Join(batch3ExpandHome(home), "tool-artifacts"), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".hive", "tool-artifacts"), nil
}

// batch3ExpandHome expands a leading "~" without touching any other path.
func batch3ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// batch3LoadReference turns one local path or http(s) URL into base64 plus its
// decoded byte length, applying the upstream size and SSRF limits.
func batch3LoadReference(ctx context.Context, rt *Runtime, deps batch3Deps, source string) (string, int, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return batch3DownloadReference(ctx, rt, deps, source)
	}
	path := batch3ExpandHome(source)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("Reference image not found: %s", source)
	}
	if info.Size() > batch3ImageMaxRefBytes {
		return "", 0, fmt.Errorf("Reference image exceeds %dMB: %s", batch3ImageMaxRefBytes/(1024*1024), source)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("Failed to read reference image %s: %v", source, err)
	}
	return base64.StdEncoding.EncodeToString(raw), len(raw), nil
}

// batch3DownloadReference fetches a reference image URL after the SSRF guard.
func batch3DownloadReference(ctx context.Context, rt *Runtime, deps batch3Deps, source string) (string, int, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Hostname() == "" {
		return "", 0, fmt.Errorf("Blocked non-public or unresolvable image URL: %s", source)
	}
	check := deps.publicURL
	if check == nil {
		check = batch3URLIsPublic
	}
	if !check(parsed) {
		return "", 0, fmt.Errorf("Blocked non-public or unresolvable image URL: %s", source)
	}
	requestCtx, cancel := context.WithTimeout(ctx, batch3ImageDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, source, nil)
	if err != nil {
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	client := batch3HTTPClient(rt, batch3ImageDownloadTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if batch3IsTimeout(err) {
			return "", 0, fmt.Errorf("Reference image download timed out: %s", source)
		}
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("Reference image fetch failed (HTTP %d): %s", resp.StatusCode, source)
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "image/") {
		if contentType == "" {
			contentType = "unknown"
		}
		return "", 0, fmt.Errorf("Reference URL is not an image (content-type %s): %s", contentType, source)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch3ImageMaxRefBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	if len(raw) > batch3ImageMaxRefBytes {
		return "", 0, fmt.Errorf("Reference image exceeds %dMB: %s", batch3ImageMaxRefBytes/(1024*1024), source)
	}
	return base64.StdEncoding.EncodeToString(raw), len(raw), nil
}

// batch3URLIsPublic resolves the URL host and rejects any non-public target.
// It fails closed on resolution errors.
func batch3URLIsPublic(parsed *url.URL) bool {
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if !batch3IsPublicIP(address.IP) {
			return false
		}
	}
	return true
}

// batch3IsPublicIP mirrors Python's ipaddress.is_global guard.
func batch3IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || !ip.IsGlobalUnicast() {
		return false
	}
	return true
}
