package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Parity-exempt: MCP defines tools/list cursor pagination, but the Python
// agent client (mcp SDK 1.26.0 ClientSession.list_tools) is single-page and
// has no test target to mirror. The reference semantics are mcp2cli 3.8.0,
// which the Go native CLI already follows; these tests close the shared gap on
// the Go client side (stdio + HTTP + SSE share mcpListToolsPaged) and pin the
// stop conditions: no cursor on page one, verbatim opaque cursors, empty pages
// followed, repeated cursors and the page cap bounded.

func mcpToolNames(tools []MCPTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestMCPHTTPToolsListFollowsCursorsAndEmptyPages drives the HTTP transport:
// page one has no cursor, the empty second page still advertises a cursor, and
// the third page closes the walk.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPHTTPToolsListFollowsCursorsAndEmptyPages(t *testing.T) {
	var requests []map[string]any
	fake := &fakeMCPHTTPClient{postFn: func(_ string, body any) (*mcpHTTPResponse, error) {
		request, _ := body.(map[string]any)
		requests = append(requests, request)
		params, _ := request["params"].(map[string]any)
		cursor, _ := params["cursor"].(string)
		switch cursor {
		case "":
			return &mcpHTTPResponse{Status: 200, Body: []byte(
				`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"alpha","description":"A","inputSchema":{}}],"nextCursor":"p2"}}`)}, nil
		case "p2":
			return &mcpHTTPResponse{Status: 200, Body: []byte(
				`{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"p3"}}`)}, nil
		case "p3":
			return &mcpHTTPResponse{Status: 200, Body: []byte(
				`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"beta","inputSchema":{"type":"object"}}]}}`)}, nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return nil, nil
		}
	}}
	previous := newMCPHTTPClient
	newMCPHTTPClient = func(*MCPServerConfig) mcpHTTPClient { return fake }
	t.Cleanup(func() { newMCPHTTPClient = previous })
	client := newHTTPMCPClient(&MCPServerConfig{Name: "paged", Transport: "http", URL: "http://fake"})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	if got := mcpToolNames(client.ListTools()); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("tools = %v, want [alpha beta]", got)
	}
	if len(requests) != 3 {
		t.Fatalf("tools/list requests = %d, want 3", len(requests))
	}
	if _, present := requests[0]["params"].(map[string]any)["cursor"]; present {
		t.Fatalf("first page carried a cursor: %#v", requests[0]["params"])
	}
	for i, want := range []string{"p2", "p3"} {
		params, _ := requests[i+1]["params"].(map[string]any)
		if params["cursor"] != want {
			t.Fatalf("request %d cursor = %v, want %q", i+2, params["cursor"], want)
		}
	}
}

// TestMCPHTTPToolsListPaginationErrorFailsConnect is the negative regression:
// a broken page must fail the connection instead of publishing a partial tool
// surface as if the server were healthy.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPHTTPToolsListPaginationErrorFailsConnect(t *testing.T) {
	fake := &fakeMCPHTTPClient{postFn: func(_ string, body any) (*mcpHTTPResponse, error) {
		request, _ := body.(map[string]any)
		params, _ := request["params"].(map[string]any)
		if params["cursor"] == nil {
			return &mcpHTTPResponse{Status: 200, Body: []byte(
				`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"alpha"}],"nextCursor":"p2"}}`)}, nil
		}
		return &mcpHTTPResponse{Status: 200, Body: []byte(
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"page two exploded"}}`)}, nil
	}}
	previous := newMCPHTTPClient
	newMCPHTTPClient = func(*MCPServerConfig) mcpHTTPClient { return fake }
	t.Cleanup(func() { newMCPHTTPClient = previous })
	client := newHTTPMCPClient(&MCPServerConfig{Name: "paged", Transport: "http", URL: "http://fake"})
	err := client.Connect()
	if err == nil || !strings.Contains(err.Error(), "MCP error") {
		t.Fatalf("Connect() = %v, want the MCP error", err)
	}
	if client.IsConnected() {
		t.Fatal("client published a connection after a failed tools/list page")
	}
	if tools := client.ListTools(); len(tools) != 0 {
		t.Fatalf("tools = %v, want none after a failed walk", tools)
	}
}

// TestMCPToolsListPagedStopsOnRepeatedCursor pins the cycle guard: a server
// that echoes the same cursor is read twice, then the walk stops. No hang.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPToolsListPagedStopsOnRepeatedCursor(t *testing.T) {
	requests := 0
	tools, err := mcpListToolsPaged(func(cursor string) (json.RawMessage, error) {
		requests++
		return json.RawMessage(`{"tools":[{"name":"alpha"}],"nextCursor":"loop"}`), nil
	})
	if err != nil {
		t.Fatalf("mcpListToolsPaged() = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (page one + one repeated cursor)", requests)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want the two pages read before the repeat", len(tools))
	}
}

// TestMCPToolsListPagedStopsAtPageCap pins the second guard: a monotonic,
// never-empty cursor walk is cut at the documented cap.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPToolsListPagedStopsAtPageCap(t *testing.T) {
	requests := 0
	_, err := mcpListToolsPaged(func(cursor string) (json.RawMessage, error) {
		requests++
		return json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"c%d"}`, requests)), nil
	})
	if err != nil {
		t.Fatalf("mcpListToolsPaged() = %v", err)
	}
	if requests != mcpToolsListMaxPages {
		t.Fatalf("requests = %d, want the %d-page cap", requests, mcpToolsListMaxPages)
	}
}

// TestMCPDecodeToolsListPageCursorSemantics pins the page decoder: only a
// non-empty string continues the walk; null, numbers and missing tools are
// tolerated, and unnamed tools are skipped.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPDecodeToolsListPageCursorSemantics(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		tools  int
		cursor string
	}{
		{"absent cursor", `{"tools":[{"name":"a"}]}`, 1, ""},
		{"null cursor", `{"tools":[{"name":"a"}],"nextCursor":null}`, 1, ""},
		{"numeric cursor", `{"tools":[{"name":"a"}],"nextCursor":7}`, 1, ""},
		{"empty cursor", `{"tools":[{"name":"a"}],"nextCursor":""}`, 1, ""},
		{"empty page keeps cursor", `{"tools":[],"nextCursor":"p2"}`, 0, "p2"},
		{"missing tools keeps cursor", `{"nextCursor":"p2"}`, 0, "p2"},
		{"null result", `null`, 0, ""},
		{"unnamed tool skipped", `{"tools":[{"name":""},{"name":"b"}]}`, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, cursor, err := mcpDecodeToolsListPage(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("mcpDecodeToolsListPage() = %v", err)
			}
			if len(tools) != tc.tools || cursor != tc.cursor {
				t.Fatalf("tools=%d cursor=%q, want tools=%d cursor=%q", len(tools), cursor, tc.tools, tc.cursor)
			}
		})
	}
}

const (
	mcpPagedEnv   = "GO_WANT_MCP_STDIO_PAGED"
	mcpPagedTrace = "MCP_PAGED_TRACE"
)

func recordMCPPagedCursor(t *testing.T, path, cursor string) {
	t.Helper()
	if cursor == "" {
		cursor = "(none)"
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(4)
	}
	defer file.Close()
	_, _ = file.WriteString(cursor + "\n")
}

// TestMCPSSEToolsListFollowsNextCursor drives the SSE transport end to end
// against a fake SSE server: the JSON-RPC replies travel over the event
// stream, so this proves realSSESession.ListTools consumes the same cursor
// walk (empty page included) as stdio and HTTP.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPSSEToolsListFollowsNextCursor(t *testing.T) {
	var mu sync.Mutex
	var stream http.ResponseWriter
	var flusher http.Flusher
	var cursors []string

	writeFrame := func(payload []byte) {
		mu.Lock()
		defer mu.Unlock()
		if stream == nil {
			return
		}
		_, _ = fmt.Fprintf(stream, "event: message\ndata: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flush, ok := w.(http.Flusher)
		if !ok {
			t.Error("fake SSE server has no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		mu.Lock()
		stream = w
		flusher = flush
		mu.Unlock()
		_, _ = fmt.Fprint(w, "event: endpoint\ndata: /mcp/v1\n\n")
		flush.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/mcp/v1", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		if request.ID == nil {
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "paged-sse", "version": "0.1.0"},
			}
		case "tools/list":
			cursor, _ := request.Params["cursor"].(string)
			mu.Lock()
			cursors = append(cursors, cursor)
			mu.Unlock()
			switch cursor {
			case "":
				result = map[string]any{
					"tools":      []any{map[string]any{"name": "alpha"}},
					"nextCursor": "page-2",
				}
			case "page-2":
				result = map[string]any{"tools": []any{}, "nextCursor": "page-3"}
			case "page-3":
				result = map[string]any{"tools": []any{map[string]any{"name": "beta"}}}
			default:
				result = map[string]any{}
			}
		default:
			result = map[string]any{}
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
		writeFrame(payload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	session, err := dialMCPSSESession(mcpSSEDialTarget{
		URL:     server.URL + "/sse",
		Server:  "paged-sse",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dialMCPSSESession() = %v", err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Initialize(); err != nil {
		t.Fatalf("Initialize() = %v", err)
	}
	tools, err := session.ListTools()
	if err != nil {
		t.Fatalf("ListTools() = %v", err)
	}
	if got := mcpToolNames(tools); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("tools = %v, want [alpha beta]", got)
	}
	mu.Lock()
	gotCursors := append([]string(nil), cursors...)
	mu.Unlock()
	if strings.Join(gotCursors, ",") != ",page-2,page-3" {
		t.Fatalf("cursors = %v, want [ page-2 page-3 ]", gotCursors)
	}
	for _, tool := range tools {
		if tool.ServerName != "paged-sse" {
			t.Fatalf("tool %q server = %q", tool.Name, tool.ServerName)
		}
	}
}

// TestMCPStdioToolsListFollowsNextCursor proves cursor semantics over the real
// stdio wire: the fixture only returns "beta" when it receives "page-3", so a
// client that ignores cursors (or stops on the empty page) cannot pass.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPStdioToolsListFollowsNextCursor(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "cursors.txt")
	client := NewMCPClient(&MCPServerConfig{
		Name:      "paged",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMCPStdioPagedProcess"},
		Env:       map[string]string{mcpPagedEnv: "1", mcpPagedTrace: trace},
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	if got := mcpToolNames(client.ListTools()); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("tools = %v, want [alpha beta]", got)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"(none)", "page-2", "page-3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cursors = %v, want %v", got, want)
	}
}

// TestMCPStdioPagedProcess is the stdio fixture for the pagination test. It
// serves three pages: alpha + cursor, an empty page + cursor, then beta.
// Parity-exempt: Go-side tools/list cursor walk; upstream Python agent
// client (mcp SDK 1.26.0) is single-page and has no pytest target.
func TestMCPStdioPagedProcess(t *testing.T) {
	if os.Getenv(mcpPagedEnv) == "" {
		return
	}
	trace := os.Getenv(mcpPagedTrace)
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		var request struct {
			ID     *int64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		if request.ID == nil {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": *request.ID}
		switch request.Method {
		case "initialize":
			reply["result"] = map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "paged", "version": "0.1.0"},
			}
		case "tools/list":
			cursor, _ := request.Params["cursor"].(string)
			recordMCPPagedCursor(t, trace, cursor)
			switch cursor {
			case "":
				reply["result"] = map[string]any{
					"tools":      []any{map[string]any{"name": "alpha", "inputSchema": map[string]any{}}},
					"nextCursor": "page-2",
				}
			case "page-2":
				reply["result"] = map[string]any{
					"tools":      []any{},
					"nextCursor": "page-3",
				}
			case "page-3":
				reply["result"] = map[string]any{
					"tools": []any{map[string]any{"name": "beta", "inputSchema": map[string]any{}}},
				}
			default:
				reply["error"] = map[string]any{"code": -32602, "message": "unexpected cursor " + cursor}
			}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		payload, _ := json.Marshal(reply)
		_, _ = os.Stdout.Write(append(payload, '\n'))
	}
}
