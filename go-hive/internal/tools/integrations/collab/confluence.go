// Confluence integration (upstream confluence_tool, 8 actions). Part of batch
// 5 lane 1; see atlassian_asana.go for the registry and shared helpers.
package collab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var confluenceTool = &Tool{
	ID:          "confluence_tool",
	Name:        "confluence",
	DisplayName: "Confluence",
	Summary:     "Confluence integration (8 actions).",
	Actions: []Action{
		{
			Name:    "confluence_list_spaces",
			Summary: "List spaces in the Confluence instance.",
			Source:  "confluence_tool/confluence_tool.py",
			Params: map[string]any{
				"limit": paramDefault("integer", 25),
			},
		},
		{
			Name:    "confluence_list_pages",
			Summary: "List pages, optionally filtered by space or title.",
			Source:  "confluence_tool/confluence_tool.py",
			Params: map[string]any{
				"space_id": paramDefault("string", ""),
				"title":    paramDefault("string", ""),
				"limit":    paramDefault("integer", 25),
			},
		},
		{
			Name:     "confluence_get_page",
			Summary:  "Get a specific Confluence page by ID, including body content.",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"page_id"},
			Params: map[string]any{
				"page_id":     param("string"),
				"body_format": paramDefault("string", "storage"),
			},
		},
		{
			Name:     "confluence_create_page",
			Summary:  "Create a new page in Confluence.",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"space_id", "title", "body"},
			Params: map[string]any{
				"space_id":  param("string"),
				"title":     param("string"),
				"body":      param("string"),
				"parent_id": paramDefault("string", ""),
			},
		},
		{
			Name:     "confluence_search",
			Summary:  "Search Confluence content using CQL (Confluence Query Language).",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"space_key": paramDefault("string", ""),
				"limit":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "confluence_update_page",
			Summary:  "Update an existing Confluence page.",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"page_id", "title", "body", "version_number"},
			Params: map[string]any{
				"page_id":        param("string"),
				"title":          param("string"),
				"body":           param("string"),
				"version_number": param("integer"),
			},
		},
		{
			Name:     "confluence_delete_page",
			Summary:  "Delete a Confluence page.",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"page_id"},
			Params: map[string]any{
				"page_id": param("string"),
			},
		},
		{
			Name:     "confluence_get_page_children",
			Summary:  "List child pages of a Confluence page.",
			Source:   "confluence_tool/confluence_tool.py",
			Required: []string{"page_id"},
			Params: map[string]any{
				"page_id": param("string"),
				"limit":   paramDefault("integer", 25),
			},
		},
	},
}

func confluenceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"confluence_list_spaces":       confluenceListSpaces,
		"confluence_list_pages":        confluenceListPages,
		"confluence_get_page":          confluenceGetPage,
		"confluence_create_page":       confluenceCreatePage,
		"confluence_search":            confluenceSearch,
		"confluence_update_page":       confluenceUpdatePage,
		"confluence_delete_page":       confluenceDeletePage,
		"confluence_get_page_children": confluenceGetPageChildren,
	}
}

func confluenceAuthError() map[string]any {
	return map[string]any{
		"error": "CONFLUENCE_DOMAIN, CONFLUENCE_EMAIL, and CONFLUENCE_API_TOKEN not set",
		"help":  "Generate an API token at https://id.atlassian.com/manage/api-tokens",
	}
}

// confluenceClient resolves the wiki base plus Basic auth pair.
// CONFLUENCE_DOMAIN may already carry the https scheme, matching upstream.
func confluenceClient(rt *Runtime) (base, email, token string, ok bool) {
	base = moduleBaseURL(rt, "confluence_tool")
	if base == "" {
		if domain := getenv(rt, "CONFLUENCE_DOMAIN"); domain != "" {
			if strings.HasPrefix(domain, "https://") {
				base = strings.TrimRight(domain, "/")
			} else {
				base = "https://" + domain
			}
		}
	}
	email = getenv(rt, "CONFLUENCE_EMAIL")
	token = moduleToken(rt, "confluence_tool")
	return base, email, token, base != "" && email != "" && token != ""
}

// confluenceRequest performs one Confluence API call and maps HTTP
// failures to the upstream error dicts.
func confluenceRequest(ctx context.Context, rt *Runtime, base, email, token, method, path string, query url.Values, payload any) map[string]any {
	headers := map[string]string{
		"Authorization": basicAuth(email, token),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	resp, err := do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Confluence timed out"}
		}
		return map[string]any{"error": "Confluence request failed: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Confluence credentials."}
	case http.StatusNotFound:
		return map[string]any{"error": "Not found"}
	case http.StatusOK, http.StatusCreated:
		// Success: fall through to decoding.
	case http.StatusNoContent:
		return map[string]any{"status": "ok"}
	default:
		return map[string]any{
			"error": fmt.Sprintf("Confluence API error %d: %s", resp.Status, truncateTextClamped(string(resp.Body), 500)),
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{"status": "ok"}
	}
	object, err := decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Confluence request failed: " + err.Error()}
	}
	return object
}

func confluenceListSpaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 250)))
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/spaces", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	spaces := make([]map[string]any, 0)
	for _, rawSpace := range asSlice(data["results"]) {
		space := asMap(rawSpace)
		spaces = append(spaces, map[string]any{
			"id":     atlassianText(space, "id"),
			"key":    atlassianText(space, "key"),
			"name":   atlassianText(space, "name"),
			"type":   atlassianText(space, "type"),
			"status": atlassianText(space, "status"),
		})
	}
	return map[string]any{"spaces": spaces, "count": len(spaces)}, nil
}

func confluenceListPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 250)))
	if title := stringArg(input, "title"); title != "" {
		query.Set("title", title)
	}
	path := "/wiki/api/v2/pages"
	if spaceID := stringArg(input, "space_id"); spaceID != "" {
		path = "/wiki/api/v2/spaces/" + spaceID + "/pages"
	}
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodGet, path, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	pages := make([]map[string]any, 0)
	for _, rawPage := range asSlice(data["results"]) {
		page := asMap(rawPage)
		pages = append(pages, map[string]any{
			"id":         atlassianText(page, "id"),
			"title":      atlassianText(page, "title"),
			"space_id":   atlassianText(page, "spaceId"),
			"status":     atlassianText(page, "status"),
			"version":    atlassianInt(asMap(page["version"]), "number", 0),
			"created_at": atlassianText(page, "createdAt"),
		})
	}
	return map[string]any{"pages": pages, "count": len(pages)}, nil
}

func confluenceGetPage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	bodyFormat := stringArg(input, "body_format")
	query := url.Values{}
	query.Set("body-format", bodyFormat)
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/pages/"+pageID, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	body := asMap(data["body"])
	bodyContent := ""
	for _, format := range []string{bodyFormat, "storage", "view"} {
		if entry, exists := body[format]; exists {
			bodyContent = atlassianText(asMap(entry), "value")
			break
		}
	}
	if len([]rune(bodyContent)) > 5000 {
		bodyContent = truncateTextClamped(bodyContent, 5000) + "... (truncated)"
	}
	return map[string]any{
		"id":         atlassianText(data, "id"),
		"title":      atlassianText(data, "title"),
		"space_id":   atlassianText(data, "spaceId"),
		"status":     atlassianText(data, "status"),
		"version":    atlassianInt(asMap(data["version"]), "number", 0),
		"body":       bodyContent,
		"created_at": atlassianText(data, "createdAt"),
	}, nil
}

func confluenceCreatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	spaceID := stringArg(input, "space_id")
	title := stringArg(input, "title")
	body := stringArg(input, "body")
	if spaceID == "" || title == "" || body == "" {
		return map[string]any{"error": "space_id, title, and body are required"}, nil
	}
	payload := map[string]any{
		"spaceId": spaceID,
		"status":  "current",
		"title":   title,
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
	}
	if parentID := stringArg(input, "parent_id"); parentID != "" {
		payload["parentId"] = parentID
	}
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodPost, "/wiki/api/v2/pages", nil, payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     atlassianText(data, "id"),
		"title":  atlassianText(data, "title"),
		"status": "created",
	}, nil
}

func confluenceSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	searchText := stringArg(input, "query")
	if searchText == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	cqlParts := []string{fmt.Sprintf(`type = page AND text ~ "%s"`, searchText)}
	if spaceKey := stringArg(input, "space_key"); spaceKey != "" {
		cqlParts = append(cqlParts, fmt.Sprintf(`space = "%s"`, spaceKey))
	}
	cql := strings.Join(cqlParts, " AND ") + " ORDER BY lastModified desc"
	query := url.Values{}
	query.Set("cql", cql)
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 50)))
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/rest/api/search", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, rawResult := range asSlice(data["results"]) {
		result := asMap(rawResult)
		content := asMap(result["content"])
		space := asMap(content["space"])
		results = append(results, map[string]any{
			"title":         atlassianText(result, "title"),
			"excerpt":       truncateTextClamped(optionalText(result, "excerpt"), 300),
			"page_id":       atlassianText(content, "id"),
			"space_key":     atlassianText(space, "key"),
			"space_name":    atlassianText(space, "name"),
			"last_modified": atlassianText(result, "lastModified"),
		})
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

func confluenceUpdatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	title := stringArg(input, "title")
	body := stringArg(input, "body")
	if pageID == "" || title == "" || body == "" {
		return map[string]any{"error": "page_id, title, and body are required"}, nil
	}
	versionNumber := intArg(input, "version_number", 0)
	if versionNumber < 1 {
		return map[string]any{"error": "version_number must be >= 1"}, nil
	}
	payload := map[string]any{
		"id":     pageID,
		"status": "current",
		"title":  title,
		"body": map[string]any{
			"representation": "storage",
			"value":          body,
		},
		"version": map[string]any{
			"number":  versionNumber,
			"message": "Updated via API",
		},
	}
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodPut, "/wiki/api/v2/pages/"+pageID, nil, payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":      atlassianText(data, "id"),
		"title":   atlassianText(data, "title"),
		"version": atlassianInt(asMap(data["version"]), "number", 0),
		"status":  "updated",
	}, nil
}

func confluenceDeletePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodDelete, "/wiki/api/v2/pages/"+pageID, nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"page_id": pageID, "status": "deleted"}, nil
}

func confluenceGetPageChildren(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := confluenceClient(rt)
	if !ok {
		return confluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 25), 1, 250)))
	data := confluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/pages/"+pageID+"/children", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	children := make([]map[string]any, 0)
	for _, rawPage := range asSlice(data["results"]) {
		page := asMap(rawPage)
		children = append(children, map[string]any{
			"id":      atlassianText(page, "id"),
			"title":   atlassianText(page, "title"),
			"status":  atlassianText(page, "status"),
			"version": atlassianInt(asMap(page["version"]), "number", 0),
		})
	}
	return map[string]any{"children": children, "count": len(children)}, nil
}
