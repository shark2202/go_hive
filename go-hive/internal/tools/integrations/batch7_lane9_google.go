// Google Analytics 4 and Google Search Console (upstream google_analytics_tool
// and google_search_console_tool, 7 + 8 actions).
//
// GA4 speaks the Data API v1beta REST surface the Python client wraps:
//
//	POST /{property}:runReport
//	POST /{property}:runRealtimeReport
//
// Search Console uses two roots: webmasters/v3 for analytics and sitemaps,
// searchconsole/v1 for URL inspection.
//
// Authentication accepts an already-minted OAuth token (WithCredential, or the
// module's environment variables — GOOGLE_ACCESS_TOKEN is shared) and
// otherwise mints one from a service-account JSON at
// GOOGLE_APPLICATION_CREDENTIALS with the read-only scope for that API. gcloud
// application-default credential files are not read; export a token instead.
package integrations

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ------------------------------------------------------- shared credentials ---

// batch7Lane9GoogleAuthError renders the GA "not configured" map.
func batch7Lane9GoogleAuthError(detail string) map[string]any {
	message := "Google Analytics credentials not configured"
	if detail != "" {
		message += ": " + detail
	}
	return map[string]any{
		"error": message,
		"help": "Set GOOGLE_APPLICATION_CREDENTIALS to your service account JSON path, " +
			"or set GA_ACCESS_TOKEN / GOOGLE_ACCESS_TOKEN to an OAuth access token",
	}
}

// batch7Lane9GoogleToken resolves a bearer token for one Google API: the direct
// token first, then a service-account JWT exchange with the given scope.
func batch7Lane9GoogleToken(ctx context.Context, rt *Runtime, moduleID, scope string) (string, map[string]any) {
	if token := batch7Lane9ModuleToken(rt, moduleID); token != "" {
		return token, nil
	}
	credentialsPath := strings.TrimSpace(batch7Lane9Getenv(rt, "GOOGLE_APPLICATION_CREDENTIALS"))
	if credentialsPath == "" {
		return "", nil
	}
	token, _, failure := batch7Lane9GoogleServiceAccountToken(ctx, rt, credentialsPath, scope)
	if failure != nil {
		return "", failure
	}
	return token, nil
}

// batch7Lane9GoogleServiceAccountToken exchanges a service-account JSON key
// for an OAuth access token using the JWT bearer grant.
func batch7Lane9GoogleServiceAccountToken(ctx context.Context, rt *Runtime, path, scope string) (string, string, map[string]any) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	var key struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: invalid service account JSON: " + err.Error()}
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return "", "", map[string]any{
			"error": "Google authentication failed: service account JSON is missing client_email or private_key",
		}
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = batch7Lane9GoogleTokenURI
	}
	assertion, err := batch7Lane9GoogleJWT(key.ClientEmail, key.PrivateKey, tokenURI, scope, batch7Lane9Now(rt).UTC())
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	form := url.Values{
		"grant_type": []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  []string{assertion},
	}
	resp, err := batch7Lane9DoForm(ctx, rt, tokenURI, nil, form, 30*time.Second)
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	if resp.Status >= 400 {
		return "", "", map[string]any{
			"error": "Google authentication failed: token endpoint returned HTTP " + strconv.Itoa(resp.Status),
		}
	}
	data, ok := batch7Lane9JSONObject(resp.Body)
	if !ok {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned invalid JSON"}
	}
	accessToken := batch7Lane9StringOr(data, "access_token", "")
	if accessToken == "" {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned no access_token"}
	}
	return accessToken, key.ProjectID, nil
}

// batch7Lane9GoogleJWT builds an RS256-signed JWT for the service-account
// OAuth flow with an explicit scope.
func batch7Lane9GoogleJWT(email, privateKeyPEM, audience, scope string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("service account private_key is not PEM encoded")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("service account private_key is not RSA")
		}
		key = rsaKey
	} else if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else {
		return "", fmt.Errorf("service account private_key could not be parsed")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   email,
		"scope": scope,
		"aud":   audience,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// ------------------------------------------------------------ google analytics ---

var batch7Lane9GoogleAnalyticsTool = &Tool{
	ID:          "google_analytics_tool",
	Name:        "google_analytics",
	DisplayName: "Google Analytics",
	Summary:     "Read-only Google Analytics 4 traffic, acquisition and conversion reports (7 actions).",
	Actions: []Action{
		{
			Name:     "ga_run_report",
			Summary:  "Run a custom Google Analytics 4 report.",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id", "metrics"},
			Params: map[string]any{
				"property_id": param("string"),
				"metrics":     batch7Lane9ArrayParam(),
				"dimensions":  batch7Lane9ArrayParam(),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 100),
			},
		},
		{
			Name:     "ga_get_realtime",
			Summary:  "Get real-time Google Analytics data (active users, current pages).",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"metrics":     batch7Lane9ArrayParam(),
			},
		},
		{
			Name:     "ga_get_top_pages",
			Summary:  "Get top pages by views and engagement.",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 10),
			},
		},
		{
			Name:     "ga_get_traffic_sources",
			Summary:  "Get traffic breakdown by source/medium.",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 10),
			},
		},
		{
			Name:     "ga_get_user_demographics",
			Summary:  "Get user demographics breakdown (country, language, device).",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 20),
			},
		},
		{
			Name:     "ga_get_conversion_events",
			Summary:  "Get conversion event counts and values.",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 20),
			},
		},
		{
			Name:     "ga_get_landing_pages",
			Summary:  "Get top landing pages with entrance metrics.",
			Source:   "google_analytics_tool/google_analytics_tool.py",
			Required: []string{"property_id"},
			Params: map[string]any{
				"property_id": param("string"),
				"start_date":  paramDefault("string", "28daysAgo"),
				"end_date":    paramDefault("string", "today"),
				"limit":       paramDefault("integer", 10),
			},
		},
	},
}

func batch7Lane9GoogleAnalyticsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"ga_run_report":            batch7Lane9GARunReportAction,
		"ga_get_realtime":          batch7Lane9GAGetRealtime,
		"ga_get_top_pages":         batch7Lane9GATopPages,
		"ga_get_traffic_sources":   batch7Lane9GATrafficSources,
		"ga_get_user_demographics": batch7Lane9GADemographics,
		"ga_get_conversion_events": batch7Lane9GAConversionEvents,
		"ga_get_landing_pages":     batch7Lane9GALandingPages,
	}
}

// batch7Lane9GAValidate mirrors upstream _validate_inputs.
func batch7Lane9GAValidate(propertyID string, limit *int) map[string]any {
	if propertyID == "" || !strings.HasPrefix(propertyID, "properties/") {
		return map[string]any{
			"error": "property_id must start with 'properties/' (e.g., 'properties/123456')",
		}
	}
	if limit != nil && (*limit < 1 || *limit > 10000) {
		return map[string]any{"error": "limit must be between 1 and 10000"}
	}
	return nil
}

// batch7Lane9GACall performs one Data API request and decodes the object.
func batch7Lane9GACall(ctx context.Context, rt *Runtime, token, endpoint string, payload any) (map[string]any, map[string]any) {
	headers := batch7Lane9Bearer(token)
	resp, err := batch7Lane9DoJSON(ctx, rt, http.MethodPost, endpoint, headers, payload, 60*time.Second)
	if err != nil {
		return nil, batch7Lane9TransportError(ctx, "Google Analytics", err)
	}
	if resp.Status >= 400 {
		message := batch4TruncateText(string(resp.Body), 500)
		if data, ok := batch7Lane9JSONObject(resp.Body); ok {
			if inner := batch4AsMap(data["error"]); inner != nil {
				if text := batch7Lane9StringOr(inner, "message", ""); text != "" {
					message = text
				}
			}
		}
		return nil, map[string]any{
			"error": "Google Analytics API error: HTTP " + strconv.Itoa(resp.Status) + ": " + message,
		}
	}
	data, ok := batch7Lane9JSONObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Google Analytics API error: invalid JSON response"}
	}
	return data, nil
}

// batch7Lane9GARowValues reads the header names of a GA response section.
func batch7Lane9GAHeaders(value any) []string {
	headers := make([]string, 0)
	for _, raw := range batch4AsSlice(value) {
		headers = append(headers, batch7Lane9StringOr(batch4AsMap(raw), "name", ""))
	}
	return headers
}

// batch7Lane9GAFormatReport mirrors _format_report_response.
func batch7Lane9GAFormatReport(response map[string]any) map[string]any {
	dimensionHeaders := batch7Lane9GAHeaders(response["dimensionHeaders"])
	metricHeaders := batch7Lane9GAHeaders(response["metricHeaders"])
	rows := make([]any, 0)
	for _, raw := range batch4AsSlice(response["rows"]) {
		row := batch4AsMap(raw)
		rowData := map[string]any{}
		for index, value := range batch4AsSlice(row["dimensionValues"]) {
			if index < len(dimensionHeaders) {
				rowData[dimensionHeaders[index]] = batch7Lane9StringOr(batch4AsMap(value), "value", "")
			}
		}
		for index, value := range batch4AsSlice(row["metricValues"]) {
			if index < len(metricHeaders) {
				rowData[metricHeaders[index]] = batch7Lane9StringOr(batch4AsMap(value), "value", "")
			}
		}
		rows = append(rows, rowData)
	}
	return map[string]any{
		"row_count":         batch7Lane9IntOr(response, "rowCount", 0),
		"rows":              rows,
		"dimension_headers": dimensionHeaders,
		"metric_headers":    metricHeaders,
	}
}

// batch7Lane9GAFormatRealtime mirrors _format_realtime_response.
func batch7Lane9GAFormatRealtime(response map[string]any) map[string]any {
	metricHeaders := batch7Lane9GAHeaders(response["metricHeaders"])
	rows := make([]any, 0)
	for _, raw := range batch4AsSlice(response["rows"]) {
		row := batch4AsMap(raw)
		rowData := map[string]any{}
		for index, value := range batch4AsSlice(row["metricValues"]) {
			if index < len(metricHeaders) {
				rowData[metricHeaders[index]] = batch7Lane9StringOr(batch4AsMap(value), "value", "")
			}
		}
		rows = append(rows, rowData)
	}
	return map[string]any{
		"row_count":      batch7Lane9IntOr(response, "rowCount", 0),
		"rows":           rows,
		"metric_headers": metricHeaders,
	}
}

// batch7Lane9GAPropertyPath renders the {property}:method path segment.
func batch7Lane9GAPropertyPath(rt *Runtime, propertyID, method string) string {
	base := batch7Lane9Base(rt, "google_analytics_tool", batch7Lane9GABase)
	return base + "/" + propertyID + ":" + method
}

// batch7Lane9GARunReport performs the shared report request.
func batch7Lane9GARunReport(ctx context.Context, rt *Runtime, token, propertyID string, metrics, dimensions []string, startDate, endDate string, limit int) (any, map[string]any) {
	metricEntries := make([]any, 0, len(metrics))
	for _, metric := range metrics {
		metricEntries = append(metricEntries, map[string]any{"name": metric})
	}
	body := map[string]any{
		"metrics":    metricEntries,
		"dateRanges": []any{map[string]any{"startDate": startDate, "endDate": endDate}},
		"limit":      limit,
	}
	if len(dimensions) > 0 {
		dimensionEntries := make([]any, 0, len(dimensions))
		for _, dimension := range dimensions {
			dimensionEntries = append(dimensionEntries, map[string]any{"name": dimension})
		}
		body["dimensions"] = dimensionEntries
	}
	response, failure := batch7Lane9GACall(ctx, rt, token, batch7Lane9GAPropertyPath(rt, propertyID, "runReport"), body)
	if failure != nil {
		return nil, failure
	}
	return batch7Lane9GAFormatReport(response), nil
}

func batch7Lane9GARunReportAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	propertyID := stringArg(input, "property_id")
	metrics := stringSliceArg(input, "metrics")
	dimensions := stringSliceArg(input, "dimensions")
	limit := intArg(input, "limit", 100)

	token, failure := batch7Lane9GoogleToken(ctx, rt, "google_analytics_tool", batch7Lane9GAReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + batch7Lane9StringOr(failure, "error", "")}, nil
		}
		return batch7Lane9GoogleAuthError(""), nil
	}
	if err := batch7Lane9GAValidate(propertyID, &limit); err != nil {
		return err, nil
	}
	if len(metrics) == 0 {
		return map[string]any{"error": "metrics list must not be empty"}, nil
	}
	result, failure := batch7Lane9GARunReport(ctx, rt, token, propertyID, metrics, dimensions,
		stringArg(input, "start_date"), stringArg(input, "end_date"), limit)
	if failure != nil {
		return failure, nil
	}
	return result, nil
}

func batch7Lane9GAGetRealtime(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	propertyID := stringArg(input, "property_id")
	token, failure := batch7Lane9GoogleToken(ctx, rt, "google_analytics_tool", batch7Lane9GAReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + batch7Lane9StringOr(failure, "error", "")}, nil
		}
		return batch7Lane9GoogleAuthError(""), nil
	}
	if err := batch7Lane9GAValidate(propertyID, nil); err != nil {
		return err, nil
	}
	metrics := stringSliceArg(input, "metrics")
	if len(metrics) == 0 {
		metrics = []string{"activeUsers"}
	}
	metricEntries := make([]any, 0, len(metrics))
	for _, metric := range metrics {
		metricEntries = append(metricEntries, map[string]any{"name": metric})
	}
	body := map[string]any{
		"metrics":      metricEntries,
		"minuteRanges": []any{map[string]any{"startMinutesAgo": 29, "endMinutesAgo": 0}},
	}
	response, failure := batch7Lane9GACall(ctx, rt, token, batch7Lane9GAPropertyPath(rt, propertyID, "runRealtimeReport"), body)
	if failure != nil {
		return failure, nil
	}
	return batch7Lane9GAFormatRealtime(response), nil
}

// batch7Lane9GAWrapper runs one of the fixed report wrappers.
func batch7Lane9GAWrapper(ctx context.Context, rt *Runtime, input map[string]any, defaultLimit int, metrics, dimensions []string) (any, error) {
	propertyID := stringArg(input, "property_id")
	limit := intArg(input, "limit", defaultLimit)
	token, failure := batch7Lane9GoogleToken(ctx, rt, "google_analytics_tool", batch7Lane9GAReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + batch7Lane9StringOr(failure, "error", "")}, nil
		}
		return batch7Lane9GoogleAuthError(""), nil
	}
	if err := batch7Lane9GAValidate(propertyID, &limit); err != nil {
		return err, nil
	}
	result, failure := batch7Lane9GARunReport(ctx, rt, token, propertyID, metrics, dimensions,
		stringArg(input, "start_date"), stringArg(input, "end_date"), limit)
	if failure != nil {
		return failure, nil
	}
	return result, nil
}

func batch7Lane9GATopPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GAWrapper(ctx, rt, input, 10,
		[]string{"screenPageViews", "averageSessionDuration", "bounceRate"},
		[]string{"pagePath", "pageTitle"})
}

func batch7Lane9GATrafficSources(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GAWrapper(ctx, rt, input, 10,
		[]string{"sessions", "totalUsers", "conversions"},
		[]string{"sessionSource", "sessionMedium"})
}

func batch7Lane9GADemographics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GAWrapper(ctx, rt, input, 20,
		[]string{"totalUsers", "sessions", "engagedSessions"},
		[]string{"country", "language", "deviceCategory"})
}

func batch7Lane9GAConversionEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GAWrapper(ctx, rt, input, 20,
		[]string{"eventCount", "conversions", "totalRevenue"},
		[]string{"eventName"})
}

func batch7Lane9GALandingPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GAWrapper(ctx, rt, input, 10,
		[]string{"sessions", "bounceRate", "conversions", "averageSessionDuration"},
		[]string{"landingPagePlusQueryString"})
}

// ------------------------------------------------------- google search console ---

var batch7Lane9SearchConsoleTool = &Tool{
	ID:          "google_search_console_tool",
	Name:        "google_search_console",
	DisplayName: "Google Search Console",
	Summary:     "Search analytics, sitemaps and URL inspection for Google Search Console (8 actions).",
	Actions: []Action{
		{
			Name:     "gsc_search_analytics",
			Summary:  "Query search analytics data from Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"dimensions":  paramDefault("string", "query"),
				"row_limit":   paramDefault("integer", 100),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:    "gsc_list_sites",
			Summary: "List all sites in the Google Search Console account.",
			Source:  "google_search_console_tool/google_search_console_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "gsc_list_sitemaps",
			Summary:  "List sitemaps for a site in Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url"},
			Params: map[string]any{
				"site_url": param("string"),
			},
		},
		{
			Name:     "gsc_inspect_url",
			Summary:  "Inspect a URL's indexing status in Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "inspection_url"},
			Params: map[string]any{
				"site_url":       param("string"),
				"inspection_url": param("string"),
			},
		},
		{
			Name:     "gsc_submit_sitemap",
			Summary:  "Submit a sitemap to Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "sitemap_url"},
			Params: map[string]any{
				"site_url":    param("string"),
				"sitemap_url": param("string"),
			},
		},
		{
			Name:     "gsc_top_queries",
			Summary:  "Get the top search queries for a site sorted by clicks.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"row_limit":   paramDefault("integer", 25),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:     "gsc_top_pages",
			Summary:  "Get the top-performing pages for a site sorted by clicks.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "start_date", "end_date"},
			Params: map[string]any{
				"site_url":    param("string"),
				"start_date":  param("string"),
				"end_date":    param("string"),
				"row_limit":   paramDefault("integer", 25),
				"search_type": paramDefault("string", "web"),
			},
		},
		{
			Name:     "gsc_delete_sitemap",
			Summary:  "Delete a sitemap from Google Search Console.",
			Source:   "google_search_console_tool/google_search_console_tool.py",
			Required: []string{"site_url", "sitemap_url"},
			Params: map[string]any{
				"site_url":    param("string"),
				"sitemap_url": param("string"),
			},
		},
	},
}

func batch7Lane9SearchConsoleImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gsc_search_analytics": batch7Lane9GSCSearchAnalytics,
		"gsc_list_sites":       batch7Lane9GSCListSites,
		"gsc_list_sitemaps":    batch7Lane9GSCListSitemaps,
		"gsc_inspect_url":      batch7Lane9GSCInspectURL,
		"gsc_submit_sitemap":   batch7Lane9GSCSubmitSitemap,
		"gsc_top_queries":      batch7Lane9GSCTopQueries,
		"gsc_top_pages":        batch7Lane9GSCTopPages,
		"gsc_delete_sitemap":   batch7Lane9GSCDeleteSitemap,
	}
}

// batch7Lane9GSCAuthError mirrors upstream _auth_error.
func batch7Lane9GSCAuthError() map[string]any {
	return map[string]any{
		"error": "GOOGLE_SEARCH_CONSOLE_TOKEN not set",
		"help":  "Generate an OAuth2 access token with webmasters.readonly scope",
	}
}

// batch7Lane9GSCToken resolves the bearer token for Search Console, minting one
// from GOOGLE_APPLICATION_CREDENTIALS when no direct token is configured.
func batch7Lane9GSCToken(ctx context.Context, rt *Runtime) (string, map[string]any) {
	token, failure := batch7Lane9GoogleToken(ctx, rt, "google_search_console_tool", batch7Lane9GSCReadScope)
	if token != "" {
		return token, nil
	}
	if failure != nil {
		return "", failure
	}
	return "", batch7Lane9GSCAuthError()
}

// batch7Lane9GSCCall performs one Search Console request. The accepted success
// codes differ per verb (PUT/DELETE answer 200 or 204).
func batch7Lane9GSCCall(ctx context.Context, rt *Runtime, token, method, endpoint string, payload any) (map[string]any, map[string]any) {
	headers := batch7Lane9Bearer(token)
	resp, err := batch7Lane9DoJSON(ctx, rt, method, endpoint, headers, payload, 30*time.Second)
	if err != nil {
		if batch7Lane9IsTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request to Google Search Console timed out"}
		}
		return nil, map[string]any{"error": "Request failed: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Check your GOOGLE_SEARCH_CONSOLE_TOKEN."}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Forbidden: " + batch4TruncateText(string(resp.Body), 300)}
	}
	success := resp.Status == http.StatusOK || resp.Status == http.StatusCreated
	if method == http.MethodPut || method == http.MethodDelete {
		success = resp.Status == http.StatusOK || resp.Status == http.StatusNoContent
	}
	if !success {
		return nil, map[string]any{
			"error": "Google API error " + strconv.Itoa(resp.Status) + ": " + batch4TruncateText(string(resp.Body), 500),
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{}, nil
	}
	data, ok := batch7Lane9JSONObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Google API error: invalid JSON response"}
	}
	return data, nil
}

// batch7Lane9GSCAnalyticsBody builds the searchAnalytics/query payload.
func batch7Lane9GSCAnalyticsBody(input map[string]any, dimensions []any) map[string]any {
	rowLimit := batch7Lane9Clamp(intArg(input, "row_limit", 100), 1, 25000)
	searchType := stringArg(input, "search_type")
	if searchType == "" {
		searchType = "web"
	}
	return map[string]any{
		"startDate":  stringArg(input, "start_date"),
		"endDate":    stringArg(input, "end_date"),
		"dimensions": dimensions,
		"rowLimit":   rowLimit,
		"type":       searchType,
	}
}

// batch7Lane9GSCRows maps analytics rows. A non-empty keyName flattens the
// first key into that field (the top-query/top-page wrappers), otherwise the
// full keys array is kept.
func batch7Lane9GSCRows(rows []any, keyName string) []any {
	out := make([]any, 0, len(rows))
	for _, raw := range rows {
		row := batch4AsMap(raw)
		keys := batch4AsSlice(row["keys"])
		entry := map[string]any{
			"clicks":      batch7Lane9IntOr(row, "clicks", 0),
			"impressions": batch7Lane9IntOr(row, "impressions", 0),
			"ctr":         batch7Lane9Round(batch7Lane9FloatOr(row, "ctr", 0), 4),
			"position":    batch7Lane9Round(batch7Lane9FloatOr(row, "position", 0), 1),
		}
		if keyName != "" {
			first := ""
			if len(keys) > 0 {
				first = batch4AsString(keys[0])
			}
			entry[keyName] = first
		} else {
			entry["keys"] = keys
		}
		out = append(out, entry)
	}
	return out
}

// batch7Lane9GSCSortByClicks sorts rows by clicks descending (stable).
func batch7Lane9GSCSortByClicks(rows []any) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftRow := batch4AsMap(rows[left])
		rightRow := batch4AsMap(rows[right])
		return batch7Lane9IntOr(leftRow, "clicks", 0) > batch7Lane9IntOr(rightRow, "clicks", 0)
	})
}

// batch7Lane9GSCSitePath escapes one site URL into a path segment.
func batch7Lane9GSCSitePath(siteURL string) string {
	return "sites/" + batch7Lane9StrictEscape(siteURL)
}

func batch7Lane9GSCSearchAnalytics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	startDate, endDate := stringArg(input, "start_date"), stringArg(input, "end_date")
	if siteURL == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "site_url, start_date, and end_date are required"}, nil
	}
	dimensions := stringSliceArg(input, "dimensions")
	if len(dimensions) == 0 {
		dimensions = []string{"query"}
	}
	dimensionList := make([]any, 0, len(dimensions))
	for _, dimension := range dimensions {
		dimensionList = append(dimensionList, dimension)
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	endpoint := base + "/" + batch7Lane9GSCSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodPost, endpoint, batch7Lane9GSCAnalyticsBody(input, dimensionList))
	if failure != nil {
		return failure, nil
	}
	rows := batch7Lane9GSCRows(batch4AsSlice(data["rows"]), "")
	return map[string]any{"site_url": siteURL, "rows": rows, "count": len(rows)}, nil
}

func batch7Lane9GSCListSites(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	data, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodGet, base+"/sites", nil)
	if failure != nil {
		return failure, nil
	}
	sites := make([]any, 0)
	for _, raw := range batch4AsSlice(data["siteEntry"]) {
		entry := batch4AsMap(raw)
		sites = append(sites, map[string]any{
			"site_url":         batch7Lane9StringOr(entry, "siteUrl", ""),
			"permission_level": batch7Lane9StringOr(entry, "permissionLevel", ""),
		})
	}
	return map[string]any{"sites": sites}, nil
}

func batch7Lane9GSCListSitemaps(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	if siteURL == "" {
		return map[string]any{"error": "site_url is required"}, nil
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	endpoint := base + "/" + batch7Lane9GSCSitePath(siteURL) + "/sitemaps"
	data, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodGet, endpoint, nil)
	if failure != nil {
		return failure, nil
	}
	sitemaps := make([]any, 0)
	for _, raw := range batch4AsSlice(data["sitemap"]) {
		entry := batch4AsMap(raw)
		sitemaps = append(sitemaps, map[string]any{
			"path":           batch7Lane9StringOr(entry, "path", ""),
			"last_submitted": batch7Lane9StringOr(entry, "lastSubmitted", ""),
			"is_pending":     batch7Lane9Bool(entry["isPending"]),
			"is_index":       batch7Lane9Bool(entry["isSitemapsIndex"]),
			"warnings":       batch7Lane9IntOr(entry, "warnings", 0),
			"errors":         batch7Lane9IntOr(entry, "errors", 0),
		})
	}
	return map[string]any{"site_url": siteURL, "sitemaps": sitemaps}, nil
}

func batch7Lane9GSCInspectURL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, inspectionURL := stringArg(input, "site_url"), stringArg(input, "inspection_url")
	if siteURL == "" || inspectionURL == "" {
		return map[string]any{"error": "site_url and inspection_url are required"}, nil
	}
	base := batch7Lane9Base(rt, "google_search_console_inspection_tool", batch7Lane9GSCInspectionBase)
	body := map[string]any{"inspectionUrl": inspectionURL, "siteUrl": siteURL}
	data, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodPost, base+"/urlInspection/index:inspect", body)
	if failure != nil {
		return failure, nil
	}
	result := batch4AsMap(data["inspectionResult"])
	indexStatus := batch4AsMap(result["indexStatusResult"])
	mobile := batch4AsMap(result["mobileUsabilityResult"])
	return map[string]any{
		"inspection_url":   inspectionURL,
		"verdict":          batch7Lane9StringOr(indexStatus, "verdict", ""),
		"coverage_state":   batch7Lane9StringOr(indexStatus, "coverageState", ""),
		"indexing_state":   batch7Lane9StringOr(indexStatus, "indexingState", ""),
		"last_crawl_time":  batch7Lane9StringOr(indexStatus, "lastCrawlTime", ""),
		"crawled_as":       batch7Lane9StringOr(indexStatus, "crawledAs", ""),
		"page_fetch_state": batch7Lane9StringOr(indexStatus, "pageFetchState", ""),
		"robots_txt_state": batch7Lane9StringOr(indexStatus, "robotsTxtState", ""),
		"mobile_verdict":   batch7Lane9StringOr(mobile, "verdict", ""),
	}, nil
}

func batch7Lane9GSCSubmitSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	endpoint := base + "/" + batch7Lane9GSCSitePath(siteURL) + "/sitemaps/" + batch7Lane9StrictEscape(sitemapURL)
	if _, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodPut, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "submitted"}, nil
}

// batch7Lane9GSCTopDimension runs the shared top-query/top-page wrapper.
func batch7Lane9GSCTopDimension(ctx context.Context, rt *Runtime, input map[string]any, dimension, key string, defaultLimit int) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	startDate, endDate := stringArg(input, "start_date"), stringArg(input, "end_date")
	if siteURL == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "site_url, start_date, and end_date are required"}, nil
	}
	if _, present := input["row_limit"]; !present {
		input = batch7Lane9CloneWith(input, "row_limit", defaultLimit)
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	endpoint := base + "/" + batch7Lane9GSCSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodPost, endpoint,
		batch7Lane9GSCAnalyticsBody(input, []any{dimension}))
	if failure != nil {
		return failure, nil
	}
	rows := batch7Lane9GSCRows(batch4AsSlice(data["rows"]), dimension)
	batch7Lane9GSCSortByClicks(rows)
	return map[string]any{"site_url": siteURL, key: rows, "count": len(rows)}, nil
}

// batch7Lane9CloneWith returns a shallow copy of input with one extra key.
func batch7Lane9CloneWith(input map[string]any, name string, value any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, existing := range input {
		out[key] = existing
	}
	out[name] = value
	return out
}

func batch7Lane9GSCTopQueries(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GSCTopDimension(ctx, rt, input, "query", "queries", 25)
}

func batch7Lane9GSCTopPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane9GSCTopDimension(ctx, rt, input, "page", "pages", 25)
}

func batch7Lane9GSCDeleteSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := batch7Lane9GSCToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := batch7Lane9Base(rt, "google_search_console_tool", batch7Lane9GSCBase)
	endpoint := base + "/" + batch7Lane9GSCSitePath(siteURL) + "/sitemaps/" + batch7Lane9StrictEscape(sitemapURL)
	if _, failure := batch7Lane9GSCCall(ctx, rt, token, http.MethodDelete, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "deleted"}, nil
}
