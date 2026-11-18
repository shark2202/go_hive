// Batch 7 lane 9 of the integration port: marketing, productivity and
// media-hosting services.
//
// Covers zoom (9 actions), google_analytics (7), google_search_console (8),
// cloudinary (8), microsoft_graph (11) and email (2) — 45 actions total.
// Every module is a REST wrapper around the same public API the upstream
// Python module targets; no vendor SDK is used.
//
// Requests run on Runtime.HTTP so each action is exercisable against an
// httptest server. Credentials resolve from WithCredential, the module's
// environment variables, or WithGetenv. Files in this lane:
//
//	batch7_lane9.go            registry, module configs, HTTP helpers
//	batch7_lane9_zoom.go       zoom meetings/recordings/users
//	batch7_lane9_google.go     GA4 Data API + Search Console
//	batch7_lane9_cloudinary.go Cloudinary Admin/Upload API (signed uploads)
//	batch7_lane9_graph.go      Microsoft Graph mail/teams/onedrive
//	batch7_lane9_email.go      Resend + Gmail senders
//
// Deliberate limits, stated per module:
//   - google_analytics: upstream uses google-analytics-data. This port calls
//     the Data API v1beta REST surface and accepts an already-minted OAuth
//     token (GA_ACCESS_TOKEN / GOOGLE_ACCESS_TOKEN, or WithCredential) or
//     mints one from a service-account JSON at GOOGLE_APPLICATION_CREDENTIALS.
//     gcloud ADC files are not read.
//   - google_search_console: Search Console has two API roots (webmasters/v3
//     for analytics and sitemaps, searchconsole/v1 for URL inspection), so the
//     lane declares two module entries. Both share one credential.
//   - cloudinary: uploads are signed with the Cloudinary SHA1 scheme
//     (sorted params + API secret); requests also carry the Basic auth header
//     the upstream module sends.
//   - microsoft_graph: onedrive_download_file follows the provider's
//     @microsoft.graph.downloadUrl and does not re-send JSON content types.
//   - email: the gmail provider builds MIME in-process (multipart/alternative
//     with one HTML part) instead of using Python's email package. The
//     optional account alias is accepted for parity; without a credential
//     store it selects nothing.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane9MaxBody = 8 << 20

	// Public API roots; tests replace them with WithBaseURL.
	batch7Lane9ZoomBase          = "https://api.zoom.us/v2"
	batch7Lane9GABase            = "https://analyticsdata.googleapis.com/v1beta"
	batch7Lane9GSCBase           = "https://www.googleapis.com/webmasters/v3"
	batch7Lane9GSCInspectionBase = "https://searchconsole.googleapis.com/v1"
	batch7Lane9CloudinaryBase    = "https://api.cloudinary.com/v1_1"
	batch7Lane9GraphBase         = "https://graph.microsoft.com/v1.0"
	batch7Lane9ResendBase        = "https://api.resend.com"
	batch7Lane9GmailBase         = "https://gmail.googleapis.com/gmail/v1"
	batch7Lane9GoogleTokenURI    = "https://oauth2.googleapis.com/token"
	batch7Lane9GAReadScope       = "https://www.googleapis.com/auth/analytics.readonly"
	batch7Lane9GSCReadScope      = "https://www.googleapis.com/auth/webmasters.readonly"
)

// InstallBatch7Lane9 publishes the batch-7 lane-9 tools into a shared registry
// and returns the number of tools registered.
func InstallBatch7Lane9(target *tools.ToolRegistry) int {
	return newBatch7Lane9Registry().Install(target)
}

// newBatch7Lane9Registry builds a registry containing only the lane-9 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane9Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane9Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{
		batch7Lane9ZoomTool,
		batch7Lane9GoogleAnalyticsTool,
		batch7Lane9SearchConsoleTool,
		batch7Lane9CloudinaryTool,
		batch7Lane9GraphTool,
		batch7Lane9EmailTool,
	} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for tool, impls := range map[string]map[string]ActionFunc{
		"zoom":                  batch7Lane9ZoomImpls(),
		"google_analytics":      batch7Lane9GoogleAnalyticsImpls(),
		"google_search_console": batch7Lane9SearchConsoleImpls(),
		"cloudinary":            batch7Lane9CloudinaryImpls(),
		"microsoft_graph":       batch7Lane9GraphImpls(),
		"email":                 batch7Lane9EmailImpls(),
	} {
		for action, impl := range impls {
			registry.addImpl(tool, action, impl)
		}
	}
	return registry
}

// batch7Lane9Modules declares the public endpoints and credential variables
// for the lane. Tests override BaseURL with WithBaseURL.
func batch7Lane9Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"zoom_tool": {
			ID:      "zoom_tool",
			BaseURL: batch7Lane9ZoomBase,
			EnvVars: []string{"ZOOM_ACCESS_TOKEN"},
		},
		"google_analytics_tool": {
			ID:      "google_analytics_tool",
			BaseURL: batch7Lane9GABase,
			EnvVars: []string{"GA_ACCESS_TOKEN", "GOOGLE_ACCESS_TOKEN"},
		},
		"google_search_console_tool": {
			ID:      "google_search_console_tool",
			BaseURL: batch7Lane9GSCBase,
			EnvVars: []string{"GOOGLE_SEARCH_CONSOLE_TOKEN", "GOOGLE_ACCESS_TOKEN"},
		},
		// Inspection lives on a second API root; the credential is still the
		// google_search_console_tool one.
		"google_search_console_inspection_tool": {
			ID:      "google_search_console_inspection_tool",
			BaseURL: batch7Lane9GSCInspectionBase,
		},
		"cloudinary_tool": {
			ID:      "cloudinary_tool",
			BaseURL: batch7Lane9CloudinaryBase,
			EnvVars: []string{"CLOUDINARY_CLOUD_NAME", "CLOUDINARY_API_KEY", "CLOUDINARY_API_SECRET"},
		},
		"microsoft_graph_tool": {
			ID:      "microsoft_graph_tool",
			BaseURL: batch7Lane9GraphBase,
			EnvVars: []string{"MICROSOFT_GRAPH_ACCESS_TOKEN"},
		},
		"email_tool": {
			ID:      "email_tool",
			BaseURL: batch7Lane9ResendBase,
			EnvVars: []string{"RESEND_API_KEY"},
		},
		"email_gmail_tool": {
			ID:      "email_gmail_tool",
			BaseURL: batch7Lane9GmailBase,
			EnvVars: []string{"GOOGLE_ACCESS_TOKEN"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane9Getenv resolves an environment variable through the runtime.
func batch7Lane9Getenv(rt *Runtime, name string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv(name)
	}
	return os.Getenv(name)
}

// batch7Lane9ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup (EnvVars order is precedence order).
func batch7Lane9ModuleToken(rt *Runtime, moduleID string) string {
	return batch4ModuleToken(rt, moduleID)
}

// batch7Lane9ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch7Lane9ModuleURL(rt *Runtime, moduleID string) string {
	return batch4ModuleURL(rt, moduleID)
}

// batch7Lane9Base resolves a module base URL with a production fallback.
func batch7Lane9Base(rt *Runtime, moduleID, fallback string) string {
	if base := batch7Lane9ModuleURL(rt, moduleID); base != "" {
		return base
	}
	return fallback
}

// batch7Lane9Now honours a deterministic clock injected by tests.
func batch7Lane9Now(rt *Runtime) time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// batch7Lane9Timestamp renders the unix-seconds timestamp Cloudinary signs.
func batch7Lane9Timestamp(rt *Runtime) int64 {
	return batch7Lane9Now(rt).UTC().Unix()
}

// batch7Lane9Bearer renders the bearer header pair.
func batch7Lane9Bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// batch7Lane9JSONHeaders adds a JSON content type to a header map.
func batch7Lane9JSONHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/json"
	return headers
}

// batch7Lane9BasicAuth renders the Cloudinary Basic auth header value.
func batch7Lane9BasicAuth(key, secret string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(key + ":" + secret))
	return "Basic " + encoded
}

// batch7Lane9Response is one completed HTTP exchange.
type batch7Lane9Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// batch7Lane9Send performs one HTTP request with an optional context timeout.
// Transport failures are returned as Go errors; HTTP status handling belongs
// to the caller.
func batch7Lane9Send(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (*batch7Lane9Response, error) {
	requestCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := batch4HTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane9MaxBody))
	if err != nil {
		return nil, err
	}
	return &batch7Lane9Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// batch7Lane9DoJSON marshals payload as JSON and sends it.
func batch7Lane9DoJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*batch7Lane9Response, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	return batch7Lane9Send(ctx, rt, method, endpoint, batch7Lane9JSONHeaders(headers), body, timeout)
}

// batch7Lane9DoForm posts an application/x-www-form-urlencoded body.
func batch7Lane9DoForm(ctx context.Context, rt *Runtime, endpoint string, headers map[string]string, form url.Values, timeout time.Duration) (*batch7Lane9Response, error) {
	merged := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for name, value := range headers {
		merged[name] = value
	}
	return batch7Lane9Send(ctx, rt, http.MethodPost, endpoint, merged, []byte(form.Encode()), timeout)
}

// batch7Lane9JSONObject decodes a JSON object body.
func batch7Lane9JSONObject(raw []byte) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

// batch7Lane9HTTPError renders the upstream "HTTP <code>: <body[:500]>" error.
func batch7Lane9HTTPError(resp *batch7Lane9Response) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch4TruncateText(string(resp.Body), 500)),
	}
}

// batch7Lane9TransportError renders a labelled transport failure.
func batch7Lane9TransportError(ctx context.Context, label string, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request to " + label + " timed out"}
	}
	return map[string]any{"error": label + " request failed: " + err.Error()}
}

// batch7Lane9StringOr reads a string field with a fallback.
func batch7Lane9StringOr(object map[string]any, name, fallback string) string {
	if object == nil {
		return fallback
	}
	if text, ok := object[name].(string); ok {
		return text
	}
	return fallback
}

// batch7Lane9Int64 converts common JSON numeric representations.
func batch7Lane9Int64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// batch7Lane9IntOr reads an integer field with a fallback.
func batch7Lane9IntOr(object map[string]any, name string, fallback int64) int64 {
	if parsed, ok := batch7Lane9Int64(object[name]); ok {
		return parsed
	}
	return fallback
}

// batch7Lane9FloatOr reads a float field with a fallback.
func batch7Lane9FloatOr(object map[string]any, name string, fallback float64) float64 {
	value, present := object[name]
	if !present || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// batch7Lane9Bool reads a decoded JSON boolean.
func batch7Lane9Bool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch7Lane9Clamp bounds an integer to [low, high].
func batch7Lane9Clamp(value, low, high int) int {
	return batch4Clamp(value, low, high)
}

// batch7Lane9Round rounds to the given number of decimal digits.
func batch7Lane9Round(value float64, digits int) float64 {
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}

// batch7Lane9PathEscape escapes one URL path segment.
func batch7Lane9PathEscape(value string) string {
	return url.PathEscape(value)
}

// batch7Lane9StrictEscape mirrors Python's urllib.parse.quote(value, safe="")
// so a site URL becomes one opaque path segment on the wire.
func batch7Lane9StrictEscape(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		switch {
		case char >= 'A' && char <= 'Z',
			char >= 'a' && char <= 'z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.', char == '~':
			builder.WriteByte(char)
		default:
			builder.WriteString(fmt.Sprintf("%%%02X", char))
		}
	}
	return builder.String()
}

// batch7Lane9IsTimeout reports whether err is a deadline or a net timeout.
func batch7Lane9IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch7Lane9ArrayParam declares an array-of-strings parameter schema.
func batch7Lane9ArrayParam() map[string]any {
	return map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
}
