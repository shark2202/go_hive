package cloudflare

// Analytics rows: traffic, top-N, security, cache, performance and HTTP
// report actions (all backed by the dashboard endpoint).

import "net/http"

var analyticsRows = []actionSpec{
	{
		Name:     "cloudflare_get_zone_analytics",
		Summary:  "Get traffic analytics for a zone (requests, bandwidth, threats).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeZoneAnalytics,
	},
	{
		Name:     "cloudflare_get_top_analytics",
		Summary:  "Get top paths, countries and status codes for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeTopAnalytics,
	},
	{
		Name:     "cloudflare_get_security_analytics",
		Summary:  "Get summarized security analytics (WAF, Bot, etc.).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/security/analytics",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "security_analytics",
	},
	{
		Name:     "cloudflare_get_cache_analytics",
		Summary:  "Get cache performance analytics (hit ratio, bandwidth saved).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/cache/analytics/dashboard",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "cache_analytics",
	},
	{
		Name:     "cloudflare_get_performance_analytics",
		Summary:  "Get performance metrics (TTFB, loading times) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapePerformance,
	},
	{
		Name:     "cloudflare_get_http_analytics_report",
		Summary:  "Get HTTP traffic reports (status codes, content types, threats).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeHTTPReport,
	},
}
