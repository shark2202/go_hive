// Batch 6, lane 1 of the integration port: Salesforce (9 actions), Pipedrive
// (13), Zoho CRM (6) and Attio (15) — 43 actions over four REST CRM modules.
//
// Every request goes through Runtime.HTTP, so each action is exercisable
// against an httptest server, and the base URL is injectable per module with
// WithBaseURL. Credentials resolve from WithCredential first, then from the
// upstream environment variables:
//
//	Salesforce  SALESFORCE_ACCESS_TOKEN + SALESFORCE_INSTANCE_URL (base URL)
//	Pipedrive   PIPEDRIVE_API_TOKEN     + PIPEDRIVE_DOMAIN (optional host)
//	Zoho CRM    ZOHO_CRM_ACCESS_TOKEN   + ZOHO_CRM_DOMAIN (optional host)
//	Attio       ATTIO_API_KEY
//
// Response shaping is per module, not generic: each provider keeps its own
// error strings, envelope unwrapping and field projections from upstream.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch6MaxBody  = 8 << 20
	batch6MaxError = 500
)

// InstallBatch6Lane1 publishes the batch-6 lane-1 CRM tools into a shared
// registry and returns the number of tools registered.
func InstallBatch6Lane1(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	return newBatch6Lane1Registry().Install(target)
}

// newBatch6Lane1Registry builds a registry containing only the batch-6 lane-1
// tools, with the same Runtime surface as New() so base URLs, transports and
// credentials stay injectable.
func newBatch6Lane1Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch6Modules(),
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
		batch6SalesforceTool,
		batch6PipedriveTool,
		batch6ZohoCRMTool,
		batch6AttioTool,
	} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"salesforce": batch6SalesforceImpls(),
		"pipedrive":  batch6PipedriveImpls(),
		"zoho_crm":   batch6ZohoCRMImpls(),
		"attio":      batch6AttioImpls(),
	} {
		for action, impl := range actions {
			registry.addImpl(tool, action, impl)
		}
	}
	return registry
}

// batch6Modules declares credential variables. Base URLs stay empty on
// purpose: they resolve at request time (WithBaseURL, then the provider host
// variable, then the built-in default), so tests can point one module at a
// fake upstream without inventing a default that shadows the environment.
func batch6Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"salesforce_tool": {
			ID: "salesforce_tool", BaseURL: "",
			EnvVars: []string{"SALESFORCE_ACCESS_TOKEN"},
		},
		"pipedrive_tool": {
			ID: "pipedrive_tool", BaseURL: "",
			EnvVars: []string{"PIPEDRIVE_API_TOKEN"},
		},
		"zoho_crm_tool": {
			ID: "zoho_crm_tool", BaseURL: "",
			EnvVars: []string{"ZOHO_CRM_ACCESS_TOKEN"},
			OAuth:   batch6ZohoOAuth,
		},
		"attio_tool": {
			ID: "attio_tool", BaseURL: "",
			EnvVars: []string{"ATTIO_API_KEY"},
		},
	}
}

// ------------------------------------------------------------- transport ---

// batch6Request is one provider call after auth and module resolution.
type batch6Request struct {
	Method string
	// Path is appended to the module base URL; an absolute http(s) URL (SOQL
	// nextRecordsUrl) is used as-is.
	Path  string
	Query url.Values
	Body  any
}

// batch6Response is a decoded provider response.
type batch6Response struct {
	Status int
	Method string
	Parsed any
	Raw    []byte
}

// batch6Env reads an environment value through the runtime lookup.
func batch6Env(rt *Runtime, name string) string {
	getenv := os.Getenv
	if rt != nil && rt.Getenv != nil {
		getenv = rt.Getenv
	}
	return strings.TrimSpace(getenv(name))
}

// batch6Base resolves the module base URL: WithBaseURL wins, then the
// provider-specific environment variable, then the built-in default.
func batch6Base(rt *Runtime, moduleID string) string {
	base := ""
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			base = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
		}
	}
	if base != "" {
		return base
	}
	switch moduleID {
	case "salesforce_tool":
		instance := strings.TrimRight(batch6Env(rt, "SALESFORCE_INSTANCE_URL"), "/")
		if instance != "" && !strings.HasPrefix(instance, "http") {
			instance = "https://" + instance
		}
		return instance
	case "pipedrive_tool":
		domain := strings.TrimRight(batch6Env(rt, "PIPEDRIVE_DOMAIN"), "/")
		if domain == "" {
			return "https://api.pipedrive.com/v1"
		}
		if !strings.HasPrefix(domain, "http") {
			domain = "https://" + domain
		}
		return domain + "/api/v1"
	case "zoho_crm_tool":
		domain := batch6Env(rt, "ZOHO_CRM_DOMAIN")
		if domain == "" {
			domain = "www.zohoapis.com"
		}
		return "https://" + domain + "/crm/v7"
	case "attio_tool":
		return "https://api.attio.com/v2"
	}
	return ""
}

// batch6AuthError is the upstream "credentials not configured" value.
func batch6AuthError(moduleID string) map[string]any {
	switch moduleID {
	case "salesforce_tool":
		return map[string]any{
			"error": "Salesforce credentials not configured",
			"help":  "Set SALESFORCE_ACCESS_TOKEN and SALESFORCE_INSTANCE_URL environment variables or configure via credential store",
		}
	case "pipedrive_tool":
		return map[string]any{
			"error": "PIPEDRIVE_API_TOKEN not set",
			"help":  "Get your API token from Pipedrive Settings > Personal preferences > API",
		}
	case "zoho_crm_tool":
		return map[string]any{
			"error": "ZOHO_CRM_ACCESS_TOKEN not set",
			"help":  "Generate an OAuth token via https://api-console.zoho.com/",
		}
	default:
		return map[string]any{
			"error": "Attio credentials not configured",
			"help":  "Set ATTIO_API_KEY environment variable or configure via credential store. Get an API key at https://attio.com/help/apps/other-apps/generating-an-api-key",
		}
	}
}

// batch6Send resolves credentials, applies provider auth and executes one
// request. A non-nil value means the call never produced an HTTP response. A
// module with an OAuth2 refresh source mints an access token when its static
// one is expired, and a 401 retries once with a freshly minted token.
func batch6Send(ctx context.Context, rt *Runtime, moduleID string, spec batch6Request) (batch6Response, map[string]any) {
	var out batch6Response
	token, refreshable, problem := oauthModuleToken(ctx, rt, moduleID)
	if problem != nil {
		return out, problem
	}
	base := batch6Base(rt, moduleID)
	// Salesforce and Pipedrive can be configured without a base URL, so the
	// credential check runs first to keep the upstream error text.
	if token == "" || base == "" {
		return out, batch6AuthError(moduleID)
	}

	attempt := func(token string) (batch6Response, map[string]any) {
		var page batch6Response
		query := url.Values{}
		for name, values := range spec.Query {
			for _, value := range values {
				query.Add(name, value)
			}
		}
		if moduleID == "pipedrive_tool" {
			query.Set("api_token", token)
		}

		target := spec.Path
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			target = base + target
		}
		if encoded := query.Encode(); encoded != "" {
			if strings.Contains(target, "?") {
				target += "&" + encoded
			} else {
				target += "?" + encoded
			}
		}

		var reader io.Reader
		if spec.Body != nil {
			payload, err := json.Marshal(spec.Body)
			if err != nil {
				return page, map[string]any{"error": batch6RequestFailedPrefix(moduleID) + err.Error()}
			}
			reader = bytes.NewReader(payload)
		}

		req, err := http.NewRequestWithContext(ctx, spec.Method, target, reader)
		if err != nil {
			return page, map[string]any{"error": batch6RequestFailedPrefix(moduleID) + err.Error()}
		}
		if spec.Body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		switch moduleID {
		case "zoho_crm_tool":
			req.Header.Set("Authorization", "Zoho-oauthtoken "+token)
		case "pipedrive_tool":
			// Pipedrive authenticates with the api_token query parameter only.
		default:
			req.Header.Set("Authorization", "Bearer "+token)
		}

		client := newHTTPClient(nil)
		if rt != nil && rt.HTTP != nil {
			client = rt.HTTP
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return page, map[string]any{"error": batch6TimeoutMessage(moduleID)}
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return page, map[string]any{"error": batch6TimeoutMessage(moduleID)}
			}
			return page, map[string]any{"error": batch6NetworkPrefix(moduleID) + err.Error()}
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(io.LimitReader(resp.Body, batch6MaxBody))
		if err != nil {
			return page, map[string]any{"error": batch6RequestFailedPrefix(moduleID) + err.Error()}
		}
		page.Status = resp.StatusCode
		page.Method = spec.Method
		page.Raw = raw
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &page.Parsed)
		}
		return page, nil
	}

	out, problem = attempt(token)
	if problem != nil || out.Status != http.StatusUnauthorized || !refreshable {
		return out, problem
	}
	resolution := rt.ResolveUnauthorized(ctx, moduleID, batch6DisplayName(moduleID))
	if !resolution.Retry {
		if resolution.Failure != nil {
			return batch6Response{}, resolution.Failure
		}
		return out, nil
	}
	return attempt(resolution.Token)
}

// batch6DisplayName names the provider in refresh failure messages.
func batch6DisplayName(moduleID string) string {
	switch moduleID {
	case "zoho_crm_tool":
		return "Zoho CRM"
	case "salesforce_tool":
		return "Salesforce"
	case "pipedrive_tool":
		return "Pipedrive"
	case "attio_tool":
		return "Attio"
	default:
		return moduleID
	}
}

// batch6TimeoutMessage mirrors each provider's timeout wording.
func batch6TimeoutMessage(moduleID string) string {
	switch moduleID {
	case "salesforce_tool":
		return "Request timed out"
	case "pipedrive_tool":
		return "Request to Pipedrive timed out"
	case "zoho_crm_tool":
		return "Request to Zoho CRM timed out"
	default:
		return "Request timed out"
	}
}

// batch6NetworkPrefix mirrors each provider's transport-error wording.
func batch6NetworkPrefix(moduleID string) string {
	switch moduleID {
	case "salesforce_tool":
		return "Network error: "
	case "pipedrive_tool":
		return "Pipedrive request failed: "
	case "zoho_crm_tool":
		return "Zoho CRM request failed: "
	default:
		return "Network error: "
	}
}

// batch6RequestFailedPrefix covers local failures (encode/read).
func batch6RequestFailedPrefix(moduleID string) string {
	switch moduleID {
	case "pipedrive_tool":
		return "Pipedrive request failed: "
	case "zoho_crm_tool":
		return "Zoho CRM request failed: "
	case "attio_tool":
		return "Network error: "
	default:
		return "Salesforce request failed: "
	}
}

// ---------------------------------------------------------- error mapping ---

// batch6SalesforceCheck maps non-2xx responses onto the upstream error dicts.
func batch6SalesforceCheck(res batch6Response) map[string]any {
	if res.Status >= 200 && res.Status < 300 {
		return nil
	}
	switch res.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Salesforce access token"}
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions for this Salesforce resource"}
	case http.StatusNotFound:
		return map[string]any{"error": "Salesforce resource not found"}
	}
	detail := string(res.Raw)
	if items, ok := res.Parsed.([]any); ok && len(items) > 0 {
		if message := batch6AsString(batch6AsMap(items[0])["message"]); message != "" {
			detail = message
		}
	}
	return map[string]any{
		"error": fmt.Sprintf("Salesforce API error (HTTP %d): %s", res.Status, detail),
	}
}

// batch6PipedriveCheck maps responses using the upstream per-verb rules:
// reads accept 200, creates 200/201, updates 200 and deletes 200/204. Only
// reads map 404 to "Not found"; writes surface the provider body.
func batch6PipedriveCheck(res batch6Response, method string) map[string]any {
	if res.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your PIPEDRIVE_API_TOKEN."}
	}
	if method == http.MethodGet && res.Status == http.StatusNotFound {
		return map[string]any{"error": "Not found"}
	}
	if res.Status >= 200 && res.Status < 300 {
		return nil
	}
	return map[string]any{
		"error": fmt.Sprintf("Pipedrive API error %d: %s", res.Status, batch6Truncate(string(res.Raw), batch6MaxError)),
	}
}

// batch6ZohoCheck maps responses; a 204 on a read means "no records".
func batch6ZohoCheck(res batch6Response, read bool) map[string]any {
	if res.Status == http.StatusUnauthorized {
		if read {
			return map[string]any{"error": "Unauthorized. Check your ZOHO_CRM_ACCESS_TOKEN (may need refresh)."}
		}
		return map[string]any{"error": "Unauthorized. Check your ZOHO_CRM_ACCESS_TOKEN."}
	}
	if read && res.Status == http.StatusNoContent {
		return nil
	}
	if res.Status >= 200 && res.Status < 300 {
		return nil
	}
	return map[string]any{
		"error": fmt.Sprintf("Zoho CRM API error %d: %s", res.Status, batch6Truncate(string(res.Raw), batch6MaxError)),
	}
}

// batch6AttioCheck maps non-2xx responses onto the upstream error dicts.
func batch6AttioCheck(res batch6Response) map[string]any {
	if res.Status >= 200 && res.Status < 300 {
		return nil
	}
	switch res.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Attio API key"}
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Attio API key scopes."}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Attio rate limit exceeded. Try again later."}
	}
	detail := string(res.Raw)
	if message := batch6AsString(batch6AsMap(res.Parsed)["message"]); message != "" {
		detail = message
	}
	return map[string]any{
		"error": fmt.Sprintf("Attio API error (HTTP %d): %s", res.Status, detail),
	}
}

// ---------------------------------------------------------------- helpers ---

// batch6AsMap views a decoded JSON object.
func batch6AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch6AsSlice views a decoded JSON array.
func batch6AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch6SliceOrEmpty mirrors upstream's `data.get(key) or []`: an absent list
// is an empty array, never JSON null.
func batch6SliceOrEmpty(value any) []any {
	if items, ok := value.([]any); ok && items != nil {
		return items
	}
	return []any{}
}

// batch6AsString reads a decoded JSON string.
func batch6AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch6AsBool reads a decoded JSON boolean.
func batch6AsBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch6AsNumber reads a decoded JSON number (0 when absent or not numeric).
func batch6AsNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// batch6Clamp bounds an integer to [low, high].
func batch6Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch6Truncate caps provider bodies at the upstream slice width.
func batch6Truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// batch6OptionalString distinguishes an absent/null argument from "".
func batch6OptionalString(input map[string]any, name string) (string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return "", false
	}
	if text, ok := value.(string); ok {
		return text, true
	}
	return fmt.Sprint(value), true
}

// batch6OptionalMap returns a map argument, decoding it when the caller sent a
// JSON string (upstream accepts dicts from MCP clients).
func batch6OptionalMap(input map[string]any, name string) (map[string]any, bool, error) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, true, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false, nil
		}
		var decoded any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil, false, err
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("%s must be a JSON object", name)
		}
		return object, true, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil, false, fmt.Errorf("%s must be an object", name)
		}
		return decoded, true, nil
	}
}

// batch6OptionalList returns a list argument, decoding JSON strings.
func batch6OptionalList(input map[string]any, name string) ([]any, bool, error) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false, nil
	}
	switch typed := value.(type) {
	case []any:
		return typed, true, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false, nil
		}
		var decoded any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil, false, err
		}
		items, ok := decoded.([]any)
		if !ok {
			return nil, false, fmt.Errorf("%s must be a JSON array", name)
		}
		return items, true, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		var decoded []any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil, false, fmt.Errorf("%s must be an array", name)
		}
		return decoded, true, nil
	}
}

// batch6JSONParam decodes an optional JSON-string argument (filter_json,
// sorts_json) into its structured form.
func batch6JSONParam(input map[string]any, name string) (any, bool, error) {
	raw, present := input[name]
	if !present || raw == nil {
		return nil, false, nil
	}
	if text, ok := raw.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil, false, nil
		}
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, false, fmt.Errorf("Invalid %s: %v", name, err)
		}
		return decoded, true, nil
	}
	return raw, true, nil
}

// batch6QueryValues turns a map of optional query parameters into url.Values,
// skipping absent/empty values.
func batch6QueryValues(values map[string]any) url.Values {
	out := url.Values{}
	for name, value := range values {
		if value == nil {
			continue
		}
		text := scalarString(value)
		if text == "" {
			continue
		}
		out.Set(name, text)
	}
	return out
}
