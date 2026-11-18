// Obsidian integration (upstream obsidian_tool, 6 actions). Part of batch 5
// lane 4; see batch5_lane4.go for the registry and shared helpers.
//
// The upstream client talks to the Obsidian "Local REST API" community plugin.
// It returns markdown text for note reads, JSON for the note/JSON accept types,
// and 204 for writes, so the response mapper mirrors those shapes instead of
// assuming JSON everywhere.
package integrations

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
	// batch5ObsidianDefaultBase is the plugin's default listener.
	batch5ObsidianDefaultBase = "https://127.0.0.1:27124"
	batch5ObsidianMaxBody     = 8 << 20
	// batch5ObsidianMaxContexts matches upstream's contexts[:5] cap.
	batch5ObsidianMaxContexts   = 5
	batch5ObsidianNoteMediaType = "application/vnd.olrapi.note+json"
)

var batch5ObsidianTool = &Tool{
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

func batch5ObsidianImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"obsidian_read_note":   batch5ObsidianReadNote,
		"obsidian_write_note":  batch5ObsidianWriteNote,
		"obsidian_append_note": batch5ObsidianAppendNote,
		"obsidian_search":      batch5ObsidianSearch,
		"obsidian_list_files":  batch5ObsidianListFiles,
		"obsidian_get_active":  batch5ObsidianGetActive,
	}
}

func batch5ObsidianAuthError() map[string]any {
	return map[string]any{
		"error": "Obsidian credentials not configured",
		"help": "Set OBSIDIAN_REST_API_KEY environment variable or configure via credential store. " +
			"Install the 'Local REST API' plugin in Obsidian first.",
	}
}

// batch5ObsidianBaseURL resolves the instance URL: WithBaseURL, then
// OBSIDIAN_REST_BASE_URL, then the plugin default.
func batch5ObsidianBaseURL(rt *Runtime) string {
	if base := batch5Lane4ModuleURL(rt, "obsidian_tool", ""); base != "" {
		return base
	}
	if env := strings.TrimSpace(batch5Lane4Getenv(rt)("OBSIDIAN_REST_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return batch5ObsidianDefaultBase
}

// batch5ObsidianClient mirrors upstream's (api_key, base_url) pair.
type batch5ObsidianClient struct {
	ctx  context.Context
	rt   *Runtime
	base string
	key  string
}

func batch5NewObsidianClient(ctx context.Context, rt *Runtime) (*batch5ObsidianClient, map[string]any) {
	key := batch5Lane4ModuleToken(rt, "obsidian_tool")
	if key == "" {
		return nil, batch5ObsidianAuthError()
	}
	return &batch5ObsidianClient{ctx: ctx, rt: rt, base: batch5ObsidianBaseURL(rt), key: key}, nil
}

// batch5ObsidianRequest is one provider call. Only the fields upstream sets for
// that action are populated.
type batch5ObsidianRequest struct {
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
func (c *batch5ObsidianClient) do(spec batch5ObsidianRequest) any {
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

	resp, err := batch4HTTP(c.rt).Do(req)
	if err != nil {
		if batch4IsTimeout(c.ctx, err) {
			return map[string]any{"error": "Request timed out"}
		}
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch5ObsidianMaxBody))
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

// batch5ObsidianFailure reports an error dict returned by do.
func batch5ObsidianFailure(result any) (map[string]any, bool) {
	object, ok := result.(map[string]any)
	if !ok {
		return nil, false
	}
	if _, failed := object["error"]; failed {
		return object, true
	}
	return nil, false
}

func batch5ObsidianReadNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(batch5ObsidianRequest{
		method: http.MethodGet,
		path:   "/vault/" + path,
		accept: batch5ObsidianNoteMediaType,
	})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	object, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"path": path, "content": fmt.Sprint(result)}, nil
	}
	return map[string]any{
		"path":        batch5Lane4Get(object, "path", path),
		"content":     batch5Lane4Get(object, "content", ""),
		"tags":        batch5Lane4Get(object, "tags", []any{}),
		"frontmatter": object["frontmatter"],
		"stat":        object["stat"],
	}, nil
}

func batch5ObsidianWriteNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(batch5ObsidianRequest{
		method:      http.MethodPut,
		path:        "/vault/" + path,
		body:        stringArg(input, "content"),
		contentType: "text/markdown",
	})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	return map[string]any{"success": true, "path": path}, nil
}

func batch5ObsidianAppendNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	if path == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	result := client.do(batch5ObsidianRequest{
		method:      http.MethodPost,
		path:        "/vault/" + path,
		body:        stringArg(input, "content"),
		contentType: "text/markdown",
	})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	return map[string]any{"success": true, "path": path}, nil
}

func batch5ObsidianSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
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
	result := client.do(batch5ObsidianRequest{
		method: http.MethodPost,
		path:   "/search/simple/",
		query:  params,
		accept: "application/json",
	})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	items := batch4AsSlice(result)
	if items == nil {
		// Upstream only reshapes a JSON array; anything else counts as no hit.
		return map[string]any{"count": 0, "results": []map[string]any{}}, nil
	}
	matches := make([]map[string]any, 0, len(items))
	for _, rawItem := range items {
		item := batch4AsMap(rawItem)
		rawMatches := batch4AsSlice(item["matches"])
		contexts := make([]any, 0, len(rawMatches))
		for _, rawMatch := range rawMatches {
			contexts = append(contexts, batch5Lane4Get(batch4AsMap(rawMatch), "context", ""))
		}
		if len(contexts) > batch5ObsidianMaxContexts {
			contexts = contexts[:batch5ObsidianMaxContexts]
		}
		matches = append(matches, map[string]any{
			"filename":    batch5Lane4Get(item, "filename", nil),
			"score":       batch5Lane4Get(item, "score", nil),
			"match_count": len(rawMatches),
			"contexts":    contexts,
		})
	}
	return map[string]any{"count": len(matches), "results": matches}, nil
}

func batch5ObsidianListFiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	path := stringArg(input, "path")
	// A trailing slash is what tells the plugin to return a directory listing.
	urlPath := "/vault/"
	if path != "" {
		urlPath = "/vault/" + strings.TrimRight(path, "/") + "/"
	}
	result := client.do(batch5ObsidianRequest{method: http.MethodGet, path: urlPath})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	var files []any
	switch typed := result.(type) {
	case []any:
		files = typed
	case map[string]any:
		files = batch4AsSlice(typed["files"])
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

func batch5ObsidianGetActive(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	client, failure := batch5NewObsidianClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	result := client.do(batch5ObsidianRequest{
		method: http.MethodGet,
		path:   "/active/",
		accept: batch5ObsidianNoteMediaType,
	})
	if failure, failed := batch5ObsidianFailure(result); failed {
		return failure, nil
	}
	object, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"content": fmt.Sprint(result)}, nil
	}
	return map[string]any{
		"path":        batch5Lane4Get(object, "path", ""),
		"content":     batch5Lane4Get(object, "content", ""),
		"tags":        batch5Lane4Get(object, "tags", []any{}),
		"frontmatter": object["frontmatter"],
	}, nil
}
