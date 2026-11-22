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
//	marketing_productivity.go            registry, module configs, HTTP helpers
//	zoom.go       zoom meetings/recordings/users
//	google_analytics.go     GA4 Data API + Search Console
//	cloudinary.go Cloudinary Admin/Upload API (signed uploads)
//	microsoft_graph.go      Microsoft Graph mail/teams/onedrive
//	email.go      Resend + Gmail senders
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
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

const (
	batch7Lane9MaxBody = 8 << 20

	// Public API roots; tests replace them with WithBaseURL.
	batch7Lane9ZoomBase       = "https://api.zoom.us/v2"
	gaBase                    = "https://analyticsdata.googleapis.com/v1beta"
	gscBase                   = "https://www.googleapis.com/webmasters/v3"
	gscInspectionBase         = "https://searchconsole.googleapis.com/v1"
	batch7Lane9CloudinaryBase = "https://api.cloudinary.com/v1_1"
	batch7Lane9GraphBase      = "https://graph.microsoft.com/v1.0"
	batch7Lane9ResendBase     = "https://api.resend.com"
	batch7Lane9GmailBase      = "https://gmail.googleapis.com/gmail/v1"
	marketingGoogleTokenURI   = "https://oauth2.googleapis.com/token"
	gaReadScope               = "https://www.googleapis.com/auth/analytics.readonly"
	gscReadScope              = "https://www.googleapis.com/auth/webmasters.readonly"
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
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch7Lane9ZoomTool,
		googleAnalyticsTool,
		searchConsoleTool,
		batch7Lane9CloudinaryTool,
		batch7Lane9GraphTool,
		batch7Lane9EmailTool,
	} {
		registry.AddTool(tool)
	}
	for tool, impls := range map[string]map[string]ActionFunc{
		"zoom":                  batch7Lane9ZoomImpls(),
		"google_analytics":      googleAnalyticsImpls(),
		"google_search_console": searchConsoleImpls(),
		"cloudinary":            batch7Lane9CloudinaryImpls(),
		"microsoft_graph":       batch7Lane9GraphImpls(),
		"email":                 batch7Lane9EmailImpls(),
	} {
		for action, impl := range impls {
			registry.AddImpl(tool, action, impl)
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
			BaseURL: gaBase,
			EnvVars: []string{"GA_ACCESS_TOKEN", "GOOGLE_ACCESS_TOKEN"},
		},
		"google_search_console_tool": {
			ID:      "google_search_console_tool",
			BaseURL: gscBase,
			EnvVars: []string{"GOOGLE_SEARCH_CONSOLE_TOKEN", "GOOGLE_ACCESS_TOKEN"},
		},
		// Inspection lives on a second API root; the credential is still the
		// google_search_console_tool one.
		"google_search_console_inspection_tool": {
			ID:      "google_search_console_inspection_tool",
			BaseURL: gscInspectionBase,
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
			// The Gmail sender shares upstream's `google` OAuth2 credential
			// (credentials/email.py); expired tokens refresh before sending.
			OAuth: batch5Lane3OAuth,
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// marketingBase resolves a module base URL with a production fallback.
func marketingBase(rt *Runtime, moduleID, fallback string) string {
	if base := moduleBaseURL(rt, moduleID); base != "" {
		return base
	}
	return fallback
}

// batch7Lane9Timestamp renders the unix-seconds timestamp Cloudinary signs.
func batch7Lane9Timestamp(rt *Runtime) int64 {
	return runtimeNow(rt).UTC().Unix()
}

// marketingBearer renders the bearer header pair.
func marketingBearer(token string) map[string]string {
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
	resp, err := runtimeHTTP(rt).Do(req)
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

// marketingDoJSON marshals payload as JSON and sends it.
func marketingDoJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*batch7Lane9Response, error) {
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

// marketingDoForm posts an application/x-www-form-urlencoded body.
func marketingDoForm(ctx context.Context, rt *Runtime, endpoint string, headers map[string]string, form url.Values, timeout time.Duration) (*batch7Lane9Response, error) {
	merged := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for name, value := range headers {
		merged[name] = value
	}
	return batch7Lane9Send(ctx, rt, http.MethodPost, endpoint, merged, []byte(form.Encode()), timeout)
}

// batch7Lane9HTTPError renders the upstream "HTTP <code>: <body[:500]>" error.
func batch7Lane9HTTPError(resp *batch7Lane9Response) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, truncateText(string(resp.Body), 500)),
	}
}

// marketingIntOr reads an integer field with a fallback.
func marketingIntOr(object map[string]any, name string, fallback int64) int64 {
	if parsed, ok := int64Value(object[name]); ok {
		return parsed
	}
	return fallback
}

// marketingFloatOr reads a float field with a fallback.
func marketingFloatOr(object map[string]any, name string, fallback float64) float64 {
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

// marketingRound rounds to the given number of decimal digits.
func marketingRound(value float64, digits int) float64 {
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}

// marketingStrictEscape mirrors Python's urllib.parse.quote(value, safe="")
// so a site URL becomes one opaque path segment on the wire.
func marketingStrictEscape(value string) string {
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

// marketingArrayParam declares an array-of-strings parameter schema.
func marketingArrayParam() map[string]any {
	return map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
}
