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
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch4NotionVersion  = "2022-06-28"
	batch4NotionMaxBody  = 8 << 20
	batch4NotionMaxError = 500
	batch4NotionMaxItems = 100
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
		Modules: batch4Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{batch4NotionTool, batch4LinearTool} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for action, impl := range batch4NotionImpls() {
		registry.addImpl("notion", action, impl)
	}
	for action, impl := range batch4LinearImpls() {
		registry.addImpl("linear", action, impl)
	}
	return registry
}

// batch4Modules declares the public endpoints and credential variables for the
// two modules. Tests override BaseURL with WithBaseURL.
func batch4Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"notion_tool": {ID: "notion_tool", BaseURL: "https://api.notion.com", EnvVars: []string{"NOTION_API_TOKEN"}},
		"linear_tool": {ID: "linear_tool", BaseURL: "https://api.linear.app", EnvVars: []string{"LINEAR_API_KEY"}},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch4ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch4ModuleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch4ModuleToken resolves the module credential from WithCredential or the
// configured environment lookup.
func batch4ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}

// batch4HTTP returns the runtime transport, falling back to a client with the
// standard timeout for hand-built runtimes.
func batch4HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch4IsTimeout reports whether err is a deadline or a net timeout.
func batch4IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch4TruncateText caps provider error bodies the same way upstream slices
// response.text[:500].
func batch4TruncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// batch4AsMap safely views a decoded JSON object.
func batch4AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch4AsSlice safely views a decoded JSON array.
func batch4AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch4AsString reads a decoded JSON string.
func batch4AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch4AsBool reads a decoded JSON boolean.
func batch4AsBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch4Clamp bounds an integer to [low, high].
func batch4Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch4OptionalString distinguishes an absent/null argument from "".
func batch4OptionalString(input map[string]any, name string) (string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value), true
	}
	return text, true
}

// batch4OptionalBool distinguishes an absent/null argument from false.
func batch4OptionalBool(input map[string]any, name string) (bool, bool) {
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

// batch4OptionalInt distinguishes an absent/null argument from 0.
func batch4OptionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// batch4OptionalStrings distinguishes an absent/null list from an empty list.
func batch4OptionalStrings(input map[string]any, name string) ([]string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false
	}
	return stringSliceArg(input, name), true
}

// ---------------------------------------------------------------- notion ----

type batch4BlockType string

const (
	batch4BlockBulletedItem batch4BlockType = "bulleted_list_item"
	batch4BlockCallout      batch4BlockType = "callout"
	batch4BlockHeading1     batch4BlockType = "heading_1"
	batch4BlockHeading2     batch4BlockType = "heading_2"
	batch4BlockHeading3     batch4BlockType = "heading_3"
	batch4BlockNumberedItem batch4BlockType = "numbered_list_item"
	batch4BlockParagraph    batch4BlockType = "paragraph"
	batch4BlockQuote        batch4BlockType = "quote"
	batch4BlockToDo         batch4BlockType = "to_do"
)

// batch4NotionBlockTypes mirrors Python's sorted(BlockType) order.
var batch4NotionBlockTypes = []batch4BlockType{
	batch4BlockBulletedItem,
	batch4BlockCallout,
	batch4BlockHeading1,
	batch4BlockHeading2,
	batch4BlockHeading3,
	batch4BlockNumberedItem,
	batch4BlockParagraph,
	batch4BlockQuote,
	batch4BlockToDo,
}

func batch4ParseBlockType(value string) (batch4BlockType, bool) {
	for _, candidate := range batch4NotionBlockTypes {
		if string(candidate) == value {
			return candidate, true
		}
	}
	return "", false
}

func batch4BlockTypeHelp() string {
	names := make([]string, 0, len(batch4NotionBlockTypes))
	for _, blockType := range batch4NotionBlockTypes {
		names = append(names, string(blockType))
	}
	return strings.Join(names, ", ")
}

func batch4InvalidBlockType(value string) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("Invalid block_type: '%s'", value),
		"help":  "Must be one of: " + batch4BlockTypeHelp(),
	}
}

func batch4NotionAuthError() map[string]any {
	return map[string]any{
		"error": "NOTION_API_TOKEN not set",
		"help":  "Create an integration at https://www.notion.so/my-integrations",
	}
}

var batch4NotionTool = &Tool{
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

func batch4NotionImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"notion_append_blocks":      batch4NotionAppendBlocks,
		"notion_create_database":    batch4NotionCreateDatabase,
		"notion_create_page":        batch4NotionCreatePage,
		"notion_delete_block":       batch4NotionDeleteBlock,
		"notion_get_block":          batch4NotionGetBlock,
		"notion_get_block_children": batch4NotionGetBlockChildren,
		"notion_get_database":       batch4NotionGetDatabase,
		"notion_get_page":           batch4NotionGetPage,
		"notion_query_database":     batch4NotionQueryDatabase,
		"notion_search":             batch4NotionSearch,
		"notion_update_block":       batch4NotionUpdateBlock,
		"notion_update_database":    batch4NotionUpdateDatabase,
		"notion_update_page":        batch4NotionUpdatePage,
	}
}

// batch4AccountToken resolves a module credential for the current call. An
// account alias the action selected wins over the module's single token, the
// way upstream `_get_token(account)` calls credentials.get_by_alias(provider,
// account) first.
func batch4AccountToken(rt *Runtime, moduleID, provider string) string {
	return accountToken(rt, moduleID, provider)
}

// batch4NotionRequest performs one Notion API call and maps HTTP failures to
// the upstream error dicts.
func batch4NotionRequest(ctx context.Context, rt *Runtime, method, path string, payload any) map[string]any {
	token := batch4AccountToken(rt, "notion_tool", "notion")
	if token == "" {
		return batch4NotionAuthError()
	}
	base := batch4ModuleURL(rt, "notion_tool")
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
	req.Header.Set("Notion-Version", batch4NotionVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := batch4HTTP(rt).Do(req)
	if err != nil {
		if batch4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Notion timed out"}
		}
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch4NotionMaxBody))
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
			"error": fmt.Sprintf("Notion API error %d: %s", resp.StatusCode, batch4TruncateText(string(raw), batch4NotionMaxError)),
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

// batch4NotionRichText concatenates rich_text text.content values.
func batch4NotionRichText(value any) string {
	var builder strings.Builder
	for _, raw := range batch4AsSlice(value) {
		part := batch4AsMap(raw)
		text := batch4AsMap(part["text"])
		builder.WriteString(batch4AsString(text["content"]))
	}
	return builder.String()
}

// batch4NotionTitle extracts the first title property's plain text.
func batch4NotionTitle(properties map[string]any) string {
	for _, raw := range properties {
		prop := batch4AsMap(raw)
		if batch4AsString(prop["type"]) == "title" {
			return batch4NotionRichText(prop["title"])
		}
	}
	return ""
}

// batch4NotionSimpleProperties flattens page properties the way upstream does.
func batch4NotionSimpleProperties(properties map[string]any) map[string]any {
	simple := make(map[string]any, len(properties))
	for name, raw := range properties {
		prop := batch4AsMap(raw)
		switch batch4AsString(prop["type"]) {
		case "title":
			simple[name] = batch4NotionRichText(prop["title"])
		case "rich_text":
			simple[name] = batch4NotionRichText(prop["rich_text"])
		case "select":
			selectProp := batch4AsMap(prop["select"])
			if len(selectProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = batch4AsString(selectProp["name"])
			}
		case "multi_select":
			names := make([]string, 0)
			for _, option := range batch4AsSlice(prop["multi_select"]) {
				names = append(names, batch4AsString(batch4AsMap(option)["name"]))
			}
			simple[name] = names
		case "number":
			simple[name] = prop["number"]
		case "checkbox":
			simple[name] = batch4AsBool(prop["checkbox"])
		case "date":
			dateProp := batch4AsMap(prop["date"])
			if len(dateProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = batch4AsString(dateProp["start"])
			}
		case "status":
			statusProp := batch4AsMap(prop["status"])
			if len(statusProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = batch4AsString(statusProp["name"])
			}
		}
	}
	return simple
}

// batch4DecodeJSONObject decodes a user-supplied JSON object parameter.
func batch4DecodeJSONObject(text string) (map[string]any, error) {
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

func batch4NotionSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{
		"page_size": batch4Clamp(intArg(input, "page_size", 20), 1, batch4NotionMaxItems),
	}
	if query := stringArg(input, "query"); query != "" {
		body["query"] = query
	}
	filterType := stringArg(input, "filter_type")
	if filterType == "page" || filterType == "database" {
		body["filter"] = map[string]any{"property": "object", "value": filterType}
	}

	data := batch4NotionRequest(ctx, rt, http.MethodPost, "/v1/search", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range batch4AsSlice(data["results"]) {
		item := batch4AsMap(raw)
		objectType := batch4AsString(item["object"])
		title := ""
		switch objectType {
		case "page":
			title = batch4NotionTitle(batch4AsMap(item["properties"]))
		case "database":
			title = batch4NotionRichText(item["title"])
		}
		results = append(results, map[string]any{
			"id":               batch4AsString(item["id"]),
			"object":           objectType,
			"title":            title,
			"url":              batch4AsString(item["url"]),
			"created_time":     batch4AsString(item["created_time"]),
			"last_edited_time": batch4AsString(item["last_edited_time"]),
		})
	}
	return map[string]any{
		"results":  results,
		"count":    len(results),
		"has_more": batch4AsBool(data["has_more"]),
	}, nil
}

func batch4NotionGetPage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodGet, "/v1/pages/"+url.PathEscape(pageID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	properties := batch4AsMap(data["properties"])
	return map[string]any{
		"id":               batch4AsString(data["id"]),
		"title":            batch4NotionTitle(properties),
		"url":              batch4AsString(data["url"]),
		"archived":         batch4AsBool(data["archived"]),
		"properties":       batch4NotionSimpleProperties(properties),
		"created_time":     batch4AsString(data["created_time"]),
		"last_edited_time": batch4AsString(data["last_edited_time"]),
	}, nil
}

func batch4NotionCreatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
			extra, err := batch4DecodeJSONObject(propertiesJSON)
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

	data := batch4NotionRequest(ctx, rt, http.MethodPost, "/v1/pages", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"url":    batch4AsString(data["url"]),
		"status": "created",
	}, nil
}

func batch4NotionQueryDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	body := map[string]any{
		"page_size": batch4Clamp(intArg(input, "page_size", 50), 1, batch4NotionMaxItems),
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
	data := batch4NotionRequest(ctx, rt, http.MethodPost, path, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	pages := make([]map[string]any, 0)
	for _, raw := range batch4AsSlice(data["results"]) {
		item := batch4AsMap(raw)
		pages = append(pages, map[string]any{
			"id":               batch4AsString(item["id"]),
			"title":            batch4NotionTitle(batch4AsMap(item["properties"])),
			"url":              batch4AsString(item["url"]),
			"created_time":     batch4AsString(item["created_time"]),
			"last_edited_time": batch4AsString(item["last_edited_time"]),
		})
	}
	return map[string]any{
		"pages":       pages,
		"count":       len(pages),
		"has_more":    batch4AsBool(data["has_more"]),
		"next_cursor": data["next_cursor"],
	}, nil
}

func batch4NotionGetDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodGet, "/v1/databases/"+url.PathEscape(databaseID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	properties := make(map[string]any)
	for name, raw := range batch4AsMap(data["properties"]) {
		prop := batch4AsMap(raw)
		properties[name] = map[string]any{
			"type": batch4AsString(prop["type"]),
			"id":   batch4AsString(prop["id"]),
		}
	}
	return map[string]any{
		"id":               batch4AsString(data["id"]),
		"title":            batch4NotionRichText(data["title"]),
		"url":              batch4AsString(data["url"]),
		"properties":       properties,
		"created_time":     batch4AsString(data["created_time"]),
		"last_edited_time": batch4AsString(data["last_edited_time"]),
	}, nil
}

func batch4NotionCreateDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	parentPageID := stringArg(input, "parent_page_id")
	title := stringArg(input, "title")
	if parentPageID == "" || title == "" {
		return map[string]any{"error": "parent_page_id and title are required"}, nil
	}
	properties := map[string]any{"Name": map[string]any{"title": map[string]any{}}}
	if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
		extra, err := batch4DecodeJSONObject(propertiesJSON)
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
	data := batch4NotionRequest(ctx, rt, http.MethodPost, "/v1/databases", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"url":    batch4AsString(data["url"]),
		"status": "created",
	}, nil
}

func batch4NotionUpdateDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	databaseID := stringArg(input, "database_id")
	if databaseID == "" {
		return map[string]any{"error": "database_id is required"}, nil
	}
	body := map[string]any{}
	if title := stringArg(input, "title"); title != "" {
		body["title"] = []any{map[string]any{"type": "text", "text": map[string]any{"content": title}}}
	}
	if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
		properties, err := batch4DecodeJSONObject(propertiesJSON)
		if err != nil {
			return map[string]any{"error": "properties_json is not valid JSON"}, nil
		}
		body["properties"] = properties
	}
	if archived, present := batch4OptionalBool(input, "archived"); present {
		body["archived"] = archived
	}
	if len(body) == 0 {
		return map[string]any{"error": "No updates provided. Set title, properties_json, or archived."}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodPatch, "/v1/databases/"+url.PathEscape(databaseID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"url":    batch4AsString(data["url"]),
		"status": "updated",
	}, nil
}

func batch4NotionUpdatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	body := map[string]any{}
	if propertiesJSON := stringArg(input, "properties_json"); propertiesJSON != "" {
		properties, err := batch4DecodeJSONObject(propertiesJSON)
		if err != nil {
			return map[string]any{"error": "properties_json is not valid JSON"}, nil
		}
		body["properties"] = properties
	}
	if archived, present := batch4OptionalBool(input, "archived"); present {
		body["archived"] = archived
	}
	if len(body) == 0 {
		return map[string]any{"error": "No updates provided. Set properties_json or archived."}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodPatch, "/v1/pages/"+url.PathEscape(pageID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"url":    batch4AsString(data["url"]),
		"status": "updated",
	}, nil
}

func batch4NotionGetBlockChildren(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	query := url.Values{"page_size": {fmt.Sprint(batch4Clamp(intArg(input, "page_size", 50), 1, batch4NotionMaxItems))}}
	path := fmt.Sprintf("/v1/blocks/%s/children?%s", url.PathEscape(blockID), query.Encode())
	data := batch4NotionRequest(ctx, rt, http.MethodGet, path, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	blocks := make([]map[string]any, 0)
	for _, raw := range batch4AsSlice(data["results"]) {
		item := batch4AsMap(raw)
		blockType := batch4AsString(item["type"])
		block := map[string]any{
			"id":           batch4AsString(item["id"]),
			"type":         blockType,
			"has_children": batch4AsBool(item["has_children"]),
		}
		if richText := batch4AsSlice(batch4AsMap(item[blockType])["rich_text"]); len(richText) > 0 {
			block["text"] = batch4NotionRichText(richText)
		}
		blocks = append(blocks, block)
	}
	return map[string]any{
		"blocks":   blocks,
		"count":    len(blocks),
		"has_more": batch4AsBool(data["has_more"]),
	}, nil
}

func batch4NotionGetBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodGet, "/v1/blocks/"+url.PathEscape(blockID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	blockType := batch4AsString(data["type"])
	result := map[string]any{
		"id":               batch4AsString(data["id"]),
		"type":             blockType,
		"has_children":     batch4AsBool(data["has_children"]),
		"archived":         batch4AsBool(data["archived"]),
		"created_time":     batch4AsString(data["created_time"]),
		"last_edited_time": batch4AsString(data["last_edited_time"]),
	}
	if richText := batch4AsSlice(batch4AsMap(data[blockType])["rich_text"]); len(richText) > 0 {
		result["text"] = batch4NotionRichText(richText)
	}
	return result, nil
}

func batch4NotionUpdateBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
		validated, ok := batch4ParseBlockType(blockType)
		if !ok {
			return batch4InvalidBlockType(blockType), nil
		}
		body[string(validated)] = map[string]any{
			"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": content}}},
		}
	}
	if archived, present := batch4OptionalBool(input, "archived"); present {
		body["archived"] = archived
	}
	if len(body) == 0 {
		return map[string]any{"error": "No updates provided. Set content or archived."}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodPatch, "/v1/blocks/"+url.PathEscape(blockID), body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"type":   batch4AsString(data["type"]),
		"status": "updated",
	}, nil
}

func batch4NotionDeleteBlock(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	if blockID == "" {
		return map[string]any{"error": "block_id is required"}, nil
	}
	data := batch4NotionRequest(ctx, rt, http.MethodDelete, "/v1/blocks/"+url.PathEscape(blockID), nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch4AsString(data["id"]),
		"status": "deleted",
	}, nil
}

func batch4NotionAppendBlocks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	blockID := stringArg(input, "block_id")
	content := stringArg(input, "content")
	if blockID == "" || content == "" {
		return map[string]any{"error": "block_id and content are required"}, nil
	}
	blockType := stringArg(input, "block_type")
	validated, ok := batch4ParseBlockType(blockType)
	if !ok {
		return batch4InvalidBlockType(blockType), nil
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
	if len(lines) > batch4NotionMaxItems {
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
		if validated == batch4BlockToDo {
			block[string(validated)].(map[string]any)["checked"] = false
		}
		children = append(children, block)
	}
	path := fmt.Sprintf("/v1/blocks/%s/children", url.PathEscape(blockID))
	data := batch4NotionRequest(ctx, rt, http.MethodPatch, path, map[string]any{"children": children})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"block_id":     blockID,
		"blocks_added": len(children),
		"status":       "appended",
	}, nil
}
