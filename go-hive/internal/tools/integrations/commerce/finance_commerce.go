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
package commerce

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	maxBody  = 8 << 20
	maxError = 500
)

// newBatch6Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
	for _, tool := range []*Tool{
		shopifyTool,
		quickBooksTool,
		plaidTool,
		razorpayTool,
	} {
		registry.AddTool(tool)
	}
	for action, impl := range shopifyImpls() {
		registry.AddImpl("shopify", action, impl)
	}
	for action, impl := range quickBooksImpls() {
		registry.AddImpl("quickbooks", action, impl)
	}
	for action, impl := range plaidImpls() {
		registry.AddImpl("plaid", action, impl)
	}
	for action, impl := range razorpayImpls() {
		registry.AddImpl("razorpay", action, impl)
	}
	return registry
}

// modules declares the credential variables for the four modules.
// Base URLs start empty where the provider needs per-account segments
// (Shopify store, QuickBooks realm); the action helpers derive them.
func modules() map[string]*ModuleConfig {
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
			ResolveToken: pairResolver(
				"PLAID_CLIENT_ID", "PLAID_SECRET",
			),
		},
		"razorpay_tool": {
			ID:      "razorpay_tool",
			EnvVars: []string{"RAZORPAY_API_KEY"},
			ResolveToken: pairResolver(
				"RAZORPAY_API_KEY", "RAZORPAY_API_SECRET",
			),
			Headers: func(token string) map[string]string {
				return map[string]string{"Authorization": "Basic " + token}
			},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// pairResolver joins two required environment variables into one
// "first|second" credential, or reports which half is missing.
func pairResolver(first, second string) func(func(string) string) (string, []string) {
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

// splitCredential splits "primary|secondary" credentials supplied
// through WithCredential. The boolean reports whether a separator was present.
func splitCredential(value string) (string, string, bool) {
	head, tail, found := strings.Cut(value, "|")
	if !found {
		return strings.TrimSpace(value), "", false
	}
	return strings.TrimSpace(head), strings.TrimSpace(tail), true
}

// optionalMap distinguishes an absent/null object from {}.
func optionalMap(input map[string]any, name string) (map[string]any, bool) {
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

// detailText renders a provider error detail field the way the
// upstream f-string did: strings verbatim, structures as compact JSON.
func detailText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return jsonString(value)
}

// readAll reads a response body under the shared size cap.
func readAll(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
