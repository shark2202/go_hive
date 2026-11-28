package tools

import "encoding/json"

// mcpToolsListMaxPages bounds a tools/list pagination walk. MCP does not define
// a page ceiling, so a server that never clears nextCursor would otherwise
// spin forever; 100 pages is far above any observed tools/list split. Like the
// REST cursor walk (internal/tools/integrationkit), hitting a guard stops the
// walk instead of failing the whole server.
const mcpToolsListMaxPages = 100

// mcpDecodeToolsListPage decodes one tools/list result into registry tools and
// the cursor for the next page. The cursor is forwarded verbatim: MCP defines
// it as an opaque string, so only an absent/null/non-string/empty value ends
// the walk.
//
// An empty result (missing "result", or "result": null) is an empty page, not
// an error, matching the previous HTTP client's tolerance. A malformed tools
// array still fails the listing.
func mcpDecodeToolsListPage(raw json.RawMessage) ([]MCPTool, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", nil
	}
	var page struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
		NextCursor any `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, "", err
	}
	tools := make([]MCPTool, 0, len(page.Tools))
	for _, tool := range page.Tools {
		if tool.Name == "" {
			continue
		}
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{}
		}
		tools = append(tools, MCPTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}
	next, _ := page.NextCursor.(string)
	return tools, next, nil
}

// mcpListToolsPaged fetches every tools/list page.
//
// Semantics (MCP pagination + mcp2cli 3.8.0, which the native CLI already
// follows): the first request carries no cursor, each later request sends the
// previous page's nextCursor, and the walk accumulates tools in page order
// until a page has no next cursor. An empty page with a next cursor is
// followed. A repeated cursor or the page cap ends the walk with the pages
// read so far, so a broken server cannot make the client hang or loop.
func mcpListToolsPaged(fetch func(cursor string) (json.RawMessage, error)) ([]MCPTool, error) {
	var tools []MCPTool
	cursor := ""
	seen := map[string]struct{}{}
	for page := 0; page < mcpToolsListMaxPages; page++ {
		raw, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		pageTools, next, err := mcpDecodeToolsListPage(raw)
		if err != nil {
			return nil, err
		}
		tools = append(tools, pageTools...)
		if next == "" {
			return tools, nil
		}
		if _, repeated := seen[next]; repeated {
			return tools, nil
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return tools, nil
}

// mcpToolsListParams builds one tools/list parameter object. The first page
// sends no cursor field at all (Python's HTTP client posts "params": {}), and
// later pages send the opaque cursor verbatim.
func mcpToolsListParams(cursor string) map[string]any {
	params := map[string]any{}
	if cursor != "" {
		params["cursor"] = cursor
	}
	return params
}
