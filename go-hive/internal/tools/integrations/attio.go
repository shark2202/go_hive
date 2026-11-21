// Attio (upstream attio_tool, 15 actions).
//
// The upstream client wraps the Attio V2 REST API at https://api.attio.com/v2
// with a Bearer key. Responses are ranked by the same rules: list actions
// project {records|entries|tasks|members, total}, single-object actions unwrap
// the "data" envelope, and 204 means success.
package integrations

import (
	"context"
	"net/http"
	"net/url"
)

// attioTool mirrors gen_catalog.py output for the module.
var attioTool = &Tool{
	ID:          "attio_tool",
	Name:        "attio",
	DisplayName: "Attio",
	Summary:     "Attio integration (15 actions).",
	Actions: []Action{
		{
			Name:     "attio_list_entries_get",
			Summary:  "List entries in a specific Attio list (e.g., a Sales Pipeline).",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"list_id"},
			Params: map[string]any{
				"list_id":     param("string"),
				"limit":       paramDefault("integer", 50),
				"offset":      paramDefault("integer", 0),
				"filter_json": param("string"),
				"sorts_json":  param("string"),
			},
		},
		{
			Name:     "attio_list_entry_create",
			Summary:  "Add a record to a specific list (e.g., adding a person to a Sales Pipeline).",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"list_id", "parent_record_id"},
			Params: map[string]any{
				"list_id":          param("string"),
				"parent_record_id": param("string"),
				"parent_object":    paramDefault("string", "people"),
				"entry_values":     param("object"),
			},
		},
		{
			Name:     "attio_list_entry_delete",
			Summary:  "Remove an entry from a list.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"list_id", "entry_id"},
			Params: map[string]any{
				"list_id":  param("string"),
				"entry_id": param("string"),
			},
		},
		{
			Name:    "attio_list_lists",
			Summary: "List all lists in the Attio workspace.",
			Source:  "attio_tool/attio_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "attio_member_get",
			Summary:  "Get a workspace member by ID.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"member_id"},
			Params: map[string]any{
				"member_id": param("string"),
			},
		},
		{
			Name:    "attio_members_list",
			Summary: "List all members in the Attio workspace for assignment purposes.",
			Source:  "attio_tool/attio_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "attio_record_assert",
			Summary:  "Upsert a record. If a record matches the unique attribute, it updates; otherwise, it creates a new one.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"object_handle", "matching_attribute", "values"},
			Params: map[string]any{
				"object_handle":      param("string"),
				"matching_attribute": param("string"),
				"values":             param("object"),
			},
		},
		{
			Name:     "attio_record_create",
			Summary:  "Create a new record in Attio.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"object_handle", "values"},
			Params: map[string]any{
				"object_handle": param("string"),
				"values":        param("object"),
			},
		},
		{
			Name:     "attio_record_get",
			Summary:  "Get a specific Attio record by its ID.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"object_handle", "record_id"},
			Params: map[string]any{
				"object_handle": param("string"),
				"record_id":     param("string"),
			},
		},
		{
			Name:     "attio_record_list",
			Summary:  "List and filter records within a specific Attio object.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"object_handle"},
			Params: map[string]any{
				"object_handle": param("string"),
				"limit":         paramDefault("integer", 50),
				"offset":        paramDefault("integer", 0),
				"filter_json":   param("string"),
				"sorts_json":    param("string"),
			},
		},
		{
			Name:     "attio_record_update",
			Summary:  "Update an existing Attio record. For multiselect attributes, new values are appended.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"object_handle", "record_id", "values"},
			Params: map[string]any{
				"object_handle": param("string"),
				"record_id":     param("string"),
				"values":        param("object"),
			},
		},
		{
			Name:     "attio_task_create",
			Summary:  "Create a task linked to specific records.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"content"},
			Params: map[string]any{
				"content":        param("string"),
				"linked_records": param("array"),
				"assignees":      param("array"),
				"deadline_at":    param("string"),
				"is_completed":   paramDefault("boolean", false),
			},
		},
		{
			Name:     "attio_task_delete",
			Summary:  "Delete a task.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"task_id"},
			Params: map[string]any{
				"task_id": param("string"),
			},
		},
		{
			Name:     "attio_task_get",
			Summary:  "Get a task by its ID.",
			Source:   "attio_tool/attio_tool.py",
			Required: []string{"task_id"},
			Params: map[string]any{
				"task_id": param("string"),
			},
		},
		{
			Name:    "attio_task_list",
			Summary: "List all tasks in the Attio workspace.",
			Source:  "attio_tool/attio_tool.py",
			Params: map[string]any{
				"limit":  paramDefault("integer", 50),
				"offset": paramDefault("integer", 0),
			},
		},
	},
}

// attioImpls binds every action name to its implementation.
func attioImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"attio_list_entries_get":  batch6AttioListEntriesGet,
		"attio_list_entry_create": batch6AttioListEntryCreate,
		"attio_list_entry_delete": batch6AttioListEntryDelete,
		"attio_list_lists":        batch6AttioListLists,
		"attio_member_get":        batch6AttioMemberGet,
		"attio_members_list":      batch6AttioMembersList,
		"attio_record_assert":     batch6AttioRecordAssert,
		"attio_record_create":     batch6AttioRecordCreate,
		"attio_record_get":        batch6AttioRecordGet,
		"attio_record_list":       batch6AttioRecordList,
		"attio_record_update":     batch6AttioRecordUpdate,
		"attio_task_create":       batch6AttioTaskCreate,
		"attio_task_delete":       batch6AttioTaskDelete,
		"attio_task_get":          batch6AttioTaskGet,
		"attio_task_list":         batch6AttioTaskList,
	}
}

// batch6AttioCall executes a request and unwraps the "data" envelope. A failed
// call returns the error dict as the second value.
func batch6AttioCall(ctx context.Context, rt *Runtime, spec crmRequest) (any, map[string]any) {
	res, fail := crmSend(ctx, rt, "attio_tool", spec)
	if fail != nil {
		return nil, fail
	}
	if errDict := attioCheck(res); errDict != nil {
		return nil, errDict
	}
	if res.Status == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	if object := asMap(res.Parsed); object != nil {
		if data, present := object["data"]; present {
			return data, nil
		}
	}
	return res.Parsed, nil
}

// batch6AttioValues reads the required attribute-values object.
func batch6AttioValues(input map[string]any) (map[string]any, map[string]any) {
	values, present, err := crmOptionalMap(input, "values")
	if err != nil || !present {
		return nil, map[string]any{"error": "values dict is required"}
	}
	return values, nil
}

// batch6AttioQueryBody builds the {limit, offset, filter?, sorts?} body shared
// by records and list entries.
func batch6AttioQueryBody(input map[string]any) (map[string]any, map[string]any) {
	body := map[string]any{
		"limit":  intArg(input, "limit", 50),
		"offset": intArg(input, "offset", 0),
	}
	if filter, present, err := crmJsonParam(input, "filter_json"); err != nil {
		return nil, map[string]any{"error": err.Error()}
	} else if present {
		body["filter"] = filter
	}
	if sorts, present, err := crmJsonParam(input, "sorts_json"); err != nil {
		return nil, map[string]any{"error": err.Error()}
	} else if present {
		body["sorts"] = sorts
	}
	return body, nil
}

// batch6AttioRecordList queries one object's records.
func batch6AttioRecordList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectHandle := stringArg(input, "object_handle")
	if objectHandle == "" {
		return map[string]any{"error": "object_handle is required"}, nil
	}
	body, errDict := batch6AttioQueryBody(input)
	if errDict != nil {
		return errDict, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPost,
		Path:   "/objects/" + url.PathEscape(objectHandle) + "/records/query",
		Body:   body,
	})
	if errDict != nil {
		return errDict, nil
	}
	records := sliceOrEmpty(data)
	return map[string]any{"records": records, "total": len(records)}, nil
}

// batch6AttioRecordGet reads one record.
func batch6AttioRecordGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectHandle := stringArg(input, "object_handle")
	recordID := stringArg(input, "record_id")
	if objectHandle == "" || recordID == "" {
		return map[string]any{"error": "object_handle and record_id are required"}, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodGet,
		Path:   "/objects/" + url.PathEscape(objectHandle) + "/records/" + url.PathEscape(recordID),
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioRecordCreate creates a record from an attribute-values object.
func batch6AttioRecordCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectHandle := stringArg(input, "object_handle")
	if objectHandle == "" {
		return map[string]any{"error": "object_handle is required"}, nil
	}
	values, errDict := batch6AttioValues(input)
	if errDict != nil {
		return errDict, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPost,
		Path:   "/objects/" + url.PathEscape(objectHandle) + "/records",
		Body:   map[string]any{"data": map[string]any{"values": values}},
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioRecordUpdate patches a record's attribute values.
func batch6AttioRecordUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectHandle := stringArg(input, "object_handle")
	recordID := stringArg(input, "record_id")
	if objectHandle == "" || recordID == "" {
		return map[string]any{"error": "object_handle and record_id are required"}, nil
	}
	values, errDict := batch6AttioValues(input)
	if errDict != nil {
		return errDict, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPatch,
		Path:   "/objects/" + url.PathEscape(objectHandle) + "/records/" + url.PathEscape(recordID),
		Body:   map[string]any{"data": map[string]any{"values": values}},
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioRecordAssert upserts by matching attribute.
func batch6AttioRecordAssert(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectHandle := stringArg(input, "object_handle")
	matchingAttribute := stringArg(input, "matching_attribute")
	if objectHandle == "" || matchingAttribute == "" {
		return map[string]any{"error": "object_handle and matching_attribute are required"}, nil
	}
	values, errDict := batch6AttioValues(input)
	if errDict != nil {
		return errDict, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPut,
		Path:   "/objects/" + url.PathEscape(objectHandle) + "/records",
		Query:  url.Values{"matching_attribute": {matchingAttribute}},
		Body:   map[string]any{"data": map[string]any{"values": values}},
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioListLists lists the workspace's lists.
func batch6AttioListLists(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{Method: http.MethodGet, Path: "/lists"})
	if errDict != nil {
		return errDict, nil
	}
	lists := sliceOrEmpty(data)
	return map[string]any{"lists": lists, "total": len(lists)}, nil
}

// batch6AttioListEntriesGet queries one list's entries.
func batch6AttioListEntriesGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	listID := stringArg(input, "list_id")
	if listID == "" {
		return map[string]any{"error": "list_id is required"}, nil
	}
	body, errDict := batch6AttioQueryBody(input)
	if errDict != nil {
		return errDict, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPost,
		Path:   "/lists/" + url.PathEscape(listID) + "/entries/query",
		Body:   body,
	})
	if errDict != nil {
		return errDict, nil
	}
	entries := sliceOrEmpty(data)
	return map[string]any{"entries": entries, "total": len(entries)}, nil
}

// batch6AttioListEntryCreate adds a record to a list.
func batch6AttioListEntryCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	listID := stringArg(input, "list_id")
	parentRecordID := stringArg(input, "parent_record_id")
	if listID == "" || parentRecordID == "" {
		return map[string]any{"error": "list_id and parent_record_id are required"}, nil
	}
	parent := map[string]any{
		"parent_record_id": parentRecordID,
		"parent_object":    stringArg(input, "parent_object"),
	}
	if values, present, err := crmOptionalMap(input, "entry_values"); err != nil {
		return map[string]any{"error": "entry_values must be an object"}, nil
	} else if present && len(values) > 0 {
		parent["entry_values"] = values
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPost,
		Path:   "/lists/" + url.PathEscape(listID) + "/entries",
		Body:   map[string]any{"data": parent},
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioListEntryDelete removes an entry from a list.
func batch6AttioListEntryDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	listID := stringArg(input, "list_id")
	entryID := stringArg(input, "entry_id")
	if listID == "" || entryID == "" {
		return map[string]any{"error": "list_id and entry_id are required"}, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodDelete,
		Path:   "/lists/" + url.PathEscape(listID) + "/entries/" + url.PathEscape(entryID),
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioTaskCreate creates a plaintext task linked to records.
func batch6AttioTaskCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	content := stringArg(input, "content")
	if content == "" {
		return map[string]any{"error": "content is required"}, nil
	}
	body := map[string]any{
		"content":      content,
		"format":       "plaintext",
		"is_completed": boolArg(input, "is_completed", false),
	}
	if records, present, err := crmOptionalList(input, "linked_records"); err != nil {
		return map[string]any{"error": "linked_records must be an array"}, nil
	} else if present && len(records) > 0 {
		body["linked_records"] = records
	}
	if assignees, present, err := crmOptionalList(input, "assignees"); err != nil {
		return map[string]any{"error": "assignees must be an array"}, nil
	} else if present && len(assignees) > 0 {
		body["assignees"] = assignees
	}
	if deadline := stringArg(input, "deadline_at"); deadline != "" {
		body["deadline_at"] = deadline
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodPost, Path: "/tasks",
		Body: map[string]any{"data": body},
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioTaskList lists tasks with pagination.
func batch6AttioTaskList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/tasks",
		Query: crmQueryValues(map[string]any{
			"limit":  intArg(input, "limit", 50),
			"offset": intArg(input, "offset", 0),
		}),
	})
	if errDict != nil {
		return errDict, nil
	}
	tasks := sliceOrEmpty(data)
	return map[string]any{"tasks": tasks, "total": len(tasks)}, nil
}

// batch6AttioTaskGet reads one task.
func batch6AttioTaskGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskID := stringArg(input, "task_id")
	if taskID == "" {
		return map[string]any{"error": "task_id is required"}, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/tasks/" + url.PathEscape(taskID),
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioTaskDelete removes a task.
func batch6AttioTaskDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskID := stringArg(input, "task_id")
	if taskID == "" {
		return map[string]any{"error": "task_id is required"}, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodDelete, Path: "/tasks/" + url.PathEscape(taskID),
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}

// batch6AttioMembersList lists workspace members.
func batch6AttioMembersList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{Method: http.MethodGet, Path: "/workspace_members"})
	if errDict != nil {
		return errDict, nil
	}
	members := sliceOrEmpty(data)
	return map[string]any{"members": members, "total": len(members)}, nil
}

// batch6AttioMemberGet reads one workspace member.
func batch6AttioMemberGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	memberID := stringArg(input, "member_id")
	if memberID == "" {
		return map[string]any{"error": "member_id is required"}, nil
	}
	data, errDict := batch6AttioCall(ctx, rt, crmRequest{
		Method: http.MethodGet, Path: "/workspace_members/" + url.PathEscape(memberID),
	})
	if errDict != nil {
		return errDict, nil
	}
	return data, nil
}
