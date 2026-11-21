package infra

// PagerDuty: catalog, eight incident/service/on-call actions and the
// upstream error-mapping helpers.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var pagerDutyTool = &Tool{
	ID:          "pagerduty_tool",
	Name:        "pagerduty",
	DisplayName: "PagerDuty",
	Summary:     "PagerDuty incident management and service listing (8 actions).",
	Actions: []Action{
		{
			Name:    "pagerduty_list_incidents",
			Summary: "List PagerDuty incidents with optional filters.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"status":     paramDefault("string", ""),
				"since":      paramDefault("string", ""),
				"until":      paramDefault("string", ""),
				"service_id": paramDefault("string", ""),
				"urgency":    paramDefault("string", ""),
				"limit":      paramDefault("integer", 25),
			},
		},
		{
			Name:     "pagerduty_get_incident",
			Summary:  "Get details of a specific PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id"},
			Params: map[string]any{
				"incident_id": param("string"),
			},
		},
		{
			Name:     "pagerduty_create_incident",
			Summary:  "Create a new PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"title", "service_id"},
			Params: map[string]any{
				"title":      param("string"),
				"service_id": param("string"),
				"urgency":    paramDefault("string", "high"),
				"details":    paramDefault("string", ""),
			},
		},
		{
			Name:     "pagerduty_update_incident",
			Summary:  "Update a PagerDuty incident (acknowledge, resolve, etc.).",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id"},
			Params: map[string]any{
				"incident_id": param("string"),
				"status":      paramDefault("string", ""),
				"resolution":  paramDefault("string", ""),
			},
		},
		{
			Name:    "pagerduty_list_services",
			Summary: "List PagerDuty services.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"query": paramDefault("string", ""),
				"limit": paramDefault("integer", 25),
			},
		},
		{
			Name:    "pagerduty_list_oncalls",
			Summary: "List current on-call entries.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"schedule_id":          paramDefault("string", ""),
				"escalation_policy_id": paramDefault("string", ""),
				"since":                paramDefault("string", ""),
				"until":                paramDefault("string", ""),
				"limit":                paramDefault("integer", 25),
			},
		},
		{
			Name:     "pagerduty_add_incident_note",
			Summary:  "Add a note to a PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id", "content"},
			Params: map[string]any{
				"incident_id": param("string"),
				"content":     param("string"),
			},
		},
		{
			Name:    "pagerduty_list_escalation_policies",
			Summary: "List PagerDuty escalation policies.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"query": paramDefault("string", ""),
				"limit": paramDefault("integer", 25),
			},
		},
	},
}

func pagerDutyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pagerduty_list_incidents":           pagerDutyListIncidents,
		"pagerduty_get_incident":             pagerDutyGetIncident,
		"pagerduty_create_incident":          pagerDutyCreateIncident,
		"pagerduty_update_incident":          pagerDutyUpdateIncident,
		"pagerduty_list_services":            pagerDutyListServices,
		"pagerduty_list_oncalls":             pagerDutyListOncalls,
		"pagerduty_add_incident_note":        pagerDutyAddIncidentNote,
		"pagerduty_list_escalation_policies": pagerDutyListEscalationPolicies,
	}
}

// pagerDutyHeaders mirrors upstream _get_headers.
func pagerDutyHeaders(rt *Runtime, write bool) (map[string]string, map[string]any) {
	apiKey := moduleToken(rt, "pagerduty_tool")
	if apiKey == "" {
		return nil, map[string]any{
			"error": "PAGERDUTY_API_KEY is required",
			"help":  "Set PAGERDUTY_API_KEY environment variable",
		}
	}
	headers := map[string]string{
		"Authorization": "Token token=" + apiKey,
		"Accept":        "application/vnd.pagerduty+json;version=2",
		"Content-Type":  "application/json",
	}
	if write {
		if from := strings.TrimSpace(runtimeEnv(rt, "PAGERDUTY_FROM_EMAIL")); from != "" {
			headers["From"] = from
		}
	}
	return headers, nil
}

// pagerDutyIncident mirrors upstream _extract_incident.
func pagerDutyIncident(raw any) map[string]any {
	incident := asMap(raw)
	service := asMap(incident["service"])
	assignments := make([]any, 0)
	for _, rawAssignment := range listOrEmpty(incident["assignments"]) {
		assignment := asMap(rawAssignment)
		assignments = append(assignments, asMap(assignment["assignee"])["summary"])
	}
	return map[string]any{
		"id":              incident["id"],
		"incident_number": incident["incident_number"],
		"title":           incident["title"],
		"status":          incident["status"],
		"urgency":         incident["urgency"],
		"created_at":      incident["created_at"],
		"html_url":        incident["html_url"],
		"service":         service["summary"],
		"service_id":      service["id"],
		"assignments":     assignments,
	}
}

// pagerDutyBase resolves the fixed PagerDuty API base.
func pagerDutyBase(rt *Runtime) string {
	return endpoint(rt, "pagerduty_tool", "PAGERDUTY_BASE_URL", "https://api.pagerduty.com")
}

func pagerDutyListIncidents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 25), 100))}}
	if status := stringArg(input, "status"); status != "" {
		for _, part := range strings.Split(status, ",") {
			params.Add("statuses[]", strings.TrimSpace(part))
		}
	}
	for name, key := range map[string]string{"since": "since", "until": "until"} {
		if value := stringArg(input, name); value != "" {
			params.Set(key, value)
		}
	}
	if serviceID := stringArg(input, "service_id"); serviceID != "" {
		params.Set("service_ids[]", serviceID)
	}
	if urgency := stringArg(input, "urgency"); urgency != "" {
		params.Set("urgencies[]", urgency)
	}
	data, ok := getJSON(ctx, rt, "PagerDuty", addPrefix(pagerDutyBase(rt), "/incidents"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	incidents := listOrEmpty(data["incidents"])
	out := make([]any, 0, len(incidents))
	for _, raw := range incidents {
		out = append(out, pagerDutyIncident(raw))
	}
	return map[string]any{
		"count":     len(out),
		"more":      asBool(data["more"]),
		"incidents": out,
	}, nil
}

func pagerDutyGetIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	if incidentID == "" {
		return map[string]any{"error": "incident_id is required"}, nil
	}
	data, ok := getJSON(ctx, rt, "PagerDuty",
		addPrefix(pagerDutyBase(rt), "/incidents/"+pathEscape(incidentID)), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	incident := asMap(data["incident"])
	result := pagerDutyIncident(incident)
	if body := asMap(incident["body"]); len(body) > 0 {
		result["details"] = body["details"]
	}
	return result, nil
}

func pagerDutyCreateIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	title := stringArg(input, "title")
	serviceID := stringArg(input, "service_id")
	if title == "" || serviceID == "" {
		return map[string]any{"error": "title and service_id are required"}, nil
	}
	incident := map[string]any{
		"type":    "incident",
		"title":   title,
		"service": map[string]any{"id": serviceID, "type": "service_reference"},
		"urgency": stringArg(input, "urgency"),
	}
	if details := stringArg(input, "details"); details != "" {
		incident["body"] = map[string]any{"type": "incident_body", "details": details}
	}
	resp, err := doJSON(ctx, rt, http.MethodPost,
		addPrefix(pagerDutyBase(rt), "/incidents"), headers,
		map[string]any{"incident": incident}, defaultTimeout)
	if err != nil {
		return transportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	result := pagerDutyIncident(data["incident"])
	result["result"] = "created"
	return result, nil
}

func pagerDutyUpdateIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	if incidentID == "" {
		return map[string]any{"error": "incident_id is required"}, nil
	}
	status := stringArg(input, "status")
	if status == "" {
		return map[string]any{"error": "status is required (acknowledged or resolved)"}, nil
	}
	incident := map[string]any{"type": "incident_reference", "status": status}
	if resolution := stringArg(input, "resolution"); resolution != "" && status == "resolved" {
		incident["resolution"] = resolution
	}
	path := "/incidents/" + pathEscape(incidentID)
	resp, err := doJSON(ctx, rt, http.MethodPut,
		addPrefix(pagerDutyBase(rt), path), headers,
		map[string]any{"incident": incident}, defaultTimeout)
	if err != nil {
		return transportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	return pagerDutyIncident(data["incident"]), nil
}

func pagerDutyListServices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 25), 100))}}
	if query := stringArg(input, "query"); query != "" {
		params.Set("query", query)
	}
	data, ok := getJSON(ctx, rt, "PagerDuty", addPrefix(pagerDutyBase(rt), "/services"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	services := listOrEmpty(data["services"])
	out := make([]any, 0, len(services))
	for _, raw := range services {
		service := asMap(raw)
		out = append(out, map[string]any{
			"id":                      service["id"],
			"name":                    service["name"],
			"description":             service["description"],
			"status":                  service["status"],
			"html_url":                service["html_url"],
			"created_at":              service["created_at"],
			"last_incident_timestamp": service["last_incident_timestamp"],
		})
	}
	return map[string]any{"count": len(out), "services": out}, nil
}

func pagerDutyListOncalls(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 25), 100))}}
	if scheduleID := stringArg(input, "schedule_id"); scheduleID != "" {
		params.Set("schedule_ids[]", scheduleID)
	}
	if policyID := stringArg(input, "escalation_policy_id"); policyID != "" {
		params.Set("escalation_policy_ids[]", policyID)
	}
	if since := stringArg(input, "since"); since != "" {
		params.Set("since", since)
	}
	if until := stringArg(input, "until"); until != "" {
		params.Set("until", until)
	}
	data, ok := getJSON(ctx, rt, "PagerDuty", addPrefix(pagerDutyBase(rt), "/oncalls"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	oncalls := listOrEmpty(data["oncalls"])
	out := make([]any, 0, len(oncalls))
	for _, raw := range oncalls {
		oncall := asMap(raw)
		user := asMap(oncall["user"])
		schedule := asMap(oncall["schedule"])
		policy := asMap(oncall["escalation_policy"])
		out = append(out, map[string]any{
			"user_name":         stringOr(user, "summary", ""),
			"user_id":           stringOr(user, "id", ""),
			"schedule_name":     stringOr(schedule, "summary", ""),
			"schedule_id":       stringOr(schedule, "id", ""),
			"escalation_policy": stringOr(policy, "summary", ""),
			"escalation_level":  intField(oncall, "escalation_level", 0),
			"start":             stringOr(oncall, "start", ""),
			"end":               stringOr(oncall, "end", ""),
		})
	}
	return map[string]any{"count": len(out), "oncalls": out}, nil
}

func pagerDutyAddIncidentNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	content := stringArg(input, "content")
	if incidentID == "" || content == "" {
		return map[string]any{"error": "incident_id and content are required"}, nil
	}
	path := "/incidents/" + pathEscape(incidentID) + "/notes"
	resp, err := doJSON(ctx, rt, http.MethodPost,
		addPrefix(pagerDutyBase(rt), path), headers,
		map[string]any{"note": map[string]any{"content": content}}, defaultTimeout)
	if err != nil {
		return transportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	note := asMap(data["note"])
	return map[string]any{
		"id":         stringOr(note, "id", ""),
		"content":    stringOr(note, "content", ""),
		"created_at": stringOr(note, "created_at", ""),
		"user":       stringOr(asMap(note["user"]), "summary", ""),
		"status":     "created",
	}, nil
}

func pagerDutyListEscalationPolicies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := pagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 25), 100))}}
	if query := stringArg(input, "query"); query != "" {
		params.Set("query", query)
	}
	data, ok := getJSON(ctx, rt, "PagerDuty",
		addPrefix(pagerDutyBase(rt), "/escalation_policies"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	policies := listOrEmpty(data["escalation_policies"])
	out := make([]any, 0, len(policies))
	for _, raw := range policies {
		policy := asMap(raw)
		teams := make([]any, 0)
		for _, rawTeam := range listOrEmpty(policy["teams"]) {
			teams = append(teams, stringOr(asMap(rawTeam), "summary", ""))
		}
		out = append(out, map[string]any{
			"id":                     stringOr(policy, "id", ""),
			"name":                   stringOr(policy, "name", ""),
			"description":            stringOr(policy, "description", ""),
			"num_loops":              intField(policy, "num_loops", 0),
			"teams":                  teams,
			"escalation_rules_count": len(listOrEmpty(policy["escalation_rules"])),
		})
	}
	return map[string]any{"count": len(out), "escalation_policies": out}, nil
}
