// Confluence integration (upstream confluence_tool, 8 actions). Part of batch
// 5 lane 1; see atlassian_asana.go for the registry and shared helpers.
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var batch5Lane1ConfluenceTool = &Tool{
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

func batch5Lane1ConfluenceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"confluence_list_spaces":       batch5Lane1ConfluenceListSpaces,
		"confluence_list_pages":        batch5Lane1ConfluenceListPages,
		"confluence_get_page":          batch5Lane1ConfluenceGetPage,
		"confluence_create_page":       batch5Lane1ConfluenceCreatePage,
		"confluence_search":            batch5Lane1ConfluenceSearch,
		"confluence_update_page":       batch5Lane1ConfluenceUpdatePage,
		"confluence_delete_page":       batch5Lane1ConfluenceDeletePage,
		"confluence_get_page_children": batch5Lane1ConfluenceGetPageChildren,
	}
}

func batch5Lane1ConfluenceAuthError() map[string]any {
	return map[string]any{
		"error": "CONFLUENCE_DOMAIN, CONFLUENCE_EMAIL, and CONFLUENCE_API_TOKEN not set",
		"help":  "Generate an API token at https://id.atlassian.com/manage/api-tokens",
	}
}

// batch5Lane1ConfluenceClient resolves the wiki base plus Basic auth pair.
// CONFLUENCE_DOMAIN may already carry the https scheme, matching upstream.
func batch5Lane1ConfluenceClient(rt *Runtime) (base, email, token string, ok bool) {
	base = batch5Lane1ModuleBase(rt, "confluence_tool")
	if base == "" {
		if domain := batch5Lane1Getenv(rt, "CONFLUENCE_DOMAIN"); domain != "" {
			if strings.HasPrefix(domain, "https://") {
				base = strings.TrimRight(domain, "/")
			} else {
				base = "https://" + domain
			}
		}
	}
	email = batch5Lane1Getenv(rt, "CONFLUENCE_EMAIL")
	token = batch5Lane1ModuleToken(rt, "confluence_tool")
	return base, email, token, base != "" && email != "" && token != ""
}

// batch5Lane1ConfluenceRequest performs one Confluence API call and maps HTTP
// failures to the upstream error dicts.
func batch5Lane1ConfluenceRequest(ctx context.Context, rt *Runtime, base, email, token, method, path string, query url.Values, payload any) map[string]any {
	headers := map[string]string{
		"Authorization": batch5Lane1BasicAuth(email, token),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	resp, err := batch5Lane1Do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if batch5Lane1IsTimeout(ctx, err) {
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
			"error": fmt.Sprintf("Confluence API error %d: %s", resp.Status, batch5Lane1Truncate(string(resp.Body), 500)),
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{"status": "ok"}
	}
	object, err := batch5Lane1Decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Confluence request failed: " + err.Error()}
	}
	return object
}

func batch5Lane1ConfluenceListSpaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 25), 1, 250)))
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/spaces", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	spaces := make([]map[string]any, 0)
	for _, rawSpace := range batch5Lane1Slice(data["results"]) {
		space := batch5Lane1Map(rawSpace)
		spaces = append(spaces, map[string]any{
			"id":     batch5Lane1Text(space, "id"),
			"key":    batch5Lane1Text(space, "key"),
			"name":   batch5Lane1Text(space, "name"),
			"type":   batch5Lane1Text(space, "type"),
			"status": batch5Lane1Text(space, "status"),
		})
	}
	return map[string]any{"spaces": spaces, "count": len(spaces)}, nil
}

func batch5Lane1ConfluenceListPages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 25), 1, 250)))
	if title := stringArg(input, "title"); title != "" {
		query.Set("title", title)
	}
	path := "/wiki/api/v2/pages"
	if spaceID := stringArg(input, "space_id"); spaceID != "" {
		path = "/wiki/api/v2/spaces/" + spaceID + "/pages"
	}
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodGet, path, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	pages := make([]map[string]any, 0)
	for _, rawPage := range batch5Lane1Slice(data["results"]) {
		page := batch5Lane1Map(rawPage)
		pages = append(pages, map[string]any{
			"id":         batch5Lane1Text(page, "id"),
			"title":      batch5Lane1Text(page, "title"),
			"space_id":   batch5Lane1Text(page, "spaceId"),
			"status":     batch5Lane1Text(page, "status"),
			"version":    batch5Lane1Int(batch5Lane1Map(page["version"]), "number", 0),
			"created_at": batch5Lane1Text(page, "createdAt"),
		})
	}
	return map[string]any{"pages": pages, "count": len(pages)}, nil
}

func batch5Lane1ConfluenceGetPage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	bodyFormat := stringArg(input, "body_format")
	query := url.Values{}
	query.Set("body-format", bodyFormat)
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/pages/"+pageID, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	body := batch5Lane1Map(data["body"])
	bodyContent := ""
	for _, format := range []string{bodyFormat, "storage", "view"} {
		if entry, exists := body[format]; exists {
			bodyContent = batch5Lane1Text(batch5Lane1Map(entry), "value")
			break
		}
	}
	if len([]rune(bodyContent)) > 5000 {
		bodyContent = batch5Lane1Truncate(bodyContent, 5000) + "... (truncated)"
	}
	return map[string]any{
		"id":         batch5Lane1Text(data, "id"),
		"title":      batch5Lane1Text(data, "title"),
		"space_id":   batch5Lane1Text(data, "spaceId"),
		"status":     batch5Lane1Text(data, "status"),
		"version":    batch5Lane1Int(batch5Lane1Map(data["version"]), "number", 0),
		"body":       bodyContent,
		"created_at": batch5Lane1Text(data, "createdAt"),
	}, nil
}

func batch5Lane1ConfluenceCreatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
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
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodPost, "/wiki/api/v2/pages", nil, payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":     batch5Lane1Text(data, "id"),
		"title":  batch5Lane1Text(data, "title"),
		"status": "created",
	}, nil
}

func batch5Lane1ConfluenceSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
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
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 25), 1, 50)))
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/rest/api/search", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, rawResult := range batch5Lane1Slice(data["results"]) {
		result := batch5Lane1Map(rawResult)
		content := batch5Lane1Map(result["content"])
		space := batch5Lane1Map(content["space"])
		results = append(results, map[string]any{
			"title":         batch5Lane1Text(result, "title"),
			"excerpt":       batch5Lane1Truncate(batch5Lane1OptionalText(result, "excerpt"), 300),
			"page_id":       batch5Lane1Text(content, "id"),
			"space_key":     batch5Lane1Text(space, "key"),
			"space_name":    batch5Lane1Text(space, "name"),
			"last_modified": batch5Lane1Text(result, "lastModified"),
		})
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

func batch5Lane1ConfluenceUpdatePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
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
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodPut, "/wiki/api/v2/pages/"+pageID, nil, payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":      batch5Lane1Text(data, "id"),
		"title":   batch5Lane1Text(data, "title"),
		"version": batch5Lane1Int(batch5Lane1Map(data["version"]), "number", 0),
		"status":  "updated",
	}, nil
}

func batch5Lane1ConfluenceDeletePage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodDelete, "/wiki/api/v2/pages/"+pageID, nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"page_id": pageID, "status": "deleted"}, nil
}

func batch5Lane1ConfluenceGetPageChildren(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1ConfluenceClient(rt)
	if !ok {
		return batch5Lane1ConfluenceAuthError(), nil
	}
	pageID := stringArg(input, "page_id")
	if pageID == "" {
		return map[string]any{"error": "page_id is required"}, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 25), 1, 250)))
	data := batch5Lane1ConfluenceRequest(ctx, rt, base, email, token, http.MethodGet, "/wiki/api/v2/pages/"+pageID+"/children", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	children := make([]map[string]any, 0)
	for _, rawPage := range batch5Lane1Slice(data["results"]) {
		page := batch5Lane1Map(rawPage)
		children = append(children, map[string]any{
			"id":      batch5Lane1Text(page, "id"),
			"title":   batch5Lane1Text(page, "title"),
			"status":  batch5Lane1Text(page, "status"),
			"version": batch5Lane1Int(batch5Lane1Map(page["version"]), "number", 0),
		})
	}
	return map[string]any{"children": children, "count": len(children)}, nil
}
