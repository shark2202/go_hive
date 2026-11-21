// Pipedrive CRM (upstream pipedrive_tool, 13 actions).
//
// The upstream client talks to a per-company host (PIPEDRIVE_DOMAIN) under
// /api/v1 and authenticates with an api_token query parameter. Responses keep
// the upstream projections: list actions flatten to a small dict per record and
// count, single-record actions return the upstream field subset.
package integrations

import (
	"context"
	"fmt"
	"net/http"
)

// pipedriveTool mirrors gen_catalog.py output for the module.
var pipedriveTool = &Tool{
	ID:          "pipedrive_tool",
	Name:        "pipedrive",
	DisplayName: "Pipedrive",
	Summary:     "Pipedrive integration (13 actions).",
	Actions: []Action{
		{
			Name:     "pipedrive_add_note",
			Summary:  "Add a note to a deal, person, or organization in Pipedrive.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"content"},
			Params: map[string]any{
				"content":   param("string"),
				"deal_id":   paramDefault("integer", 0),
				"person_id": paramDefault("integer", 0),
				"org_id":    paramDefault("integer", 0),
			},
		},
		{
			Name:     "pipedrive_create_activity",
			Summary:  "Create a new activity (call, meeting, task, etc.) in Pipedrive.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"subject"},
			Params: map[string]any{
				"subject":       param("string"),
				"activity_type": paramDefault("string", "task"),
				"due_date":      paramDefault("string", ""),
				"due_time":      paramDefault("string", ""),
				"deal_id":       paramDefault("integer", 0),
				"person_id":     paramDefault("integer", 0),
				"org_id":        paramDefault("integer", 0),
				"note":          paramDefault("string", ""),
			},
		},
		{
			Name:     "pipedrive_create_deal",
			Summary:  "Create a new deal in Pipedrive.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":     param("string"),
				"value":     paramDefault("number", 0),
				"currency":  paramDefault("string", "USD"),
				"person_id": paramDefault("integer", 0),
				"org_id":    paramDefault("integer", 0),
				"stage_id":  paramDefault("integer", 0),
			},
		},
		{
			Name:     "pipedrive_create_person",
			Summary:  "Create a new person (contact) in Pipedrive.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"name"},
			Params: map[string]any{
				"name":   param("string"),
				"email":  paramDefault("string", ""),
				"phone":  paramDefault("string", ""),
				"org_id": paramDefault("integer", 0),
			},
		},
		{
			Name:     "pipedrive_get_deal",
			Summary:  "Get details of a specific Pipedrive deal.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"deal_id"},
			Params: map[string]any{
				"deal_id": param("integer"),
			},
		},
		{
			Name:    "pipedrive_list_activities",
			Summary: "List activities (calls, meetings, tasks, etc.) from Pipedrive.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params: map[string]any{
				"done":          paramDefault("string", ""),
				"activity_type": paramDefault("string", ""),
				"limit":         paramDefault("integer", 50),
				"start":         paramDefault("integer", 0),
			},
		},
		{
			Name:    "pipedrive_list_deals",
			Summary: "List deals from Pipedrive CRM.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params: map[string]any{
				"status": paramDefault("string", "open"),
				"limit":  paramDefault("integer", 50),
				"start":  paramDefault("integer", 0),
			},
		},
		{
			Name:    "pipedrive_list_organizations",
			Summary: "List organizations from Pipedrive.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params: map[string]any{
				"limit": paramDefault("integer", 50),
				"start": paramDefault("integer", 0),
			},
		},
		{
			Name:    "pipedrive_list_persons",
			Summary: "List persons (contacts) from Pipedrive.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params: map[string]any{
				"limit": paramDefault("integer", 50),
				"start": paramDefault("integer", 0),
			},
		},
		{
			Name:    "pipedrive_list_pipelines",
			Summary: "List all sales pipelines in Pipedrive.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:    "pipedrive_list_stages",
			Summary: "List pipeline stages in Pipedrive.",
			Source:  "pipedrive_tool/pipedrive_tool.py",
			Params: map[string]any{
				"pipeline_id": paramDefault("integer", 0),
			},
		},
		{
			Name:     "pipedrive_search_persons",
			Summary:  "Search for persons in Pipedrive by name, email, or phone.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query": param("string"),
				"limit": paramDefault("integer", 20),
			},
		},
		{
			Name:     "pipedrive_update_deal",
			Summary:  "Update an existing Pipedrive deal.",
			Source:   "pipedrive_tool/pipedrive_tool.py",
			Required: []string{"deal_id"},
			Params: map[string]any{
				"deal_id":             param("integer"),
				"title":               paramDefault("string", ""),
				"value":               paramDefault("number", 0),
				"currency":            paramDefault("string", ""),
				"status":              paramDefault("string", ""),
				"stage_id":            paramDefault("integer", 0),
				"expected_close_date": paramDefault("string", ""),
				"lost_reason":         paramDefault("string", ""),
			},
		},
	},
}

// pipedriveImpls binds every action name to its implementation.
func pipedriveImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pipedrive_add_note":           batch6PipedriveAddNote,
		"pipedrive_create_activity":    batch6PipedriveCreateActivity,
		"pipedrive_create_deal":        batch6PipedriveCreateDeal,
		"pipedrive_create_person":      batch6PipedriveCreatePerson,
		"pipedrive_get_deal":           batch6PipedriveGetDeal,
		"pipedrive_list_activities":    batch6PipedriveListActivities,
		"pipedrive_list_deals":         batch6PipedriveListDeals,
		"pipedrive_list_organizations": batch6PipedriveListOrganizations,
		"pipedrive_list_persons":       batch6PipedriveListPersons,
		"pipedrive_list_pipelines":     batch6PipedriveListPipelines,
		"pipedrive_list_stages":        batch6PipedriveListStages,
		"pipedrive_search_persons":     batch6PipedriveSearchPersons,
		"pipedrive_update_deal":        batch6PipedriveUpdateDeal,
	}
}

// batch6PipedriveCall executes a request and unwraps the {success, data}
// envelope. A failed call returns the error dict as the second value.
func batch6PipedriveCall(ctx context.Context, rt *Runtime, spec crmRequest, fallback string) (map[string]any, map[string]any) {
	res, fail := crmSend(ctx, rt, "pipedrive_tool", spec)
	if fail != nil {
		return nil, fail
	}
	if errDict := pipedriveCheck(res, res.Method); errDict != nil {
		return nil, errDict
	}
	data := asMap(res.Parsed)
	if success, present := data["success"].(bool); !present || !success {
		message := asString(data["error"])
		if message == "" {
			message = fallback
		}
		return nil, map[string]any{"error": message}
	}
	return data, nil
}

// batch6PipedriveName reads a nested {"name": ...} reference.
func batch6PipedriveName(value any) string {
	return asString(asMap(value)["name"])
}

// batch6PipedriveFirstValue reads the first {"value": ...} entry of a list.
func batch6PipedriveFirstValue(value any) string {
	items := asSlice(value)
	if len(items) == 0 {
		return ""
	}
	return asString(asMap(items[0])["value"])
}

// batch6PipedriveListDeals lists deals filtered by status.
func batch6PipedriveListDeals(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := crmQueryValues(map[string]any{
		"status": stringArg(input, "status"),
		"limit":  clampInt(intArg(input, "limit", 50), 1, 500),
		"start":  intArg(input, "start", 0),
	})
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/deals", Query: query,
	}, "Unknown Pipedrive error")
	if errDict != nil {
		return errDict, nil
	}
	deals := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		deal := asMap(raw)
		deals = append(deals, map[string]any{
			"id":          deal["id"],
			"title":       asString(deal["title"]),
			"value":       crmAsNumber(deal["value"]),
			"currency":    asString(deal["currency"]),
			"status":      asString(deal["status"]),
			"person_name": batch6PipedriveName(deal["person_id"]),
			"org_name":    batch6PipedriveName(deal["org_id"]),
			"stage_id":    deal["stage_id"],
			"add_time":    asString(deal["add_time"]),
		})
	}
	return map[string]any{"deals": deals, "count": len(deals)}, nil
}

// batch6PipedriveGetDeal reads one deal.
func batch6PipedriveGetDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	dealID := intArg(input, "deal_id", 0)
	if dealID == 0 {
		return map[string]any{"error": "deal_id is required"}, nil
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: fmt.Sprintf("/deals/%d", dealID),
	}, "Deal not found")
	if errDict != nil {
		return errDict, nil
	}
	deal := asMap(data["data"])
	return map[string]any{
		"id":                  deal["id"],
		"title":               asString(deal["title"]),
		"value":               crmAsNumber(deal["value"]),
		"currency":            asString(deal["currency"]),
		"status":              asString(deal["status"]),
		"person_name":         batch6PipedriveName(deal["person_id"]),
		"org_name":            batch6PipedriveName(deal["org_id"]),
		"stage_id":            deal["stage_id"],
		"pipeline_id":         deal["pipeline_id"],
		"expected_close_date": asString(deal["expected_close_date"]),
		"probability":         deal["probability"],
		"add_time":            asString(deal["add_time"]),
		"won_time":            asString(deal["won_time"]),
		"lost_time":           asString(deal["lost_time"]),
		"lost_reason":         asString(deal["lost_reason"]),
	}, nil
}

// batch6PipedriveCreateDeal creates a deal; zero-valued optionals stay absent.
func batch6PipedriveCreateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	title := stringArg(input, "title")
	if title == "" {
		return map[string]any{"error": "title is required"}, nil
	}
	body := map[string]any{"title": title}
	if value := floatArg(input, "value", 0); value != 0 {
		body["value"] = value
		body["currency"] = stringArg(input, "currency")
	}
	if personID := intArg(input, "person_id", 0); personID != 0 {
		body["person_id"] = personID
	}
	if orgID := intArg(input, "org_id", 0); orgID != 0 {
		body["org_id"] = orgID
	}
	if stageID := intArg(input, "stage_id", 0); stageID != 0 {
		body["stage_id"] = stageID
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodPost, Path: "/deals", Body: body,
	}, "Failed to create deal")
	if errDict != nil {
		return errDict, nil
	}
	deal := asMap(data["data"])
	return map[string]any{
		"id":     deal["id"],
		"title":  asString(deal["title"]),
		"status": "created",
	}, nil
}

// batch6PipedriveListPersons lists contacts with their primary email/phone.
func batch6PipedriveListPersons(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := crmQueryValues(map[string]any{
		"limit": clampInt(intArg(input, "limit", 50), 1, 500),
		"start": intArg(input, "start", 0),
	})
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/persons", Query: query,
	}, "Unknown error")
	if errDict != nil {
		return errDict, nil
	}
	persons := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		person := asMap(raw)
		persons = append(persons, map[string]any{
			"id":               person["id"],
			"name":             asString(person["name"]),
			"email":            batch6PipedriveFirstValue(person["email"]),
			"phone":            batch6PipedriveFirstValue(person["phone"]),
			"org_name":         batch6PipedriveName(person["org_id"]),
			"open_deals_count": crmAsNumber(person["open_deals_count"]),
		})
	}
	return map[string]any{"persons": persons, "count": len(persons)}, nil
}

// batch6PipedriveSearchPersons searches persons by term.
func batch6PipedriveSearchPersons(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/persons/search",
		Query: crmQueryValues(map[string]any{
			"term":  query,
			"limit": clampInt(intArg(input, "limit", 20), 1, 100),
		}),
	}, "Search failed")
	if errDict != nil {
		return errDict, nil
	}
	results := make([]any, 0)
	for _, raw := range asSlice(asMap(data["data"])["items"]) {
		item := asMap(asMap(raw)["item"])
		results = append(results, map[string]any{
			"id":       item["id"],
			"name":     asString(item["name"]),
			"email":    batch6PipedriveFirstString(item["emails"]),
			"phone":    batch6PipedriveFirstString(item["phones"]),
			"org_name": batch6PipedriveName(item["organization"]),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}

// batch6PipedriveFirstString reads the first plain string of a list (the search
// endpoint returns emails/phones as strings, unlike the list endpoint).
func batch6PipedriveFirstString(value any) string {
	items := asSlice(value)
	if len(items) == 0 {
		return ""
	}
	return asString(items[0])
}

// batch6PipedriveListOrganizations lists organizations.
func batch6PipedriveListOrganizations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := crmQueryValues(map[string]any{
		"limit": clampInt(intArg(input, "limit", 50), 1, 500),
		"start": intArg(input, "start", 0),
	})
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/organizations", Query: query,
	}, "Unknown error")
	if errDict != nil {
		return errDict, nil
	}
	organizations := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		org := asMap(raw)
		organizations = append(organizations, map[string]any{
			"id":               org["id"],
			"name":             asString(org["name"]),
			"address":          asString(org["address"]),
			"open_deals_count": crmAsNumber(org["open_deals_count"]),
			"people_count":     crmAsNumber(org["people_count"]),
		})
	}
	return map[string]any{"organizations": organizations, "count": len(organizations)}, nil
}

// batch6PipedriveListActivities lists activities with optional filters.
func batch6PipedriveListActivities(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	filters := map[string]any{
		"limit": clampInt(intArg(input, "limit", 50), 1, 500),
		"start": intArg(input, "start", 0),
	}
	if done := stringArg(input, "done"); done != "" {
		filters["done"] = done
	}
	if activityType := stringArg(input, "activity_type"); activityType != "" {
		filters["type"] = activityType
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/activities", Query: crmQueryValues(filters),
	}, "Unknown error")
	if errDict != nil {
		return errDict, nil
	}
	activities := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		activity := asMap(raw)
		note := crmTruncate(asString(activity["note"]), 200)
		activities = append(activities, map[string]any{
			"id":          activity["id"],
			"subject":     asString(activity["subject"]),
			"type":        asString(activity["type"]),
			"done":        asBool(activity["done"]),
			"due_date":    asString(activity["due_date"]),
			"due_time":    asString(activity["due_time"]),
			"deal_title":  asString(activity["deal_title"]),
			"person_name": asString(activity["person_name"]),
			"org_name":    asString(activity["org_name"]),
			"note":        note,
		})
	}
	return map[string]any{"activities": activities, "count": len(activities)}, nil
}

// batch6PipedriveListPipelines lists sales pipelines.
func batch6PipedriveListPipelines(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/pipelines",
	}, "Unknown error")
	if errDict != nil {
		return errDict, nil
	}
	pipelines := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		pipeline := asMap(raw)
		pipelines = append(pipelines, map[string]any{
			"id":               pipeline["id"],
			"name":             asString(pipeline["name"]),
			"active":           asBool(pipeline["active"]),
			"deal_probability": asBool(pipeline["deal_probability"]),
			"order_nr":         crmAsNumber(pipeline["order_nr"]),
		})
	}
	return map[string]any{"pipelines": pipelines}, nil
}

// batch6PipedriveListStages lists stages, optionally for one pipeline.
func batch6PipedriveListStages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	filters := map[string]any{}
	if pipelineID := intArg(input, "pipeline_id", 0); pipelineID != 0 {
		filters["pipeline_id"] = pipelineID
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/stages", Query: crmQueryValues(filters),
	}, "Unknown error")
	if errDict != nil {
		return errDict, nil
	}
	stages := make([]any, 0)
	for _, raw := range sliceOrEmpty(data["data"]) {
		stage := asMap(raw)
		stages = append(stages, map[string]any{
			"id":          stage["id"],
			"name":        asString(stage["name"]),
			"pipeline_id": stage["pipeline_id"],
			"order_nr":    crmAsNumber(stage["order_nr"]),
			"active_flag": batch6AsBoolDefault(stage["active_flag"], true),
		})
	}
	return map[string]any{"stages": stages}, nil
}

// batch6PipedriveAddNote attaches a note to a deal, person or organization.
func batch6PipedriveAddNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	content := stringArg(input, "content")
	if content == "" {
		return map[string]any{"error": "content is required"}, nil
	}
	body := map[string]any{"content": content}
	dealID := intArg(input, "deal_id", 0)
	personID := intArg(input, "person_id", 0)
	orgID := intArg(input, "org_id", 0)
	if dealID == 0 && personID == 0 && orgID == 0 {
		return map[string]any{"error": "At least one of deal_id, person_id, or org_id is required"}, nil
	}
	if dealID != 0 {
		body["deal_id"] = dealID
	}
	if personID != 0 {
		body["person_id"] = personID
	}
	if orgID != 0 {
		body["org_id"] = orgID
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodPost, Path: "/notes", Body: body,
	}, "Failed to add note")
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"id":     asMap(data["data"])["id"],
		"status": "created",
	}, nil
}

// batch6PipedriveUpdateDeal updates the fields the caller supplied.
func batch6PipedriveUpdateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	dealID := intArg(input, "deal_id", 0)
	if dealID == 0 {
		return map[string]any{"error": "deal_id is required"}, nil
	}
	body := map[string]any{}
	if title := stringArg(input, "title"); title != "" {
		body["title"] = title
	}
	if value := floatArg(input, "value", 0); value != 0 {
		body["value"] = value
	}
	for _, name := range []string{"currency", "status", "expected_close_date", "lost_reason"} {
		if text := stringArg(input, name); text != "" {
			body[name] = text
		}
	}
	if stageID := intArg(input, "stage_id", 0); stageID != 0 {
		body["stage_id"] = stageID
	}
	if len(body) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodPut, Path: fmt.Sprintf("/deals/%d", dealID), Body: body,
	}, "Failed to update deal")
	if errDict != nil {
		return errDict, nil
	}
	deal := asMap(data["data"])
	return map[string]any{
		"id":     deal["id"],
		"title":  asString(deal["title"]),
		"status": asString(deal["status"]),
		"result": "updated",
	}, nil
}

// batch6PipedriveCreatePerson creates a contact with primary email/phone.
func batch6PipedriveCreatePerson(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	name := stringArg(input, "name")
	if name == "" {
		return map[string]any{"error": "name is required"}, nil
	}
	body := map[string]any{"name": name}
	if email := stringArg(input, "email"); email != "" {
		body["email"] = []any{map[string]any{"value": email, "primary": true, "label": "work"}}
	}
	if phone := stringArg(input, "phone"); phone != "" {
		body["phone"] = []any{map[string]any{"value": phone, "primary": true, "label": "work"}}
	}
	if orgID := intArg(input, "org_id", 0); orgID != 0 {
		body["org_id"] = orgID
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodPost, Path: "/persons", Body: body,
	}, "Failed to create person")
	if errDict != nil {
		return errDict, nil
	}
	person := asMap(data["data"])
	return map[string]any{
		"id":     person["id"],
		"name":   asString(person["name"]),
		"status": "created",
	}, nil
}

// batch6PipedriveCreateActivity creates an activity; activity_type maps to the
// provider's "type" field.
func batch6PipedriveCreateActivity(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	subject := stringArg(input, "subject")
	if subject == "" {
		return map[string]any{"error": "subject is required"}, nil
	}
	body := map[string]any{
		"subject": subject,
		"type":    stringArg(input, "activity_type"),
	}
	for _, name := range []string{"due_date", "due_time", "note"} {
		if text := stringArg(input, name); text != "" {
			body[name] = text
		}
	}
	for _, name := range []string{"deal_id", "person_id", "org_id"} {
		if value := intArg(input, name, 0); value != 0 {
			body[name] = value
		}
	}
	data, errDict := batch6PipedriveCall(ctx, rt, crmRequest{
		Method: http.MethodPost, Path: "/activities", Body: body,
	}, "Failed to create activity")
	if errDict != nil {
		return errDict, nil
	}
	activity := asMap(data["data"])
	return map[string]any{
		"id":      activity["id"],
		"subject": asString(activity["subject"]),
		"type":    asString(activity["type"]),
		"status":  "created",
	}, nil
}
