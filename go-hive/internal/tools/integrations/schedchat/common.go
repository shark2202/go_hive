package schedchat

// Shared request/decoding helpers: one bounded HTTP exchange (sendRequest),
// the provider error mapping and the JSON accessors every action uses.

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

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// ------------------------------------------------------------ shared bits ---

// baseURL resolves the module base URL. Mattermost mirrors the
// upstream client: strip the trailing slash and append /api/v4 when the
// configured server URL does not already include it.
func baseURL(rt *Runtime, moduleID string) string {
	if moduleID != "mattermost_tool" {
		return moduleBaseURL(rt, moduleID)
	}
	base := moduleBaseURL(rt, moduleID)
	if base == "" {
		base = strings.TrimSpace(runtimeEnv(rt, "MATTERMOST_URL"))
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

// credentialError mirrors the upstream "not configured" value.
func credentialError(moduleID string) map[string]any {
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

// missingURLError is the upstream mattermost URL error.
func missingURLError(moduleID string) map[string]any {
	if moduleID == "mattermost_tool" {
		return map[string]any{
			"error": "Mattermost server URL not configured",
			"help":  "Set MATTERMOST_URL environment variable (e.g. https://mattermost.example.com) or configure via credential store",
		}
	}
	return map[string]any{"error": "Module endpoint not configured"}
}

// requireModule applies the upstream credential check ordering:
// token first, then the required base URL.
func requireModule(rt *Runtime, moduleID string) map[string]any {
	if accountToken(rt, moduleID, accountProvider(moduleID)) == "" {
		return credentialError(moduleID)
	}
	if baseURL(rt, moduleID) == "" {
		return missingURLError(moduleID)
	}
	return nil
}

// httpReply is one completed HTTP exchange.
type httpReply struct {
	Status int
	Header http.Header
	Body   []byte
	Parsed any
}

// sendRequest executes one request, applying the module's auth scheme.
// Transport failures are returned as error dicts; HTTP status mapping belongs
// to checkResponse.
func sendRequest(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (*httpReply, map[string]any) {
	token := accountToken(rt, moduleID, accountProvider(moduleID))
	if token == "" {
		return nil, credentialError(moduleID)
	}
	base := baseURL(rt, moduleID)
	if base == "" {
		return nil, missingURLError(moduleID)
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

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	out := &httpReply{Status: resp.StatusCode, Header: resp.Header, Body: raw}
	if len(raw) > 0 {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			out.Parsed = decoded
		}
	}
	return out, nil
}

// providerMessage reads the provider's {"message": ...} field,
// falling back to the raw body like the upstream handlers do.
func providerMessage(resp *httpReply) string {
	if message := asString(asMap(resp.Parsed)["message"]); message != "" {
		return message
	}
	return string(resp.Body)
}

// rateLimitError mirrors the discord/mattermost 429 value.
func rateLimitError(moduleID string, resp *httpReply) map[string]any {
	if moduleID == "mattermost_tool" {
		retry, ok := numberValue(headerRetryAfter(resp))
		if !ok {
			retry = 60
		}
		return map[string]any{
			"error":       fmt.Sprintf("Mattermost rate limit exceeded. Retry after %vs", retry),
			"retry_after": retry,
		}
	}
	retry, ok := numberValue(asMap(resp.Parsed)["retry_after"])
	if !ok {
		retry = 60
	}
	message := asString(asMap(resp.Parsed)["message"])
	if message == "" {
		message = "Rate limit exceeded"
	}
	return map[string]any{
		"error":       fmt.Sprintf("Discord rate limit exceeded. Retry after %vs", retry),
		"retry_after": retry,
		"message":     message,
	}
}

// headerRetryAfter returns the raw Retry-After header, or nil.
func headerRetryAfter(resp *httpReply) any {
	if resp == nil || resp.Header == nil {
		return nil
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	return raw
}

// checkResponse maps non-success statuses onto the upstream error
// dicts for each module.
func checkResponse(moduleID string, resp *httpReply) map[string]any {
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
				"error": fmt.Sprintf("Cal.com API error (HTTP %d): %s", resp.Status, providerMessage(resp)),
			}
		}
	case "calendly_tool":
		if resp.Status >= 400 {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, truncateText(string(resp.Body), maxErrorBytes)),
			}
		}
	case "discord_tool":
		if resp.Status == http.StatusNoContent {
			return nil
		}
		if resp.Status == http.StatusTooManyRequests {
			return rateLimitError(moduleID, resp)
		}
		if resp.Status != http.StatusOK {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, providerMessage(resp)),
			}
		}
	case "mattermost_tool":
		if resp.Status == http.StatusNoContent {
			return nil
		}
		if resp.Status == http.StatusTooManyRequests {
			return rateLimitError(moduleID, resp)
		}
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
			return map[string]any{
				"error": fmt.Sprintf("HTTP %d: %s", resp.Status, providerMessage(resp)),
			}
		}
	}
	return nil
}

// decodeReply mirrors the upstream response readers: an empty body is a
// success value, valid JSON is returned as-is, and anything else keeps the raw
// text so the caller still sees what the provider said.
func decodeReply(resp *httpReply) any {
	if resp.Status == http.StatusNoContent || len(resp.Body) == 0 {
		return map[string]any{"success": true}
	}
	if resp.Parsed != nil {
		return resp.Parsed
	}
	return map[string]any{"raw": truncateText(string(resp.Body), maxErrorBytes)}
}

// retryAfterDelay computes the 429 wait for discord/mattermost.
func retryAfterDelay(moduleID string, resp *httpReply, attempt int) float64 {
	fallback := math.Pow(2, float64(attempt))
	if moduleID == "mattermost_tool" {
		raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if raw == "" {
			return math.Min(1, maxRetryWaitSeconds)
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return math.Min(fallback, maxRetryWaitSeconds)
		}
		return math.Min(parsed, maxRetryWaitSeconds)
	}
	data := asMap(resp.Parsed)
	value, present := data["retry_after"]
	if !present {
		return math.Min(1, maxRetryWaitSeconds)
	}
	parsed, ok := numberValue(value)
	if !ok {
		return math.Min(fallback, maxRetryWaitSeconds)
	}
	return math.Min(parsed, maxRetryWaitSeconds)
}

// retryingCall runs the discord/mattermost retry loop: at most two
// retries on 429, then the classified rate-limit error.
func retryingCall(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (any, map[string]any) {
	var resp *httpReply
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		next, fail := sendRequest(ctx, rt, moduleID, method, path, query, payload)
		if fail != nil {
			return nil, fail
		}
		resp = next
		if resp.Status != http.StatusTooManyRequests || attempt == MaxRetries {
			break
		}
		wait := retryAfterDelay(moduleID, resp, attempt)
		RetrySleep(time.Duration(wait * float64(time.Second)))
	}
	if errDict := checkResponse(moduleID, resp); errDict != nil {
		return nil, errDict
	}
	return decodeReply(resp), nil
}

// call executes one action request and returns either the decoded
// payload or the provider's error dict.
func call(ctx context.Context, rt *Runtime, moduleID, method, path string, query url.Values, payload any) (any, map[string]any) {
	if moduleID == "discord_tool" || moduleID == "mattermost_tool" {
		return retryingCall(ctx, rt, moduleID, method, path, query, payload)
	}
	resp, fail := sendRequest(ctx, rt, moduleID, method, path, query, payload)
	if fail != nil {
		return nil, fail
	}
	if errDict := checkResponse(moduleID, resp); errDict != nil {
		return nil, errDict
	}
	return decodeReply(resp), nil
}

// boolArg reads a boolean input with a fallback.
func boolArg(input map[string]any, name string, fallback bool) bool {
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

// intFrom reads an integer with a fallback (provider totals).
func intFrom(value any, fallback int) int {
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

// uriTail extracts the last path segment of a Calendly URI.
func uriTail(uri string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(uri), "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// quotePath mirrors urllib.parse.quote(value) with the default
// safe="/" that upstream uses for Discord custom emoji names ("name:id").
func quotePath(value string) string {
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

// stringList normalises an array input to a list of strings,
// preserving upstream's "empty list is omitted" behaviour.
func stringList(value any) []any {
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

// ------------------------------------------------------- kit forwarding ---

// The helpers below forward to integrationkit so the moved provider code keeps
// its terse unqualified names while this package depends on integrationkit only.

func moduleBaseURL(rt *Runtime, moduleID string) string { return rt.ModuleBaseURL(moduleID) }

func runtimeEnv(rt *Runtime, name string) string { return rt.EnvOrOS(name) }

func runtimeHTTP(rt *Runtime) *httpClient { return rt.HTTPClientOrNew() }

func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asSlice(value any) []any { return integrationkit.AsSlice(value) }

func asString(value any) string { return integrationkit.AsString(value) }

func asBool(value any) bool { return integrationkit.AsBool(value) }

func sliceOrEmpty(value any) []any { return integrationkit.SliceOrEmpty(value) }

func truncateText(text string, limit int) string { return integrationkit.TruncateText(text, limit) }

func clampInt(value, low, high int) int { return integrationkit.ClampInt(value, low, high) }

func scalarString(value any) string { return integrationkit.ScalarString(value) }

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func intArg(input map[string]any, name string, fallback int) int {
	return integrationkit.IntArg(input, name, fallback)
}

func param(typ string) map[string]any { return integrationkit.Param(typ) }

func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func numberValue(value any) (float64, bool) { return integrationkit.Number(value) }

// queryValues turns a map of optional query parameters into url.Values,
// skipping absent/empty values.
func queryValues(values map[string]any) url.Values {
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

// accountToken resolves a module credential for the current call: an account
// alias the action selected wins over the module's single token. A selected
// alias that does not resolve yields "" so the caller reports its own auth
// error, matching upstream returning None from get_by_alias.
func accountToken(rt *Runtime, moduleID, provider string) string {
	if token, handled := rt.ResolveAccountToken(provider); handled {
		return token
	}
	return moduleToken(rt, moduleID)
}

// moduleToken resolves the module's own credential from WithCredential or the
// runtime environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
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

// accountProvider maps a module to the upstream OAuth provider name its
// `account` parameter selects. Empty means the module has no account-aware
// credential (brevo/calcom/calendly keep their single token).
func accountProvider(moduleID string) string {
	switch moduleID {
	case "discord_tool":
		return "discord"
	case "mattermost_tool":
		return "mattermost"
	default:
		return ""
	}
}
