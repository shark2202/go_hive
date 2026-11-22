// Notion database actions: query, schema get, create and update.
package collab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func notionQueryDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	body := map[string]any{
		"page_size": clampInt(intArg(input, "page_size", 50), 1, notionMaxItems),
	}
	if filterJSON := stringArg(input, "filter_json"); filterJSON != "" {
		var filter any
		if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
			return map[string]any{"error": "filter_json is not valid JSON"}, nil
		}
		body["filter"] = filter
	}
	if sortsJSON := stringArg(input, "sorts_json"); sortsJSON != "" {
		var sorts any
		if err := json.Unmarshal([]byte(sortsJSON), &sorts); err != nil {
			return map[string]any{"error": "sorts_json is not valid JSON"}, nil
		}
		body["sorts"] = sorts
	}
	if cursor := stringArg(input, "start_cursor"); cursor != "" {
		body["start_cursor"] = cursor
	}

	path := fmt.Sprintf("/v1/databases/%s/query", url.PathEscape(databaseID))
	data := notionRequest(ctx, rt, http.MethodPost, path, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	pages := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		pages = append(pages, map[string]any{
			"id":               asString(item["id"]),
			"title":            notionTitle(asMap(item["properties"])),
			"url":              asString(item["url"]),
			"created_time":     asString(item["created_time"]),
			"last_edited_time": asString(item["last_edited_time"]),
		})
	}
	return map[string]any{
		"pages":       pages,
		"count":       len(pages),
		"has_more":    asBool(data["has_more"]),
		"next_cursor": data["next_cursor"],
	}, nil
}

func notionGetDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	data := notionRequest(ctx, rt, http.MethodGet, "/v1/databases/"+url.PathEscape(databaseID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	properties := make(map[string]any)
	for name, raw := range asMap(data["properties"]) {
		prop := asMap(raw)
		properties[name] = map[string]any{
			"type": asString(prop["type"]),
			"id":   asString(prop["id"]),
		}
	}
	return map[string]any{
		"id":               asString(data["id"]),
		"title":            notionRichText(data["title"]),
		"url":              asString(data["url"]),
		"properties":       properties,
		"created_time":     asString(data["created_time"]),
		"last_edited_time": asString(data["last_edited_time"]),
	}, nil
}

func notionCreateDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	parentPageID := stringArg(input, "parent_page_id")
	title := stringArg(input, "title")
	if parentPageID == "" || title == "" {
		return map[string]any{"error": "parent_page_id and title are required"}, nil
	}
	properties := map[string]any{"Name": map[string]any{"title": map[string]any{}}}
	if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
		extra, err := notionDecodeJSONObject(propertiesJSON)
		if err != nil {
			return map[string]any{"error": "properties_json is not valid JSON"}, nil
		}
		for key, value := range extra {
			properties[key] = value
		}
	}
	body := map[string]any{
		"parent":     map[string]any{"type": "page_id", "page_id": parentPageID},
		"title":      []any{map[string]any{"type": "text", "text": map[string]any{"content": title}}},
		"properties": properties,
	}
	data := notionRequest(ctx, rt, http.MethodPost, "/v1/databases", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"url":    asString(data["url"]),
		"status": "created",
	}, nil
}

func notionUpdateDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	body := map[string]any{}
	if title := stringArg(input, "title"); title != "" {
		body["title"] = []any{map[string]any{"type": "text", "text": map[string]any{"content": title}}}
	}
	if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
		properties, err := notionDecodeJSONObject(propertiesJSON)
		if err != nil {
			return map[string]any{"error": "properties_json is not valid JSON"}, nil
		}
		body["properties"] = properties
	}
	if archived, present := notionOptionalBool(input, "archived"); present {
		body["archived"] = archived
	}
	if len(body) == 0 {
		return map[string]any{"error": "No updates provided. Set title, properties_json, or archived."}, nil
	}
	data := notionRequest(ctx, rt, http.MethodPatch, "/v1/databases/"+url.PathEscape(databaseID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"url":    asString(data["url"]),
		"status": "updated",
	}, nil
}
