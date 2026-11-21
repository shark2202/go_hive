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

// googleAuthError renders the GA "not configured" map.
func googleAuthError(detail string) map[string]any {
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

// googleToken resolves a bearer token for one Google API: the direct
// token first, then a service-account JWT exchange with the given scope.
func googleToken(ctx context.Context, rt *Runtime, moduleID, scope string) (string, map[string]any) {
	if token := moduleToken(rt, moduleID); token != "" {
		return token, nil
	}
	credentialsPath := strings.TrimSpace(runtimeEnv(rt, "GOOGLE_APPLICATION_CREDENTIALS"))
	if credentialsPath == "" {
		return "", nil
	}
	token, _, failure := googleServiceAccountToken(ctx, rt, credentialsPath, scope)
	if failure != nil {
		return "", failure
	}
	return token, nil
}

// googleServiceAccountToken exchanges a service-account JSON key
// for an OAuth access token using the JWT bearer grant.
func googleServiceAccountToken(ctx context.Context, rt *Runtime, path, scope string) (string, string, map[string]any) {
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
		tokenURI = marketingGoogleTokenURI
	}
	assertion, err := googleJWT(key.ClientEmail, key.PrivateKey, tokenURI, scope, runtimeNow(rt).UTC())
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	form := url.Values{
		"grant_type": []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  []string{assertion},
	}
	resp, err := marketingDoForm(ctx, rt, tokenURI, nil, form, 30*time.Second)
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	if resp.Status >= 400 {
		return "", "", map[string]any{
			"error": "Google authentication failed: token endpoint returned HTTP " + strconv.Itoa(resp.Status),
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned invalid JSON"}
	}
	accessToken := stringOr(data, "access_token", "")
	if accessToken == "" {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned no access_token"}
	}
	return accessToken, key.ProjectID, nil
}

// googleJWT builds an RS256-signed JWT for the service-account
// OAuth flow with an explicit scope.
func googleJWT(email, privateKeyPEM, audience, scope string, now time.Time) (string, error) {
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

var googleAnalyticsTool = &Tool{
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
				"metrics":     marketingArrayParam(),
				"dimensions":  marketingArrayParam(),
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
				"metrics":     marketingArrayParam(),
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

func googleAnalyticsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"ga_run_report":            gaRunReportAction,
		"ga_get_realtime":          gaGetRealtime,
		"ga_get_top_pages":         gaTopPages,
		"ga_get_traffic_sources":   gaTrafficSources,
		"ga_get_user_demographics": gaDemographics,
		"ga_get_conversion_events": gaConversionEvents,
		"ga_get_landing_pages":     gaLandingPages,
	}
}

// gaValidate mirrors upstream _validate_inputs.
func gaValidate(propertyID string, limit *int) map[string]any {
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

// gaCall performs one Data API request and decodes the object.
func gaCall(ctx context.Context, rt *Runtime, token, endpoint string, payload any) (map[string]any, map[string]any) {
	headers := marketingBearer(token)
	resp, err := marketingDoJSON(ctx, rt, http.MethodPost, endpoint, headers, payload, 60*time.Second)
	if err != nil {
		return nil, transportError(ctx, "Google Analytics", err)
	}
	if resp.Status >= 400 {
		message := truncateText(string(resp.Body), 500)
		if data, ok := jsonObject(resp.Body); ok {
			if inner := asMap(data["error"]); inner != nil {
				if text := stringOr(inner, "message", ""); text != "" {
					message = text
				}
			}
		}
		return nil, map[string]any{
			"error": "Google Analytics API error: HTTP " + strconv.Itoa(resp.Status) + ": " + message,
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Google Analytics API error: invalid JSON response"}
	}
	return data, nil
}

// gaHeaders reads the header names of a GA response section.
func gaHeaders(value any) []string {
	headers := make([]string, 0)
	for _, raw := range asSlice(value) {
		headers = append(headers, stringOr(asMap(raw), "name", ""))
	}
	return headers
}

// gaFormatReport mirrors _format_report_response.
func gaFormatReport(response map[string]any) map[string]any {
	dimensionHeaders := gaHeaders(response["dimensionHeaders"])
	metricHeaders := gaHeaders(response["metricHeaders"])
	rows := make([]any, 0)
	for _, raw := range asSlice(response["rows"]) {
		row := asMap(raw)
		rowData := map[string]any{}
		for index, value := range asSlice(row["dimensionValues"]) {
			if index < len(dimensionHeaders) {
				rowData[dimensionHeaders[index]] = stringOr(asMap(value), "value", "")
			}
		}
		for index, value := range asSlice(row["metricValues"]) {
			if index < len(metricHeaders) {
				rowData[metricHeaders[index]] = stringOr(asMap(value), "value", "")
			}
		}
		rows = append(rows, rowData)
	}
	return map[string]any{
		"row_count":         marketingIntOr(response, "rowCount", 0),
		"rows":              rows,
		"dimension_headers": dimensionHeaders,
		"metric_headers":    metricHeaders,
	}
}

// gaFormatRealtime mirrors _format_realtime_response.
func gaFormatRealtime(response map[string]any) map[string]any {
	metricHeaders := gaHeaders(response["metricHeaders"])
	rows := make([]any, 0)
	for _, raw := range asSlice(response["rows"]) {
		row := asMap(raw)
		rowData := map[string]any{}
		for index, value := range asSlice(row["metricValues"]) {
			if index < len(metricHeaders) {
				rowData[metricHeaders[index]] = stringOr(asMap(value), "value", "")
			}
		}
		rows = append(rows, rowData)
	}
	return map[string]any{
		"row_count":      marketingIntOr(response, "rowCount", 0),
		"rows":           rows,
		"metric_headers": metricHeaders,
	}
}

// gaPropertyPath renders the {property}:method path segment.
func gaPropertyPath(rt *Runtime, propertyID, method string) string {
	base := marketingBase(rt, "google_analytics_tool", gaBase)
	return base + "/" + propertyID + ":" + method
}

// gaRunReport performs the shared report request.
func gaRunReport(ctx context.Context, rt *Runtime, token, propertyID string, metrics, dimensions []string, startDate, endDate string, limit int) (any, map[string]any) {
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
	response, failure := gaCall(ctx, rt, token, gaPropertyPath(rt, propertyID, "runReport"), body)
	if failure != nil {
		return nil, failure
	}
	return gaFormatReport(response), nil
}

func gaRunReportAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	propertyID := stringArg(input, "property_id")
	metrics := stringSliceArg(input, "metrics")
	dimensions := stringSliceArg(input, "dimensions")
	limit := intArg(input, "limit", 100)

	token, failure := googleToken(ctx, rt, "google_analytics_tool", gaReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + stringOr(failure, "error", "")}, nil
		}
		return googleAuthError(""), nil
	}
	if err := gaValidate(propertyID, &limit); err != nil {
		return err, nil
	}
	if len(metrics) == 0 {
		return map[string]any{"error": "metrics list must not be empty"}, nil
	}
	result, failure := gaRunReport(ctx, rt, token, propertyID, metrics, dimensions,
		stringArg(input, "start_date"), stringArg(input, "end_date"), limit)
	if failure != nil {
		return failure, nil
	}
	return result, nil
}

func gaGetRealtime(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	propertyID := stringArg(input, "property_id")
	token, failure := googleToken(ctx, rt, "google_analytics_tool", gaReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + stringOr(failure, "error", "")}, nil
		}
		return googleAuthError(""), nil
	}
	if err := gaValidate(propertyID, nil); err != nil {
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
	response, failure := gaCall(ctx, rt, token, gaPropertyPath(rt, propertyID, "runRealtimeReport"), body)
	if failure != nil {
		return failure, nil
	}
	return gaFormatRealtime(response), nil
}

// gaWrapper runs one of the fixed report wrappers.
func gaWrapper(ctx context.Context, rt *Runtime, input map[string]any, defaultLimit int, metrics, dimensions []string) (any, error) {
	propertyID := stringArg(input, "property_id")
	limit := intArg(input, "limit", defaultLimit)
	token, failure := googleToken(ctx, rt, "google_analytics_tool", gaReadScope)
	if token == "" {
		if failure != nil {
			return map[string]any{"error": "Failed to initialize Google Analytics client: " + stringOr(failure, "error", "")}, nil
		}
		return googleAuthError(""), nil
	}
	if err := gaValidate(propertyID, &limit); err != nil {
		return err, nil
	}
	result, failure := gaRunReport(ctx, rt, token, propertyID, metrics, dimensions,
		stringArg(input, "start_date"), stringArg(input, "end_date"), limit)
	if failure != nil {
		return failure, nil
	}
	return result, nil
}

func gaTopPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gaWrapper(ctx, rt, input, 10,
		[]string{"screenPageViews", "averageSessionDuration", "bounceRate"},
		[]string{"pagePath", "pageTitle"})
}

func gaTrafficSources(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gaWrapper(ctx, rt, input, 10,
		[]string{"sessions", "totalUsers", "conversions"},
		[]string{"sessionSource", "sessionMedium"})
}

func gaDemographics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gaWrapper(ctx, rt, input, 20,
		[]string{"totalUsers", "sessions", "engagedSessions"},
		[]string{"country", "language", "deviceCategory"})
}

func gaConversionEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gaWrapper(ctx, rt, input, 20,
		[]string{"eventCount", "conversions", "totalRevenue"},
		[]string{"eventName"})
}

func gaLandingPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gaWrapper(ctx, rt, input, 10,
		[]string{"sessions", "bounceRate", "conversions", "averageSessionDuration"},
		[]string{"landingPagePlusQueryString"})
}

// ------------------------------------------------------- google search console ---

var searchConsoleTool = &Tool{
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

func searchConsoleImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gsc_search_analytics": gscSearchAnalytics,
		"gsc_list_sites":       gscListSites,
		"gsc_list_sitemaps":    gscListSitemaps,
		"gsc_inspect_url":      gscInspectURL,
		"gsc_submit_sitemap":   gscSubmitSitemap,
		"gsc_top_queries":      gscTopQueries,
		"gsc_top_pages":        gscTopPages,
		"gsc_delete_sitemap":   gscDeleteSitemap,
	}
}

// gscAuthError mirrors upstream _auth_error.
func gscAuthError() map[string]any {
	return map[string]any{
		"error": "GOOGLE_SEARCH_CONSOLE_TOKEN not set",
		"help":  "Generate an OAuth2 access token with webmasters.readonly scope",
	}
}

// gscToken resolves the bearer token for Search Console, minting one
// from GOOGLE_APPLICATION_CREDENTIALS when no direct token is configured.
func gscToken(ctx context.Context, rt *Runtime) (string, map[string]any) {
	token, failure := googleToken(ctx, rt, "google_search_console_tool", gscReadScope)
	if token != "" {
		return token, nil
	}
	if failure != nil {
		return "", failure
	}
	return "", gscAuthError()
}

// gscCall performs one Search Console request. The accepted success
// codes differ per verb (PUT/DELETE answer 200 or 204).
func gscCall(ctx context.Context, rt *Runtime, token, method, endpoint string, payload any) (map[string]any, map[string]any) {
	headers := marketingBearer(token)
	resp, err := marketingDoJSON(ctx, rt, method, endpoint, headers, payload, 30*time.Second)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request to Google Search Console timed out"}
		}
		return nil, map[string]any{"error": "Request failed: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Check your GOOGLE_SEARCH_CONSOLE_TOKEN."}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Forbidden: " + truncateText(string(resp.Body), 300)}
	}
	success := resp.Status == http.StatusOK || resp.Status == http.StatusCreated
	if method == http.MethodPut || method == http.MethodDelete {
		success = resp.Status == http.StatusOK || resp.Status == http.StatusNoContent
	}
	if !success {
		return nil, map[string]any{
			"error": "Google API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{}, nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Google API error: invalid JSON response"}
	}
	return data, nil
}

// gscAnalyticsBody builds the searchAnalytics/query payload.
func gscAnalyticsBody(input map[string]any, dimensions []any) map[string]any {
	rowLimit := clampInt(intArg(input, "row_limit", 100), 1, 25000)
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

// gscRows maps analytics rows. A non-empty keyName flattens the
// first key into that field (the top-query/top-page wrappers), otherwise the
// full keys array is kept.
func gscRows(rows []any, keyName string) []any {
	out := make([]any, 0, len(rows))
	for _, raw := range rows {
		row := asMap(raw)
		keys := asSlice(row["keys"])
		entry := map[string]any{
			"clicks":      marketingIntOr(row, "clicks", 0),
			"impressions": marketingIntOr(row, "impressions", 0),
			"ctr":         marketingRound(marketingFloatOr(row, "ctr", 0), 4),
			"position":    marketingRound(marketingFloatOr(row, "position", 0), 1),
		}
		if keyName != "" {
			first := ""
			if len(keys) > 0 {
				first = asString(keys[0])
			}
			entry[keyName] = first
		} else {
			entry["keys"] = keys
		}
		out = append(out, entry)
	}
	return out
}

// gscSortByClicks sorts rows by clicks descending (stable).
func gscSortByClicks(rows []any) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftRow := asMap(rows[left])
		rightRow := asMap(rows[right])
		return marketingIntOr(leftRow, "clicks", 0) > marketingIntOr(rightRow, "clicks", 0)
	})
}

// gscSitePath escapes one site URL into a path segment.
func gscSitePath(siteURL string) string {
	return "sites/" + marketingStrictEscape(siteURL)
}

func gscSearchAnalytics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
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
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := gscCall(ctx, rt, token, http.MethodPost, endpoint, gscAnalyticsBody(input, dimensionList))
	if failure != nil {
		return failure, nil
	}
	rows := gscRows(asSlice(data["rows"]), "")
	return map[string]any{"site_url": siteURL, "rows": rows, "count": len(rows)}, nil
}

func gscListSites(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	data, failure := gscCall(ctx, rt, token, http.MethodGet, base+"/sites", nil)
	if failure != nil {
		return failure, nil
	}
	sites := make([]any, 0)
	for _, raw := range asSlice(data["siteEntry"]) {
		entry := asMap(raw)
		sites = append(sites, map[string]any{
			"site_url":         stringOr(entry, "siteUrl", ""),
			"permission_level": stringOr(entry, "permissionLevel", ""),
		})
	}
	return map[string]any{"sites": sites}, nil
}

func gscListSitemaps(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	if siteURL == "" {
		return map[string]any{"error": "site_url is required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps"
	data, failure := gscCall(ctx, rt, token, http.MethodGet, endpoint, nil)
	if failure != nil {
		return failure, nil
	}
	sitemaps := make([]any, 0)
	for _, raw := range asSlice(data["sitemap"]) {
		entry := asMap(raw)
		sitemaps = append(sitemaps, map[string]any{
			"path":           stringOr(entry, "path", ""),
			"last_submitted": stringOr(entry, "lastSubmitted", ""),
			"is_pending":     asBool(entry["isPending"]),
			"is_index":       asBool(entry["isSitemapsIndex"]),
			"warnings":       marketingIntOr(entry, "warnings", 0),
			"errors":         marketingIntOr(entry, "errors", 0),
		})
	}
	return map[string]any{"site_url": siteURL, "sitemaps": sitemaps}, nil
}

func gscInspectURL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, inspectionURL := stringArg(input, "site_url"), stringArg(input, "inspection_url")
	if siteURL == "" || inspectionURL == "" {
		return map[string]any{"error": "site_url and inspection_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_inspection_tool", gscInspectionBase)
	body := map[string]any{"inspectionUrl": inspectionURL, "siteUrl": siteURL}
	data, failure := gscCall(ctx, rt, token, http.MethodPost, base+"/urlInspection/index:inspect", body)
	if failure != nil {
		return failure, nil
	}
	result := asMap(data["inspectionResult"])
	indexStatus := asMap(result["indexStatusResult"])
	mobile := asMap(result["mobileUsabilityResult"])
	return map[string]any{
		"inspection_url":   inspectionURL,
		"verdict":          stringOr(indexStatus, "verdict", ""),
		"coverage_state":   stringOr(indexStatus, "coverageState", ""),
		"indexing_state":   stringOr(indexStatus, "indexingState", ""),
		"last_crawl_time":  stringOr(indexStatus, "lastCrawlTime", ""),
		"crawled_as":       stringOr(indexStatus, "crawledAs", ""),
		"page_fetch_state": stringOr(indexStatus, "pageFetchState", ""),
		"robots_txt_state": stringOr(indexStatus, "robotsTxtState", ""),
		"mobile_verdict":   stringOr(mobile, "verdict", ""),
	}, nil
}

func gscSubmitSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps/" + marketingStrictEscape(sitemapURL)
	if _, failure := gscCall(ctx, rt, token, http.MethodPut, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "submitted"}, nil
}

// gscTopDimension runs the shared top-query/top-page wrapper.
func gscTopDimension(ctx context.Context, rt *Runtime, input map[string]any, dimension, key string, defaultLimit int) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL := stringArg(input, "site_url")
	startDate, endDate := stringArg(input, "start_date"), stringArg(input, "end_date")
	if siteURL == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "site_url, start_date, and end_date are required"}, nil
	}
	if _, present := input["row_limit"]; !present {
		input = googleCloneWith(input, "row_limit", defaultLimit)
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/searchAnalytics/query"
	data, failure := gscCall(ctx, rt, token, http.MethodPost, endpoint,
		gscAnalyticsBody(input, []any{dimension}))
	if failure != nil {
		return failure, nil
	}
	rows := gscRows(asSlice(data["rows"]), dimension)
	gscSortByClicks(rows)
	return map[string]any{"site_url": siteURL, key: rows, "count": len(rows)}, nil
}

// googleCloneWith returns a shallow copy of input with one extra key.
func googleCloneWith(input map[string]any, name string, value any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, existing := range input {
		out[key] = existing
	}
	out[name] = value
	return out
}

func gscTopQueries(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gscTopDimension(ctx, rt, input, "query", "queries", 25)
}

func gscTopPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return gscTopDimension(ctx, rt, input, "page", "pages", 25)
}

func gscDeleteSitemap(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, authFailure := gscToken(ctx, rt)
	if token == "" {
		return authFailure, nil
	}
	siteURL, sitemapURL := stringArg(input, "site_url"), stringArg(input, "sitemap_url")
	if siteURL == "" || sitemapURL == "" {
		return map[string]any{"error": "site_url and sitemap_url are required"}, nil
	}
	base := marketingBase(rt, "google_search_console_tool", gscBase)
	endpoint := base + "/" + gscSitePath(siteURL) + "/sitemaps/" + marketingStrictEscape(sitemapURL)
	if _, failure := gscCall(ctx, rt, token, http.MethodDelete, endpoint, nil); failure != nil {
		return failure, nil
	}
	return map[string]any{"sitemap_url": sitemapURL, "status": "deleted"}, nil
}
