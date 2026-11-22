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
//
// File layout after the phase-2 split: this file keeps the batch-4 registry
// wiring and both tool definitions; notion_common.go holds the shared helpers
// and the Notion transport, notion_pages.go the page/block actions and
// notion_databases.go the database actions.
package collab

import (
	"os"
	"time"
)

// newBatch4Registry builds a registry containing only the batch-4 tools,
// sharing the Runtime surface used by the rest of the port.
func NewNotionRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: notionModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
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
