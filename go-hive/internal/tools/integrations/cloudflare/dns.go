package cloudflare

// DNS face: record shaping, CRUD actions and the domain health check.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// recordSummary flattens a DNS record for list responses.
func recordSummary(source map[string]any) map[string]any {
	return map[string]any{
		"id":       source["id"],
		"type":     textAt(source, "type"),
		"name":     textAt(source, "name"),
		"content":  source["content"],
		"ttl":      source["ttl"],
		"proxied":  source["proxied"],
		"priority": source["priority"],
	}
}

// recordDetail flattens a DNS record for the get action.
func recordDetail(source map[string]any) map[string]any {
	detail := recordSummary(source)
	detail["created_on"] = textAt(source, "created_on")
	detail["modified_on"] = textAt(source, "modified_on")
	return detail
}

// shapeRecordDetail applies recordDetail to a reply.
func shapeRecordDetail(reply apiReply, _ map[string]any) any {
	return recordDetail(asMap(reply.result))
}

// listDNSRecords mirrors cloudflare_list_dns_records.
func listDNSRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	page := clampInt(intArg(input, "page", 1), 1, 1<<31-1)
	perPage := clampInt(intArg(input, "per_page", 20), 1, maxPageSize)
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	if name := strings.TrimSpace(stringArg(input, "name")); name != "" {
		query.Set("name", name)
	}
	if recordType := strings.TrimSpace(stringArg(input, "type")); recordType != "" {
		query.Set("type", strings.ToUpper(recordType))
	}
	reply := doRequest(ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	records := make([]map[string]any, 0)
	if items := sliceValue(reply.result); items != nil {
		for _, raw := range items {
			records = append(records, recordSummary(asMap(raw)))
		}
	} else if object := asMap(reply.result); object != nil {
		records = append(records, recordSummary(object))
	}
	total := len(records)
	if info := asMap(reply.envelope["result_info"]); info != nil {
		total = intAt(info, "total_count", total)
	}
	return map[string]any{
		"records": records, "zone_id": zoneID, "page": page, "per_page": perPage, "total": total,
	}, nil
}

// createDNSRecord mirrors cloudflare_create_dns_record.
func createDNSRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	payload := map[string]any{
		"type":    strings.ToUpper(stringArg(input, "type")),
		"name":    stringArg(input, "name"),
		"content": stringArg(input, "content"),
		"ttl":     intArg(input, "ttl", 1),
		"proxied": boolArg(input, "proxied", false),
	}
	if priority, present := optionalInt(input, "priority"); present {
		payload["priority"] = priority
	}
	reply := doRequest(
		ctx, rt, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, payload,
	)
	if !reply.ok() {
		return map[string]any{"created_record": reply.failure}, nil
	}
	return map[string]any{"created_record": reply.result}, nil
}

// updateDNSRecord mirrors cloudflare_update_dns_record, including
// the upstream "only non-empty fields are sent" rule.
func updateDNSRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	recordID := stringArg(input, "record_id")
	payload := map[string]any{}
	if value := stringArg(input, "type"); value != "" {
		payload["type"] = strings.ToUpper(value)
	}
	if value := stringArg(input, "name"); value != "" {
		payload["name"] = value
	}
	if value := stringArg(input, "content"); value != "" {
		payload["content"] = value
	}
	if ttl := intArg(input, "ttl", 0); ttl != 0 {
		payload["ttl"] = ttl
	}
	if value, present := input["proxied"]; present && value != nil {
		payload["proxied"] = boolArg(input, "proxied", false)
	}
	reply := doRequest(
		ctx, rt, http.MethodPatch,
		"/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, payload,
	)
	if !reply.ok() {
		return map[string]any{"updated_record": reply.failure}, nil
	}
	return map[string]any{"updated_record": reply.result}, nil
}

// domainHealth mirrors cloudflare_check_domain_dns_health.
func domainHealth(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	rawDomain := stringArg(input, "domain")
	if failure := validateDomain(rawDomain); failure != nil {
		return failure, nil
	}
	domain := strings.ToLower(strings.TrimSpace(rawDomain))
	zoneMissing := []any{map[string]any{
		"code": "ZONE_NOT_FOUND", "message": "No zone found for domain " + domain,
	}}
	zonesReply := doRequest(ctx, rt, http.MethodGet, "/zones", url.Values{
		"name":     {domain},
		"per_page": {"1"},
	}, nil)
	if !zonesReply.ok() {
		return map[string]any{
			"domain": domain, "zone_found": false,
			"error":  payloadError(zonesReply.failure),
			"issues": zoneMissing,
		}, nil
	}
	var zone map[string]any
	if zones := sliceValue(zonesReply.result); len(zones) > 0 {
		zone = asMap(zones[0])
	}
	if zone == nil || textAt(zone, "name") != domain {
		return map[string]any{
			"domain": domain, "zone_found": false,
			"issues":  zoneMissing,
			"summary": "Zone not found for " + domain + ".",
		}, nil
	}
	zoneID := textAt(zone, "id")
	zoneStatus := textAt(zone, "status")
	recordsReply := doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records",
		url.Values{"per_page": {"100"}}, nil,
	)
	if !recordsReply.ok() {
		return map[string]any{
			"domain": domain, "zone_found": true, "zone_status": zoneStatus,
			"error": payloadError(recordsReply.failure),
			"issues": []any{map[string]any{
				"code": "FETCH_ERROR", "message": "Failed to fetch DNS records",
			}},
		}, nil
	}
	records := sliceValue(recordsReply.result)
	if records == nil {
		records = []any{}
	}
	rootRecords := make([]any, 0)
	wwwRecords := make([]any, 0)
	cnameRecords := make([]any, 0)
	mxRecords := make([]any, 0)
	nsRecords := make([]any, 0)
	for _, raw := range records {
		record := asMap(raw)
		name := textAt(record, "name")
		recordType := textAt(record, "type")
		if name == domain && (recordType == "A" || recordType == "AAAA") {
			rootRecords = append(rootRecords, raw)
		}
		if name == "www."+domain && (recordType == "A" || recordType == "AAAA") {
			wwwRecords = append(wwwRecords, raw)
		}
		switch recordType {
		case "CNAME":
			cnameRecords = append(cnameRecords, raw)
		case "MX":
			mxRecords = append(mxRecords, raw)
		case "NS":
			nsRecords = append(nsRecords, raw)
		}
	}
	issues := make([]any, 0)
	if zoneStatus != "active" {
		issues = append(issues, map[string]any{
			"code":    "ZONE_INACTIVE",
			"message": "Zone status is " + zoneStatus + ", should be 'active'",
		})
	}
	if len(rootRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "ROOT_MISSING",
			"message": "No A/AAAA records found for root domain " + domain,
		})
	}
	if len(wwwRecords) == 0 && len(cnameRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "WWW_MISSING",
			"message": "No www DNS record (A/AAAA or CNAME) found for " + domain,
		})
	}
	if len(mxRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "MX_MISSING",
			"message": "No MX records configured for " + domain,
		})
	}
	for _, raw := range append(append([]any{}, rootRecords...), wwwRecords...) {
		record := asMap(raw)
		if proxied, _ := record["proxied"].(bool); !proxied {
			continue
		}
		if textAt(record, "content") == "" {
			issues = append(issues, map[string]any{
				"code":    "PROXY_INVALID",
				"message": "Proxied record " + textAt(record, "name") + " has no target",
			})
		}
	}
	summary := fmt.Sprintf("Zone is %s. Found %d DNS records.", zoneStatus, len(records))
	if len(issues) > 0 {
		codes := make([]string, 0, len(issues))
		for _, raw := range issues {
			codes = append(codes, textAt(asMap(raw), "code"))
		}
		summary += " Issues detected: " + strings.Join(codes, ", ") + "."
	} else {
		summary += " No obvious DNS configuration issues detected."
	}
	return map[string]any{
		"domain":        domain,
		"zone_found":    true,
		"zone_id":       zoneID,
		"zone_status":   zoneStatus,
		"root_records":  rootRecords,
		"www_records":   wwwRecords,
		"mx_records":    mxRecords,
		"ns_records":    nsRecords,
		"total_records": len(records),
		"issues":        issues,
		"summary":       summary,
	}, nil
}
