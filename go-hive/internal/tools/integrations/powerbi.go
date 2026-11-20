// Microsoft Power BI REST API (upstream powerbi_tool, 5 actions).
//
// Preserves the upstream myorg endpoints, $top/$skip/$filter parameters and
// projection of workspaces, datasets, reports and refresh history.
package integrations

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var batch6Lane4PowerBITool = &Tool{
	ID:          "powerbi_tool",
	Name:        "powerbi",
	DisplayName: "Power BI",
	Summary:     "Microsoft Power BI REST API integration (5 actions).",
	Actions: []Action{
		{
			Name:    "powerbi_list_workspaces",
			Summary: "List Power BI workspaces (groups).",
			Source:  "powerbi_tool/powerbi_tool.py",
			Params: map[string]any{
				"search": paramDefault("string", ""),
				"top":    paramDefault("integer", 100),
				"skip":   paramDefault("integer", 0),
			},
		},
		{
			Name:     "powerbi_list_datasets",
			Summary:  "List datasets in a Power BI workspace.",
			Source:   "powerbi_tool/powerbi_tool.py",
			Required: []string{"workspace_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
			},
		},
		{
			Name:     "powerbi_list_reports",
			Summary:  "List reports in a Power BI workspace.",
			Source:   "powerbi_tool/powerbi_tool.py",
			Required: []string{"workspace_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
			},
		},
		{
			Name:     "powerbi_refresh_dataset",
			Summary:  "Trigger a refresh for a Power BI dataset.",
			Source:   "powerbi_tool/powerbi_tool.py",
			Required: []string{"workspace_id", "dataset_id"},
			Params: map[string]any{
				"workspace_id":  param("string"),
				"dataset_id":    param("string"),
				"notify_option": paramDefault("string", "NoNotification"),
			},
		},
		{
			Name:     "powerbi_get_refresh_history",
			Summary:  "Get refresh history for a Power BI dataset.",
			Source:   "powerbi_tool/powerbi_tool.py",
			Required: []string{"workspace_id", "dataset_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
				"dataset_id":   param("string"),
				"top":          paramDefault("integer", 10),
			},
		},
	},
}

func batch6Lane4PowerBIToolImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"powerbi_list_workspaces":     batch6Lane4PowerBIListWorkspaces,
		"powerbi_list_datasets":       batch6Lane4PowerBIListDatasets,
		"powerbi_list_reports":        batch6Lane4PowerBIListReports,
		"powerbi_refresh_dataset":     batch6Lane4PowerBIRefreshDataset,
		"powerbi_get_refresh_history": batch6Lane4PowerBIGetRefreshHistory,
	}
}

func batch6Lane4PowerBIConfig(rt *Runtime) (map[string]string, map[string]any) {
	token := batch6Lane4ModuleToken(rt, "powerbi_tool")
	if token == "" {
		return nil, map[string]any{
			"error": "POWERBI_ACCESS_TOKEN is required",
			"help":  "Set POWERBI_ACCESS_TOKEN environment variable",
		}
	}
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}, nil
}

func batch6Lane4PowerBIBase(rt *Runtime) string {
	base := batch6Lane4ModuleURL(rt, "powerbi_tool")
	if base == "" {
		base = "https://api.powerbi.com/v1.0/myorg"
	}
	return strings.TrimRight(base, "/")
}

func batch6Lane4PowerBIGet(ctx context.Context, rt *Runtime, headers map[string]string, path string, params url.Values) map[string]any {
	target := batch6Lane4PowerBIBase(rt) + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, target, headers, nil, 30*time.Second)
	if err != nil {
		return batch6Lane4TransportError(ctx, "Power BI", err)
	}
	if resp.Status >= 400 {
		return batch6Lane4HTTPError(resp)
	}
	data, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Power BI API error: invalid JSON response"}
	}
	return data
}

func batch6Lane4PowerBIListWorkspaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch6Lane4PowerBIConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{
		"$top":  []string{strconv.Itoa(intArg(input, "top", 100))},
		"$skip": []string{strconv.Itoa(intArg(input, "skip", 0))},
	}
	if search := stringArg(input, "search"); search != "" {
		params.Set("$filter", "contains(name,'"+search+"')")
	}
	data := batch6Lane4PowerBIGet(ctx, rt, headers, "/groups", params)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	groups := batch4AsSlice(data["value"])
	if groups == nil {
		groups = []any{}
	}
	workspaces := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group := batch4AsMap(rawGroup)
		workspaces = append(workspaces, map[string]any{
			"id":                       group["id"],
			"name":                     group["name"],
			"is_read_only":             group["isReadOnly"],
			"is_on_dedicated_capacity": group["isOnDedicatedCapacity"],
		})
	}
	return map[string]any{"count": len(workspaces), "workspaces": workspaces}, nil
}

func batch6Lane4PowerBIListDatasets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch6Lane4PowerBIConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	data := batch6Lane4PowerBIGet(ctx, rt, headers, "/groups/"+batch6Lane4PathEscape(workspaceID)+"/datasets", nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	datasets := batch4AsSlice(data["value"])
	if datasets == nil {
		datasets = []any{}
	}
	extracted := make([]any, 0, len(datasets))
	for _, rawDataset := range datasets {
		dataset := batch4AsMap(rawDataset)
		extracted = append(extracted, map[string]any{
			"id":             dataset["id"],
			"name":           dataset["name"],
			"configured_by":  dataset["configuredBy"],
			"is_refreshable": dataset["isRefreshable"],
			"created_date":   dataset["createdDate"],
			"description":    dataset["description"],
			"web_url":        dataset["webUrl"],
		})
	}
	return map[string]any{"count": len(extracted), "datasets": extracted}, nil
}

func batch6Lane4PowerBIListReports(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch6Lane4PowerBIConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	data := batch6Lane4PowerBIGet(ctx, rt, headers, "/groups/"+batch6Lane4PathEscape(workspaceID)+"/reports", nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	reports := batch4AsSlice(data["value"])
	if reports == nil {
		reports = []any{}
	}
	extracted := make([]any, 0, len(reports))
	for _, rawReport := range reports {
		report := batch4AsMap(rawReport)
		extracted = append(extracted, map[string]any{
			"id":          report["id"],
			"name":        report["name"],
			"dataset_id":  report["datasetId"],
			"report_type": report["reportType"],
			"web_url":     report["webUrl"],
			"description": report["description"],
		})
	}
	return map[string]any{"count": len(extracted), "reports": extracted}, nil
}

func batch6Lane4PowerBIRefreshDataset(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch6Lane4PowerBIConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	datasetID := stringArg(input, "dataset_id")
	if workspaceID == "" || datasetID == "" {
		return map[string]any{"error": "workspace_id and dataset_id are required"}, nil
	}
	notifyOption := stringArg(input, "notify_option")
	if notifyOption == "" {
		notifyOption = "NoNotification"
	}
	target := batch6Lane4PowerBIBase(rt) + "/groups/" + batch6Lane4PathEscape(workspaceID) +
		"/datasets/" + batch6Lane4PathEscape(datasetID) + "/refreshes"
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodPost, target, headers, map[string]any{"notifyOption": notifyOption}, 30*time.Second)
	if err != nil {
		return batch6Lane4TransportError(ctx, "Power BI", err), nil
	}
	if resp.Status >= 400 {
		return batch6Lane4HTTPError(resp), nil
	}
	if resp.Status == http.StatusAccepted {
		return map[string]any{"result": "accepted", "request_id": resp.Header.Get("x-ms-request-id")}, nil
	}
	if len(strings.TrimSpace(string(resp.Body))) == 0 {
		return map[string]any{"result": "ok"}, nil
	}
	data, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Power BI API error: invalid JSON response"}, nil
	}
	return data, nil
}

func batch6Lane4PowerBIGetRefreshHistory(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch6Lane4PowerBIConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	datasetID := stringArg(input, "dataset_id")
	if workspaceID == "" || datasetID == "" {
		return map[string]any{"error": "workspace_id and dataset_id are required"}, nil
	}
	params := url.Values{"$top": []string{strconv.Itoa(intArg(input, "top", 10))}}
	data := batch6Lane4PowerBIGet(ctx, rt, headers,
		"/groups/"+batch6Lane4PathEscape(workspaceID)+"/datasets/"+batch6Lane4PathEscape(datasetID)+"/refreshes", params)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	refreshes := batch4AsSlice(data["value"])
	if refreshes == nil {
		refreshes = []any{}
	}
	extracted := make([]any, 0, len(refreshes))
	for _, rawRefresh := range refreshes {
		refresh := batch4AsMap(rawRefresh)
		extracted = append(extracted, map[string]any{
			"request_id":   refresh["requestId"],
			"refresh_type": refresh["refreshType"],
			"status":       refresh["status"],
			"start_time":   refresh["startTime"],
			"end_time":     refresh["endTime"],
		})
	}
	return map[string]any{"count": len(extracted), "refreshes": extracted}, nil
}
