package credentials

// Parity: tools/src/aden_tools/credentials/health_check.py
//
// Credential health checks per integration: validate that a stored credential
// actually works before reporting it usable. The Python reference ships a
// registry of ~42 checkers (dedicated classes plus a generic
// OAuthBearerHealthChecker fallback); this file is the Go projection of the
// same registry, message-for-message, status-for-status.
//
// Divergences (documented, transport-level only):
//   - Transport error text is Go-native (httpx's exception strings do not
//     exist here); the "Failed to connect to X: ..." wrappers and the
//     redaction rules match upstream.
//   - Redirects are not followed (httpx.Client default).

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

// HealthCheckResult is the result of a credential health check.
// Parity: tools/src/aden_tools/credentials/health_check.py::HealthCheckResult
type HealthCheckResult struct {
	// Valid reports whether the credential is valid.
	Valid bool
	// Message is the human-readable status message (surface: validation_message).
	Message string
	// Details carries additional data (status codes, identity, rate limits).
	Details map[string]any
}

const (
	healthDefaultTimeout    = 10 * time.Second
	healthPrometheusTimeout = 5 * time.Second
)

// healthHTTPClient mirrors httpx.Client(timeout=...): a total timeout and no
// redirect following.
func healthHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout: timeout,
			}).DialContext,
			TLSClientConfig: &tls.Config{},
		},
	}
}

var healthCheckers = map[string]func(string) HealthCheckResult{}

func registerHealthChecker(name string, checker func(string) HealthCheckResult) {
	healthCheckers[name] = checker
}

// CheckCredentialHealth validates a credential value against the checker
// registered for its name; a spec-declared endpoint without a dedicated
// checker uses the generic OAuth bearer fallback.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::check_credential_health
func CheckCredentialHealth(credentialName, credentialValue, healthCheckEndpoint, healthCheckMethod string) HealthCheckResult {
	if checker, ok := healthCheckers[credentialName]; ok {
		return checker(credentialValue)
	}
	if healthCheckEndpoint != "" {
		service := PyTitle(strings.ReplaceAll(credentialName, "_", " "))
		return runOAuthBearerHealthCheck(credentialValue, healthCheckEndpoint, service)
	}
	return HealthCheckResult{
		Valid:   true,
		Message: fmt.Sprintf("No health checker for '%s', assuming valid", credentialName),
		Details: map[string]any{"no_checker": true},
	}
}

// HasHealthChecker reports whether a dedicated checker is registered for
// credentialName — the Go projection of upstream
// “credential_id in HEALTH_CHECKERS“ used by the local-save route to decide
// whether to run a health check at all.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::HEALTH_CHECKERS
func HasHealthChecker(credentialName string) bool {
	_, ok := healthCheckers[credentialName]
	return ok
}

// CheckCredentialHealthWithExtra mirrors check_credential_health(name, value,
// **kwargs): the local registry passes extra form fields (cse_id today) to the
// checker. Without extras it behaves exactly like CheckCredentialHealth with
// no spec fallback endpoint, which is what upstream's local save does.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::check_credential_health
func CheckCredentialHealthWithExtra(credentialName, credentialValue string, extraKeys map[string]string) HealthCheckResult {
	if credentialName == "google_search" {
		if cseID := extraKeys["cse_id"]; cseID != "" {
			return runGoogleCustomSearchHealthCheck(credentialValue, cseID)
		}
	}
	return CheckCredentialHealth(credentialName, credentialValue, "", "")
}

// googleCustomSearchEndpoint is a variable so tests can point the checker at a
// local server.
var googleCustomSearchEndpoint = "https://www.googleapis.com/customsearch/v1"

// runGoogleCustomSearchHealthCheck is GoogleSearchHealthChecker.check with a
// CSE ID supplied: the API key alone only proves the format.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::GoogleSearchHealthChecker.check
func runGoogleCustomSearchHealthCheck(apiKey, cseID string) HealthCheckResult {
	status, _, reqErr := performHealthRequest(healthRequestConfig{
		endpoint:       googleCustomSearchEndpoint,
		serviceName:    "Google",
		authType:       healthAuthQuery,
		authQueryParam: "key",
		extraParams: map[string]string{
			"cx":  cseID,
			"q":   "test",
			"num": "1",
		},
	}, apiKey)
	if reqErr != nil {
		if reqErr.timeout {
			return HealthCheckResult{Valid: false, Message: "Google API request timed out", Details: map[string]any{"error": "timeout"}}
		}
		return HealthCheckResult{Valid: false, Message: "Failed to connect to Google API: " + reqErr.text, Details: map[string]any{"error": reqErr.text}}
	}
	switch status {
	case 200:
		return HealthCheckResult{Valid: true, Message: "Google Custom Search credentials valid"}
	case 400:
		return HealthCheckResult{Valid: false, Message: "Google Custom Search: Invalid CSE ID", Details: map[string]any{"status_code": 400}}
	case 403:
		return HealthCheckResult{Valid: false, Message: "Google API key is invalid or quota exceeded", Details: map[string]any{"status_code": 403}}
	default:
		return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Google API returned status %d", status), Details: map[string]any{"status_code": status}}
	}
}

// --- HTTP plumbing ----------------------------------------------------------

type healthAuthType int

const (
	healthAuthBearer healthAuthType = iota
	healthAuthHeader
	healthAuthQuery
	healthAuthBasic
	healthAuthURL
)

type healthRequestConfig struct {
	endpoint    string
	serviceName string
	method      string
	authType    healthAuthType

	authHeaderName     string
	authHeaderTemplate string
	authQueryParam     string

	extraHeaders map[string]string
	extraParams  map[string]string
	jsonBody     map[string]any

	timeout time.Duration

	// buildURL overrides the static endpoint (URL-embedded tokens).
	buildURL func(credential string) string
	// interpret overrides the default status interpretation entirely.
	interpret func(status int, body []byte) HealthCheckResult
	// extractIdentity pulls identity fields from a successful JSON response.
	extractIdentity func(data map[string]any) map[string]string
}

type healthRequestError struct {
	timeout bool
	text    string
}

func (e *healthRequestError) Error() string { return e.text }

func performHealthRequest(cfg healthRequestConfig, credential string) (int, []byte, *healthRequestError) {
	target := cfg.endpoint
	if cfg.buildURL != nil {
		target = cfg.buildURL(credential)
	}
	method := cfg.method
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)

	headers := map[string]string{"Accept": "application/json"}
	switch cfg.authType {
	case healthAuthBearer:
		headers["Authorization"] = "Bearer " + credential
	case healthAuthHeader:
		template := cfg.authHeaderTemplate
		if template == "" {
			template = "Bearer {token}"
		}
		headers[cfg.authHeaderName] = strings.ReplaceAll(template, "{token}", credential)
	}
	for key, value := range cfg.extraHeaders {
		headers[key] = value
	}

	params := map[string]string{}
	if cfg.authType == healthAuthQuery {
		name := cfg.authQueryParam
		if name == "" {
			name = "key"
		}
		params[name] = credential
	}
	for key, value := range cfg.extraParams {
		params[key] = value
	}

	requestURL := target
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for key := range params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		query := url.Values{}
		for _, key := range keys {
			query.Set(key, params[key])
		}
		separator := "?"
		if strings.Contains(requestURL, "?") {
			separator = "&"
		}
		requestURL += separator + query.Encode()
	}

	var bodyReader *bytes.Reader
	if cfg.jsonBody != nil && method == http.MethodPost {
		payload, err := json.Marshal(cfg.jsonBody)
		if err != nil {
			return 0, nil, &healthRequestError{text: err.Error()}
		}
		bodyReader = bytes.NewReader(payload)
	}

	var request *http.Request
	var err error
	if bodyReader != nil {
		request, err = http.NewRequestWithContext(context.Background(), method, requestURL, bodyReader)
	} else {
		request, err = http.NewRequestWithContext(context.Background(), method, requestURL, nil)
	}
	if err != nil {
		return 0, nil, &healthRequestError{text: err.Error()}
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if cfg.authType == healthAuthBasic {
		request.SetBasicAuth(credential, "")
	}

	timeout := cfg.timeout
	if timeout == 0 {
		timeout = healthDefaultTimeout
	}
	response, err := healthHTTPClient(timeout).Do(request)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return 0, nil, &healthRequestError{timeout: true, text: err.Error()}
		}
		return 0, nil, &healthRequestError{text: err.Error()}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return response.StatusCode, nil, &healthRequestError{text: readErr.Error()}
	}
	return response.StatusCode, data, nil
}

// --- Shared interpretations -------------------------------------------------

func healthIdentityDetails(identity map[string]string) map[string]any {
	if len(identity) == 0 {
		return map[string]any{}
	}
	details := map[string]any{}
	raw := map[string]any{}
	for key, value := range identity {
		raw[key] = value
	}
	details["identity"] = raw
	return details
}

func decodeHealthJSON(body []byte) map[string]any {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil
	}
	return out
}

func pyMapGetString(data map[string]any, key string, fallback string) string {
	value, present := data[key]
	if !present || value == nil {
		return fallback
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// pyGetenvDefault mirrors os.getenv(name, default): an explicitly empty value
// wins over the default.
func pyGetenvDefault(name, fallback string) string {
	if value, present := os.LookupEnv(name); present {
		return value
	}
	return fallback
}

func redactHealthErrorText(text string, needles []string) string {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return "Request failed (details redacted for security)"
		}
	}
	return text
}

// runOAuthBearerHealthCheck is the generic fallback for a spec-declared
// endpoint without a dedicated checker.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::OAuthBearerHealthChecker.check
func runOAuthBearerHealthCheck(credentialValue, endpoint, serviceName string) HealthCheckResult {
	status, _, reqErr := performHealthRequest(healthRequestConfig{
		endpoint:    endpoint,
		serviceName: serviceName,
		authType:    healthAuthBearer,
		extractIdentity: func(map[string]any) map[string]string {
			return map[string]string{}
		},
	}, credentialValue)
	if reqErr != nil {
		if reqErr.timeout {
			return HealthCheckResult{Valid: false, Message: serviceName + " API request timed out", Details: map[string]any{"error": "timeout"}}
		}
		text := redactHealthErrorText(reqErr.text, []string{"Bearer", "Authorization"})
		return HealthCheckResult{Valid: false, Message: "Failed to connect to " + serviceName + ": " + text, Details: map[string]any{"error": text}}
	}

	switch {
	case status == 200:
		identity := map[string]string{}
		return HealthCheckResult{Valid: true, Message: serviceName + " credentials valid", Details: healthIdentityDetails(identity)}
	case status == 401:
		return HealthCheckResult{Valid: false, Message: serviceName + " token is invalid or expired", Details: map[string]any{"status_code": 401}}
	case status == 403:
		return HealthCheckResult{Valid: false, Message: serviceName + " token lacks required scopes", Details: map[string]any{"status_code": 403}}
	default:
		return HealthCheckResult{Valid: false, Message: fmt.Sprintf("%s API returned status %d", serviceName, status), Details: map[string]any{"status_code": status}}
	}
}

// runBaseHTTPHealthCheck implements BaseHttpHealthChecker's default flow.
//
// Parity: tools/src/aden_tools/credentials/health_check.py::BaseHttpHealthChecker.check / _interpret_response
func runBaseHTTPHealthCheck(cfg healthRequestConfig, credentialValue string, redactTokens []string) HealthCheckResult {
	status, body, reqErr := performHealthRequest(cfg, credentialValue)
	if reqErr != nil {
		if reqErr.timeout {
			return HealthCheckResult{Valid: false, Message: cfg.serviceName + " API request timed out", Details: map[string]any{"error": "timeout"}}
		}
		text := redactHealthErrorText(reqErr.text, redactTokens)
		return HealthCheckResult{Valid: false, Message: "Failed to connect to " + cfg.serviceName + ": " + text, Details: map[string]any{"error": text}}
	}
	if cfg.interpret != nil {
		return cfg.interpret(status, body)
	}

	identity := map[string]string{}
	if status == 200 {
		if data := decodeHealthJSON(body); data != nil && cfg.extractIdentity != nil {
			identity = cfg.extractIdentity(data)
		}
	}

	switch {
	case status == 200:
		return HealthCheckResult{Valid: true, Message: cfg.serviceName + " credentials valid", Details: healthIdentityDetails(identity)}
	case status == 429:
		return HealthCheckResult{Valid: true, Message: cfg.serviceName + " credentials valid (rate limited)", Details: map[string]any{"status_code": status, "rate_limited": true}}
	case status == 401:
		return HealthCheckResult{Valid: false, Message: cfg.serviceName + " credentials are invalid or expired", Details: map[string]any{"status_code": status}}
	case status == 403:
		return HealthCheckResult{Valid: false, Message: cfg.serviceName + " credentials lack required permissions", Details: map[string]any{"status_code": status}}
	default:
		return HealthCheckResult{Valid: false, Message: fmt.Sprintf("%s API returned status %d", cfg.serviceName, status), Details: map[string]any{"status_code": status}}
	}
}

// --- Dedicated checkers -----------------------------------------------------

func init() {
	registerHealthCheckersPart1()
	registerHealthCheckersPart2()
	registerHealthCheckersPart3()
	registerHealthCheckersPart4()
	registerHealthCheckersPart5()
}

// registerBaseHTTP registers a declarative BaseHttpHealthChecker subclass:
// same auth-header probing and redaction list as the class-based checkers.
func registerBaseHTTP(name string, cfg healthRequestConfig) {
	registerHealthChecker(name, func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(cfg, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})
}

// registerHealthCheckersPart1 registers one slice of the upstream health-checker registry.
func registerHealthCheckersPart1() {
	// HubSpot
	registerHealthChecker("hubspot", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:     "https://api.hubapi.com/crm/v3/objects/contacts",
			serviceName:  "HubSpot",
			authType:     healthAuthBearer,
			extraParams:  map[string]string{"limit": "1"},
			extraHeaders: map[string]string{},
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					return HealthCheckResult{Valid: true, Message: "HubSpot credentials valid"}
				case 401:
					return HealthCheckResult{Valid: false, Message: "HubSpot token is invalid or expired", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "HubSpot token lacks required scopes", Details: map[string]any{"status_code": 403, "required": "crm.objects.contacts.read"}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("HubSpot API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Zoho CRM
	registerHealthChecker("zoho_crm", func(value string) HealthCheckResult {
		domain := strings.TrimRight(pyGetenvDefault("ZOHO_API_DOMAIN", "https://www.zohoapis.com"), "/")
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:           domain + "/crm/v2/users?type=CurrentUser",
			serviceName:        "Zoho CRM",
			authType:           healthAuthHeader,
			authHeaderName:     "Authorization",
			authHeaderTemplate: "Zoho-oauthtoken {token}",
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					return HealthCheckResult{Valid: true, Message: "Zoho CRM credentials valid"}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Zoho CRM token is invalid or expired", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "Zoho CRM token lacks required scopes", Details: map[string]any{"status_code": 403}}
				case 429:
					return HealthCheckResult{Valid: true, Message: "Zoho CRM credentials valid (rate limited)", Details: map[string]any{"status_code": 429, "rate_limited": true}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Zoho CRM API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Brave Search
	registerHealthChecker("brave_search", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:           "https://api.search.brave.com/res/v1/web/search",
			serviceName:        "Brave Search",
			authType:           healthAuthHeader,
			authHeaderName:     "X-Subscription-Token",
			authHeaderTemplate: "{token}",
			extraParams:        map[string]string{"q": "test", "count": "1"},
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					return HealthCheckResult{Valid: true, Message: "Brave Search API key valid"}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Brave Search API key is invalid", Details: map[string]any{"status_code": 401}}
				case 429:
					return HealthCheckResult{Valid: true, Message: "Brave Search API key valid (rate limited)", Details: map[string]any{"status_code": 429, "rate_limited": true}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Brave Search API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Google OAuth (Gmail, Calendar, Sheets)
	registerHealthChecker("google", func(value string) HealthCheckResult {
		type googleEndpoint struct {
			scope  string
			url    string
			params map[string]string
		}
		endpoints := []googleEndpoint{
			{scope: "gmail", url: "https://gmail.googleapis.com/gmail/v1/users/me/profile"},
			{scope: "calendar", url: "https://www.googleapis.com/calendar/v3/users/me/calendarList", params: map[string]string{"maxResults": "1"}},
			{scope: "sheets", url: "https://sheets.googleapis.com/v4/spreadsheets/healthcheck_nonexistent"},
		}
		missingScopes := []string{}
		for _, endpoint := range endpoints {
			status, _, reqErr := performHealthRequest(healthRequestConfig{
				endpoint:    endpoint.url,
				serviceName: "Google",
				authType:    healthAuthBearer,
				extraParams: endpoint.params,
			}, value)
			if reqErr != nil {
				if reqErr.timeout {
					return HealthCheckResult{Valid: false, Message: "Google API request timed out", Details: map[string]any{"error": "timeout"}}
				}
				text := redactHealthErrorText(reqErr.text, []string{"Bearer", "Authorization"})
				return HealthCheckResult{Valid: false, Message: "Failed to connect to Google: " + text, Details: map[string]any{"error": text}}
			}
			switch {
			case status == 401:
				return HealthCheckResult{Valid: false, Message: "Google token is invalid or expired", Details: map[string]any{"status_code": 401}}
			case status == 403:
				missingScopes = append(missingScopes, endpoint.scope)
			case status == 200 || status == 404:
				// 404 on the Sheets probe means the token + scope are valid.
			default:
				return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Google %s API returned status %d", endpoint.scope, status), Details: map[string]any{"status_code": status, "scope": endpoint.scope}}
			}
		}
		if len(missingScopes) > 0 {
			missing := make([]any, 0, len(missingScopes))
			for _, scope := range missingScopes {
				missing = append(missing, scope)
			}
			return HealthCheckResult{
				Valid:   false,
				Message: "Google token lacks scopes for: " + strings.Join(missingScopes, ", "),
				Details: map[string]any{"status_code": 403, "missing_scopes": missing},
			}
		}
		return HealthCheckResult{Valid: true, Message: "Google credentials valid (Gmail, Calendar, Sheets)"}
	})

	// Google Custom Search (no CSE ID available at validation time)
	registerHealthChecker("google_search", func(value string) HealthCheckResult {
		// check_credential_health calls the checker with the credential value
		// only; without a CSE ID this is a format-only partial check.
		return HealthCheckResult{
			Valid:   true,
			Message: "Google API key format valid (CSE ID needed for full check)",
			Details: map[string]any{"partial_check": true},
		}
	})
}

// registerHealthCheckersPart2 registers one slice of the upstream health-checker registry.
func registerHealthCheckersPart2() {

	// Slack
	registerHealthChecker("slack", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:    "https://slack.com/api/auth.test",
			serviceName: "Slack",
			method:      http.MethodPost,
			authType:    healthAuthBearer,
			extraHeaders: map[string]string{
				"Content-Type": "application/json",
			},
			interpret: func(status int, body []byte) HealthCheckResult {
				if status != 200 {
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Slack API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
				data := decodeHealthJSON(body)
				if data == nil {
					data = map[string]any{}
				}
				if pyTruthy(data["ok"]) {
					team := pyMapGetString(data, "team", "unknown")
					details := map[string]any{
						"team":    data["team"],
						"user":    data["user"],
						"team_id": data["team_id"],
					}
					return HealthCheckResult{Valid: true, Message: fmt.Sprintf("Slack token valid (team: %s)", team), Details: details}
				}
				errText := pyMapGetString(data, "error", "unknown error")
				return HealthCheckResult{Valid: false, Message: "Slack token invalid: " + errText, Details: map[string]any{"slack_error": data["error"]}}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Calendly
	registerHealthChecker("calendly_pat", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:    "https://api.calendly.com/users/me",
			serviceName: "Calendly",
			authType:    healthAuthBearer,
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					data := decodeHealthJSON(body)
					user, _ := data["resource"].(map[string]any)
					if user == nil {
						user = map[string]any{}
					}
					name := pyMapGetString(user, "name", "unknown")
					return HealthCheckResult{
						Valid:   true,
						Message: fmt.Sprintf("Calendly PAT valid (user: %s)", name),
						Details: map[string]any{"name": name, "email": user["email"]},
					}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Calendly PAT is invalid or expired", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "Calendly PAT lacks required scopes", Details: map[string]any{"status_code": 403}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Calendly API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// GitHub
	registerHealthChecker("github", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:    "https://api.github.com/user",
			serviceName: "GitHub",
			authType:    healthAuthBearer,
			extraHeaders: map[string]string{
				"Accept":               "application/vnd.github+json",
				"X-GitHub-Api-Version": "2022-11-28",
			},
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					data := decodeHealthJSON(body)
					username := pyMapGetString(data, "login", "unknown")
					return HealthCheckResult{Valid: true, Message: fmt.Sprintf("GitHub token valid (user: %s)", username), Details: map[string]any{"username": username}}
				case 401:
					return HealthCheckResult{Valid: false, Message: "GitHub token is invalid or expired", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "GitHub token lacks required scopes", Details: map[string]any{"status_code": 403}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("GitHub API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Discord
	registerHealthChecker("discord", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:           "https://discord.com/api/v10/users/@me",
			serviceName:        "Discord",
			authType:           healthAuthHeader,
			authHeaderName:     "Authorization",
			authHeaderTemplate: "Bot {token}",
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					data := decodeHealthJSON(body)
					username := pyMapGetString(data, "username", "unknown")
					bot := any(true)
					if value, present := data["bot"]; present {
						bot = value
					}
					return HealthCheckResult{Valid: true, Message: fmt.Sprintf("Discord bot token valid (bot: %s)", username), Details: map[string]any{"username": username, "bot": bot}}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Discord bot token is invalid", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "Discord bot token lacks required intents/permissions", Details: map[string]any{"status_code": 403}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Discord API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Resend
	registerHealthChecker("resend", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:    "https://api.resend.com/domains",
			serviceName: "Resend",
			authType:    healthAuthBearer,
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					return HealthCheckResult{Valid: true, Message: "Resend API key valid"}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Resend API key is invalid", Details: map[string]any{"status_code": 401}}
				case 403:
					return HealthCheckResult{Valid: false, Message: "Resend API key lacks required permissions", Details: map[string]any{"status_code": 403}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Resend API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})
}

// registerHealthCheckersPart3 registers one slice of the upstream health-checker registry.
func registerHealthCheckersPart3() {

	// Google Maps
	registerHealthChecker("google_maps", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:    "https://maps.googleapis.com/maps/api/geocode/json",
			serviceName: "Google Maps",
			authType:    healthAuthQuery,
			extraParams: map[string]string{
				"key":     value,
				"address": "1600 Amphitheatre Parkway, Mountain View, CA",
			},
			interpret: func(status int, body []byte) HealthCheckResult {
				if status != 200 {
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Google Maps API returned HTTP %d", status), Details: map[string]any{"status_code": status}}
				}
				data := decodeHealthJSON(body)
				apiStatus := pyMapGetString(data, "status", "")
				switch apiStatus {
				case "OK":
					return HealthCheckResult{Valid: true, Message: "Google Maps API key valid"}
				case "REQUEST_DENIED":
					return HealthCheckResult{Valid: false, Message: "Google Maps API key is invalid or restricted", Details: map[string]any{"status": apiStatus}}
				case "OVER_QUERY_LIMIT":
					return HealthCheckResult{Valid: true, Message: "Google Maps API key valid (quota exceeded)", Details: map[string]any{"rate_limited": true}}
				default:
					return HealthCheckResult{Valid: false, Message: "Google Maps API returned status: " + apiStatus, Details: map[string]any{"status": apiStatus}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Lusha
	registerHealthChecker("lusha_api_key", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			endpoint:           "https://api.lusha.com/person",
			serviceName:        "Lusha",
			authType:           healthAuthHeader,
			authHeaderName:     "api_key",
			authHeaderTemplate: "{token}",
			extraParams:        map[string]string{"firstName": "test", "lastName": "test", "company": "test"},
			interpret: func(status int, body []byte) HealthCheckResult {
				switch status {
				case 200:
					return HealthCheckResult{Valid: true, Message: "Lusha API key valid"}
				case 401:
					return HealthCheckResult{Valid: false, Message: "Lusha API key is invalid", Details: map[string]any{"status_code": 401}}
				case 429:
					return HealthCheckResult{Valid: true, Message: "Lusha API key valid (rate limited)", Details: map[string]any{"rate_limited": true}}
				default:
					return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Lusha API returned status %d", status), Details: map[string]any{"status_code": status}}
				}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// Prometheus (no auth; the credential value is the base URL)
	registerHealthChecker("prometheus", func(value string) HealthCheckResult {
		url := strings.TrimRight(value, "/") + "/-/ready"
		status, _, reqErr := performHealthRequest(healthRequestConfig{
			endpoint:    url,
			serviceName: "Prometheus",
			authType:    healthAuthURL,
			timeout:     healthPrometheusTimeout,
		}, value)
		if reqErr != nil {
			if reqErr.timeout {
				return HealthCheckResult{Valid: false, Message: "Prometheus health check timed out", Details: map[string]any{"error": "timeout"}}
			}
			return HealthCheckResult{Valid: false, Message: "Failed to connect to Prometheus: " + reqErr.text, Details: map[string]any{"error": reqErr.text}}
		}
		if status == 200 {
			return HealthCheckResult{Valid: true, Message: "Prometheus is healthy"}
		}
		return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Prometheus returned status %d", status), Details: map[string]any{"status_code": status}}
	})

	// Intercom (OAuth bearer with a fixed endpoint)
	registerHealthChecker("intercom", func(value string) HealthCheckResult {
		return runOAuthBearerHealthCheck(value, "https://api.intercom.io/me", "Intercom")
	})

	// Telegram (URL-embedded token)
	registerHealthChecker("telegram", func(value string) HealthCheckResult {
		return runBaseHTTPHealthCheck(healthRequestConfig{
			serviceName: "Telegram",
			authType:    healthAuthURL,
			buildURL: func(credential string) string {
				return "https://api.telegram.org/bot" + credential + "/getMe"
			},
			interpret: func(status int, body []byte) HealthCheckResult {
				if status == 200 {
					data := decodeHealthJSON(body)
					if data == nil {
						return HealthCheckResult{Valid: true, Message: "Telegram credentials valid"}
					}
					if pyTruthy(data["ok"]) {
						result, _ := data["result"].(map[string]any)
						if result == nil {
							result = map[string]any{}
						}
						username := pyMapGetString(result, "username", "unknown")
						identity := map[string]string{}
						if username != "unknown" {
							identity["username"] = username
						}
						return HealthCheckResult{Valid: true, Message: fmt.Sprintf("Telegram bot token valid (bot: @%s)", username), Details: healthIdentityDetails(identity)}
					}
					return HealthCheckResult{Valid: false, Message: "Telegram bot token is invalid", Details: map[string]any{"telegram_error": pyMapGetString(data, "description", "")}}
				}
				if status == 401 {
					return HealthCheckResult{Valid: false, Message: "Telegram bot token is invalid", Details: map[string]any{"status_code": 401}}
				}
				return HealthCheckResult{Valid: false, Message: fmt.Sprintf("Telegram API returned status %d", status), Details: map[string]any{"status_code": status}}
			},
		}, value, []string{"Bearer", "Authorization", "api_key", "token"})
	})

	// --- BaseHttpHealthChecker subclasses (declarative configs) ------------

	registerBaseHTTP("stripe", healthRequestConfig{
		endpoint:    "https://api.stripe.com/v1/balance",
		serviceName: "Stripe",
		authType:    healthAuthBearer,
	})
}

// registerHealthCheckersPart4 registers one slice of the upstream health-checker registry.
func registerHealthCheckersPart4() {
	registerBaseHTTP("exa_search", healthRequestConfig{
		endpoint:    "https://api.exa.ai/search",
		serviceName: "Exa Search",
		method:      http.MethodPost,
		authType:    healthAuthBearer,
		jsonBody:    map[string]any{"query": "test", "numResults": 1},
	})
	registerBaseHTTP("calcom", healthRequestConfig{
		endpoint:       "https://api.cal.com/v1/me",
		serviceName:    "Cal.com",
		authType:       healthAuthQuery,
		authQueryParam: "apiKey",
	})
	registerBaseHTTP("serpapi", healthRequestConfig{
		endpoint:       "https://serpapi.com/account.json",
		serviceName:    "SerpAPI",
		authType:       healthAuthQuery,
		authQueryParam: "api_key",
	})
	registerBaseHTTP("similarweb", healthRequestConfig{
		endpoint:           "https://api.similarweb.com/v5/website-analysis/websites/traffic-and-engagement/",
		serviceName:        "SimilarWeb",
		authType:           healthAuthHeader,
		authHeaderName:     "api-key",
		authHeaderTemplate: "{token}",
		extraParams: map[string]string{
			"domain": "google.com", "start_date": "2024-01", "end_date": "2024-01",
			"country": "world", "granularity": "monthly",
		},
	})
	registerBaseHTTP("apollo", healthRequestConfig{
		endpoint:       "https://api.apollo.io/v1/auth/health",
		serviceName:    "Apollo",
		authType:       healthAuthQuery,
		authQueryParam: "api_key",
	})
	registerBaseHTTP("newsdata", healthRequestConfig{
		endpoint:       "https://newsdata.io/api/1/news",
		serviceName:    "Newsdata",
		authType:       healthAuthQuery,
		authQueryParam: "apikey",
		extraParams:    map[string]string{"q": "test"},
	})
	registerBaseHTTP("finlight", healthRequestConfig{
		endpoint:    "https://api.finlight.me/v1/news",
		serviceName: "Finlight",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("brevo", healthRequestConfig{
		endpoint:           "https://api.brevo.com/v3/account",
		serviceName:        "Brevo",
		authType:           healthAuthHeader,
		authHeaderName:     "api-key",
		authHeaderTemplate: "{token}",
		extractIdentity: func(data map[string]any) map[string]string {
			identity := map[string]string{}
			if email := pyMapGetString(data, "email", ""); email != "" {
				identity["email"] = email
			}
			if company := pyMapGetString(data, "companyName", ""); company != "" {
				identity["company"] = company
			}
			return identity
		},
	})
	registerBaseHTTP("apify", healthRequestConfig{
		endpoint:    "https://api.apify.com/v2/users/me",
		serviceName: "Apify",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("asana", healthRequestConfig{
		endpoint:    "https://app.asana.com/api/1.0/users/me",
		serviceName: "Asana",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("attio", healthRequestConfig{
		endpoint:    "https://api.attio.com/v2/workspace_members",
		serviceName: "Attio",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("docker_hub", healthRequestConfig{
		endpoint:    "https://hub.docker.com/v2/user/login",
		serviceName: "Docker Hub",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("google_search_console", healthRequestConfig{
		endpoint:    "https://www.googleapis.com/webmasters/v3/sites",
		serviceName: "Google Search Console",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("huggingface", healthRequestConfig{
		endpoint:    "https://huggingface.co/api/whoami-v2",
		serviceName: "Hugging Face",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("linear", healthRequestConfig{
		endpoint:    "https://api.linear.app/graphql",
		serviceName: "Linear",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("microsoft_graph", healthRequestConfig{
		endpoint:    "https://graph.microsoft.com/v1.0/me",
		serviceName: "Microsoft Graph",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("pinecone", healthRequestConfig{
		endpoint:    "https://api.pinecone.io/indexes",
		serviceName: "Pinecone",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("vercel", healthRequestConfig{
		endpoint:    "https://api.vercel.com/v2/user",
		serviceName: "Vercel",
		authType:    healthAuthBearer,
	})
	registerBaseHTTP("gitlab_token", healthRequestConfig{
		endpoint:           "https://gitlab.com/api/v4/user",
		serviceName:        "GitLab",
		authType:           healthAuthHeader,
		authHeaderName:     "PRIVATE-TOKEN",
		authHeaderTemplate: "{token}",
	})
	registerBaseHTTP("notion", healthRequestConfig{
		endpoint:    "https://api.notion.com/v1/users/me",
		serviceName: "Notion",
		authType:    healthAuthBearer,
		extraHeaders: map[string]string{
			"Notion-Version": "2022-06-28",
		},
	})
}

// registerHealthCheckersPart5 registers one slice of the upstream health-checker registry.
func registerHealthCheckersPart5() {
	registerBaseHTTP("greenhouse_token", healthRequestConfig{
		endpoint:    "https://harvest.greenhouse.io/v1/jobs?per_page=1",
		serviceName: "Greenhouse",
		authType:    healthAuthBasic,
	})
	registerBaseHTTP("pipedrive", healthRequestConfig{
		endpoint:       "https://api.pipedrive.com/v1/users/me",
		serviceName:    "Pipedrive",
		authType:       healthAuthQuery,
		authQueryParam: "api_token",
	})
	registerBaseHTTP("trello_key", healthRequestConfig{
		endpoint:       "https://api.trello.com/1/members/me",
		serviceName:    "Trello",
		authType:       healthAuthQuery,
		authQueryParam: "key",
	})
	registerBaseHTTP("trello_token", healthRequestConfig{
		endpoint:       "https://api.trello.com/1/members/me",
		serviceName:    "Trello",
		authType:       healthAuthQuery,
		authQueryParam: "token",
	})
	registerBaseHTTP("youtube", healthRequestConfig{
		endpoint:       "https://www.googleapis.com/youtube/v3/videoCategories?part=snippet&regionCode=US",
		serviceName:    "YouTube",
		authType:       healthAuthQuery,
		authQueryParam: "key",
	})
	registerBaseHTTP("cloudflare", healthRequestConfig{
		endpoint:    "https://api.cloudflare.com/client/v4/user/tokens/verify",
		serviceName: "Cloudflare",
		authType:    healthAuthBearer,
	})
}

// PyTitle mirrors Python's str.title() for ASCII words: first letter of each
// word upper-case, every other letter lower-case, apostrophes inside a word
// keep the break behaviour of the CPython implementation closely enough for
// the service names here (snake_case labels).
func PyTitle(value string) string {
	runes := []rune(value)
	out := make([]rune, len(runes))
	previousIsCased := false
	for index, r := range runes {
		if unicode.IsLetter(r) {
			if previousIsCased {
				out[index] = unicode.ToLower(r)
			} else {
				out[index] = unicode.ToTitle(r)
			}
			previousIsCased = true
			continue
		}
		out[index] = r
		previousIsCased = false
	}
	return string(out)
}
