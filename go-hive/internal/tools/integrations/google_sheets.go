// Google Sheets actions for batch 5, lane 3 (upstream google_sheets_tool,
// 10 actions). See google_workspace.go for the registry and shared helpers.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func batch5Lane3SheetsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"google_sheets_get_spreadsheet":     batch5Lane3SheetsGetSpreadsheet,
		"google_sheets_create_spreadsheet":  batch5Lane3SheetsCreateSpreadsheet,
		"google_sheets_get_values":          batch5Lane3SheetsGetValues,
		"google_sheets_update_values":       batch5Lane3SheetsUpdateValues,
		"google_sheets_append_values":       batch5Lane3SheetsAppendValues,
		"google_sheets_clear_values":        batch5Lane3SheetsClearValues,
		"google_sheets_batch_update_values": batch5Lane3SheetsBatchUpdateValues,
		"google_sheets_batch_clear_values":  batch5Lane3SheetsBatchClearValues,
		"google_sheets_add_sheet":           batch5Lane3SheetsAddSheet,
		"google_sheets_delete_sheet":        batch5Lane3SheetsDeleteSheet,
	}
}

func batch5Lane3SheetsAuthError() map[string]any {
	return map[string]any{
		"error": "Google Sheets credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable or configure 'google' via credential store",
	}
}

// batch5Lane3SheetsResponse mirrors _GoogleSheetsClient._handle_response.
func batch5Lane3SheetsResponse(status int, raw []byte) map[string]any {
	switch {
	case status == http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Google Sheets access token"}
	case status == http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Google API scopes."}
	case status == http.StatusNotFound:
		return map[string]any{"error": "Spreadsheet or range not found"}
	case status == http.StatusTooManyRequests:
		return map[string]any{"error": "Google API rate limit exceeded. Try again later."}
	case status >= 400:
		return map[string]any{
			"error": fmt.Sprintf("Google Sheets API error (HTTP %d): %s", status, batch5Lane3ErrorDetail(raw)),
		}
	}
	payload, ok := batch5Lane3JSONObject(raw)
	if !ok {
		return map[string]any{"error": "Google Sheets API error: invalid JSON response"}
	}
	return payload
}

func batch5Lane3SheetsCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !batch5Lane3Credential(rt, batch5Lane3SheetsModule) {
		return batch5Lane3SheetsAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3SheetsModule, method, path, query, body)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	return batch5Lane3SheetsResponse(status, raw), nil
}

// batch5Lane3SheetsValues accepts the upstream list-or-JSON-string input.
func batch5Lane3SheetsValues(input map[string]any) ([]any, map[string]any) {
	value := input["values"]
	if text, ok := value.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, map[string]any{"error": "values is not valid JSON"}
		}
		value = decoded
	}
	values, ok := value.([]any)
	if !ok {
		return nil, map[string]any{
			"error": fmt.Sprintf("values must be a 2D list or JSON string, got %s", batch5Lane3PyType(value)),
		}
	}
	return values, nil
}

func batch5Lane3SheetsGetSpreadsheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := map[string]string{}
	if boolArg(input, "include_grid_data", false) {
		query["includeGridData"] = "true"
	}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodGet,
		batch5Lane3SheetsPrefix+"/"+batch5Lane3Segments(stringArg(input, "spreadsheet_id")),
		query, nil)
}

func batch5Lane3SheetsCreateSpreadsheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"properties": map[string]any{"title": stringArg(input, "title")}}
	if titles := stringSliceArg(input, "sheet_titles"); len(titles) > 0 {
		sheets := make([]any, 0, len(titles))
		for _, title := range titles {
			sheets = append(sheets, map[string]any{"properties": map[string]any{"title": title}})
		}
		body["sheets"] = sheets
	}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost, batch5Lane3SheetsPrefix, nil, body)
}

func batch5Lane3SheetsGetValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	path := batch5Lane3SheetsPrefix + "/" + batch5Lane3Segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + batch5Lane3Segments(stringArg(input, "range_name"))
	query := map[string]string{"valueRenderOption": stringArg(input, "value_render_option")}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodGet, path, query, nil)
}

func batch5Lane3SheetsUpdateValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !batch5Lane3Credential(rt, batch5Lane3SheetsModule) {
		return batch5Lane3SheetsAuthError(), nil
	}
	values, failure := batch5Lane3SheetsValues(input)
	if failure != nil {
		return failure, nil
	}
	path := batch5Lane3SheetsPrefix + "/" + batch5Lane3Segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + batch5Lane3Segments(stringArg(input, "range_name"))
	query := map[string]string{"valueInputOption": stringArg(input, "value_input_option")}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPut, path, query, map[string]any{"values": values})
}

func batch5Lane3SheetsAppendValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !batch5Lane3Credential(rt, batch5Lane3SheetsModule) {
		return batch5Lane3SheetsAuthError(), nil
	}
	values, failure := batch5Lane3SheetsValues(input)
	if failure != nil {
		return failure, nil
	}
	path := batch5Lane3SheetsPrefix + "/" + batch5Lane3Segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + batch5Lane3Segments(stringArg(input, "range_name")) + ":append"
	query := map[string]string{"valueInputOption": stringArg(input, "value_input_option")}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost, path, query, map[string]any{"values": values})
}

func batch5Lane3SheetsClearValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	path := batch5Lane3SheetsPrefix + "/" + batch5Lane3Segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + batch5Lane3Segments(stringArg(input, "range_name")) + ":clear"
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost, path, nil, nil)
}

func batch5Lane3SheetsBatchUpdateValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"valueInputOption": stringArg(input, "value_input_option"),
		"data":             input["data"],
	}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost,
		batch5Lane3SheetsPrefix+"/"+batch5Lane3Segments(stringArg(input, "spreadsheet_id"))+"/values:batchUpdate",
		nil, body)
}

func batch5Lane3SheetsBatchClearValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"ranges": stringSliceArg(input, "ranges")}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost,
		batch5Lane3SheetsPrefix+"/"+batch5Lane3Segments(stringArg(input, "spreadsheet_id"))+"/values:batchClear",
		nil, body)
}

func batch5Lane3SheetsAddSheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"requests": []any{
			map[string]any{
				"addSheet": map[string]any{
					"properties": map[string]any{
						"title": stringArg(input, "title"),
						"gridProperties": map[string]any{
							"rowCount":    intArg(input, "row_count", 1000),
							"columnCount": intArg(input, "column_count", 26),
						},
					},
				},
			},
		},
	}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost,
		batch5Lane3SheetsPrefix+"/"+batch5Lane3Segments(stringArg(input, "spreadsheet_id"))+":batchUpdate",
		nil, body)
}

func batch5Lane3SheetsDeleteSheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"requests": []any{
			map[string]any{
				"deleteSheet": map[string]any{"sheetId": intArg(input, "sheet_id", 0)},
			},
		},
	}
	return batch5Lane3SheetsCall(ctx, rt, http.MethodPost,
		batch5Lane3SheetsPrefix+"/"+batch5Lane3Segments(stringArg(input, "spreadsheet_id"))+":batchUpdate",
		nil, body)
}
