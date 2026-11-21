// Batch 7 lane 8 of the integration port: scheduling and chat services.
//
// Covers brevo (9 actions), calcom (9), calendly (8), discord (7) and
// mattermost (7) — 40 actions total. Every module is a REST wrapper around the
// same public API the upstream Python module targets; no SDK is used.
//
// Requests run on Runtime.HTTP so each action is exercisable against an
// httptest server. Credentials resolve from WithCredential, the module's
// environment variables, or WithGetenv:
//
//	brevo       BREVO_API_KEY      (header: api-key)
//	calcom      CALCOM_API_KEY     (query: apiKey)
//	calendly    CALENDLY_PAT       (Authorization: Bearer)
//	discord     DISCORD_BOT_TOKEN  (Authorization: Bot)
//	mattermost  MATTERMOST_ACCESS_TOKEN + MATTERMOST_URL
//	                               (Authorization: Bearer, base gets /api/v4)
//
// Response shaping is per module, not generic: brevo and calendly keep the
// upstream field projections, calcom returns the provider JSON unchanged, and
// discord/mattermost wrap the raw resource in their upstream envelope.
//
// Deliberate limits, stated per module:
//   - discord/mattermost: upstream takes an `account` alias for multi-account
//     credential stores. This port is single-credential, so `account` stays in
//     the schema for parity but is ignored at execution time.
//   - discord/mattermost: a 429 is retried twice using the provider's
//     retry_after (capped at 60s). The sleep is a package seam so tests never
//     stall; after the third 429 the classified rate-limit error is returned.
//   - mattermost: the base URL is MATTERMOST_URL with /api/v4 appended when it
//     is not already present, exactly like the upstream client constructor.
//   - calcom: the API key travels as a query parameter because the upstream
//     client does not send an Authorization header.
package integrations

import (
	"bytes"
	"context"
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
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane8MaxBody           = 8 << 20
	batch7Lane8MaxError          = 500
	batch7Lane8MaxRetries        = 2
	batch7Lane8MaxRetryWait      = 60
	batch7Lane8DiscordMaxMessage = 2000
	batch7Lane8MattermostMaxText = 16383
)

// batch7Lane8Sleep is a seam so the 429 retry loop never stalls a test.
var batch7Lane8Sleep = time.Sleep

// InstallBatch7Lane8 publishes the batch-7 lane-8 tools into a shared registry
// and returns the number of tools registered.
func InstallBatch7Lane8(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	return newBatch7Lane8Registry().Install(target)
}

// newBatch7Lane8Registry builds a registry containing only the lane-8 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane8Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane8Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch7Lane8BrevoTool,
		batch7Lane8CalcomTool,
		batch7Lane8CalendlyTool,
		batch7Lane8DiscordTool,
		batch7Lane8MattermostTool,
	} {
		registry.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"brevo":      batch7Lane8BrevoImpls(),
		"calcom":     batch7Lane8CalcomImpls(),
		"calendly":   batch7Lane8CalendlyImpls(),
		"discord":    batch7Lane8DiscordImpls(),
		"mattermost": batch7Lane8MattermostImpls(),
	} {
		for action, impl := range actions {
			registry.AddImpl(tool, action, impl)
		}
	}
	return registry
}

// batch7Lane8Modules declares the public endpoints and credential variables.
// Mattermost has no default base: it always comes from MATTERMOST_URL.
func batch7Lane8Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"brevo_tool": {
			ID:      "brevo_tool",
			BaseURL: "https://api.brevo.com/v3",
			EnvVars: []string{"BREVO_API_KEY"},
		},
		"calcom_tool": {
			ID:      "calcom_tool",
			BaseURL: "https://api.cal.com/v1",
			EnvVars: []string{"CALCOM_API_KEY"},
		},
		"calendly_tool": {
			ID:      "calendly_tool",
			BaseURL: "https://api.calendly.com",
			EnvVars: []string{"CALENDLY_PAT"},
		},
		"discord_tool": {
			ID:      "discord_tool",
			BaseURL: "https://discord.com/api/v10",
			EnvVars: []string{"DISCORD_BOT_TOKEN"},
		},
		"mattermost_tool": {
			ID:      "mattermost_tool",
			EnvVars: []string{"MATTERMOST_ACCESS_TOKEN"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane8Getenv resolves an environment variable through the runtime.
func batch7Lane8Getenv(rt *Runtime, name string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv(name)
	}
	return os.Getenv(name)
}

// batch7Lane8Base resolves the module base URL. Mattermost mirrors the
// upstream client: strip the trailing slash and append /api/v4 when the
// configured server URL does not already include it.
func batch7Lane8Base(rt *Runtime, moduleID string) string {
	if moduleID != "mattermost_tool" {
		return batch4ModuleURL(rt, moduleID)
	}
	base := batch4ModuleURL(rt, moduleID)
	if base == "" {
		base = strings.TrimSpace(batch7Lane8Getenv(rt, "MATTERMOST_URL"))
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	return base
}

// batch7Lane8CredentialError mirrors the upstream "not configured" value.
func batch7Lane8CredentialError(moduleID string) map[string]any {
	switch moduleID {
	case "brevo_tool":
		return map[string]any{
			"error": "Brevo credentials not configured",
			"help": "Set BREVO_API_KEY environment variable or configure via credential store. " +
				"Get your API key at https://app.brevo.com/settings/keys/api",
		}
	case "calcom_tool":
		return map[string]any{
			"error": "Cal.com API key not configured",
			"help":  "Set CALCOM_API_KEY environment variable or configure via credential store",
		}
	case "calendly_tool":
		return map[string]any{
			"error": "CALENDLY_PAT is required",
			"help":  "Set CALENDLY_PAT environment variable",
		}
	case "discord_tool":
		return map[string]any{
			"error": "Discord credentials not configured",
			"help":  "Set DISCORD_BOT_TOKEN environment variable or configure via credential store",
		}
	default:
		return map[string]any{
			"error": "Mattermost credentials not configured",
			"help":  "Set MATTERMOST_ACCESS_TOKEN and MATTERMOST_URL environment variables or configure via credential store",
		}
	}
}

// batch7Lane8MissingURLError is the upstream mattermost URL error.
func batch7Lane8MissingURLError(moduleID string) map[string]any {
	if moduleID == "mattermost_tool" {
		return map[string]any{
			"error": "Mattermost server URL not configured",
			"help":  "Set MATTERMOST_URL environment variable (e.g. https://mattermost.example.com) or configure via credential store",
		}
	}
	return map[string]any{"error": "Module endpoint not configured"}
}

// batch7Lane8RequireModule applies the upstream credential check ordering:
// token first, then the required base URL.
func batch7Lane8RequireModule(rt *Runtime, moduleID string) map[string]any {
	if accountToken(rt, moduleID, batch7Lane8AccountProvider(moduleID)) == "" {
		return batch7Lane8CredentialError(moduleID)
	}
	if batch7Lane8Base(rt, moduleID) == "" {
		return batch7Lane8MissingURLError(moduleID)
	}
	return nil
}

// batch7Lane8Response is one completed HTTP exchange.
type batch7Lane8Response struct {
	Status int
	Header http.Header
	Body   []byte
	Parsed any
}

// batch7Lane8Send executes one request, applying the module's auth scheme.
// Transport failures are returned as error dicts; HTTP status mapping belongs
// to batch7Lane8CheckResponse.
func batch7Lane8Send(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (*batch7Lane8Response, map[string]any) {
	token := accountToken(rt, moduleID, batch7Lane8AccountProvider(moduleID))
	if token == "" {
		return nil, batch7Lane8CredentialError(moduleID)
	}
	base := batch7Lane8Base(rt, moduleID)
	if base == "" {
		return nil, batch7Lane8MissingURLError(moduleID)
	}

	values := url.Values{}
	for name, list := range query {
		for _, value := range list {
			values.Add(name, value)
		}
	}
	if moduleID == "calcom_tool" {
		values.Set("apiKey", token)
	}
	target := base + path
	if encoded := values.Encode(); encoded != "" {
		if strings.Contains(target, "?") {
			target += "&" + encoded
		} else {
			target += "?" + encoded
		}
	}

	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, map[string]any{"error": err.Error()}
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, map[string]any{"error": err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if moduleID == "brevo_tool" || moduleID == "calcom_tool" {
		req.Header.Set("Accept", "application/json")
	}
	switch moduleID {
	case "brevo_tool":
		req.Header.Set("api-key", token)
	case "discord_tool":
		req.Header.Set("Authorization", "Bot "+token)
	default:
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := batch4HTTP(rt).Do(req)
	if err != nil {
		if batch4IsTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane8MaxBody))
	if err != nil {
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	out := &batch7Lane8Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
	if len(raw) > 0 {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			out.Parsed = decoded
		}
	}
	return out, nil
}

// batch7Lane8ProviderMessage reads the provider's {"message": ...} field,
// falling back to the raw body like the upstream handlers do.
func batch7Lane8ProviderMessage(resp *batch7Lane8Response) string {
	if message := batch6AsString(batch4AsMap(resp.Parsed)["message"]); message != "" {
		return message
	}
	return string(resp.Body)
}

// batch7Lane8RateLimitError mirrors the discord/mattermost 429 value.
func batch7Lane8RateLimitError(moduleID string, resp *batch7Lane8Response) map[string]any {
	if moduleID == "mattermost_tool" {
		retry, ok := batch7Lane8FloatValue(batch7Lane8HeaderRetryAfter(resp))
		if !ok {
			retry = 60
		}
		return map[string]any{
			"error":       fmt.Sprintf("Mattermost rate limit exceeded. Retry after %vs", retry),
			"retry_after": retry,
		}
	}
	retry, ok := batch7Lane8FloatValue(batch4AsMap(resp.Parsed)["retry_after"])
	if !ok {
		retry = 60
	}
	message := batch6AsString(batch4AsMap(resp.Parsed)["message"])
	if message == "" {
		message = "Rate limit exceeded"
	}
	return map[string]any{
		"error":       fmt.Sprintf("Discord rate limit exceeded. Retry after %vs", retry),
		"retry_after": retry,
		"message":     message,
	}
}

// batch7Lane8HeaderRetryAfter returns the raw Retry-After header, or nil.
func batch7Lane8HeaderRetryAfter(resp *batch7Lane8Response) any {
	if resp == nil || resp.Header == nil {
		return nil
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	return raw
}

// batch7Lane8CheckResponse maps non-success statuses onto the upstream error
// dicts for each module.
func batch7Lane8CheckResponse(moduleID string, resp *batch7Lane8Response) map[string]any {
	switch moduleID {
	case "brevo_tool":
		switch resp.Status {
		case http.StatusUnauthorized:
			return map[string]any{"error": "Invalid Brevo API key"}
		case http.StatusForbidden:
			return map[string]any{"error": "Access forbidden - check API key permissions"}
		case http.StatusNotFound:
			return map[string]any{"error": "Resource not found"}
		case http.StatusTooManyRequests:
			return map[string]any{"error": "Rate limit exceeded. Try again later."}
		}
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated && resp.Status != http.StatusNoContent {
			return map[string]any{
				"error": fmt.Sprintf("HTTP error %d: %s", resp.Status, string(resp.Body)),
			}
		}
	case "calcom_tool":
		switch resp.Status {
		case http.StatusUnauthorized:
			return map[string]any{"error": "Invalid or expired Cal.com API key"}
		case http.StatusForbidden:
			return map[string]any{"error": "Access forbidden. Check API key permissions."}
		case http.StatusNotFound:
			return map[string]any{"error": "Resource not found"}
		case http.StatusTooManyRequests:
			return map[string]any{"error": "Rate limit exceeded. Try again later."}
		}
		if resp.Status >= 400 {
			return map[string]any{
				"error": fmt.Sprintf("Cal.com API error (HTTP %d): %s", resp.Status, batch7Lane8ProviderMessage(resp)),
			}
		}
	case "calendly_tool":
		if resp.Status >= 400 {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch4TruncateText(string(resp.Body), batch7Lane8MaxError)),
			}
		}
	case "discord_tool":
		if resp.Status == http.StatusNoContent {
			return nil
		}
		if resp.Status == http.StatusTooManyRequests {
			return batch7Lane8RateLimitError(moduleID, resp)
		}
		if resp.Status != http.StatusOK {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch7Lane8ProviderMessage(resp)),
			}
		}
	case "mattermost_tool":
		if resp.Status == http.StatusNoContent {
			return nil
		}
		if resp.Status == http.StatusTooManyRequests {
			return batch7Lane8RateLimitError(moduleID, resp)
		}
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch7Lane8ProviderMessage(resp)),
			}
		}
	}
	return nil
}

// batch7Lane8Decode mirrors the upstream response readers: an empty body is a
// success value, valid JSON is returned as-is, and anything else keeps the raw
// text so the caller still sees what the provider said.
func batch7Lane8Decode(resp *batch7Lane8Response) any {
	if resp.Status == http.StatusNoContent || len(resp.Body) == 0 {
		return map[string]any{"success": true}
	}
	if resp.Parsed != nil {
		return resp.Parsed
	}
	return map[string]any{"raw": batch4TruncateText(string(resp.Body), batch7Lane8MaxError)}
}

// batch7Lane8RetryAfter computes the 429 wait for discord/mattermost.
func batch7Lane8RetryAfter(moduleID string, resp *batch7Lane8Response, attempt int) float64 {
	fallback := math.Pow(2, float64(attempt))
	if moduleID == "mattermost_tool" {
		raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if raw == "" {
			return math.Min(1, batch7Lane8MaxRetryWait)
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return math.Min(fallback, batch7Lane8MaxRetryWait)
		}
		return math.Min(parsed, batch7Lane8MaxRetryWait)
	}
	data := batch4AsMap(resp.Parsed)
	value, present := data["retry_after"]
	if !present {
		return math.Min(1, batch7Lane8MaxRetryWait)
	}
	parsed, ok := batch7Lane8FloatValue(value)
	if !ok {
		return math.Min(fallback, batch7Lane8MaxRetryWait)
	}
	return math.Min(parsed, batch7Lane8MaxRetryWait)
}

// batch7Lane8RetryingCall runs the discord/mattermost retry loop: at most two
// retries on 429, then the classified rate-limit error.
func batch7Lane8RetryingCall(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (any, map[string]any) {
	var resp *batch7Lane8Response
	for attempt := 0; attempt <= batch7Lane8MaxRetries; attempt++ {
		next, fail := batch7Lane8Send(ctx, rt, moduleID, method, path, query, payload)
		if fail != nil {
			return nil, fail
		}
		resp = next
		if resp.Status != http.StatusTooManyRequests || attempt == batch7Lane8MaxRetries {
			break
		}
		wait := batch7Lane8RetryAfter(moduleID, resp, attempt)
		batch7Lane8Sleep(time.Duration(wait * float64(time.Second)))
	}
	if errDict := batch7Lane8CheckResponse(moduleID, resp); errDict != nil {
		return nil, errDict
	}
	return batch7Lane8Decode(resp), nil
}

// batch7Lane8Call executes one action request and returns either the decoded
// payload or the provider's error dict.
func batch7Lane8Call(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (any, map[string]any) {
	if moduleID == "discord_tool" || moduleID == "mattermost_tool" {
		return batch7Lane8RetryingCall(ctx, rt, moduleID, method, path, query, payload)
	}
	resp, fail := batch7Lane8Send(ctx, rt, moduleID, method, path, query, payload)
	if fail != nil {
		return nil, fail
	}
	if errDict := batch7Lane8CheckResponse(moduleID, resp); errDict != nil {
		return nil, errDict
	}
	return batch7Lane8Decode(resp), nil
}

// batch7Lane8BoolArg reads a boolean input with a fallback.
func batch7Lane8BoolArg(input map[string]any, name string, fallback bool) bool {
	value, present := input[name]
	if !present || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// batch7Lane8FloatValue converts common JSON numeric representations.
func batch7Lane8FloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// batch7Lane8IntFrom reads an integer with a fallback (provider totals).
func batch7Lane8IntFrom(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// batch7Lane8URITail extracts the last path segment of a Calendly URI.
func batch7Lane8URITail(uri string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(uri), "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// batch7Lane8QuotePath mirrors urllib.parse.quote(value) with the default
// safe="/" that upstream uses for Discord custom emoji names ("name:id").
func batch7Lane8QuotePath(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		switch {
		case char >= 'A' && char <= 'Z',
			char >= 'a' && char <= 'z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.', char == '~', char == '/':
			out.WriteByte(char)
		default:
			out.WriteString("%")
			out.WriteString(strings.ToUpper(fmt.Sprintf("%02x", char)))
		}
	}
	return out.String()
}

// batch7Lane8StringList normalises an array input to a list of strings,
// preserving upstream's "empty list is omitted" behaviour.
func batch7Lane8StringList(value any) []any {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, scalarString(item))
	}
	return out
}

// ---------------------------------------------------------------- brevo ---

// batch7Lane8BrevoTool mirrors gen_catalog.py output for brevo (9 actions).
var batch7Lane8BrevoTool = &Tool{
	ID:          "brevo_tool",
	Name:        "brevo",
	DisplayName: "Brevo",
	Summary:     "Brevo integration (9 actions).",
	Actions: []Action{
		{
			Name:     "brevo_create_contact",
			Summary:  "Create a new contact in Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email":      param("string"),
				"first_name": paramDefault("string", ""),
				"last_name":  paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"list_ids":   paramDefault("string", ""),
			},
		},
		{
			Name:     "brevo_delete_contact",
			Summary:  "Delete a contact from Brevo by email address.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email": param("string"),
			},
		},
		{
			Name:     "brevo_get_contact",
			Summary:  "Retrieve a contact from Brevo by email address.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email": param("string"),
			},
		},
		{
			Name:     "brevo_get_email_stats",
			Summary:  "Get delivery statistics for a sent transactional email.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
			},
		},
		{
			Name:    "brevo_list_contacts",
			Summary: "List contacts in Brevo with pagination.",
			Source:  "brevo_tool/brevo_tool.py",
			Params: map[string]any{
				"limit":          paramDefault("integer", 50),
				"offset":         paramDefault("integer", 0),
				"modified_since": paramDefault("string", ""),
			},
		},
		{
			Name:    "brevo_list_email_campaigns",
			Summary: "List email campaigns from Brevo.",
			Source:  "brevo_tool/brevo_tool.py",
			Params: map[string]any{
				"status": paramDefault("string", ""),
				"limit":  paramDefault("integer", 50),
				"offset": paramDefault("integer", 0),
			},
		},
		{
			Name:     "brevo_send_email",
			Summary:  "Send a transactional email via Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"to_email", "to_name", "subject", "html_content", "from_email", "from_name"},
			Params: map[string]any{
				"to_email":     param("string"),
				"to_name":      param("string"),
				"subject":      param("string"),
				"html_content": param("string"),
				"from_email":   param("string"),
				"from_name":    param("string"),
				"text_content": paramDefault("string", ""),
			},
		},
		{
			Name:     "brevo_send_sms",
			Summary:  "Send a transactional SMS via Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"to", "content", "sender"},
			Params: map[string]any{
				"to":      param("string"),
				"content": param("string"),
				"sender":  param("string"),
			},
		},
		{
			Name:     "brevo_update_contact",
			Summary:  "Update an existing contact in Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email":      param("string"),
				"first_name": paramDefault("string", ""),
				"last_name":  paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"list_ids":   paramDefault("string", ""),
			},
		},
	},
}

// batch7Lane8BrevoImpls binds every brevo action to its implementation.
func batch7Lane8BrevoImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"brevo_create_contact":       batch7Lane8BrevoCreateContact,
		"brevo_delete_contact":       batch7Lane8BrevoDeleteContact,
		"brevo_get_contact":          batch7Lane8BrevoGetContact,
		"brevo_get_email_stats":      batch7Lane8BrevoGetEmailStats,
		"brevo_list_contacts":        batch7Lane8BrevoListContacts,
		"brevo_list_email_campaigns": batch7Lane8BrevoListEmailCampaigns,
		"brevo_send_email":           batch7Lane8BrevoSendEmail,
		"brevo_send_sms":             batch7Lane8BrevoSendSms,
		"brevo_update_contact":       batch7Lane8BrevoUpdateContact,
	}
}

// batch7Lane8BrevoListIDs parses the upstream comma-separated list_ids string.
func batch7Lane8BrevoListIDs(input map[string]any) ([]any, map[string]any) {
	raw := strings.TrimSpace(stringArg(input, "list_ids"))
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]any, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, map[string]any{
				"error": "list_ids must be comma-separated integers (e.g. '2,5,8')",
			}
		}
		ids = append(ids, parsed)
	}
	return ids, nil
}

// batch7Lane8BrevoAttributes builds the FIRSTNAME/LASTNAME/SMS block.
func batch7Lane8BrevoAttributes(input map[string]any) map[string]any {
	attributes := map[string]any{}
	if value := stringArg(input, "first_name"); value != "" {
		attributes["FIRSTNAME"] = value
	}
	if value := stringArg(input, "last_name"); value != "" {
		attributes["LASTNAME"] = value
	}
	if value := stringArg(input, "phone"); value != "" {
		attributes["SMS"] = value
	}
	return attributes
}

// batch7Lane8BrevoSendEmail sends a transactional email.
func batch7Lane8BrevoSendEmail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	toEmail := stringArg(input, "to_email")
	if toEmail == "" || !strings.Contains(toEmail, "@") {
		return map[string]any{"error": "Invalid recipient email address"}, nil
	}
	subject := stringArg(input, "subject")
	if subject == "" {
		return map[string]any{"error": "Email subject cannot be empty"}, nil
	}
	htmlContent := stringArg(input, "html_content")
	if htmlContent == "" {
		return map[string]any{"error": "Email content cannot be empty"}, nil
	}
	body := map[string]any{
		"sender": map[string]any{
			"email": stringArg(input, "from_email"),
			"name":  stringArg(input, "from_name"),
		},
		"to": []any{map[string]any{
			"email": toEmail,
			"name":  stringArg(input, "to_name"),
		}},
		"subject":     subject,
		"htmlContent": htmlContent,
	}
	if text := stringArg(input, "text_content"); text != "" {
		body["textContent"] = text
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodPost, "/smtp/email", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"success":    true,
		"message_id": batch4AsMap(parsed)["messageId"],
	}, nil
}

// batch7Lane8BrevoSendSms sends a transactional SMS.
func batch7Lane8BrevoSendSms(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	to := stringArg(input, "to")
	if !strings.HasPrefix(to, "+") {
		return map[string]any{"error": "Phone number must be in international format starting with '+'"}, nil
	}
	content := stringArg(input, "content")
	if content == "" {
		return map[string]any{"error": "SMS content cannot be empty"}, nil
	}
	if utf8.RuneCountInString(content) > 640 {
		return map[string]any{"error": "SMS content too long (max 640 characters)"}, nil
	}
	body := map[string]any{
		"sender":    stringArg(input, "sender"),
		"recipient": to,
		"content":   content,
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodPost, "/transactionalSMS/sms", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	result := batch4AsMap(parsed)
	return map[string]any{
		"success":           true,
		"reference":         result["reference"],
		"remaining_credits": result["remainingCredits"],
	}, nil
}

// batch7Lane8BrevoCreateContact creates a contact.
func batch7Lane8BrevoCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	listIDs, errDict := batch7Lane8BrevoListIDs(input)
	if errDict != nil {
		return errDict, nil
	}
	body := map[string]any{
		"email":      email,
		"attributes": batch7Lane8BrevoAttributes(input),
	}
	if listIDs != nil {
		body["listIds"] = listIDs
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodPost, "/contacts", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"success": true,
		"id":      batch4AsMap(parsed)["id"],
		"email":   email,
	}, nil
}

// batch7Lane8BrevoGetContact reads one contact.
func batch7Lane8BrevoGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodGet,
		"/contacts/"+url.PathEscape(email), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	contact := batch4AsMap(parsed)
	attributes := batch4AsMap(contact["attributes"])
	return map[string]any{
		"success":           true,
		"id":                contact["id"],
		"email":             contact["email"],
		"first_name":        attributes["FIRSTNAME"],
		"last_name":         attributes["LASTNAME"],
		"phone":             attributes["SMS"],
		"list_ids":          batch6SliceOrEmpty(contact["listIds"]),
		"email_blacklisted": batch6AsBool(contact["emailBlacklisted"]),
		"sms_blacklisted":   batch6AsBool(contact["smsBlacklisted"]),
		"created_at":        contact["createdAt"],
		"modified_at":       contact["modifiedAt"],
	}, nil
}

// batch7Lane8BrevoUpdateContact updates a contact.
func batch7Lane8BrevoUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	listIDs, errDict := batch7Lane8BrevoListIDs(input)
	if errDict != nil {
		return errDict, nil
	}
	body := map[string]any{"attributes": batch7Lane8BrevoAttributes(input)}
	if listIDs != nil {
		body["listIds"] = listIDs
	}
	if _, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodPut,
		"/contacts/"+url.PathEscape(email), nil, body); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "email": email}, nil
}

// batch7Lane8BrevoListContacts lists contacts with pagination.
func batch7Lane8BrevoListContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	limit := batch4Clamp(intArg(input, "limit", 50), 1, 1000)
	query := batch6QueryValues(map[string]any{
		"limit":         limit,
		"offset":        intArg(input, "offset", 0),
		"modifiedSince": stringArg(input, "modified_since"),
	})
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodGet, "/contacts", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := batch4AsMap(parsed)
	contacts := make([]any, 0)
	for _, raw := range batch6SliceOrEmpty(result["contacts"]) {
		contact := batch4AsMap(raw)
		attributes := batch4AsMap(contact["attributes"])
		contacts = append(contacts, map[string]any{
			"id":                contact["id"],
			"email":             contact["email"],
			"first_name":        attributes["FIRSTNAME"],
			"last_name":         attributes["LASTNAME"],
			"list_ids":          batch6SliceOrEmpty(contact["listIds"]),
			"email_blacklisted": batch6AsBool(contact["emailBlacklisted"]),
			"modified_at":       contact["modifiedAt"],
		})
	}
	return map[string]any{
		"count":    len(contacts),
		"total":    batch7Lane8IntFrom(result["count"], len(contacts)),
		"contacts": contacts,
	}, nil
}

// batch7Lane8BrevoDeleteContact deletes a contact.
func batch7Lane8BrevoDeleteContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	if _, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodDelete,
		"/contacts/"+url.PathEscape(email), nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "email": email, "status": "deleted"}, nil
}

// batch7Lane8BrevoListEmailCampaigns lists email campaigns.
func batch7Lane8BrevoListEmailCampaigns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	limit := batch4Clamp(intArg(input, "limit", 50), 1, 1000)
	query := batch6QueryValues(map[string]any{
		"status": stringArg(input, "status"),
		"limit":  limit,
		"offset": intArg(input, "offset", 0),
	})
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodGet, "/emailCampaigns", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := batch4AsMap(parsed)
	campaigns := make([]any, 0)
	for _, raw := range batch6SliceOrEmpty(result["campaigns"]) {
		campaign := batch4AsMap(raw)
		statistics := batch4AsMap(batch4AsMap(campaign["statistics"])["globalStats"])
		if statistics == nil {
			statistics = map[string]any{}
		}
		campaigns = append(campaigns, map[string]any{
			"id":           campaign["id"],
			"name":         campaign["name"],
			"subject":      campaign["subject"],
			"status":       campaign["status"],
			"type":         campaign["type"],
			"created_at":   campaign["createdAt"],
			"scheduled_at": campaign["scheduledAt"],
			"statistics":   statistics,
		})
	}
	return map[string]any{
		"count":     len(campaigns),
		"total":     batch7Lane8IntFrom(result["count"], len(campaigns)),
		"campaigns": campaigns,
	}, nil
}

// batch7Lane8BrevoGetEmailStats reads delivery stats for one message.
func batch7Lane8BrevoGetEmailStats(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	messageID := stringArg(input, "message_id")
	if messageID == "" {
		return map[string]any{"error": "message_id cannot be empty"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "brevo_tool", http.MethodGet,
		"/smtp/emails/"+url.PathEscape(messageID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := batch4AsMap(parsed)
	return map[string]any{
		"success":    true,
		"message_id": result["messageId"],
		"email":      result["email"],
		"subject":    result["subject"],
		"date":       result["date"],
		"events":     batch6SliceOrEmpty(result["events"]),
	}, nil
}

// --------------------------------------------------------------- calcom ---

// batch7Lane8CalcomTool mirrors gen_catalog.py output for calcom (9 actions).
var batch7Lane8CalcomTool = &Tool{
	ID:          "calcom_tool",
	Name:        "calcom",
	DisplayName: "Cal.com",
	Summary:     "Cal.com integration (9 actions).",
	Actions: []Action{
		{
			Name:     "calcom_cancel_booking",
			Summary:  "Cancel an existing booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"booking_id"},
			Params: map[string]any{
				"booking_id": param("integer"),
				"reason":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calcom_create_booking",
			Summary:  "Create a new booking for an event type.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id", "start", "name", "email"},
			Params: map[string]any{
				"event_type_id": param("integer"),
				"start":         param("string"),
				"name":          param("string"),
				"email":         param("string"),
				"timezone":      paramDefault("string", "UTC"),
				"language":      paramDefault("string", "en"),
				"notes":         paramDefault("string", ""),
				"guests":        param("array"),
			},
		},
		{
			Name:     "calcom_get_availability",
			Summary:  "Get available time slots for booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id", "start_time", "end_time"},
			Params: map[string]any{
				"event_type_id": param("integer"),
				"start_time":    param("string"),
				"end_time":      param("string"),
				"timezone":      paramDefault("string", "UTC"),
			},
		},
		{
			Name:     "calcom_get_booking",
			Summary:  "Get detailed information about a specific booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"booking_id"},
			Params: map[string]any{
				"booking_id": param("integer"),
			},
		},
		{
			Name:     "calcom_get_event_type",
			Summary:  "Get detailed information about an event type.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id"},
			Params: map[string]any{
				"event_type_id": param("integer"),
			},
		},
		{
			Name:    "calcom_list_bookings",
			Summary: "List Cal.com bookings with optional filters.",
			Source:  "calcom_tool/calcom_tool.py",
			Params: map[string]any{
				"status":        paramDefault("string", ""),
				"event_type_id": paramDefault("integer", 0),
				"start_date":    paramDefault("string", ""),
				"end_date":      paramDefault("string", ""),
				"limit":         paramDefault("integer", 50),
			},
		},
		{
			Name:    "calcom_list_event_types",
			Summary: "List all configured event types.",
			Source:  "calcom_tool/calcom_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:    "calcom_list_schedules",
			Summary: "List all availability schedules for the authenticated user.",
			Source:  "calcom_tool/calcom_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "calcom_update_schedule",
			Summary:  "Update a user's availability schedule.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"schedule_id"},
			Params: map[string]any{
				"schedule_id":  param("integer"),
				"name":         paramDefault("string", ""),
				"timezone":     paramDefault("string", ""),
				"availability": param("array"),
			},
		},
	},
}

// batch7Lane8CalcomImpls binds every calcom action to its implementation.
func batch7Lane8CalcomImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calcom_cancel_booking":   batch7Lane8CalcomCancelBooking,
		"calcom_create_booking":   batch7Lane8CalcomCreateBooking,
		"calcom_get_availability": batch7Lane8CalcomGetAvailability,
		"calcom_get_booking":      batch7Lane8CalcomGetBooking,
		"calcom_get_event_type":   batch7Lane8CalcomGetEventType,
		"calcom_list_bookings":    batch7Lane8CalcomListBookings,
		"calcom_list_event_types": batch7Lane8CalcomListEventTypes,
		"calcom_list_schedules":   batch7Lane8CalcomListSchedules,
		"calcom_update_schedule":  batch7Lane8CalcomUpdateSchedule,
	}
}

// batch7Lane8CalcomListBookings lists bookings with filters.
func batch7Lane8CalcomListBookings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(intArg(input, "limit", 50)))
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	if eventTypeID := intArg(input, "event_type_id", 0); eventTypeID != 0 {
		query.Set("eventTypeId", strconv.Itoa(eventTypeID))
	}
	if start := stringArg(input, "start_date"); start != "" {
		query.Set("afterStart", start)
	}
	if end := stringArg(input, "end_date"); end != "" {
		query.Set("beforeEnd", end)
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet, "/bookings", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomGetBooking reads one booking.
func batch7Lane8CalcomGetBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet,
		"/bookings/"+strconv.Itoa(intArg(input, "booking_id", 0)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomCreateBooking creates a booking.
func batch7Lane8CalcomCreateBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	start := stringArg(input, "start")
	if start == "" {
		return map[string]any{"error": "start time is required"}, nil
	}
	name := stringArg(input, "name")
	if name == "" {
		return map[string]any{"error": "name is required"}, nil
	}
	email := stringArg(input, "email")
	if email == "" {
		return map[string]any{"error": "email is required"}, nil
	}
	responses := map[string]any{"name": name, "email": email}
	if notes := stringArg(input, "notes"); notes != "" {
		responses["notes"] = notes
	}
	if guests := batch7Lane8StringList(input["guests"]); guests != nil {
		responses["guests"] = guests
	}
	body := map[string]any{
		"eventTypeId": eventTypeID,
		"start":       start,
		"responses":   responses,
		"timeZone":    stringArg(input, "timezone"),
		"language":    stringArg(input, "language"),
		"metadata":    map[string]any{},
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodPost, "/bookings", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomCancelBooking cancels a booking.
func batch7Lane8CalcomCancelBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	bookingID := intArg(input, "booking_id", 0)
	if bookingID == 0 {
		return map[string]any{"error": "booking_id is required"}, nil
	}
	var body any
	if reason := stringArg(input, "reason"); reason != "" {
		body = map[string]any{"cancellationReason": reason}
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodDelete,
		"/bookings/"+strconv.Itoa(bookingID), nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomGetAvailability reads available slots.
func batch7Lane8CalcomGetAvailability(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	start := stringArg(input, "start_time")
	end := stringArg(input, "end_time")
	if start == "" || end == "" {
		return map[string]any{"error": "start_time and end_time are required"}, nil
	}
	query := url.Values{}
	query.Set("eventTypeId", strconv.Itoa(eventTypeID))
	query.Set("startTime", start)
	query.Set("endTime", end)
	query.Set("timeZone", stringArg(input, "timezone"))
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet, "/slots", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomUpdateSchedule updates a schedule.
func batch7Lane8CalcomUpdateSchedule(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	scheduleID := intArg(input, "schedule_id", 0)
	if scheduleID == 0 {
		return map[string]any{"error": "schedule_id is required"}, nil
	}
	body := map[string]any{}
	if name := stringArg(input, "name"); name != "" {
		body["name"] = name
	}
	if timezone := stringArg(input, "timezone"); timezone != "" {
		body["timeZone"] = timezone
	}
	if availability, ok := input["availability"].([]any); ok && len(availability) > 0 {
		body["availability"] = availability
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodPatch,
		"/schedules/"+strconv.Itoa(scheduleID), nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomListSchedules lists schedules.
func batch7Lane8CalcomListSchedules(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet, "/schedules", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomListEventTypes lists event types.
func batch7Lane8CalcomListEventTypes(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet, "/event-types", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// batch7Lane8CalcomGetEventType reads one event type.
func batch7Lane8CalcomGetEventType(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calcom_tool", http.MethodGet,
		"/event-types/"+strconv.Itoa(eventTypeID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// ------------------------------------------------------------- calendly ---

// batch7Lane8CalendlyTool mirrors gen_catalog.py output for calendly (8).
var batch7Lane8CalendlyTool = &Tool{
	ID:          "calendly_tool",
	Name:        "calendly",
	DisplayName: "Calendly",
	Summary:     "Calendly integration (8 actions).",
	Actions: []Action{
		{
			Name:     "calendly_cancel_event",
			Summary:  "Cancel a scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
				"reason":    paramDefault("string", ""),
			},
		},
		{
			Name:    "calendly_get_current_user",
			Summary: "Get the current authenticated Calendly user.",
			Source:  "calendly_tool/calendly_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "calendly_get_event_type",
			Summary:  "Get details of a specific Calendly event type (meeting template).",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_type_uri"},
			Params: map[string]any{
				"event_type_uri": param("string"),
			},
		},
		{
			Name:     "calendly_get_scheduled_event",
			Summary:  "Get details of a specific scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
			},
		},
		{
			Name:     "calendly_list_event_types",
			Summary:  "List Calendly event types (meeting templates) for a user.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"user_uri"},
			Params: map[string]any{
				"user_uri": param("string"),
				"active":   paramDefault("boolean", true),
				"count":    paramDefault("integer", 20),
			},
		},
		{
			Name:     "calendly_list_invitees",
			Summary:  "List invitees for a scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
				"count":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "calendly_list_scheduled_events",
			Summary:  "List scheduled Calendly events (booked meetings).",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"user_uri"},
			Params: map[string]any{
				"user_uri":       param("string"),
				"status":         paramDefault("string", "active"),
				"min_start_time": paramDefault("string", ""),
				"max_start_time": paramDefault("string", ""),
				"count":          paramDefault("integer", 20),
			},
		},
		{
			Name:     "calendly_list_webhooks",
			Summary:  "List webhook subscriptions for a Calendly organization or user.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"organization_uri"},
			Params: map[string]any{
				"organization_uri": param("string"),
				"scope":            paramDefault("string", "organization"),
				"count":            paramDefault("integer", 20),
			},
		},
	},
}

// batch7Lane8CalendlyImpls binds every calendly action to its implementation.
func batch7Lane8CalendlyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calendly_cancel_event":          batch7Lane8CalendlyCancelEvent,
		"calendly_get_current_user":      batch7Lane8CalendlyGetCurrentUser,
		"calendly_get_event_type":        batch7Lane8CalendlyGetEventType,
		"calendly_get_scheduled_event":   batch7Lane8CalendlyGetScheduledEvent,
		"calendly_list_event_types":      batch7Lane8CalendlyListEventTypes,
		"calendly_list_invitees":         batch7Lane8CalendlyListInvitees,
		"calendly_list_scheduled_events": batch7Lane8CalendlyListScheduledEvents,
		"calendly_list_webhooks":         batch7Lane8CalendlyListWebhooks,
	}
}

// batch7Lane8CalendlyGetCurrentUser reads /users/me.
func batch7Lane8CalendlyGetCurrentUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet, "/users/me", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	user := batch4AsMap(batch4AsMap(parsed)["resource"])
	return map[string]any{
		"uri":            user["uri"],
		"name":           user["name"],
		"email":          user["email"],
		"scheduling_url": user["scheduling_url"],
		"timezone":       user["timezone"],
		"organization":   user["current_organization"],
	}, nil
}

// batch7Lane8CalendlyListEventTypes lists event types for a user.
func batch7Lane8CalendlyListEventTypes(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	userURI := stringArg(input, "user_uri")
	if userURI == "" {
		return map[string]any{"error": "user_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("user", userURI)
	query.Set("count", strconv.Itoa(count))
	if batch7Lane8BoolArg(input, "active", true) {
		query.Set("active", "true")
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet, "/event_types", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := batch6SliceOrEmpty(batch4AsMap(parsed)["collection"])
	eventTypes := make([]any, 0, len(items))
	for _, raw := range items {
		eventType := batch4AsMap(raw)
		eventTypes = append(eventTypes, map[string]any{
			"uri":            eventType["uri"],
			"name":           eventType["name"],
			"slug":           eventType["slug"],
			"active":         eventType["active"],
			"duration":       eventType["duration"],
			"kind":           eventType["kind"],
			"scheduling_url": eventType["scheduling_url"],
			"description":    eventType["description_plain"],
		})
	}
	return map[string]any{"count": len(eventTypes), "event_types": eventTypes}, nil
}

// batch7Lane8CalendlyListScheduledEvents lists booked meetings.
func batch7Lane8CalendlyListScheduledEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	userURI := stringArg(input, "user_uri")
	if userURI == "" {
		return map[string]any{"error": "user_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("user", userURI)
	query.Set("count", strconv.Itoa(count))
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	if minStart := stringArg(input, "min_start_time"); minStart != "" {
		query.Set("min_start_time", minStart)
	}
	if maxStart := stringArg(input, "max_start_time"); maxStart != "" {
		query.Set("max_start_time", maxStart)
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet, "/scheduled_events", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := batch6SliceOrEmpty(batch4AsMap(parsed)["collection"])
	events := make([]any, 0, len(items))
	for _, raw := range items {
		event := batch4AsMap(raw)
		invitees := batch4AsMap(event["invitees_counter"])
		events = append(events, map[string]any{
			"uri":            event["uri"],
			"name":           event["name"],
			"status":         event["status"],
			"start_time":     event["start_time"],
			"end_time":       event["end_time"],
			"event_type":     event["event_type"],
			"location":       batch4AsMap(event["location"])["location"],
			"invitees_count": batch7Lane8IntFrom(invitees["total"], 0),
		})
	}
	return map[string]any{"count": len(events), "events": events}, nil
}

// batch7Lane8CalendlyGetScheduledEvent reads one scheduled event.
func batch7Lane8CalendlyGetScheduledEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet,
		"/scheduled_events/"+url.PathEscape(batch7Lane8URITail(eventURI)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	event := batch4AsMap(batch4AsMap(parsed)["resource"])
	return map[string]any{
		"uri":               event["uri"],
		"name":              event["name"],
		"status":            event["status"],
		"start_time":        event["start_time"],
		"end_time":          event["end_time"],
		"event_type":        event["event_type"],
		"location":          event["location"],
		"invitees_counter":  event["invitees_counter"],
		"event_memberships": event["event_memberships"],
		"created_at":        event["created_at"],
	}, nil
}

// batch7Lane8CalendlyListInvitees lists invitees for an event.
func batch7Lane8CalendlyListInvitees(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	count := intArg(input, "count", 25)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("count", strconv.Itoa(count))
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet,
		"/scheduled_events/"+url.PathEscape(batch7Lane8URITail(eventURI))+"/invitees", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := batch6SliceOrEmpty(batch4AsMap(parsed)["collection"])
	invitees := make([]any, 0, len(items))
	for _, raw := range items {
		invitee := batch4AsMap(raw)
		invitees = append(invitees, map[string]any{
			"uri":                   invitee["uri"],
			"name":                  invitee["name"],
			"email":                 invitee["email"],
			"status":                invitee["status"],
			"timezone":              invitee["timezone"],
			"questions_and_answers": batch6SliceOrEmpty(invitee["questions_and_answers"]),
			"created_at":            invitee["created_at"],
		})
	}
	return map[string]any{"count": len(invitees), "invitees": invitees}, nil
}

// batch7Lane8CalendlyCancelEvent cancels one scheduled event.
func batch7Lane8CalendlyCancelEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	body := map[string]any{}
	if reason := stringArg(input, "reason"); reason != "" {
		body["reason"] = reason
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodPost,
		"/scheduled_events/"+url.PathEscape(batch7Lane8URITail(eventURI))+"/cancellation", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	resource := batch4AsMap(batch4AsMap(parsed)["resource"])
	return map[string]any{
		"canceled_by": batch6AsString(resource["canceled_by"]),
		"reason":      batch6AsString(resource["reason"]),
		"created_at":  batch6AsString(resource["created_at"]),
		"status":      "canceled",
	}, nil
}

// batch7Lane8CalendlyListWebhooks lists webhook subscriptions.
func batch7Lane8CalendlyListWebhooks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	organizationURI := stringArg(input, "organization_uri")
	if organizationURI == "" {
		return map[string]any{"error": "organization_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("organization", organizationURI)
	query.Set("scope", stringArg(input, "scope"))
	query.Set("count", strconv.Itoa(count))
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet, "/webhook_subscriptions", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := batch6SliceOrEmpty(batch4AsMap(parsed)["collection"])
	webhooks := make([]any, 0, len(items))
	for _, raw := range items {
		webhook := batch4AsMap(raw)
		webhooks = append(webhooks, map[string]any{
			"uri":          batch6AsString(webhook["uri"]),
			"callback_url": batch6AsString(webhook["callback_url"]),
			"state":        batch6AsString(webhook["state"]),
			"events":       batch6SliceOrEmpty(webhook["events"]),
			"scope":        batch6AsString(webhook["scope"]),
			"created_at":   batch6AsString(webhook["created_at"]),
		})
	}
	return map[string]any{"count": len(webhooks), "webhooks": webhooks}, nil
}

// batch7Lane8CalendlyGetEventType reads one event type.
func batch7Lane8CalendlyGetEventType(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeURI := stringArg(input, "event_type_uri")
	if eventTypeURI == "" {
		return map[string]any{"error": "event_type_uri is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "calendly_tool", http.MethodGet,
		"/event_types/"+url.PathEscape(batch7Lane8URITail(eventTypeURI)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	eventType := batch4AsMap(batch4AsMap(parsed)["resource"])
	return map[string]any{
		"uri":              batch6AsString(eventType["uri"]),
		"name":             batch6AsString(eventType["name"]),
		"slug":             batch6AsString(eventType["slug"]),
		"active":           batch6AsBool(eventType["active"]),
		"duration":         eventType["duration"],
		"kind":             batch6AsString(eventType["kind"]),
		"type":             batch6AsString(eventType["type"]),
		"color":            batch6AsString(eventType["color"]),
		"scheduling_url":   batch6AsString(eventType["scheduling_url"]),
		"description":      batch6AsString(eventType["description_plain"]),
		"custom_questions": batch6SliceOrEmpty(eventType["custom_questions"]),
	}, nil
}

// --------------------------------------------------------------- discord ---

// batch7Lane8DiscordTool mirrors gen_catalog.py output for discord (7).
var batch7Lane8DiscordTool = &Tool{
	ID:          "discord_tool",
	Name:        "discord",
	DisplayName: "Discord",
	Summary:     "Discord integration (7 actions).",
	Actions: []Action{
		{
			Name:     "discord_create_reaction",
			Summary:  "Add a reaction to a Discord message.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "message_id", "emoji"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message_id": param("string"),
				"emoji":      param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_delete_message",
			Summary:  "Delete a message from a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "message_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_get_channel",
			Summary:  "Get detailed information about a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_get_messages",
			Summary:  "Get recent messages from a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"limit":      paramDefault("integer", 50),
				"before":     paramDefault("string", ""),
				"after":      paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_list_channels",
			Summary:  "List channels for a Discord guild (server).",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"guild_id"},
			Params: map[string]any{
				"guild_id":  param("string"),
				"text_only": paramDefault("boolean", true),
				"account":   paramDefault("string", ""),
			},
		},
		{
			Name:    "discord_list_guilds",
			Summary: "List Discord guilds (servers) the bot is a member of.",
			Source:  "discord_tool/discord_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "discord_send_message",
			Summary:  "Send a message to a Discord channel.",
			Source:   "discord_tool/discord_tool.py",
			Required: []string{"channel_id", "content"},
			Params: map[string]any{
				"channel_id": param("string"),
				"content":    param("string"),
				"tts":        paramDefault("boolean", false),
				"account":    paramDefault("string", ""),
			},
		},
	},
}

// batch7Lane8DiscordImpls binds every discord action to its implementation.
func batch7Lane8DiscordImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"discord_create_reaction": batch7Lane8DiscordCreateReaction,
		"discord_delete_message":  batch7Lane8DiscordDeleteMessage,
		"discord_get_channel":     batch7Lane8DiscordGetChannel,
		"discord_get_messages":    batch7Lane8DiscordGetMessages,
		"discord_list_channels":   batch7Lane8DiscordListChannels,
		"discord_list_guilds":     batch7Lane8DiscordListGuilds,
		"discord_send_message":    batch7Lane8DiscordSendMessage,
	}
}

// batch7Lane8DiscordIsTextChannel reports channel types 0 and 5.
func batch7Lane8DiscordIsTextChannel(value any) bool {
	number, ok := value.(float64)
	if !ok {
		return false
	}
	return number == 0 || number == 5
}

// batch7Lane8DiscordListGuilds lists the bot's guilds.
func batch7Lane8DiscordListGuilds(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodGet, "/users/@me/guilds", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"guilds": parsed, "success": true}, nil
}

// batch7Lane8DiscordListChannels lists guild channels, optionally text-only.
func batch7Lane8DiscordListChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	guildID := stringArg(input, "guild_id")
	if guildID == "" {
		return map[string]any{"error": "guild_id is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodGet,
		"/guilds/"+url.PathEscape(guildID)+"/channels", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	if !batch7Lane8BoolArg(input, "text_only", true) {
		return map[string]any{"channels": parsed, "success": true}, nil
	}
	channels := make([]any, 0)
	for _, raw := range batch4AsSlice(parsed) {
		channel := batch4AsMap(raw)
		if batch7Lane8DiscordIsTextChannel(channel["type"]) {
			channels = append(channels, channel)
		}
	}
	return map[string]any{"channels": channels, "success": true}, nil
}

// batch7Lane8DiscordSendMessage sends a channel message.
func batch7Lane8DiscordSendMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	content := stringArg(input, "content")
	if utf8.RuneCountInString(content) > batch7Lane8DiscordMaxMessage {
		return map[string]any{
			"error":      fmt.Sprintf("Message exceeds %d character limit", batch7Lane8DiscordMaxMessage),
			"max_length": batch7Lane8DiscordMaxMessage,
			"provided":   utf8.RuneCountInString(content),
		}, nil
	}
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	body := map[string]any{"content": content, "tts": batch7Lane8BoolArg(input, "tts", false)}
	parsed, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodPost,
		"/channels/"+url.PathEscape(channelID)+"/messages", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "message": parsed}, nil
}

// batch7Lane8DiscordGetMessages reads recent channel messages.
func batch7Lane8DiscordGetMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	limit := intArg(input, "limit", 50)
	if limit > 100 {
		limit = 100
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	if before := stringArg(input, "before"); before != "" {
		query.Set("before", before)
	}
	if after := stringArg(input, "after"); after != "" {
		query.Set("after", after)
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID)+"/messages", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"messages": parsed, "success": true}, nil
}

// batch7Lane8DiscordGetChannel reads one channel.
func batch7Lane8DiscordGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channel": parsed, "success": true}, nil
}

// batch7Lane8DiscordCreateReaction adds a reaction to a message.
func batch7Lane8DiscordCreateReaction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	messageID := stringArg(input, "message_id")
	emoji := stringArg(input, "emoji")
	if channelID == "" || messageID == "" || emoji == "" {
		return map[string]any{"error": "channel_id, message_id and emoji are required"}, nil
	}
	path := "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID) +
		"/reactions/" + batch7Lane8QuotePath(emoji) + "/@me"
	if _, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodPut, path, nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true}, nil
}

// batch7Lane8DiscordDeleteMessage deletes a message.
func batch7Lane8DiscordDeleteMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "discord_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	messageID := stringArg(input, "message_id")
	if channelID == "" || messageID == "" {
		return map[string]any{"error": "channel_id and message_id are required"}, nil
	}
	path := "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID)
	if _, errDict := batch7Lane8Call(ctx, rt, "discord_tool", http.MethodDelete, path, nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "deleted_message_id": messageID}, nil
}

// ------------------------------------------------------------ mattermost ---

// batch7Lane8MattermostTool mirrors gen_catalog.py output for mattermost (7).
var batch7Lane8MattermostTool = &Tool{
	ID:          "mattermost_tool",
	Name:        "mattermost",
	DisplayName: "Mattermost",
	Summary:     "Mattermost integration (7 actions).",
	Actions: []Action{
		{
			Name:     "mattermost_create_reaction",
			Summary:  "Add a reaction to a Mattermost post.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"post_id", "emoji_name"},
			Params: map[string]any{
				"post_id":    param("string"),
				"emoji_name": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_delete_post",
			Summary:  "Delete a post from Mattermost.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"post_id"},
			Params: map[string]any{
				"post_id": param("string"),
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_get_channel",
			Summary:  "Get detailed information about a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_get_posts",
			Summary:  "Get posts from a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id"},
			Params: map[string]any{
				"channel_id": param("string"),
				"per_page":   paramDefault("integer", 60),
				"page":       paramDefault("integer", 0),
				"before":     paramDefault("string", ""),
				"after":      paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_list_channels",
			Summary:  "List public channels for a Mattermost team.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id":  param("string"),
				"per_page": paramDefault("integer", 100),
				"account":  paramDefault("string", ""),
			},
		},
		{
			Name:    "mattermost_list_teams",
			Summary: "List Mattermost teams the authenticated user belongs to.",
			Source:  "mattermost_tool/mattermost_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "mattermost_send_message",
			Summary:  "Send a message (post) to a Mattermost channel.",
			Source:   "mattermost_tool/mattermost_tool.py",
			Required: []string{"channel_id", "message"},
			Params: map[string]any{
				"channel_id": param("string"),
				"message":    param("string"),
				"root_id":    paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
	},
}

// batch7Lane8MattermostImpls binds every mattermost action to its implementation.
func batch7Lane8MattermostImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"mattermost_create_reaction": batch7Lane8MattermostCreateReaction,
		"mattermost_delete_post":     batch7Lane8MattermostDeletePost,
		"mattermost_get_channel":     batch7Lane8MattermostGetChannel,
		"mattermost_get_posts":       batch7Lane8MattermostGetPosts,
		"mattermost_list_channels":   batch7Lane8MattermostListChannels,
		"mattermost_list_teams":      batch7Lane8MattermostListTeams,
		"mattermost_send_message":    batch7Lane8MattermostSendMessage,
	}
}

// batch7Lane8MattermostListTeams lists the user's teams.
func batch7Lane8MattermostListTeams(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodGet, "/users/me/teams", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"teams": parsed, "success": true}, nil
}

// batch7Lane8MattermostListChannels lists public team channels.
func batch7Lane8MattermostListChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	teamID := stringArg(input, "team_id")
	if teamID == "" {
		return map[string]any{"error": "team_id is required"}, nil
	}
	perPage := intArg(input, "per_page", 100)
	if perPage > 200 {
		perPage = 200
	}
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(perPage))
	parsed, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/teams/"+url.PathEscape(teamID)+"/channels", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channels": parsed, "success": true}, nil
}

// batch7Lane8MattermostGetChannel reads one channel.
func batch7Lane8MattermostGetChannel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"channel": parsed, "success": true}, nil
}

// batch7Lane8MattermostSendMessage creates a post.
func batch7Lane8MattermostSendMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	message := stringArg(input, "message")
	if utf8.RuneCountInString(message) > batch7Lane8MattermostMaxText {
		return map[string]any{
			"error":      fmt.Sprintf("Message exceeds %d character limit", batch7Lane8MattermostMaxText),
			"max_length": batch7Lane8MattermostMaxText,
			"provided":   utf8.RuneCountInString(message),
		}, nil
	}
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	body := map[string]any{"channel_id": channelID, "message": message}
	if rootID := stringArg(input, "root_id"); rootID != "" {
		body["root_id"] = rootID
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodPost, "/posts", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "post": parsed}, nil
}

// batch7Lane8MattermostGetPosts reads channel posts.
func batch7Lane8MattermostGetPosts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	channelID := stringArg(input, "channel_id")
	if channelID == "" {
		return map[string]any{"error": "channel_id is required"}, nil
	}
	perPage := intArg(input, "per_page", 60)
	if perPage > 200 {
		perPage = 200
	}
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(perPage))
	query.Set("page", strconv.Itoa(intArg(input, "page", 0)))
	if before := stringArg(input, "before"); before != "" {
		query.Set("before", before)
	}
	if after := stringArg(input, "after"); after != "" {
		query.Set("after", after)
	}
	parsed, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodGet,
		"/channels/"+url.PathEscape(channelID)+"/posts", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{"posts": parsed, "success": true}, nil
}

// batch7Lane8MattermostCreateReaction adds a reaction, resolving the user id
// from /users/me exactly like the upstream client.
func batch7Lane8MattermostCreateReaction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	postID := stringArg(input, "post_id")
	emojiName := stringArg(input, "emoji_name")
	if postID == "" || emojiName == "" {
		return map[string]any{"error": "post_id and emoji_name are required"}, nil
	}
	me, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodGet, "/users/me", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	userID := batch6AsString(batch4AsMap(me)["id"])
	body := map[string]any{"user_id": userID, "post_id": postID, "emoji_name": emojiName}
	if _, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodPost, "/reactions", nil, body); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true}, nil
}

// batch7Lane8MattermostDeletePost deletes a post.
func batch7Lane8MattermostDeletePost(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := batch7Lane8RequireModule(rt, "mattermost_tool"); errDict != nil {
		return errDict, nil
	}
	postID := stringArg(input, "post_id")
	if postID == "" {
		return map[string]any{"error": "post_id is required"}, nil
	}
	if _, errDict := batch7Lane8Call(ctx, rt, "mattermost_tool", http.MethodDelete,
		"/posts/"+url.PathEscape(postID), nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "deleted_post_id": postID}, nil
}
