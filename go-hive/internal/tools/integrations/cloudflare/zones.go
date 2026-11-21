package cloudflare

// Zones face: zone list/detail shaping, zone-scoped settings lists and the
// SSL certificate pair.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// zoneSummary flattens a zone for the list action.
func zoneSummary(source map[string]any) map[string]any {
	return map[string]any{
		"id":           source["id"],
		"name":         textAt(source, "name"),
		"status":       textAt(source, "status"),
		"name_servers": textList(sliceAt(source, "name_servers")),
		"plan":         planName(mapAt(source, "plan")),
	}
}

// zoneDetail flattens a zone for the get action.
func zoneDetail(source map[string]any) map[string]any {
	return map[string]any{
		"id":           source["id"],
		"name":         textAt(source, "name"),
		"status":       textAt(source, "status"),
		"name_servers": textList(sliceAt(source, "name_servers")),
		"created_on":   textAt(source, "created_on"),
		"modified_on":  textAt(source, "modified_on"),
		"plan":         planName(mapAt(source, "plan")),
		"type":         textAt(source, "type"),
	}
}

// planName reads plan.name, preserving the upstream None.
func planName(plan map[string]any) any {
	if plan == nil {
		return nil
	}
	return plan["name"]
}

// textList renders a decoded array of strings as []string.
func textList(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(item))
	}
	return out
}

// shapeZoneDetail applies zoneDetail to a reply.
func shapeZoneDetail(reply apiReply, _ map[string]any) any {
	return zoneDetail(asMap(reply.result))
}

// shapeListOrEmpty mirrors the upstream
// `result if isinstance(result, list) else []` wrapper for list endpoints.
func shapeListOrEmpty(key string) func(apiReply, map[string]any) any {
	return func(reply apiReply, _ map[string]any) any {
		items := sliceValue(reply.result)
		if items == nil {
			items = []any{}
		}
		return map[string]any{key: items}
	}
}

// listZones mirrors cloudflare_list_zones (full envelope).
func listZones(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	page := clampInt(intArg(input, "page", 1), 1, 1<<31-1)
	perPage := clampInt(intArg(input, "per_page", 20), 1, maxPageSize)
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	if name := strings.TrimSpace(stringArg(input, "name")); name != "" {
		query.Set("name", name)
	}
	reply := doRequest(ctx, rt, http.MethodGet, "/zones", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	zones := make([]map[string]any, 0)
	for _, raw := range resultArray(reply.result) {
		zones = append(zones, zoneSummary(asMap(raw)))
	}
	total := len(zones)
	if info := asMap(reply.envelope["result_info"]); info != nil {
		total = intAt(info, "total_count", total)
	}
	return map[string]any{
		"zones": zones, "page": page, "per_page": perPage, "total": total,
	}, nil
}

// listZoneCertificates mirrors cloudflare_list_zone_certificates:
// universal SSL settings plus custom certificates, each surfaced even when one
// of the two calls fails.
func listZoneCertificates(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	base := "/zones/" + url.PathEscape(zoneID)
	universal := doRequest(ctx, rt, http.MethodGet, base+"/ssl/universal/settings", nil, nil)
	custom := doRequest(ctx, rt, http.MethodGet, base+"/custom_certificates", nil, nil)
	certificates := sliceValue(custom.value())
	if certificates == nil {
		certificates = []any{}
	}
	return map[string]any{
		"universal_ssl":       universal.value(),
		"custom_certificates": certificates,
	}, nil
}
