// Obsidian integration (upstream obsidian_tool, 6 actions). Part of batch 5
// lane 4; see hubspot_obsidian.go for the registry and shared helpers.
//
// The upstream client talks to the Obsidian "Local REST API" community plugin.
// It returns markdown text for note reads, JSON for the note/JSON accept types,
// and 204 for writes, so the response mapper mirrors those shapes instead of
// assuming JSON everywhere.
package crm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// obsidianDefaultBase is the plugin's default listener.
	obsidianDefaultBase = "https://127.0.0.1:27124"
	obsidianMaxBody     = 8 << 20
	// obsidianMaxContexts matches upstream's contexts[:5] cap.
	obsidianMaxContexts   = 5
	obsidianNoteMediaType = "application/vnd.olrapi.note+json"
)

var obsidianTool = &Tool{
	ID:          "obsidian_tool",
	Name:        "obsidian",
	DisplayName: "Obsidian",
	Summary:     "Obsidian vault integration (6 actions).",
	Actions: []Action{
		{
			Name:     "obsidian_read_note",
			Summary:  "Read a note from the Obsidian vault with metadata.",
			Source:   "obsidian_tool/obsidian_tool.py",
			Required: []string{"path"},
			Params: map[string]any{
				"path": param("string"),
			},
		},
		{
			Name:     "obsidian_write_note",
			Summary:  "Create or overwrite a note in the Obsidian vault.",
			Source:   "obsidian_tool/obsidian_tool.py",
			Required: []string{"path", "content"},
			Params: map[string]any{
				"path":    param("string"),
				"content": param("string"),
			},
		},
		{
			Name:     "obsidian_append_note",
			Summary:  "Append content to an existing note, or create it.",
			Source:   "obsidian_tool/obsidian_tool.py",
			Required: []string{"path", "content"},
			Params: map[string]any{
				"path":    param("string"),
				"content": param("string"),
			},
		},
		{
			Name:     "obsidian_search",
			Summary:  "Search for text across all notes in the Obsidian vault.",
			Source:   "obsidian_tool/obsidian_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":          param("string"),
				"context_length": paramDefault("integer", 100),
			},
		},
		{
			Name:    "obsidian_list_files",
			Summary: "List files and directories in the Obsidian vault.",
			Source:  "obsidian_tool/obsidian_tool.py",
			Params: map[string]any{
				"path": paramDefault("string", ""),
			},
		},
		{
			Name:    "obsidian_get_active",
			Summary: "Get the currently active (open) file in Obsidian.",
			Source:  "obsidian_tool/obsidian_tool.py",
		},
	},
}

func obsidianImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"obsidian_read_note":   obsidianReadNote,
		"obsidian_write_note":  obsidianWriteNote,
		"obsidian_append_note": obsidianAppendNote,
		"obsidian_search":      obsidianSearch,
		"obsidian_list_files":  obsidianListFiles,
		"obsidian_get_active":  obsidianGetActive,
	}
}

func obsidianAuthError() map[string]any {
	return map[string]any{
		"error": "Obsidian credentials not configured",
		"help": "Set OBSIDIAN_REST_API_KEY environment variable or configure via credential store. " +
			"Install the 'Local REST API' plugin in Obsidian first.",
	}
}

// obsidianBaseURL resolves the instance URL: WithBaseURL, then
// OBSIDIAN_REST_BASE_URL, then the plugin default.
func obsidianBaseURL(rt *Runtime) string {
	if base := moduleURL(rt, "obsidian_tool", ""); base != "" {
		return base
	}
	if env := strings.TrimSpace(runtimeGetenv(rt)("OBSIDIAN_REST_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return obsidianDefaultBase
}

// obsidianClient mirrors upstream's (api_key, base_url) pair.
type obsidianClient struct {
	ctx  context.Context
	rt   *Runtime
	base string
	key  string
}

func newObsidianClient(ctx context.Context, rt *Runtime) (*obsidianClient, map[string]any) {
	key := moduleToken(rt, "obsidian_tool")
	if key == "" {
		return nil, obsidianAuthError()
	}
	return &obsidianClient{ctx: ctx, rt: rt, base: obsidianBaseURL(rt), key: key}, nil
}

// obsidianRequest is one provider call. Only the fields upstream sets for
// that action are populated.
type obsidianRequest struct {
	method      string
	path        string
	query       url.Values
	body        string
	contentType string
	accept      string
}

// do performs one request and mirrors upstream _handle_response: 204 becomes
// {"success": true}, known failures become fixed error dicts, and success is
// decoded as JSON when the provider says so, otherwise returned as text.
func (c *obsidianClient) do(spec obsidianRequest) any {
	var body io.Reader
	if spec.body != "" {
		body = strings.NewReader(spec.body)
	}
	req, err := http.NewRequestWithContext(c.ctx, spec.method, c.base+spec.path, body)
	if err != nil {
		return map[string]any{"error": "Obsidian request failed: " + err.Error()}
	}
	if spec.query != nil {
		req.URL.RawQuery = spec.query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if spec.contentType != "" {
		req.Header.Set("Content-Type", spec.contentType)
	}
	if spec.accept != "" {
		req.Header.Set("Accept", spec.accept)
	}

	resp, err := runtimeHTTP(c.rt).Do(req)
	if err != nil {
		if isTimeout(c.ctx, err) {
			return map[string]any{"error": "Request timed out"}
		}
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, obsidianMaxBody))
	if err != nil {
		return map[string]any{"error": "Obsidian request failed: " + err.Error()}
	}

	switch resp.StatusCode {
	case http.StatusNoContent:
		return map[string]any{"success": true}
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Obsidian REST API key"}
	case http.StatusNotFound:
		return map[string]any{"error": "File or resource not found in Obsidian vault"}
	case http.StatusMethodNotAllowed:
		return map[string]any{"error": "No active file open in Obsidian"}
	}
	if resp.StatusCode >= 400 {
		detail := string(raw)
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil {
			if message, ok := payload["message"].(string); ok {
				detail = message
			}
		}
		return map[string]any{
			"error": fmt.Sprintf("Obsidian API error (HTTP %d): %s", resp.StatusCode, detail),
		}
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
		// A JSON content type with a non-JSON body falls back to text instead
		// of failing the action; the plugin can answer markdown this way.
	}
	return string(raw)
}

// obsidianFailure reports an error dict returned by do.
func obsidianFailure(result any) (map[string]any, bool) {
	object, ok := result.(map[string]any)
	if !ok {
		return nil, false
	}
	if _, failed := object["error"]; failed {
		return object, true
	}
	return nil, false
}

func obsidianReadNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(obsidianRequest{
		method: http.MethodGet,
		path:   "/vault/" + path,
		accept: obsidianNoteMediaType,
	})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	object, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"path": path, "content": fmt.Sprint(result)}, nil
	}
	return map[string]any{
		"path":        valueOr(object, "path", path),
		"content":     valueOr(object, "content", ""),
		"tags":        valueOr(object, "tags", []any{}),
		"frontmatter": object["frontmatter"],
		"stat":        object["stat"],
	}, nil
}

func obsidianWriteNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(obsidianRequest{
		method:      http.MethodPut,
		path:        "/vault/" + path,
		body:        stringArg(input, "content"),
		contentType: "text/markdown",
	})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	return map[string]any{"success": true, "path": path}, nil
}

func obsidianAppendNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(obsidianRequest{
		method:      http.MethodPost,
		path:        "/vault/" + path,
		body:        stringArg(input, "content"),
		contentType: "text/markdown",
	})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	return map[string]any{"success": true, "path": path}, nil
}

func obsidianSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("contextLength", strconv.Itoa(intArg(input, "context_length", 100)))
	result := client.do(obsidianRequest{
		method: http.MethodPost,
		path:   "/search/simple/",
		query:  params,
		accept: "application/json",
	})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	items := asSlice(result)
	if items == nil {
		// Upstream only reshapes a JSON array; anything else counts as no hit.
		return map[string]any{"count": 0, "results": []map[string]any{}}, nil
	}
	matches := make([]map[string]any, 0, len(items))
	for _, rawItem := range items {
		item := asMap(rawItem)
		rawMatches := asSlice(item["matches"])
		contexts := make([]any, 0, len(rawMatches))
		for _, rawMatch := range rawMatches {
			contexts = append(contexts, valueOr(asMap(rawMatch), "context", ""))
		}
		if len(contexts) > obsidianMaxContexts {
			contexts = contexts[:obsidianMaxContexts]
		}
		matches = append(matches, map[string]any{
			"filename":    valueOr(item, "filename", nil),
			"score":       valueOr(item, "score", nil),
			"match_count": len(rawMatches),
			"contexts":    contexts,
		})
	}
	return map[string]any{"count": len(matches), "results": matches}, nil
}

func obsidianListFiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	// A trailing slash is what tells the plugin to return a directory listing.
	urlPath := "/vault/"
	if path != "" {
		urlPath = "/vault/" + strings.TrimRight(path, "/") + "/"
	}
	result := client.do(obsidianRequest{method: http.MethodGet, path: urlPath})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	var files []any
	switch typed := result.(type) {
	case []any:
		files = typed
	case map[string]any:
		files = asSlice(typed["files"])
	}
	if files == nil {
		files = []any{}
	}
	display := path
	if display == "" {
		display = "/"
	}
	return map[string]any{"path": display, "count": len(files), "files": files}, nil
}

func obsidianGetActive(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := newObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	result := client.do(obsidianRequest{
		method: http.MethodGet,
		path:   "/active/",
		accept: obsidianNoteMediaType,
	})
	if failure, failed := obsidianFailure(result); failed {
		return failure, nil
	}
	object, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"content": fmt.Sprint(result)}, nil
	}
	return map[string]any{
		"path":        valueOr(object, "path", ""),
		"content":     valueOr(object, "content", ""),
		"tags":        valueOr(object, "tags", []any{}),
		"frontmatter": object["frontmatter"],
	}, nil
}
