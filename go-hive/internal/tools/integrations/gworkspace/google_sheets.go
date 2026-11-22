// Google Sheets actions for batch 5, lane 3 (upstream google_sheets_tool,
// 10 actions). See google_workspace.go for the registry and shared helpers.
package gworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func sheetsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"google_sheets_get_spreadsheet":     sheetsGetSpreadsheet,
		"google_sheets_create_spreadsheet":  sheetsCreateSpreadsheet,
		"google_sheets_get_values":          sheetsGetValues,
		"google_sheets_update_values":       sheetsUpdateValues,
		"google_sheets_append_values":       sheetsAppendValues,
		"google_sheets_clear_values":        sheetsClearValues,
		"google_sheets_batch_update_values": sheetsBatchUpdateValues,
		"google_sheets_batch_clear_values":  sheetsBatchClearValues,
		"google_sheets_add_sheet":           sheetsAddSheet,
		"google_sheets_delete_sheet":        sheetsDeleteSheet,
	}
}

func sheetsAuthError() map[string]any {
	return map[string]any{
		"error": "Google Sheets credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable or configure 'google' via credential store",
	}
}

// sheetsResponse mirrors _GoogleSheetsClient._handle_response.
func sheetsResponse(status int, raw []byte) map[string]any {
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
			"error": fmt.Sprintf("Google Sheets API error (HTTP %d): %s", status, errorDetail(raw)),
		}
	}
	payload, ok := jsonObject(raw)
	if !ok {
		return map[string]any{"error": "Google Sheets API error: invalid JSON response"}
	}
	return payload
}

func sheetsCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !credential(rt, sheetsModule) {
		return sheetsAuthError(), nil
	}
	status, raw, err := do(ctx, rt, sheetsModule, method, path, query, body)
	if err != nil {
		return transportError(ctx, err), nil
	}
	return sheetsResponse(status, raw), nil
}

// sheetsValues accepts the upstream list-or-JSON-string input.
func sheetsValues(input map[string]any) ([]any, map[string]any) {
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
			"error": fmt.Sprintf("values must be a 2D list or JSON string, got %s", pyType(value)),
		}
	}
	return values, nil
}

func sheetsGetSpreadsheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := map[string]string{}
	if boolArg(input, "include_grid_data", false) {
		query["includeGridData"] = "true"
	}
	return sheetsCall(ctx, rt, http.MethodGet,
		sheetsPrefix+"/"+segments(stringArg(input, "spreadsheet_id")),
		query, nil)
}

func sheetsCreateSpreadsheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"properties": map[string]any{"title": stringArg(input, "title")}}
	if titles := stringSliceArg(input, "sheet_titles"); len(titles) > 0 {
		sheets := make([]any, 0, len(titles))
		for _, title := range titles {
			sheets = append(sheets, map[string]any{"properties": map[string]any{"title": title}})
		}
		body["sheets"] = sheets
	}
	return sheetsCall(ctx, rt, http.MethodPost, sheetsPrefix, nil, body)
}

func sheetsGetValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	path := sheetsPrefix + "/" + segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + segments(stringArg(input, "range_name"))
	query := map[string]string{"valueRenderOption": stringArg(input, "value_render_option")}
	return sheetsCall(ctx, rt, http.MethodGet, path, query, nil)
}

func sheetsUpdateValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !credential(rt, sheetsModule) {
		return sheetsAuthError(), nil
	}
	values, failure := sheetsValues(input)
	if failure != nil {
		return failure, nil
	}
	path := sheetsPrefix + "/" + segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + segments(stringArg(input, "range_name"))
	query := map[string]string{"valueInputOption": stringArg(input, "value_input_option")}
	return sheetsCall(ctx, rt, http.MethodPut, path, query, map[string]any{"values": values})
}

func sheetsAppendValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !credential(rt, sheetsModule) {
		return sheetsAuthError(), nil
	}
	values, failure := sheetsValues(input)
	if failure != nil {
		return failure, nil
	}
	path := sheetsPrefix + "/" + segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + segments(stringArg(input, "range_name")) + ":append"
	query := map[string]string{"valueInputOption": stringArg(input, "value_input_option")}
	return sheetsCall(ctx, rt, http.MethodPost, path, query, map[string]any{"values": values})
}

func sheetsClearValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	path := sheetsPrefix + "/" + segments(stringArg(input, "spreadsheet_id")) +
		"/values/" + segments(stringArg(input, "range_name")) + ":clear"
	return sheetsCall(ctx, rt, http.MethodPost, path, nil, nil)
}

func sheetsBatchUpdateValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"valueInputOption": stringArg(input, "value_input_option"),
		"data":             input["data"],
	}
	return sheetsCall(ctx, rt, http.MethodPost,
		sheetsPrefix+"/"+segments(stringArg(input, "spreadsheet_id"))+"/values:batchUpdate",
		nil, body)
}

func sheetsBatchClearValues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"ranges": stringSliceArg(input, "ranges")}
	return sheetsCall(ctx, rt, http.MethodPost,
		sheetsPrefix+"/"+segments(stringArg(input, "spreadsheet_id"))+"/values:batchClear",
		nil, body)
}

func sheetsAddSheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	return sheetsCall(ctx, rt, http.MethodPost,
		sheetsPrefix+"/"+segments(stringArg(input, "spreadsheet_id"))+":batchUpdate",
		nil, body)
}

func sheetsDeleteSheet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"requests": []any{
			map[string]any{
				"deleteSheet": map[string]any{"sheetId": intArg(input, "sheet_id", 0)},
			},
		},
	}
	return sheetsCall(ctx, rt, http.MethodPost,
		sheetsPrefix+"/"+segments(stringArg(input, "spreadsheet_id"))+":batchUpdate",
		nil, body)
}
