// Batch 6, lane 3 of the integration port: finance and commerce modules from
// the Python upstream — shopify_tool (9 actions), quickbooks_tool (8),
// plaid_tool (6) and razorpay_tool (6); 29 actions in total.
//
// Each action keeps the upstream name, parameter names/defaults and response
// projection. Requests go through Runtime.HTTP so every action is exercisable
// against an httptest server; credentials resolve from WithCredential or the
// documented environment variables.
//
// Credential shapes (one module token string, "primary|secondary"):
//   - shopify_tool:    "<access_token>|<store_name>"; a bare token falls back
//     to SHOPIFY_STORE_NAME.
//   - quickbooks_tool: "<access_token>|<realm_id>"; a bare token falls back to
//     QUICKBOOKS_REALM_ID.
//   - plaid_tool:      "<client_id>|<secret>".
//   - razorpay_tool:   "<api_key>|<api_secret>".
//
// Money handling mirrors upstream: Shopify and QuickBooks pass provider amounts
// through unchanged (price/balance stay strings or JSON numbers exactly as the
// API sent them), Plaid keeps signed transaction amounts as JSON numbers, and
// Razorpay keeps amounts in the smallest currency unit as integers.
//
// Deliberate limits:
//   - Shopify pins Admin API version 2025-01, like upstream.
//   - QuickBooks timeouts/network errors become error dicts instead of
//     propagating, matching the rest of the port; HTTP failures keep the
//     upstream "HTTP <status>: <body[:500]>" text.
//   - Plaid requests are always POSTed with client_id/secret in the body;
//     PLAID_ENV only selects the default base URL (WithBaseURL wins).
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch6Lane3MaxBody  = 8 << 20
	batch6Lane3MaxError = 500
)

// InstallBatch6Lane3 publishes the batch-6 lane-3 tools (shopify, quickbooks,
// plaid, razorpay) into a shared registry and returns the tool count.
func InstallBatch6Lane3(target *tools.ToolRegistry) int {
	return newBatch6Lane3Registry().Install(target)
}

// newBatch6Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane3Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch6Lane3Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch6Lane3ShopifyTool,
		batch6Lane3QuickBooksTool,
		batch6Lane3PlaidTool,
		batch6Lane3RazorpayTool,
	} {
		registry.AddTool(tool)
	}
	for action, impl := range batch6Lane3ShopifyImpls() {
		registry.AddImpl("shopify", action, impl)
	}
	for action, impl := range batch6Lane3QuickBooksImpls() {
		registry.AddImpl("quickbooks", action, impl)
	}
	for action, impl := range batch6Lane3PlaidImpls() {
		registry.AddImpl("plaid", action, impl)
	}
	for action, impl := range batch6Lane3RazorpayImpls() {
		registry.AddImpl("razorpay", action, impl)
	}
	return registry
}

// batch6Lane3Modules declares the credential variables for the four modules.
// Base URLs start empty where the provider needs per-account segments
// (Shopify store, QuickBooks realm); the action helpers derive them.
func batch6Lane3Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"shopify_tool": {
			ID:      "shopify_tool",
			EnvVars: []string{"SHOPIFY_ACCESS_TOKEN"},
		},
		"quickbooks_tool": {
			ID:      "quickbooks_tool",
			EnvVars: []string{"QUICKBOOKS_ACCESS_TOKEN"},
		},
		"plaid_tool": {
			ID:      "plaid_tool",
			EnvVars: []string{"PLAID_CLIENT_ID"},
			ResolveToken: batch6Lane3PairResolver(
				"PLAID_CLIENT_ID", "PLAID_SECRET",
			),
		},
		"razorpay_tool": {
			ID:      "razorpay_tool",
			EnvVars: []string{"RAZORPAY_API_KEY"},
			ResolveToken: batch6Lane3PairResolver(
				"RAZORPAY_API_KEY", "RAZORPAY_API_SECRET",
			),
			Headers: func(token string) map[string]string {
				return map[string]string{"Authorization": "Basic " + token}
			},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch6Lane3PairResolver joins two required environment variables into one
// "first|second" credential, or reports which half is missing.
func batch6Lane3PairResolver(first, second string) func(func(string) string) (string, []string) {
	return func(getenv func(string) string) (string, []string) {
		head := strings.TrimSpace(getenv(first))
		tail := strings.TrimSpace(getenv(second))
		if head == "" || tail == "" {
			missing := make([]string, 0, 2)
			if head == "" {
				missing = append(missing, first)
			}
			if tail == "" {
				missing = append(missing, second)
			}
			return "", missing
		}
		return head + "|" + tail, nil
	}
}

// batch6Lane3SplitCredential splits "primary|secondary" credentials supplied
// through WithCredential. The boolean reports whether a separator was present.
func batch6Lane3SplitCredential(value string) (string, string, bool) {
	head, tail, found := strings.Cut(value, "|")
	if !found {
		return strings.TrimSpace(value), "", false
	}
	return strings.TrimSpace(head), strings.TrimSpace(tail), true
}

// batch6Lane3ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch6Lane3ModuleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch6Lane3ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch6Lane3ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}

// batch6Lane3Env resolves an auxiliary environment value (store name, realm,
// sandbox flag) through the runtime lookup.
func batch6Lane3Env(rt *Runtime, name string) string {
	getenv := os.Getenv
	if rt != nil && rt.Getenv != nil {
		getenv = rt.Getenv
	}
	return strings.TrimSpace(getenv(name))
}

// batch6Lane3HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch6Lane3HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch6Lane3Now reads the runtime clock, defaulting to time.Now.
func batch6Lane3Now(rt *Runtime) time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// batch6Lane3IsTimeout reports whether err is a deadline or a net timeout.
func batch6Lane3IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch6Lane3Truncate caps provider error bodies the way upstream slices
// response.text[:500].
func batch6Lane3Truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// batch6Lane3AsMap safely views a decoded JSON object.
func batch6Lane3AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch6Lane3AsSlice safely views a decoded JSON array.
func batch6Lane3AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch6Lane3AsString reads a decoded JSON string.
func batch6Lane3AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch6Lane3AsBool reads a decoded JSON boolean.
func batch6Lane3AsBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch6Lane3ValueOr mirrors Python's dict.get(key, fallback): a present null
// stays null, only an absent key takes the fallback.
func batch6Lane3ValueOr(object map[string]any, key string, fallback any) any {
	if value, present := object[key]; present {
		return value
	}
	return fallback
}

// batch6Lane3OptionalInt distinguishes an absent/null argument from 0.
func batch6Lane3OptionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// batch6Lane3OptionalFloat distinguishes an absent/null argument from 0.
func batch6Lane3OptionalFloat(input map[string]any, name string) (float64, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// batch6Lane3OptionalMap distinguishes an absent/null object from {}.
func batch6Lane3OptionalMap(input map[string]any, name string) (map[string]any, bool) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return object, true
}

// batch6Lane3Failure reports an API error carried in a result map so list
// actions can return it before reshaping, like upstream's
// `if "error" in result: return result`.
func batch6Lane3Failure(result map[string]any) (map[string]any, bool) {
	if _, failed := result["error"]; failed {
		return result, true
	}
	return nil, false
}

// batch6Lane3DetailText renders a provider error detail field the way the
// upstream f-string did: strings verbatim, structures as compact JSON.
func batch6Lane3DetailText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return jsonString(value)
}

// batch6Lane3ReadAll reads a response body under the shared size cap.
func batch6Lane3ReadAll(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, batch6Lane3MaxBody))
}
