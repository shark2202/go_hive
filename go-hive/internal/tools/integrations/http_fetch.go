package integrations

// Compatibility seam for the HTTP and error-mapping helpers. The
// implementations live in internal/tools/integrationkit; the historical
// unqualified names below stay so existing provider files (including
// scrape.go) keep compiling unchanged.

import (
	"context"
	"net/http"
	"net/url"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// batch2Get performs one GET and returns the status, body and transport error.
// Redirect handling is left to the caller's http.Client.
func batch2Get(ctx context.Context, rt *Runtime, target string, headers map[string]string) (int, []byte, error) {
	return rt.GetBytes(ctx, target, headers)
}

// batch2GetWithHeaders is batch2Get plus the response headers (content type).
func batch2GetWithHeaders(ctx context.Context, rt *Runtime, target string, headers map[string]string) (int, http.Header, []byte, error) {
	return rt.GetBytesWithHeaders(ctx, target, headers)
}

// batch2GetJSON fetches a JSON document. A non-2xx status or malformed body is
// reported as a model-facing error map rather than a Go error.
func batch2GetJSON(ctx context.Context, rt *Runtime, target string, headers map[string]string) (any, map[string]any) {
	return rt.GetJSON(ctx, target, headers)
}

// batch2GetObject is batch2GetJSON narrowed to a JSON object.
func batch2GetObject(ctx context.Context, rt *Runtime, target string, headers map[string]string) (map[string]any, map[string]any) {
	return rt.GetJSONObject(ctx, target, headers)
}

// batch2PostForm posts an application/x-www-form-urlencoded body.
func batch2PostForm(ctx context.Context, rt *Runtime, target string, form url.Values, headers map[string]string) (int, []byte, error) {
	return rt.PostForm(ctx, target, form, headers)
}

// errTimeout reports whether an error (or a wrapped cause) is a timeout.
func errTimeout(err error) bool { return integrationkit.ErrTimeout(err) }

// batch2DecodeJSONObject decodes a JSON object body, reporting malformed
// documents as a model-facing error.
func batch2DecodeJSONObject(body []byte) (map[string]any, map[string]any) {
	return integrationkit.DecodeJSONObject(body)
}

// batch2Snippet trims a response body for error messages.
func batch2Snippet(body []byte) string { return integrationkit.Snippet(body) }

// batch2Object reads a nested object, returning an empty map when absent.
func batch2Object(root map[string]any, key string) map[string]any {
	return integrationkit.Object(root, key)
}

// batch2Array reads a nested array, returning nil when absent.
func batch2Array(root map[string]any, key string) []any {
	return integrationkit.Array(root, key)
}

// batch2EntityPattern is the entity matcher now owned by integrationkit; the
// name stays for provider files that post-process scraped markup.
var batch2EntityPattern = integrationkit.EntityPattern

// stripHTMLTags mirrors wikipedia_tool's regex-based tag removal.
func stripHTMLTags(text string) string { return integrationkit.StripHTMLTags(text) }

// htmlUnescape decodes the small entity set Wikipedia and DuckDuckGo emit.
func htmlUnescape(text string) string { return integrationkit.UnescapeHTML(text) }

// cleanHTMLText strips tags, decodes entities and collapses whitespace.
func cleanHTMLText(text string) string { return integrationkit.CleanHTMLText(text) }

// batch2Number coerces a JSON number (or numeric string) to a float64.
func batch2Number(value any) (float64, bool) { return integrationkit.Number(value) }

// batch2Round rounds to two decimals, matching Python's round(x, 2).
func batch2Round(value float64) float64 { return integrationkit.Round(value) }
