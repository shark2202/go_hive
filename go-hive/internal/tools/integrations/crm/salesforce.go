// Salesforce CRM (upstream salesforce_tool, 9 actions).
//
// The upstream client targets a per-org instance URL obtained from
// SALESFORCE_INSTANCE_URL; this port resolves it at request time so one Go
// process can talk to whichever org the environment names, while tests point
// WithBaseURL at an httptest server. Everything lives under the API version
// the upstream tool pins (v62.0).
package crm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const salesforceAPIVersion = "v62.0"

// salesforceTool mirrors gen_catalog.py output: one action per @mcp.tool
// function, alphabetical, with the upstream parameter names and defaults.
var salesforceTool = &Tool{
	ID:          "salesforce_tool",
	Name:        "salesforce",
	DisplayName: "Salesforce",
	Summary:     "Salesforce integration (9 actions).",
	Actions: []Action{
		{
			Name:     "salesforce_create_record",
			Summary:  "Create a new Salesforce record.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type", "fields"},
			Params: map[string]any{
				"object_type": param("string"),
				"fields":      param("object"),
			},
		},
		{
			Name:     "salesforce_delete_record",
			Summary:  "Delete a Salesforce record by its ID.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type", "record_id"},
			Params: map[string]any{
				"object_type": param("string"),
				"record_id":   param("string"),
			},
		},
		{
			Name:     "salesforce_describe_object",
			Summary:  "Get metadata for a Salesforce SObject type (fields, types, picklist values).",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type"},
			Params: map[string]any{
				"object_type": param("string"),
			},
		},
		{
			Name:     "salesforce_get_record",
			Summary:  "Get a single Salesforce record by its ID.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type", "record_id"},
			Params: map[string]any{
				"object_type": param("string"),
				"record_id":   param("string"),
				"fields":      paramDefault("string", ""),
			},
		},
		{
			Name:     "salesforce_get_record_count",
			Summary:  "Get the total number of records for a Salesforce SObject type.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type"},
			Params: map[string]any{
				"object_type": param("string"),
			},
		},
		{
			Name:    "salesforce_list_objects",
			Summary: "List all available SObject types in the Salesforce org.",
			Source:  "salesforce_tool/salesforce_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "salesforce_search_records",
			Summary:  "Full-text search across Salesforce records using SOSL.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"search_query"},
			Params: map[string]any{
				"search_query": param("string"),
			},
		},
		{
			Name:    "salesforce_soql_query",
			Summary: "Execute a SOQL query against Salesforce.",
			Source:  "salesforce_tool/salesforce_tool.py",
			Params: map[string]any{
				"query":            paramDefault("string", ""),
				"next_records_url": paramDefault("string", ""),
			},
		},
		{
			Name:     "salesforce_update_record",
			Summary:  "Update fields on an existing Salesforce record.",
			Source:   "salesforce_tool/salesforce_tool.py",
			Required: []string{"object_type", "record_id", "fields"},
			Params: map[string]any{
				"object_type": param("string"),
				"record_id":   param("string"),
				"fields":      param("object"),
			},
		},
	},
}

// salesforceImpls binds every action name to its implementation.
func salesforceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"salesforce_create_record":    salesforceCreateRecord,
		"salesforce_delete_record":    salesforceDeleteRecord,
		"salesforce_describe_object":  salesforceDescribeObject,
		"salesforce_get_record":       salesforceGetRecord,
		"salesforce_get_record_count": salesforceGetRecordCount,
		"salesforce_list_objects":     salesforceListObjects,
		"salesforce_search_records":   salesforceSearchRecords,
		"salesforce_soql_query":       salesforceSOQLQuery,
		"salesforce_update_record":    salesforceUpdateRecord,
	}
}

// salesforcePath builds a versioned instance path.
func salesforcePath(suffix string) string {
	return "/services/data/" + salesforceAPIVersion + suffix
}

// salesforceFields accepts the object form the model sends for create and
// update, or a JSON string for clients that pre-encode it.
func salesforceFields(input map[string]any) (map[string]any, map[string]any) {
	fields, present, err := crmOptionalMap(input, "fields")
	if err != nil || !present || len(fields) == 0 {
		return nil, map[string]any{"error": "fields dict is required"}
	}
	return fields, nil
}

// salesforceSOQLQuery executes SOQL or follows a nextRecordsUrl page.
func salesforceSOQLQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	nextURL := stringArg(input, "next_records_url")
	if query == "" && nextURL == "" {
		return map[string]any{"error": "Either query or next_records_url is required"}, nil
	}

	spec := crmRequest{Method: http.MethodGet}
	if nextURL != "" {
		spec.Path = nextURL
	} else {
		spec.Path = salesforcePath("/query/")
		spec.Query = url.Values{"q": {query}}
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", spec)
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := asMap(res.Parsed)
	output := map[string]any{
		"total_size": crmAsNumber(data["totalSize"]),
		"done":       true,
		"records":    []any{},
	}
	if done, present := data["done"].(bool); present {
		output["done"] = done
	}
	if records, ok := data["records"].([]any); ok {
		output["records"] = records
	}
	if next := asString(data["nextRecordsUrl"]); next != "" {
		output["next_records_url"] = next
	}
	return output, nil
}

// salesforceGetRecord reads one record, optionally projecting fields.
func salesforceGetRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	spec := crmRequest{
		Method: http.MethodGet,
		Path:   salesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
	}
	if fields := salesforceFieldList(input); fields != "" {
		spec.Query = url.Values{"fields": {fields}}
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", spec)
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return res.Parsed, nil
}

// salesforceFieldList accepts the comma-separated string upstream
// documents, plus a list for clients that send an array.
func salesforceFieldList(input map[string]any) string {
	value, present := input["fields"]
	if !present || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if items, ok := value.([]any); ok {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := asString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// salesforceCreateRecord posts a fields dict to the SObject endpoint.
func salesforceCreateRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	fields, errDict := salesforceFields(input)
	if errDict != nil {
		return errDict, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodPost,
		Path:   salesforcePath("/sobjects/" + url.PathEscape(objectType)),
		Body:   fields,
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	return res.Parsed, nil
}

// salesforceUpdateRecord patches a record with a fields dict.
func salesforceUpdateRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	fields, errDict := salesforceFields(input)
	if errDict != nil {
		return errDict, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodPatch,
		Path:   salesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
		Body:   fields,
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return res.Parsed, nil
}

// salesforceDeleteRecord removes a record; Salesforce answers 204.
func salesforceDeleteRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodDelete,
		Path:   salesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return map[string]any{"success": true}, nil
}

// salesforceDescribeObject returns the slimmed metadata upstream builds.
func salesforceDescribeObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodGet,
		Path:   salesforcePath("/sobjects/" + url.PathEscape(objectType) + "/describe"),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := asMap(res.Parsed)
	allFields := asSlice(data["fields"])
	fields := make([]any, 0, len(allFields))
	for _, raw := range allFields {
		if len(fields) >= 200 {
			break
		}
		field := asMap(raw)
		entry := map[string]any{
			"name":     field["name"],
			"label":    field["label"],
			"type":     field["type"],
			"required": !asBoolDefault(field["nillable"], true) && asBool(field["createable"]),
		}
		picklist := make([]any, 0)
		for _, rawValue := range asSlice(field["picklistValues"]) {
			option := asMap(rawValue)
			if asBool(option["active"]) {
				picklist = append(picklist, option["value"])
			}
		}
		if len(picklist) > 0 {
			entry["picklist_values"] = picklist
		}
		fields = append(fields, entry)
	}
	return map[string]any{
		"name":        data["name"],
		"label":       data["label"],
		"key_prefix":  data["keyPrefix"],
		"createable":  data["createable"],
		"updateable":  data["updateable"],
		"field_count": len(allFields),
		"fields":      fields,
	}, nil
}

// salesforceListObjects lists the org's SObject types.
func salesforceListObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodGet,
		Path:   salesforcePath("/sobjects/"),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := asMap(res.Parsed)
	sobjects := make([]any, 0)
	for _, raw := range asSlice(data["sobjects"]) {
		object := asMap(raw)
		sobjects = append(sobjects, map[string]any{
			"name":       object["name"],
			"label":      object["label"],
			"key_prefix": object["keyPrefix"],
			"queryable":  object["queryable"],
			"createable": object["createable"],
			"custom":     object["custom"],
		})
	}
	return map[string]any{"count": len(sobjects), "sobjects": sobjects}, nil
}

// salesforceSearchRecords runs SOSL through the search endpoint.
func salesforceSearchRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	searchQuery := stringArg(input, "search_query")
	if searchQuery == "" {
		return map[string]any{"error": "search_query is required"}, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodGet,
		Path:   salesforcePath("/search/"),
		Query:  url.Values{"q": {searchQuery}},
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if records, ok := res.Parsed.([]any); ok {
		return map[string]any{"records": records, "count": len(records)}, nil
	}
	records := asSlice(asMap(res.Parsed)["searchRecords"])
	if records == nil {
		records = []any{}
	}
	return map[string]any{"records": records, "count": len(records)}, nil
}

// salesforceGetRecordCount runs SELECT COUNT() for one SObject.
func salesforceGetRecordCount(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	res, fail := crmSend(ctx, rt, "salesforce_tool", crmRequest{
		Method: http.MethodGet,
		Path:   salesforcePath("/query/"),
		Query:  url.Values{"q": {fmt.Sprintf("SELECT COUNT() FROM %s", objectType)}},
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := salesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"object_type": objectType,
		"total_size":  crmAsNumber(asMap(res.Parsed)["totalSize"]),
	}, nil
}

// asBoolDefault reads a boolean, falling back when the key is absent.
func asBoolDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}
