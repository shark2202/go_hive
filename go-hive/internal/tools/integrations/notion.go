// Batch 4 of the integration port: Notion (upstream notion_tool, 13 actions)
// and Linear (upstream linear_tool, 21 actions).
//
// Both modules keep upstream action names, parameter names/defaults and
// response shapes. Requests go through Runtime.HTTP so every action is
// exercisable against an httptest server; credentials resolve from
// WithCredential or NOTION_API_TOKEN / LINEAR_API_KEY.
//
// Deliberate limits:
//   - notion_* accept the upstream `account` workspace selector, but the Go
//     runtime holds one token per module; credential-store aliases are not
//     reproduced here.
//   - Notion requests pin API version 2022-06-28, like upstream.
//   - Notion 429 responses are surfaced to the caller; there is no automatic
//     retry (upstream has none either).
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	notionVersion  = "2022-06-28"
	notionMaxBody  = 8 << 20
	notionMaxError = 500
	notionMaxItems = 100
)

// InstallBatch4 publishes the batch-4 SaaS tools (notion and linear) into a
// shared registry and returns the number of tools registered.
func InstallBatch4(target *tools.ToolRegistry) int {
	return newBatch4Registry().Install(target)
}

// newBatch4Registry builds a registry containing only the batch-4 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch4Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: notionModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{notionTool, linearTool} {
		registry.AddTool(tool)
	}
	for action, impl := range notionImpls() {
		registry.AddImpl("notion", action, impl)
	}
	for action, impl := range linearImpls() {
		registry.AddImpl("linear", action, impl)
	}
	return registry
}

// notionModules declares the public endpoints and credential variables for the
// two modules. Tests override BaseURL with WithBaseURL.
func notionModules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"notion_tool": {ID: "notion_tool", BaseURL: "https://api.notion.com", EnvVars: []string{"NOTION_API_TOKEN"}},
		"linear_tool": {ID: "linear_tool", BaseURL: "https://api.linear.app", EnvVars: []string{"LINEAR_API_KEY"}},
	}
}

// ------------------------------------------------------------ shared bits ---

// notionOptionalBool distinguishes an absent/null argument from false.
func notionOptionalBool(input map[string]any, name string) (bool, bool) {
	value, present := input[name]
	if !present || value == nil {
		return false, false
	}
	flag, ok := value.(bool)
	if !ok {
		return false, false
	}
	return flag, true
}

// notionOptionalStrings distinguishes an absent/null list from an empty list.
func notionOptionalStrings(input map[string]any, name string) ([]string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false
	}
	return stringSliceArg(input, name), true
}

// ---------------------------------------------------------------- notion ----

type notionBlockType string

const (
	notionBlockBulletedItem notionBlockType = "bulleted_list_item"
	notionBlockCallout      notionBlockType = "callout"
	notionBlockHeading1     notionBlockType = "heading_1"
	notionBlockHeading2     notionBlockType = "heading_2"
	notionBlockHeading3     notionBlockType = "heading_3"
	notionBlockNumberedItem notionBlockType = "numbered_list_item"
	notionBlockParagraph    notionBlockType = "paragraph"
	notionBlockQuote        notionBlockType = "quote"
	notionBlockToDo         notionBlockType = "to_do"
)

// notionBlockTypes mirrors Python's sorted(BlockType) order.
var notionBlockTypes = []notionBlockType{
	notionBlockBulletedItem,
	notionBlockCallout,
	notionBlockHeading1,
	notionBlockHeading2,
	notionBlockHeading3,
	notionBlockNumberedItem,
	notionBlockParagraph,
	notionBlockQuote,
	notionBlockToDo,
}

func notionParseBlockType(value string) (notionBlockType, bool) {
	for _, candidate := range notionBlockTypes {
		if string(candidate) == value {
			return candidate, true
		}
	}
	return "", false
}

func notionBlockTypeHelp() string {
	names := make([]string, 0, len(notionBlockTypes))
	for _, blockType := range notionBlockTypes {
		names = append(names, string(blockType))
	}
	return strings.Join(names, ", ")
}

func notionInvalidBlockType(value string) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("Invalid block_type: '%s'", value),
		"help":  "Must be one of: " + notionBlockTypeHelp(),
	}
}

func notionAuthError() map[string]any {
	return map[string]any{
		"error": "NOTION_API_TOKEN not set",
		"help":  "Create an integration at https://www.notion.so/my-integrations",
	}
}

var notionTool = &Tool{
	ID:          "notion_tool",
	Name:        "notion",
	DisplayName: "Notion",
	Summary:     "Notion integration (13 actions).",
	Actions: []Action{
		{
			Name:     "notion_append_blocks",
			Summary:  "Append content blocks to a page or block.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"block_id", "content"},
			Params: map[string]any{
				"block_id":   param("string"),
				"content":    param("string"),
				"block_type": paramDefault("string", "paragraph"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_create_database",
			Summary:  "Create a new database as a child of an existing page.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"parent_page_id", "title"},
			Params: map[string]any{
				"parent_page_id":  param("string"),
				"title":           param("string"),
				"properties_json": paramDefault("string", ""),
				"account":         paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_create_page",
			Summary:  "Create a new page in a Notion database or as a child of another page.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":              param("string"),
				"parent_database_id": paramDefault("string", ""),
				"parent_page_id":     paramDefault("string", ""),
				"title_property":     paramDefault("string", ""),
				"properties_json":    paramDefault("string", ""),
				"content":            paramDefault("string", ""),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_delete_block",
			Summary:  "Delete a block (moves to trash).",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"block_id"},
			Params: map[string]any{
				"block_id": param("string"),
				"account":  paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_get_block",
			Summary:  "Retrieve a single block by ID.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"block_id"},
			Params: map[string]any{
				"block_id": param("string"),
				"account":  paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_get_block_children",
			Summary:  "Get child blocks (content) of a page or block.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"block_id"},
			Params: map[string]any{
				"block_id":  param("string"),
				"page_size": paramDefault("integer", 50),
				"account":   paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_get_database",
			Summary:  "Get a Notion database schema.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"database_id"},
			Params: map[string]any{
				"database_id": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_get_page",
			Summary:  "Get a Notion page by ID.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"page_id"},
			Params: map[string]any{
				"page_id": param("string"),
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_query_database",
			Summary:  "Query rows/pages from a Notion database.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"database_id"},
			Params: map[string]any{
				"database_id":  param("string"),
				"filter_json":  paramDefault("string", ""),
				"sorts_json":   paramDefault("string", ""),
				"start_cursor": paramDefault("string", ""),
				"page_size":    paramDefault("integer", 50),
				"account":      paramDefault("string", ""),
			},
		},
		{
			Name:    "notion_search",
			Summary: "Search Notion pages and databases.",
			Source:  "notion_tool/notion_tool.py",
			Params: map[string]any{
				"query":       paramDefault("string", ""),
				"filter_type": paramDefault("string", ""),
				"page_size":   paramDefault("integer", 20),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_update_block",
			Summary:  "Update a block's content or archive it.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"block_id"},
			Params: map[string]any{
				"block_id":   param("string"),
				"content":    paramDefault("string", ""),
				"block_type": paramDefault("string", ""),
				"archived":   param("boolean"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_update_database",
			Summary:  "Update a database's title, properties, or archive it.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"database_id"},
			Params: map[string]any{
				"database_id":     param("string"),
				"title":           paramDefault("string", ""),
				"properties_json": paramDefault("string", ""),
				"archived":        param("boolean"),
				"account":         paramDefault("string", ""),
			},
		},
		{
			Name:     "notion_update_page",
			Summary:  "Update a Notion page's properties.",
			Source:   "notion_tool/notion_tool.py",
			Required: []string{"page_id"},
			Params: map[string]any{
				"page_id":         param("string"),
				"properties_json": paramDefault("string", ""),
				"archived":        param("boolean"),
				"account":         paramDefault("string", ""),
			},
		},
	},
}

func notionImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"notion_append_blocks":      notionAppendBlocks,
		"notion_create_database":    notionCreateDatabase,
		"notion_create_page":        notionCreatePage,
		"notion_delete_block":       notionDeleteBlock,
		"notion_get_block":          notionGetBlock,
		"notion_get_block_children": notionGetBlockChildren,
		"notion_get_database":       notionGetDatabase,
		"notion_get_page":           notionGetPage,
		"notion_query_database":     notionQueryDatabase,
		"notion_search":             notionSearch,
		"notion_update_block":       notionUpdateBlock,
		"notion_update_database":    notionUpdateDatabase,
		"notion_update_page":        notionUpdatePage,
	}
}

// notionRequest performs one Notion API call and maps HTTP failures to
// the upstream error dicts.
func notionRequest(ctx context.Context, rt *Runtime, method, path string, payload any) map[string]any {
	token := accountToken(rt, "notion_tool", "notion")
	if token == "" {
		return notionAuthError()
	}
	base := moduleBaseURL(rt, "notion_tool")
	if base == "" {
		base = "https://api.notion.com"
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return map[string]any{"error": "Notion request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Notion timed out"}
		}
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, notionMaxBody))
	if err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Notion integration token."}
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Ensure the page/database is shared with the integration."}
	case http.StatusNotFound:
		return map[string]any{"error": "Not found. The page or database may not exist or not be shared."}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}
	case http.StatusOK, http.StatusCreated:
		// Success: fall through to decoding.
	default:
		return map[string]any{
			"error": fmt.Sprintf("Notion API error %d: %s", resp.StatusCode, truncateText(string(raw), notionMaxError)),
		}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// notionRichText concatenates rich_text text.content values.
func notionRichText(value any) string {
	var builder strings.Builder
	for _, raw := range asSlice(value) {
		part := asMap(raw)
		text := asMap(part["text"])
		builder.WriteString(asString(text["content"]))
	}
	return builder.String()
}

// notionTitle extracts the first title property's plain text.
func notionTitle(properties map[string]any) string {
	for _, raw := range properties {
		prop := asMap(raw)
		if asString(prop["type"]) == "title" {
			return notionRichText(prop["title"])
		}
	}
	return ""
}

// notionSimpleProperties flattens page properties the way upstream does.
func notionSimpleProperties(properties map[string]any) map[string]any {
	simple := make(map[string]any, len(properties))
	for name, raw := range properties {
		prop := asMap(raw)
		switch asString(prop["type"]) {
		case "title":
			simple[name] = notionRichText(prop["title"])
		case "rich_text":
			simple[name] = notionRichText(prop["rich_text"])
		case "select":
			selectProp := asMap(prop["select"])
			if len(selectProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(selectProp["name"])
			}
		case "multi_select":
			names := make([]string, 0)
			for _, option := range asSlice(prop["multi_select"]) {
				names = append(names, asString(asMap(option)["name"]))
			}
			simple[name] = names
		case "number":
			simple[name] = prop["number"]
		case "checkbox":
			simple[name] = asBool(prop["checkbox"])
		case "date":
			dateProp := asMap(prop["date"])
			if len(dateProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(dateProp["start"])
			}
		case "status":
			statusProp := asMap(prop["status"])
			if len(statusProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(statusProp["name"])
			}
		}
	}
	return simple
}

// notionDecodeJSONObject decodes a user-supplied JSON object parameter.
func notionDecodeJSONObject(text string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	return object, nil
}

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
