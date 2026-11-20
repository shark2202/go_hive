package nativecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fixtureEnv marks the test binary as the MCP fixture server.
const fixtureEnv = "HIVE_MCP_FIXTURE"

// fixtureToolsJSON is the tools/list payload both fake transports serve.
// tagList and boom arrive on the second page so pagination is exercised.
const fixtureToolsJSON = `[
  {"name": "echo", "description": "Echo the text back, optionally shouting.",
   "inputSchema": {"type": "object", "properties": {
     "text": {"type": "string", "description": "Text to echo"},
     "loud": {"type": "boolean", "description": "Uppercase the text"}},
    "required": ["text"]}},
  {"name": "add", "description": "Add two numbers together.",
   "inputSchema": {"type": "object", "properties": {
     "a": {"type": "number", "description": "Left operand"},
     "b": {"type": "number", "description": "Right operand"}},
    "required": ["a", "b"]}},
  {"name": "tagList", "description": "Join a list of tags with a prefix.",
   "inputSchema": {"type": "object", "properties": {
     "tags": {"type": "array", "items": {"type": "string"}, "description": "Tags"},
     "prefix": {"type": "string", "description": "Optional prefix"},
     "limit": {"type": "integer", "description": "Max tags"}},
    "required": ["tags"]}},
  {"name": "boom", "description": "Always reports an error result.",
   "inputSchema": {"type": "object", "properties": {}}},
  {"name": "envCheck", "description": "Report the value of a parameter named env.",
   "inputSchema": {"type": "object", "properties": {
     "env": {"type": "string", "description": "A value that shares a global flag name"}},
    "required": ["env"]}},
  {"name": "structured", "description": "Return structured content only.",
   "inputSchema": {"type": "object", "properties": {}}},
  {"name": "longDoc", "description": "Reads a page of the fixture document and returns every paragraph it finds, keeping the original ordering so callers can stitch pages back together without tracking offsets themselves.",
   "inputSchema": {"type": "object", "properties": {
     "page": {"type": "integer", "description": "One-based page number to read from the fixture document; values past the last page return an empty result instead of an error."}},
    "required": ["page"]}}
]`

func fixtureTools() []any {
	parsed, err := ParseJSON([]byte(fixtureToolsJSON))
	if err != nil {
		panic(err)
	}
	return parsed.([]any)
}

// fixtureServer answers JSON-RPC for both the stdio and HTTP fixtures.
type fixtureServer struct {
	mu       sync.Mutex
	requests int
	headers  map[string]string
	events   []string
}

func newFixtureServer() *fixtureServer {
	return &fixtureServer{headers: map[string]string{}}
}

func (s *fixtureServer) record(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers[name] = value
	s.events = append(s.events, name)
}

// handle dispatches one JSON-RPC message. A nil return means "notification,
// nothing to send back".
func (s *fixtureServer) handle(message []byte) []byte {
	parsed, err := ParseJSON(message)
	if err != nil {
		return nil
	}
	request, ok := parsed.(Ordered)
	if !ok {
		return nil
	}
	id, hasID := request.Lookup("id")
	method := stringOf(mustLookup(request, "method"))
	if !hasID {
		return nil
	}
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()

	result, rpcErr := s.result(method, request)
	response := Ordered{{Key: "jsonrpc", Value: "2.0"}, {Key: "id", Value: id}}
	if rpcErr != nil {
		response = append(response, Field{Key: "error", Value: Ordered{
			{Key: "code", Value: -32601},
			{Key: "message", Value: rpcErr.Error()},
		}})
		return []byte(MarshalPy(response))
	}
	response = append(response, Field{Key: "result", Value: result})
	return []byte(MarshalPy(response))
}

func (s *fixtureServer) result(method string, request Ordered) (any, error) {
	params, _ := mustLookup(request, "params").(Ordered)
	switch method {
	case "initialize":
		return Ordered{
			{Key: "protocolVersion", Value: "2025-06-18"},
			{Key: "capabilities", Value: Ordered{
				{Key: "tools", Value: Ordered{{Key: "listChanged", Value: false}}},
				{Key: "resources", Value: Ordered{}},
				{Key: "prompts", Value: Ordered{}},
			}},
			{Key: "serverInfo", Value: Ordered{{Key: "name", Value: "fixture"}, {Key: "version", Value: "1.0.0"}}},
		}, nil
	case "tools/list":
		cursor := stringOf(mustLookup(params, "cursor"))
		tools := fixtureTools()
		if os.Getenv("HIVE_MCP_FIXTURE_PAGINATE") != "1" {
			return Ordered{{Key: "tools", Value: tools}}, nil
		}
		if cursor == "" {
			return Ordered{
				{Key: "tools", Value: tools[:2]},
				{Key: "nextCursor", Value: "page-2"},
			}, nil
		}
		return Ordered{{Key: "tools", Value: tools[2:]}}, nil
	case "tools/call":
		return s.callTool(params)
	case "resources/list":
		return Ordered{{Key: "resources", Value: []any{
			Ordered{
				{Key: "uri", Value: "fixture://notes"},
				{Key: "name", Value: "notes"},
				{Key: "description", Value: "Fixture notes"},
				{Key: "mimeType", Value: "text/plain"},
			},
		}}}, nil
	case "resources/templates/list":
		return Ordered{{Key: "resourceTemplates", Value: []any{
			Ordered{
				{Key: "uriTemplate", Value: "fixture://notes/{id}"},
				{Key: "name", Value: "note"},
				{Key: "description", Value: "One note"},
				{Key: "mimeType", Value: "text/plain"},
			},
		}}}, nil
	case "resources/read":
		return Ordered{{Key: "contents", Value: []any{
			Ordered{
				{Key: "uri", Value: stringOf(mustLookup(params, "uri"))},
				{Key: "mimeType", Value: "text/plain"},
				{Key: "text", Value: "fixture note body"},
			},
		}}}, nil
	case "prompts/list":
		return Ordered{{Key: "prompts", Value: []any{
			Ordered{
				{Key: "name", Value: "greet"},
				{Key: "description", Value: "Say hello"},
				{Key: "arguments", Value: []any{
					Ordered{
						{Key: "name", Value: "who"},
						{Key: "description", Value: "Who to greet"},
						{Key: "required", Value: true},
					},
				}},
			},
		}}}, nil
	case "prompts/get":
		arguments := mustLookup(params, "arguments")
		who := "world"
		if object, ok := arguments.(Ordered); ok {
			if value, found := object.Lookup("who"); found {
				who = stringOf(value)
			}
		}
		return Ordered{
			{Key: "description", Value: "Say hello"},
			{Key: "messages", Value: []any{
				Ordered{
					{Key: "role", Value: "user"},
					{Key: "content", Value: Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: "hello " + who}}},
				},
			}},
		}, nil
	}
	return nil, fmt.Errorf("Method not found: %s", method)
}

func (s *fixtureServer) callTool(params Ordered) (any, error) {
	name := stringOf(mustLookup(params, "name"))
	arguments, _ := mustLookup(params, "arguments").(Ordered)
	switch name {
	case "echo":
		text := stringOf(mustLookup(arguments, "text"))
		if flag, _ := mustLookup(arguments, "loud").(bool); flag {
			text = strings.ToUpper(text)
		}
		return Ordered{{Key: "content", Value: []any{
			Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: text}},
		}}}, nil
	case "add":
		return Ordered{{Key: "content", Value: []any{
			Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: formatSum(arguments)}},
		}}}, nil
	case "tagList":
		return Ordered{{Key: "content", Value: []any{
			Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: formatTags(arguments)}},
		}}}, nil
	case "boom":
		return Ordered{
			{Key: "content", Value: []any{
				Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: "kaboom"}},
			}},
			{Key: "isError", Value: true},
		}, nil
	case "envCheck":
		return Ordered{{Key: "content", Value: []any{
			Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: stringOf(mustLookup(arguments, "env"))}},
		}}}, nil
	case "structured":
		return Ordered{
			{Key: "content", Value: []any{}},
			{Key: "structuredContent", Value: Ordered{
				{Key: "answer", Value: json.Number("42")},
				{Key: "ok", Value: true},
			}},
		}, nil
	}
	return Ordered{
		{Key: "content", Value: []any{
			Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: "no such tool"}},
		}},
		{Key: "isError", Value: true},
	}, nil
}

func formatSum(arguments Ordered) string {
	total := 0.0
	for _, key := range []string{"a", "b"} {
		value, _ := mustLookup(arguments, key).(json.Number)
		parsed, err := value.Float64()
		if err != nil {
			continue
		}
		total += parsed
	}
	return formatPyFloat(total)
}

func formatTags(arguments Ordered) string {
	tags, _ := mustLookup(arguments, "tags").([]any)
	prefix := stringOf(mustLookup(arguments, "prefix"))
	limit := -1
	if value, ok := mustLookup(arguments, "limit").(json.Number); ok {
		if parsed, err := value.Int64(); err == nil {
			limit = int(parsed)
		}
	}
	out := []string{}
	for _, tag := range tags {
		out = append(out, prefix+stringOf(tag))
	}
	if limit >= 0 && limit < len(out) {
		out = out[:limit]
	}
	return strings.Join(out, "\n")
}

// runFixtureServer serves JSON-RPC until stdin closes. With
// HIVE_MCP_FIXTURE_FRAMING=header it insists on Content-Length framing from the
// first message, the way an LSP-style server does.
func runFixtureServer(in io.Reader, out io.Writer) {
	server := newFixtureServer()
	reader := bufio.NewReaderSize(in, 1<<20)
	writer := bufio.NewWriter(out)
	headerMode := os.Getenv("HIVE_MCP_FIXTURE_FRAMING") == "header"
	for {
		payload, fromHeader, err := readFixtureMessage(reader)
		if err != nil {
			return
		}
		if headerMode && !fromHeader {
			// Refuse newline framing outright so the client must retry.
			os.Exit(1)
		}
		if response := server.handle(payload); response != nil {
			if headerMode {
				fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(response))
				writer.Write(response)
			} else {
				writer.Write(response)
				writer.WriteByte('\n')
			}
			writer.Flush()
		}
	}
}

// readFixtureMessage reads either a Content-Length frame or a JSON line.
func readFixtureMessage(reader *bufio.Reader) ([]byte, bool, error) {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return nil, false, err
	}
	trimmed := strings.TrimRight(line, "\r\n")
	if length, ok := contentLength(trimmed); ok {
		for {
			header, err := reader.ReadString('\n')
			if err != nil && header == "" {
				return nil, false, err
			}
			if strings.TrimSpace(header) == "" {
				break
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, false, err
		}
		return payload, true, nil
	}
	if strings.TrimSpace(trimmed) == "" {
		return nil, false, fmt.Errorf("empty line")
	}
	return []byte(trimmed), false, nil
}

// TestNativeCLIStdioFixture is the helper-process entry point: it only acts
// as a server when spawned by the tests below.
//
// Parity-exempt: fixture server for the native CLI tests; no upstream Python case.
func TestNativeCLIStdioFixture(t *testing.T) {
	if os.Getenv(fixtureEnv) != "1" {
		t.Skip("fixture process")
	}
	runFixtureServer(os.Stdin, os.Stdout)
	os.Exit(0)
}

// fixtureCommand returns the shell command line that spawns the fixture.
func fixtureCommand() string {
	return pyShellQuote(os.Args[0]) + " -test.run=TestNativeCLIStdioFixture"
}

func pyShellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`*?[](){}|&;<>#!~") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// fixtureHTTPTools serves one JSON-RPC POST request.
func (s *fixtureServer) serveHTTP(writer http.ResponseWriter, request *http.Request, sseResponses bool) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, name := range []string{"Authorization", "X-Fixture", "Mcp-Session-Id", "MCP-Protocol-Version"} {
		if value := request.Header.Get(name); value != "" {
			s.record("header:"+name, value)
		}
	}
	s.record("method:"+request.Method, request.URL.Path)
	response := s.handle(body)
	if response == nil || !sseResponses {
		if response == nil {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.Write(response)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	fmt.Fprintf(writer, "event: message\ndata: %s\n\n", response)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// newFakeStreamableServer serves the streamable HTTP transport.
func newFakeStreamableServer(t *testing.T, sseResponses bool) (*httptest.Server, *fixtureServer) {
	t.Helper()
	state := newFixtureServer()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/" {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.Contains(string(request.Header.Get("Accept")), "text/event-stream") {
			state.record("accept", "streamable")
		}
		writer.Header().Set("Mcp-Session-Id", "fixture-session")
		state.serveHTTP(writer, request, sseResponses)
	}))
	t.Cleanup(server.Close)
	return server, state
}

// fakeSSEServer serves the legacy HTTP+SSE transport.
type fakeSSEServer struct {
	http   *httptest.Server
	state  *fixtureServer
	mutex  sync.Mutex
	queue  chan []byte
	closed chan struct{}
}

func newFakeSSEServer(t *testing.T) *fakeSSEServer {
	t.Helper()
	server := &fakeSSEServer{
		state:  newFixtureServer(),
		queue:  make(chan []byte, 16),
		closed: make(chan struct{}),
	}
	server.http = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/sse":
			server.serveStream(t, writer, request)
		case request.Method == http.MethodPost && request.URL.Path == "/messages":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if value := request.Header.Get("Authorization"); value != "" {
				server.state.record("header:Authorization", value)
			}
			server.state.record("post", request.URL.Path)
			response := server.state.handle(body)
			if response == nil {
				writer.WriteHeader(http.StatusAccepted)
				return
			}
			select {
			case server.queue <- response:
			case <-server.closed:
			}
			writer.WriteHeader(http.StatusAccepted)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		close(server.closed)
		server.http.Close()
	})
	return server
}

func (s *fakeSSEServer) serveStream(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	if value := request.Header.Get("Authorization"); value != "" {
		s.state.record("header:Authorization", value)
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return
	}
	fmt.Fprintf(writer, "event: endpoint\ndata: /messages?session_id=fixture\n\n")
	flusher.Flush()
	for {
		select {
		case response := <-s.queue:
			fmt.Fprintf(writer, "event: message\ndata: %s\n\n", response)
			flusher.Flush()
		case <-request.Context().Done():
			return
		case <-s.closed:
			return
		}
	}
}

func (s *fakeSSEServer) url() string { return s.http.URL + "/sse" }

// headerValue returns a recorded header value.
func (s *fixtureServer) headerValue(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[name]
}
