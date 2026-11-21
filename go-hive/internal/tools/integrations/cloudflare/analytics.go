package cloudflare

// Analytics face: dashboard payload normalisation and the traffic, top-N,
// performance and HTTP report shapes.

// dashboard normalises the analytics dashboard payload, which the
// upstream code accepts as either an object or a one-element array.
func dashboard(value any) map[string]any {
	if items := sliceValue(value); items != nil {
		if len(items) == 0 {
			return map[string]any{}
		}
		return asMap(items[0])
	}
	return asMap(value)
}

// allValue reads the "all" bucket of a dashboard metric.
func allValue(source map[string]any, key string) any {
	metric := mapAt(source, key)
	value, present := metric["all"]
	if !present {
		return nil
	}
	return value
}

// shapeZoneAnalytics mirrors cloudflare_get_zone_analytics.
func shapeZoneAnalytics(reply apiReply, _ map[string]any) any {
	dashboard := dashboard(reply.result)
	totals := mapAt(dashboard, "totals")
	query := mapAt(dashboard, "query")
	return map[string]any{
		"requests":        allValue(totals, "requests"),
		"bandwidth_bytes": allValue(totals, "bandwidth"),
		"threats":         allValue(totals, "threats"),
		"pageviews":       allValue(totals, "pageviews"),
		"uniques":         allValue(totals, "uniques"),
		"since":           query["since"],
		"until":           query["until"],
	}
}

// shapeTopAnalytics mirrors cloudflare_get_top_analytics.
func shapeTopAnalytics(reply apiReply, _ map[string]any) any {
	requests := mapAt(mapAt(dashboard(reply.result), "totals"), "requests")
	return map[string]any{
		"top_countries":     mapAt(requests, "country"),
		"top_http_status":   mapAt(requests, "http_status"),
		"top_content_types": mapAt(requests, "content_type"),
	}
}

// shapePerformance mirrors cloudflare_get_performance_analytics.
func shapePerformance(reply apiReply, _ map[string]any) any {
	totals := mapAt(dashboard(reply.result), "totals")
	return map[string]any{"performance_metrics": mapAt(totals, "performance")}
}

// shapeHTTPReport mirrors cloudflare_get_http_analytics_report.
func shapeHTTPReport(reply apiReply, _ map[string]any) any {
	requests := mapAt(mapAt(dashboard(reply.result), "totals"), "requests")
	return map[string]any{
		"status_codes":  mapAt(requests, "http_status"),
		"content_types": mapAt(requests, "content_type"),
		"threats":       mapAt(requests, "threats"),
	}
}
