// Zoho CRM (upstream zoho_crm_tool, 6 actions).
//
// The upstream client prefixes every path with https://<ZOHO_CRM_DOMAIN>/crm/v7
// and authenticates with "Authorization: Zoho-oauthtoken <token>". Reads treat
// 204 as an empty record set; everything else keeps the upstream error strings
// and per-action projections.
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// batch6ZohoCRMTool mirrors gen_catalog.py output for the module.
var batch6ZohoCRMTool = &Tool{
	ID:          "zoho_crm_tool",
	Name:        "zoho_crm",
	DisplayName: "Zoho Crm",
	Summary:     "Zoho Crm integration (6 actions).",
	Actions: []Action{
		{
			Name:     "zoho_crm_add_note",
			Summary:  "Add a note to a record in Zoho CRM.",
			Source:   "zoho_crm_tool/zoho_crm_tool.py",
			Required: []string{"module", "record_id", "title", "content"},
			Params: map[string]any{
				"module":    param("string"),
				"record_id": param("string"),
				"title":     param("string"),
				"content":   param("string"),
			},
		},
		{
			Name:     "zoho_crm_create_record",
			Summary:  "Create a new record in a Zoho CRM module.",
			Source:   "zoho_crm_tool/zoho_crm_tool.py",
			Required: []string{"module"},
			Params: map[string]any{
				"module":      param("string"),
				"record_data": param("object"),
			},
		},
		{
			Name:     "zoho_crm_get_record",
			Summary:  "Get a specific record from a Zoho CRM module.",
			Source:   "zoho_crm_tool/zoho_crm_tool.py",
			Required: []string{"module", "record_id"},
			Params: map[string]any{
				"module":    param("string"),
				"record_id": param("string"),
			},
		},
		{
			Name:    "zoho_crm_list_modules",
			Summary: "List all available modules in the Zoho CRM account.",
			Source:  "zoho_crm_tool/zoho_crm_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "zoho_crm_list_records",
			Summary:  "List records from a Zoho CRM module.",
			Source:   "zoho_crm_tool/zoho_crm_tool.py",
			Required: []string{"module"},
			Params: map[string]any{
				"module":     param("string"),
				"fields":     paramDefault("string", ""),
				"page":       paramDefault("integer", 1),
				"per_page":   paramDefault("integer", 50),
				"sort_by":    paramDefault("string", ""),
				"sort_order": paramDefault("string", "desc"),
			},
		},
		{
			Name:     "zoho_crm_search_records",
			Summary:  "Search records in a Zoho CRM module.",
			Source:   "zoho_crm_tool/zoho_crm_tool.py",
			Required: []string{"module"},
			Params: map[string]any{
				"module":   param("string"),
				"criteria": paramDefault("string", ""),
				"email":    paramDefault("string", ""),
				"phone":    paramDefault("string", ""),
				"word":     paramDefault("string", ""),
				"page":     paramDefault("integer", 1),
				"per_page": paramDefault("integer", 50),
			},
		},
	},
}

// batch6ZohoCRMImpls binds every action name to its implementation.
func batch6ZohoCRMImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"zoho_crm_add_note":       batch6ZohoCRMAddNote,
		"zoho_crm_create_record":  batch6ZohoCRMCreateRecord,
		"zoho_crm_get_record":     batch6ZohoCRMGetRecord,
		"zoho_crm_list_modules":   batch6ZohoCRMListModules,
		"zoho_crm_list_records":   batch6ZohoCRMListRecords,
		"zoho_crm_search_records": batch6ZohoCRMSearchRecords,
	}
}

// batch6ZohoCall executes a request and returns the decoded envelope.
func batch6ZohoCall(ctx context.Context, rt *Runtime, spec batch6Request, read bool) (map[string]any, map[string]any) {
	res, fail := batch6Send(ctx, rt, "zoho_crm_tool", spec)
	if fail != nil {
		return nil, fail
	}
	if errDict := batch6ZohoCheck(res, read); errDict != nil {
		return nil, errDict
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"data": []any{}}, nil
	}
	data := batch6AsMap(res.Parsed)
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

// batch6ZohoListRecords lists one module's records with pagination info.
func batch6ZohoCRMListRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	module := stringArg(input, "module")
	if module == "" {
		return map[string]any{"error": "module is required (e.g. Leads, Contacts, Deals)"}, nil
	}
	page := intArg(input, "page", 1)
	filters := map[string]any{
		"page":     page,
		"per_page": batch6Clamp(intArg(input, "per_page", 50), 1, 200),
	}
	if fields := stringArg(input, "fields"); fields != "" {
		filters["fields"] = fields
	}
	if sortBy := stringArg(input, "sort_by"); sortBy != "" {
		filters["sort_by"] = sortBy
		filters["sort_order"] = stringArg(input, "sort_order")
	}
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodGet,
		Path:   "/" + url.PathEscape(module),
		Query:  batch6QueryValues(filters),
	}, true)
	if errDict != nil {
		return errDict, nil
	}
	records := batch6SliceOrEmpty(data["data"])
	info := batch6AsMap(data["info"])
	count := len(records)
	if _, present := info["count"]; present {
		count = int(batch6AsNumber(info["count"]))
	}
	pageValue := page
	if _, present := info["page"]; present {
		pageValue = int(batch6AsNumber(info["page"]))
	}
	return map[string]any{
		"module":       module,
		"records":      records,
		"count":        count,
		"more_records": batch6AsBool(info["more_records"]),
		"page":         pageValue,
	}, nil
}

// batch6ZohoCRMGetRecord reads one record.
func batch6ZohoCRMGetRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	module := stringArg(input, "module")
	recordID := stringArg(input, "record_id")
	if module == "" || recordID == "" {
		return map[string]any{"error": "module and record_id are required"}, nil
	}
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodGet,
		Path:   "/" + url.PathEscape(module) + "/" + url.PathEscape(recordID),
	}, true)
	if errDict != nil {
		return errDict, nil
	}
	records := batch6SliceOrEmpty(data["data"])
	if len(records) == 0 {
		return map[string]any{"error": "Record not found"}, nil
	}
	return map[string]any{"module": module, "record": records[0]}, nil
}

// batch6ZohoCRMCreateRecord posts one record inside the upstream data array.
func batch6ZohoCRMCreateRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	module := stringArg(input, "module")
	if module == "" {
		return map[string]any{"error": "module is required"}, nil
	}
	record, present, err := batch6OptionalMap(input, "record_data")
	if err != nil || !present || len(record) == 0 {
		return map[string]any{"error": "record_data dict is required"}, nil
	}
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodPost,
		Path:   "/" + url.PathEscape(module),
		Body:   map[string]any{"data": []any{record}},
	}, false)
	if errDict != nil {
		return errDict, nil
	}
	results := batch6SliceOrEmpty(data["data"])
	if len(results) == 0 {
		return map[string]any{"error": "Failed to create record"}, nil
	}
	first := batch6AsMap(results[0])
	return map[string]any{
		"id":      batch6AsString(batch6AsMap(first["details"])["id"]),
		"status":  batch6AsString(first["status"]),
		"message": batch6AsString(first["message"]),
	}, nil
}

// batch6ZohoCRMSearchRecords searches one module by criteria or shortcut.
func batch6ZohoCRMSearchRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	module := stringArg(input, "module")
	if module == "" {
		return map[string]any{"error": "module is required"}, nil
	}
	filters := map[string]any{
		"page":     intArg(input, "page", 1),
		"per_page": batch6Clamp(intArg(input, "per_page", 50), 1, 200),
	}
	searched := false
	for _, name := range []string{"criteria", "email", "phone", "word"} {
		if text := stringArg(input, name); text != "" {
			filters[name] = text
			searched = true
		}
	}
	if !searched {
		return map[string]any{
			"error": "At least one search parameter is required (criteria, email, phone, or word)",
		}, nil
	}
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodGet,
		Path:   "/" + url.PathEscape(module) + "/search",
		Query:  batch6QueryValues(filters),
	}, true)
	if errDict != nil {
		return errDict, nil
	}
	records := batch6SliceOrEmpty(data["data"])
	return map[string]any{"module": module, "results": records, "count": len(records)}, nil
}

// batch6ZohoCRMListModules lists the account's modules.
func batch6ZohoCRMListModules(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodGet, Path: "/settings/modules",
	}, true)
	if errDict != nil {
		return errDict, nil
	}
	modules := make([]any, 0)
	for _, raw := range batch6SliceOrEmpty(data["modules"]) {
		module := batch6AsMap(raw)
		modules = append(modules, map[string]any{
			"api_name":     batch6AsString(module["api_name"]),
			"module_name":  batch6AsString(module["module_name"]),
			"plural_label": batch6AsString(module["plural_label"]),
			"editable":     batch6AsBool(module["editable"]),
		})
	}
	return map[string]any{"modules": modules}, nil
}

// batch6ZohoCRMAddNote creates a note attached to one record.
func batch6ZohoCRMAddNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	module := stringArg(input, "module")
	recordID := stringArg(input, "record_id")
	if module == "" || recordID == "" {
		return map[string]any{"error": "module and record_id are required"}, nil
	}
	content := stringArg(input, "content")
	if content == "" {
		return map[string]any{"error": "content is required"}, nil
	}
	data, errDict := batch6ZohoCall(ctx, rt, batch6Request{
		Method: http.MethodPost,
		Path:   fmt.Sprintf("/%s/%s/Notes", url.PathEscape(module), url.PathEscape(recordID)),
		Body: map[string]any{"data": []any{map[string]any{
			"Note_Title":   stringArg(input, "title"),
			"Note_Content": content,
		}}},
	}, false)
	if errDict != nil {
		return errDict, nil
	}
	results := batch6SliceOrEmpty(data["data"])
	if len(results) == 0 {
		return map[string]any{"error": "Failed to create note"}, nil
	}
	first := batch6AsMap(results[0])
	return map[string]any{
		"id":     batch6AsString(batch6AsMap(first["details"])["id"]),
		"status": batch6AsString(first["status"]),
	}, nil
}
