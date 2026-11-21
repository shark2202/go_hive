// Google Analytics 4 tool: the google_analytics_tool catalog entry, the
// runReport/realtime helpers and the seven GA4 actions (split from
// google_analytics.go; the shared OAuth/service-account helpers stay there).

package integrations

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
