// Salesforce CRM (upstream salesforce_tool, 9 actions).
//
// The upstream client targets a per-org instance URL obtained from
// SALESFORCE_INSTANCE_URL; this port resolves it at request time so one Go
// process can talk to whichever org the environment names, while tests point
// WithBaseURL at an httptest server. Everything lives under the API version
// the upstream tool pins (v62.0).
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const batch6SalesforceAPIVersion = "v62.0"

// batch6SalesforceTool mirrors gen_catalog.py output: one action per @mcp.tool
// function, alphabetical, with the upstream parameter names and defaults.
var batch6SalesforceTool = &Tool{
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

// batch6SalesforceImpls binds every action name to its implementation.
func batch6SalesforceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"salesforce_create_record":    batch6SalesforceCreateRecord,
		"salesforce_delete_record":    batch6SalesforceDeleteRecord,
		"salesforce_describe_object":  batch6SalesforceDescribeObject,
		"salesforce_get_record":       batch6SalesforceGetRecord,
		"salesforce_get_record_count": batch6SalesforceGetRecordCount,
		"salesforce_list_objects":     batch6SalesforceListObjects,
		"salesforce_search_records":   batch6SalesforceSearchRecords,
		"salesforce_soql_query":       batch6SalesforceSOQLQuery,
		"salesforce_update_record":    batch6SalesforceUpdateRecord,
	}
}

// batch6SalesforcePath builds a versioned instance path.
func batch6SalesforcePath(suffix string) string {
	return "/services/data/" + batch6SalesforceAPIVersion + suffix
}

// batch6SalesforceFields accepts the object form the model sends for create and
// update, or a JSON string for clients that pre-encode it.
func batch6SalesforceFields(input map[string]any) (map[string]any, map[string]any) {
	fields, present, err := batch6OptionalMap(input, "fields")
	if err != nil || !present || len(fields) == 0 {
		return nil, map[string]any{"error": "fields dict is required"}
	}
	return fields, nil
}

// batch6SalesforceSOQLQuery executes SOQL or follows a nextRecordsUrl page.
func batch6SalesforceSOQLQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	nextURL := stringArg(input, "next_records_url")
	if query == "" && nextURL == "" {
		return map[string]any{"error": "Either query or next_records_url is required"}, nil
	}

	spec := batch6Request{Method: http.MethodGet}
	if nextURL != "" {
		spec.Path = nextURL
	} else {
		spec.Path = batch6SalesforcePath("/query/")
		spec.Query = url.Values{"q": {query}}
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", spec)
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := batch6AsMap(res.Parsed)
	output := map[string]any{
		"total_size": batch6AsNumber(data["totalSize"]),
		"done":       true,
		"records":    []any{},
	}
	if done, present := data["done"].(bool); present {
		output["done"] = done
	}
	if records, ok := data["records"].([]any); ok {
		output["records"] = records
	}
	if next := batch6AsString(data["nextRecordsUrl"]); next != "" {
		output["next_records_url"] = next
	}
	return output, nil
}

// batch6SalesforceGetRecord reads one record, optionally projecting fields.
func batch6SalesforceGetRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	spec := batch6Request{
		Method: http.MethodGet,
		Path:   batch6SalesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
	}
	if fields := batch6SalesforceFieldList(input); fields != "" {
		spec.Query = url.Values{"fields": {fields}}
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", spec)
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return res.Parsed, nil
}

// batch6SalesforceFieldList accepts the comma-separated string upstream
// documents, plus a list for clients that send an array.
func batch6SalesforceFieldList(input map[string]any) string {
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
			if text := batch6AsString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// batch6SalesforceCreateRecord posts a fields dict to the SObject endpoint.
func batch6SalesforceCreateRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	fields, errDict := batch6SalesforceFields(input)
	if errDict != nil {
		return errDict, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodPost,
		Path:   batch6SalesforcePath("/sobjects/" + url.PathEscape(objectType)),
		Body:   fields,
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	return res.Parsed, nil
}

// batch6SalesforceUpdateRecord patches a record with a fields dict.
func batch6SalesforceUpdateRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	fields, errDict := batch6SalesforceFields(input)
	if errDict != nil {
		return errDict, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodPatch,
		Path:   batch6SalesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
		Body:   fields,
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return res.Parsed, nil
}

// batch6SalesforceDeleteRecord removes a record; Salesforce answers 204.
func batch6SalesforceDeleteRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	recordID := stringArg(input, "record_id")
	if objectType == "" || recordID == "" {
		return map[string]any{"error": "object_type and record_id are required"}, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodDelete,
		Path:   batch6SalesforcePath("/sobjects/" + url.PathEscape(objectType) + "/" + url.PathEscape(recordID)),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	return map[string]any{"success": true}, nil
}

// batch6SalesforceDescribeObject returns the slimmed metadata upstream builds.
func batch6SalesforceDescribeObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodGet,
		Path:   batch6SalesforcePath("/sobjects/" + url.PathEscape(objectType) + "/describe"),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := batch6AsMap(res.Parsed)
	allFields := batch6AsSlice(data["fields"])
	fields := make([]any, 0, len(allFields))
	for _, raw := range allFields {
		if len(fields) >= 200 {
			break
		}
		field := batch6AsMap(raw)
		entry := map[string]any{
			"name":     field["name"],
			"label":    field["label"],
			"type":     field["type"],
			"required": !batch6AsBoolDefault(field["nillable"], true) && batch6AsBool(field["createable"]),
		}
		picklist := make([]any, 0)
		for _, rawValue := range batch6AsSlice(field["picklistValues"]) {
			option := batch6AsMap(rawValue)
			if batch6AsBool(option["active"]) {
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

// batch6SalesforceListObjects lists the org's SObject types.
func batch6SalesforceListObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodGet,
		Path:   batch6SalesforcePath("/sobjects/"),
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	data := batch6AsMap(res.Parsed)
	sobjects := make([]any, 0)
	for _, raw := range batch6AsSlice(data["sobjects"]) {
		object := batch6AsMap(raw)
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

// batch6SalesforceSearchRecords runs SOSL through the search endpoint.
func batch6SalesforceSearchRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	searchQuery := stringArg(input, "search_query")
	if searchQuery == "" {
		return map[string]any{"error": "search_query is required"}, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodGet,
		Path:   batch6SalesforcePath("/search/"),
		Query:  url.Values{"q": {searchQuery}},
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	if records, ok := res.Parsed.([]any); ok {
		return map[string]any{"records": records, "count": len(records)}, nil
	}
	records := batch6AsSlice(batch6AsMap(res.Parsed)["searchRecords"])
	if records == nil {
		records = []any{}
	}
	return map[string]any{"records": records, "count": len(records)}, nil
}

// batch6SalesforceGetRecordCount runs SELECT COUNT() for one SObject.
func batch6SalesforceGetRecordCount(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	if objectType == "" {
		return map[string]any{"error": "object_type is required"}, nil
	}
	res, fail := batch6Send(ctx, rt, "salesforce_tool", batch6Request{
		Method: http.MethodGet,
		Path:   batch6SalesforcePath("/query/"),
		Query:  url.Values{"q": {fmt.Sprintf("SELECT COUNT() FROM %s", objectType)}},
	})
	if fail != nil {
		return fail, nil
	}
	if errDict := batch6SalesforceCheck(res); errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"object_type": objectType,
		"total_size":  batch6AsNumber(batch6AsMap(res.Parsed)["totalSize"]),
	}, nil
}

// batch6AsBoolDefault reads a boolean, falling back when the key is absent.
func batch6AsBoolDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}
