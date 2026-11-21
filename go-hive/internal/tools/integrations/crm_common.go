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
	crmMaxBody  = 8 << 20
	crmMaxError = 500
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
		Modules: crmModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		salesforceTool,
		pipedriveTool,
		zohoCRMTool,
		attioTool,
	} {
		registry.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"salesforce": salesforceImpls(),
		"pipedrive":  pipedriveImpls(),
		"zoho_crm":   zohoCRMImpls(),
		"attio":      attioImpls(),
	} {
		for action, impl := range actions {
			registry.AddImpl(tool, action, impl)
		}
	}
	return registry
}

// crmModules declares credential variables. Base URLs stay empty on
// purpose: they resolve at request time (WithBaseURL, then the provider host
// variable, then the built-in default), so tests can point one module at a
// fake upstream without inventing a default that shadows the environment.
func crmModules() map[string]*ModuleConfig {
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
			OAuth:   oauthZohoOAuth,
		},
		"attio_tool": {
			ID: "attio_tool", BaseURL: "",
			EnvVars: []string{"ATTIO_API_KEY"},
		},
	}
}

// ------------------------------------------------------------- transport ---

// crmRequest is one provider call after auth and module resolution.
type crmRequest struct {
	Method string
	// Path is appended to the module base URL; an absolute http(s) URL (SOQL
	// nextRecordsUrl) is used as-is.
	Path  string
	Query url.Values
	Body  any
}

// crmResponse is a decoded provider response.
type crmResponse struct {
	Status int
	Method string
	Parsed any
	Raw    []byte
}

// crmBase resolves the module base URL: WithBaseURL wins, then the
// provider-specific environment variable, then the built-in default.
func crmBase(rt *Runtime, moduleID string) string {
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
		instance := strings.TrimRight(runtimeEnvTrimmed(rt, "SALESFORCE_INSTANCE_URL"), "/")
		if instance != "" && !strings.HasPrefix(instance, "http") {
			instance = "https://" + instance
		}
		return instance
	case "pipedrive_tool":
		domain := strings.TrimRight(runtimeEnvTrimmed(rt, "PIPEDRIVE_DOMAIN"), "/")
		if domain == "" {
			return "https://api.pipedrive.com/v1"
		}
		if !strings.HasPrefix(domain, "http") {
			domain = "https://" + domain
		}
		return domain + "/api/v1"
	case "zoho_crm_tool":
		domain := runtimeEnvTrimmed(rt, "ZOHO_CRM_DOMAIN")
		if domain == "" {
			domain = "www.zohoapis.com"
		}
		return "https://" + domain + "/crm/v7"
	case "attio_tool":
		return "https://api.attio.com/v2"
	}
	return ""
}

// crmAuthError is the upstream "credentials not configured" value.
func crmAuthError(moduleID string) map[string]any {
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

// crmSend resolves credentials, applies provider auth and executes one
// request. A non-nil value means the call never produced an HTTP response. A
// module with an OAuth2 refresh source mints an access token when its static
// one is expired, and a 401 retries once with a freshly minted token.
func crmSend(ctx context.Context, rt *Runtime, moduleID string, spec crmRequest) (crmResponse, map[string]any) {
	var out crmResponse
	token, refreshable, problem := oauthModuleToken(ctx, rt, moduleID)
	if problem != nil {
		return out, problem
	}
	base := crmBase(rt, moduleID)
	// Salesforce and Pipedrive can be configured without a base URL, so the
	// credential check runs first to keep the upstream error text.
	if token == "" || base == "" {
		return out, crmAuthError(moduleID)
	}

	attempt := func(token string) (crmResponse, map[string]any) {
		var page crmResponse
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
				return page, map[string]any{"error": crmRequestFailedPrefix(moduleID) + err.Error()}
			}
			reader = bytes.NewReader(payload)
		}

		req, err := http.NewRequestWithContext(ctx, spec.Method, target, reader)
		if err != nil {
			return page, map[string]any{"error": crmRequestFailedPrefix(moduleID) + err.Error()}
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
				return page, map[string]any{"error": crmTimeoutMessage(moduleID)}
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return page, map[string]any{"error": crmTimeoutMessage(moduleID)}
			}
			return page, map[string]any{"error": crmNetworkPrefix(moduleID) + err.Error()}
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(io.LimitReader(resp.Body, crmMaxBody))
		if err != nil {
			return page, map[string]any{"error": crmRequestFailedPrefix(moduleID) + err.Error()}
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
	resolution := rt.ResolveUnauthorized(ctx, moduleID, crmDisplayName(moduleID))
	if !resolution.Retry {
		if resolution.Failure != nil {
			return crmResponse{}, resolution.Failure
		}
		return out, nil
	}
	return attempt(resolution.Token)
}

// crmDisplayName names the provider in refresh failure messages.
func crmDisplayName(moduleID string) string {
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

// crmTimeoutMessage mirrors each provider's timeout wording.
func crmTimeoutMessage(moduleID string) string {
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

// crmNetworkPrefix mirrors each provider's transport-error wording.
func crmNetworkPrefix(moduleID string) string {
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

// crmRequestFailedPrefix covers local failures (encode/read).
func crmRequestFailedPrefix(moduleID string) string {
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

// salesforceCheck maps non-2xx responses onto the upstream error dicts.
func salesforceCheck(res crmResponse) map[string]any {
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
		if message := asString(asMap(items[0])["message"]); message != "" {
			detail = message
		}
	}
	return map[string]any{
		"error": fmt.Sprintf("Salesforce API error (HTTP %d): %s", res.Status, detail),
	}
}

// pipedriveCheck maps responses using the upstream per-verb rules:
// reads accept 200, creates 200/201, updates 200 and deletes 200/204. Only
// reads map 404 to "Not found"; writes surface the provider body.
func pipedriveCheck(res crmResponse, method string) map[string]any {
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
		"error": fmt.Sprintf("Pipedrive API error %d: %s", res.Status, crmTruncate(string(res.Raw), crmMaxError)),
	}
}

// zohoCheck maps responses; a 204 on a read means "no records".
func zohoCheck(res crmResponse, read bool) map[string]any {
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
		"error": fmt.Sprintf("Zoho CRM API error %d: %s", res.Status, crmTruncate(string(res.Raw), crmMaxError)),
	}
}

// attioCheck maps non-2xx responses onto the upstream error dicts.
func attioCheck(res crmResponse) map[string]any {
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
	if message := asString(asMap(res.Parsed)["message"]); message != "" {
		detail = message
	}
	return map[string]any{
		"error": fmt.Sprintf("Attio API error (HTTP %d): %s", res.Status, detail),
	}
}

// ---------------------------------------------------------------- helpers ---

// crmAsNumber reads a decoded JSON number (0 when absent or not numeric).
func crmAsNumber(value any) float64 {
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

// crmTruncate caps provider bodies at the upstream slice width.
func crmTruncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// crmOptionalString distinguishes an absent/null argument from "".
func crmOptionalString(input map[string]any, name string) (string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return "", false
	}
	if text, ok := value.(string); ok {
		return text, true
	}
	return fmt.Sprint(value), true
}

// crmOptionalMap returns a map argument, decoding it when the caller sent a
// JSON string (upstream accepts dicts from MCP clients).
func crmOptionalMap(input map[string]any, name string) (map[string]any, bool, error) {
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

// crmOptionalList returns a list argument, decoding JSON strings.
func crmOptionalList(input map[string]any, name string) ([]any, bool, error) {
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

// crmJsonParam decodes an optional JSON-string argument (filter_json,
// sorts_json) into its structured form.
func crmJsonParam(input map[string]any, name string) (any, bool, error) {
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

// crmQueryValues turns a map of optional query parameters into url.Values,
// skipping absent/empty values.
func crmQueryValues(values map[string]any) url.Values {
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
