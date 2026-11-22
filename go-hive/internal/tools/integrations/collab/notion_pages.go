// Notion page and block actions: search, page get/create/update and block
// get/update/delete/append.
package collab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func notionSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"page_size": clampInt(intArg(input, "page_size", 20), 1, notionMaxItems),
	}
	if query := stringArg(input, "query"); query != "" {
		body["query"] = query
	}
	filterType := stringArg(input, "filter_type")
	if filterType == "page" || filterType == "database" {
		body["filter"] = map[string]any{"property": "object", "value": filterType}
	}

	data := notionRequest(ctx, rt, http.MethodPost, "/v1/search", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		objectType := asString(item["object"])
		title := ""
		switch objectType {
		case "page":
			title = notionTitle(asMap(item["properties"]))
		case "database":
			title = notionRichText(item["title"])
		}
		results = append(results, map[string]any{
			"id":               asString(item["id"]),
			"object":           objectType,
			"title":            title,
			"url":              asString(item["url"]),
			"created_time":     asString(item["created_time"]),
			"last_edited_time": asString(item["last_edited_time"]),
		})
	}
	return map[string]any{
		"results":  results,
		"count":    len(results),
		"has_more": asBool(data["has_more"]),
	}, nil
}

func notionGetPage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	data := notionRequest(ctx, rt, http.MethodGet, "/v1/pages/"+url.PathEscape(pageID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	properties := asMap(data["properties"])
	return map[string]any{
		"id":               asString(data["id"]),
		"title":            notionTitle(properties),
		"url":              asString(data["url"]),
		"archived":         asBool(data["archived"]),
		"properties":       notionSimpleProperties(properties),
		"created_time":     asString(data["created_time"]),
		"last_edited_time": asString(data["last_edited_time"]),
	}, nil
}

func notionCreatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	title := stringArg(input, "title")
	if title == "" {
		return map[string]any{"error": "title is required"}, nil
	}
	parentDatabaseID := stringArg(input, "parent_database_id")
	parentPageID := stringArg(input, "parent_page_id")
	if parentDatabaseID == "" && parentPageID == "" {
		return map[string]any{"error": "Provide parent_database_id or parent_page_id"}, nil
	}
	if parentDatabaseID != "" && parentPageID != "" {
		return map[string]any{"error": "Provide only one of parent_database_id or parent_page_id, not both"}, nil
	}

	body := map[string]any{}
	if parentDatabaseID != "" {
		titleProperty := stringArg(input, "title_property")
		if titleProperty == "" {
			return map[string]any{
				"error": "title_property is required when using parent_database_id. Use notion_get_database to find the title column name.",
			}, nil
		}
		body["parent"] = map[string]any{"database_id": parentDatabaseID}
		properties := map[string]any{
			titleProperty: map[string]any{
				"title": []any{map[string]any{"text": map[string]any{"content": title}}},
			},
		}
		if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
			extra, err := notionDecodeJSONObject(propertiesJSON)
			if err != nil {
				return map[string]any{"error": "properties_json is not valid JSON"}, nil
			}
			for key, value := range extra {
				properties[key] = value
			}
		}
		body["properties"] = properties
	} else {
		body["parent"] = map[string]any{"page_id": parentPageID}
		body["properties"] = map[string]any{
			"title": map[string]any{
				"title": []any{map[string]any{"text": map[string]any{"content": title}}},
			},
		}
	}

	if content := stringArg(input, "content"); content != "" {
		body["children"] = []any{
			map[string]any{
				"object": "block",
				"type":   "paragraph",
				"paragraph": map[string]any{
					"rich_text": []any{
						map[string]any{"type": "text", "text": map[string]any{"content": content}},
					},
				},
			},
		}
	}

	data := notionRequest(ctx, rt, http.MethodPost, "/v1/pages", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"url":    asString(data["url"]),
		"status": "created",
	}, nil
}

func notionUpdatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	body := map[string]any{}
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
		return map[string]any{"error": "No updates provided. Set properties_json or archived."}, nil
	}
	data := notionRequest(ctx, rt, http.MethodPatch, "/v1/pages/"+url.PathEscape(pageID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"url":    asString(data["url"]),
		"status": "updated",
	}, nil
}

func notionGetBlockChildren(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	query := url.Values{"page_size": {fmt.Sprint(clampInt(intArg(input, "page_size", 50), 1, notionMaxItems))}}
	path := fmt.Sprintf("/v1/blocks/%s/children?%s", url.PathEscape(blockID), query.Encode())
	data := notionRequest(ctx, rt, http.MethodGet, path, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	blocks := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		blockType := asString(item["type"])
		block := map[string]any{
			"id":           asString(item["id"]),
			"type":         blockType,
			"has_children": asBool(item["has_children"]),
		}
		if richText := asSlice(asMap(item[blockType])["rich_text"]); len(richText) > 0 {
			block["text"] = notionRichText(richText)
		}
		blocks = append(blocks, block)
	}
	return map[string]any{
		"blocks":   blocks,
		"count":    len(blocks),
		"has_more": asBool(data["has_more"]),
	}, nil
}

func notionGetBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	data := notionRequest(ctx, rt, http.MethodGet, "/v1/blocks/"+url.PathEscape(blockID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	blockType := asString(data["type"])
	result := map[string]any{
		"id":               asString(data["id"]),
		"type":             blockType,
		"has_children":     asBool(data["has_children"]),
		"archived":         asBool(data["archived"]),
		"created_time":     asString(data["created_time"]),
		"last_edited_time": asString(data["last_edited_time"]),
	}
	if richText := asSlice(asMap(data[blockType])["rich_text"]); len(richText) > 0 {
		result["text"] = notionRichText(richText)
	}
	return result, nil
}

func notionUpdateBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	body := map[string]any{}
	if content := stringArg(input, "content"); content != "" {
		blockType := stringArg(input, "block_type")
		if blockType == "" {
			return map[string]any{"error": "block_type is required when setting content. Use notion_get_block to find the type."}, nil
		}
		validated, ok := notionParseBlockType(blockType)
		if !ok {
			return notionInvalidBlockType(blockType), nil
		}
		body[string(validated)] = map[string]any{
			"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": content}}},
		}
	}
	if archived, present := notionOptionalBool(input, "archived"); present {
		body["archived"] = archived
	}
	if len(body) == 0 {
		return map[string]any{"error": "No updates provided. Set content or archived."}, nil
	}
	data := notionRequest(ctx, rt, http.MethodPatch, "/v1/blocks/"+url.PathEscape(blockID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"type":   asString(data["type"]),
		"status": "updated",
	}, nil
}

func notionDeleteBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	data := notionRequest(ctx, rt, http.MethodDelete, "/v1/blocks/"+url.PathEscape(blockID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     asString(data["id"]),
		"status": "deleted",
	}, nil
}

func notionAppendBlocks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	content := stringArg(input, "content")
	if blockID == "" || content == "" {
		return map[string]any{"error": "block_id and content are required"}, nil
	}
	blockType := stringArg(input, "block_type")
	validated, ok := notionParseBlockType(blockType)
	if !ok {
		return notionInvalidBlockType(blockType), nil
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return map[string]any{"error": "content is empty after stripping blank lines"}, nil
	}
	if len(lines) > notionMaxItems {
		return map[string]any{"error": "Too many blocks. Notion API allows max 100 per request."}, nil
	}
	children := make([]any, 0, len(lines))
	for _, line := range lines {
		block := map[string]any{
			"object": "block",
			"type":   string(validated),
			string(validated): map[string]any{
				"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": line}}},
			},
		}
		if validated == notionBlockToDo {
			block[string(validated)].(map[string]any)["checked"] = false
		}
		children = append(children, block)
	}
	path := fmt.Sprintf("/v1/blocks/%s/children", url.PathEscape(blockID))
	data := notionRequest(ctx, rt, http.MethodPatch, path, map[string]any{"children": children})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"block_id":     blockID,
		"blocks_added": len(children),
		"status":       "appended",
	}, nil
}
