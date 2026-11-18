package tools

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeMCPHTTPClient struct {
	config    *MCPServerConfig
	getCalls  []string
	postCalls []string
	closed    bool
	postFn    func(path string, body any) (*mcpHTTPResponse, error)
}

func (f *fakeMCPHTTPClient) Get(path string) (*mcpHTTPResponse, error) {
	f.getCalls = append(f.getCalls, path)
	return &mcpHTTPResponse{Status: 200, Body: []byte(`{}`)}, nil
}

func (f *fakeMCPHTTPClient) Post(path string, body any) (*mcpHTTPResponse, error) {
	f.postCalls = append(f.postCalls, path)
	if f.postFn != nil {
		return f.postFn(path, body)
	}
	return &mcpHTTPResponse{Status: 200, Body: []byte(
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
			`{"name":"ping","description":"Ping tool","inputSchema":{}}]}}`)}, nil
}

func (f *fakeMCPHTTPClient) Close() error {
	f.closed = true
	return nil
}

func markHTTPConnected(c *httpMCPClient, tools ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	byName := map[string]MCPTool{}
	for _, name := range tools {
		byName[name] = MCPTool{
			Name:        name,
			Description: "Ping tool",
			InputSchema: map[string]any{},
			ServerName:  c.config.Name,
		}
	}
	c.tools = byName
	c.conn = true
}

// Parity: core/tests/test_mcp_client.py::test_connect_unix_transport_uses_socket_path
func TestMCPConnectUnixTransportUsesSocketPath(t *testing.T) {
	fake := &fakeMCPHTTPClient{}
	previous := newMCPHTTPClient
	var seen *MCPServerConfig
	newMCPHTTPClient = func(config *MCPServerConfig) mcpHTTPClient {
		seen = config
		return fake
	}
	t.Cleanup(func() { newMCPHTTPClient = previous })

	client := NewMCPClient(&MCPServerConfig{
		Name:       "unix-server",
		Transport:  "unix",
		URL:        "http://localhost",
		SocketPath: "/tmp/test.sock",
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	if seen == nil {
		t.Fatal("transport factory was not called")
	}
	if seen.SocketPath != "/tmp/test.sock" {
		t.Fatalf("socket path = %q", seen.SocketPath)
	}
	if seen.URL != "http://localhost" {
		t.Fatalf("base url = %q", seen.URL)
	}
	if !reflect.DeepEqual(fake.getCalls, []string{"/health"}) {
		t.Fatalf("get calls = %v", fake.getCalls)
	}
	if !reflect.DeepEqual(fake.postCalls, []string{"/mcp/v1"}) {
		t.Fatalf("post calls = %v", fake.postCalls)
	}
	tools := client.ListTools()
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("tools = %+v", tools)
	}

	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect() = %v", err)
	}
	if !fake.closed {
		t.Fatal("transport was not closed on disconnect")
	}
}

type fakeSSESession struct {
	tools   []MCPTool
	closed  bool
	initErr error
}

func (s *fakeSSESession) Initialize() error { return s.initErr }
func (s *fakeSSESession) ListTools() ([]MCPTool, error) {
	return s.tools, nil
}
func (s *fakeSSESession) CallTool(string, map[string]any) (any, error) { return nil, nil }
func (s *fakeSSESession) Close() error {
	s.closed = true
	return nil
}

// Parity: core/tests/test_mcp_client.py::test_connect_sse_and_list_tools
func TestMCPConnectSSEAndListTools(t *testing.T) {
	session := &fakeSSESession{tools: []MCPTool{{
		Name:        "search",
		Description: "Search docs",
		InputSchema: map[string]any{"type": "object"},
	}}}
	var targets []mcpSSEDialTarget
	previous := newMCPSSESession
	newMCPSSESession = func(target mcpSSEDialTarget) (mcpSSESession, error) {
		targets = append(targets, target)
		return session, nil
	}
	t.Cleanup(func() { newMCPSSESession = previous })

	client := NewMCPClient(&MCPServerConfig{
		Name:      "sse-server",
		Transport: "sse",
		URL:       "http://localhost/sse",
		Headers:   map[string]string{"Authorization": "Bearer token"},
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	tools := client.ListTools()
	if len(tools) != 1 || tools[0].Name != "search" || tools[0].Description != "Search docs" {
		t.Fatalf("tools = %+v", tools)
	}
	if len(targets) != 1 {
		t.Fatalf("dial targets = %v", targets)
	}
	if targets[0].URL != "http://localhost/sse" {
		t.Fatalf("url = %q", targets[0].URL)
	}
	if !reflect.DeepEqual(targets[0].Headers, map[string]string{"Authorization": "Bearer token"}) {
		t.Fatalf("headers = %v", targets[0].Headers)
	}
	if targets[0].Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", targets[0].Timeout)
	}

	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect() = %v", err)
	}
	if !session.closed {
		t.Fatal("SSE session was not closed on disconnect")
	}
}

// Parity: core/tests/test_mcp_client.py::test_call_tool_retries_once_on_connect_error_for_unix
func TestMCPCallToolRetriesOnceOnConnectErrorForUnix(t *testing.T) {
	client := newHTTPMCPClient(&MCPServerConfig{Name: "unix-server", Transport: "unix"})
	markHTTPConnected(client, "ping")

	calls := 0
	reconnects := 0
	firstErr := &mcpHTTPConnectError{err: errors.New("first failure")}
	client.callFn = func(toolName string, arguments map[string]any) (any, error) {
		calls++
		if calls == 1 {
			return nil, firstErr
		}
		return []any{map[string]any{
			"type": "text",
			"text": fmt.Sprintf("%s:%v", toolName, arguments["value"]),
		}}, nil
	}
	client.reconnectFn = func() { reconnects++ }

	result, err := client.CallTool("ping", map[string]any{"value": "ok"})
	if err != nil {
		t.Fatalf("CallTool() = %v", err)
	}
	want := []any{map[string]any{"type": "text", "text": "ping:ok"}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if reconnects != 1 {
		t.Fatalf("reconnects = %d, want 1", reconnects)
	}
}

// Parity: core/tests/test_mcp_client.py::test_call_tool_retry_exhausted_raises_original_error_for_unix
func TestMCPCallToolRetryExhaustedRaisesOriginalErrorForUnix(t *testing.T) {
	client := newHTTPMCPClient(&MCPServerConfig{Name: "unix-server", Transport: "unix"})
	markHTTPConnected(client, "ping")

	calls := 0
	reconnects := 0
	firstErr := &mcpHTTPConnectError{err: errors.New("first failure")}
	secondErr := &mcpHTTPConnectError{err: errors.New("second failure")}
	client.callFn = func(string, map[string]any) (any, error) {
		calls++
		if calls == 1 {
			return nil, firstErr
		}
		return nil, secondErr
	}
	client.reconnectFn = func() { reconnects++ }

	_, err := client.CallTool("ping", map[string]any{"value": "ok"})
	if err != firstErr {
		t.Fatalf("err = %v, want the first error", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if reconnects != 1 {
		t.Fatalf("reconnects = %d, want 1", reconnects)
	}
}

// Parity: core/tests/test_mcp_client.py::test_call_tool_http_preserves_runtime_error_wrapping
func TestMCPCallToolHTTPPreservesRuntimeErrorWrapping(t *testing.T) {
	client := newHTTPMCPClient(&MCPServerConfig{Name: "http-server", Transport: "http"})
	markHTTPConnected(client, "ping")

	connectErr := errors.New("first failure")
	client.http = &fakeMCPHTTPClient{
		postFn: func(string, any) (*mcpHTTPResponse, error) {
			return nil, &mcpHTTPConnectError{err: connectErr}
		},
	}
	reconnects := 0
	client.reconnectFn = func() { reconnects++ }

	_, err := client.CallTool("ping", map[string]any{"value": "ok"})
	if err == nil {
		t.Fatal("CallTool() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "Failed to call tool via HTTP") {
		t.Fatalf("error %q does not carry the HTTP wrapping", err)
	}
	if !errors.Is(err, connectErr) {
		t.Fatalf("error %q does not preserve the cause", err)
	}
	if reconnects != 0 {
		t.Fatalf("reconnects = %d, want 0 (http never retries)", reconnects)
	}
}

func mcpTextItem(text string) map[string]any {
	return map[string]any{"text": text}
}

func mcpImageItem(data, mime string, meta map[string]any) map[string]any {
	item := map[string]any{"data": data, "mimeType": mime}
	if meta != nil {
		item["_meta"] = meta
	}
	return item
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_text_only
func TestMCPSplitContentTextOnly(t *testing.T) {
	text, images := SplitMCPContent([]any{mcpTextItem("hello"), mcpTextItem("world")})
	if text != "hello\nworld" {
		t.Fatalf("text = %q", text)
	}
	if len(images) != 0 {
		t.Fatalf("images = %v", images)
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_image_emits_image_url_block
func TestMCPSplitContentImageEmitsImageURLBlock(t *testing.T) {
	text, images := SplitMCPContent([]any{
		mcpTextItem("alt-text"),
		mcpImageItem("AAA", "image/png", nil),
	})
	if text != "alt-text" {
		t.Fatalf("text = %q", text)
	}
	want := []map[string]any{{
		"type":      "image_url",
		"image_url": map[string]any{"url": "data:image/png;base64,AAA"},
	}}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("images = %#v", images)
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_pdf_emits_file_block_not_image
func TestMCPSplitContentPDFEmitsFileBlockNotImage(t *testing.T) {
	text, images := SplitMCPContent([]any{
		mcpTextItem("here is a PDF"),
		mcpImageItem("JVBE", "application/pdf", nil),
	})
	if text != "here is a PDF" {
		t.Fatalf("text = %q", text)
	}
	if len(images) != 1 {
		t.Fatalf("images = %#v", images)
	}
	block := images[0]
	if block["type"] != "file" {
		t.Fatalf("block type = %v", block["type"])
	}
	file, _ := block["file"].(map[string]any)
	if file["file_data"] != "data:application/pdf;base64,JVBE" {
		t.Fatalf("file_data = %v", file["file_data"])
	}
	if _, ok := block["image_url"]; ok {
		t.Fatal("PDF leaked through as an image_url block")
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_pdf_picks_up_filename_from_meta
func TestMCPSplitContentPDFPicksUpFilenameFromMeta(t *testing.T) {
	_, images := SplitMCPContent([]any{
		mcpImageItem("JVBE", "application/pdf", map[string]any{"filename": "invoice_q3.pdf"}),
	})
	file, _ := images[0]["file"].(map[string]any)
	if file["filename"] != "invoice_q3.pdf" {
		t.Fatalf("filename = %v", file["filename"])
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_pdf_default_filename_when_no_meta
func TestMCPSplitContentPDFDefaultFilenameWhenNoMeta(t *testing.T) {
	_, images := SplitMCPContent([]any{
		mcpImageItem("JVBE", "application/pdf", nil),
	})
	file, _ := images[0]["file"].(map[string]any)
	if file["filename"] != "document.pdf" {
		t.Fatalf("filename = %v", file["filename"])
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_pdf_case_insensitive_mime
func TestMCPSplitContentPDFCaseInsensitiveMime(t *testing.T) {
	_, images := SplitMCPContent([]any{
		mcpImageItem("JVBE", "Application/PDF", nil),
	})
	if images[0]["type"] != "file" {
		t.Fatalf("block type = %v, want file", images[0]["type"])
	}
}

// Parity: core/tests/test_mcp_client.py::test_split_mcp_content_mixed_text_image_pdf
func TestMCPSplitContentMixedTextImagePDF(t *testing.T) {
	text, images := SplitMCPContent([]any{
		mcpTextItem("a"),
		mcpImageItem("img1", "image/jpeg", nil),
		mcpTextItem("b"),
		mcpImageItem("pdf1", "application/pdf", nil),
	})
	if text != "a\nb" {
		t.Fatalf("text = %q", text)
	}
	types := []any{images[0]["type"], images[1]["type"]}
	if !reflect.DeepEqual(types, []any{"image_url", "file"}) {
		t.Fatalf("block types = %v", types)
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/jpeg;base64,img1" {
		t.Fatalf("image url = %v", imageURL["url"])
	}
	file, _ := images[1]["file"].(map[string]any)
	if file["file_data"] != "data:application/pdf;base64,pdf1" {
		t.Fatalf("file_data = %v", file["file_data"])
	}
}

const mcpHelperEnv = "GO_WANT_MCP_STDIO_HELPER"

// Parity-exempt: child-process fixture for the handshake-timeout test; upstream
func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv(mcpHelperEnv) == "" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// Parity: core/tests/test_mcp_client.py::test_connect_stdio_fails_when_the_server_never_initializes
func TestMCPConnectStdioFailsWhenServerNeverInitializes(t *testing.T) {
	previous := mcpConnectReadyTimeout
	mcpConnectReadyTimeout = 1 * time.Second
	t.Cleanup(func() { mcpConnectReadyTimeout = previous })

	client := NewMCPClient(&MCPServerConfig{
		Name:      "silent-server",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMCPStdioHelperProcess", "-test.bench=s3cret"},
		Env:       map[string]string{mcpHelperEnv: "1"},
	})

	started := time.Now()
	err := client.Connect()
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Connect() = nil, want a handshake timeout")
	}
	if !strings.Contains(err.Error(), "Timed out waiting for MCP stdio connection") {
		t.Fatalf("error = %q", err)
	}
	if elapsed >= 15*time.Second {
		t.Fatalf("connect took %s, want well under 15s", elapsed)
	}
	if stdio, ok := client.(*stdioMCPClient); ok && stdio.IsConnected() {
		t.Fatal("client published a connection after a failed handshake")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error leaks a credential: %q", err)
	}
}

// Parity-exempt: Go-only wire-level round trip; no Python counterpart frames
func TestMCPStdioRoundTrip(t *testing.T) {
	client := NewMCPClient(&MCPServerConfig{
		Name:      "echo-server",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMCPStdioEchoProcess"},
		Env:       map[string]string{mcpEchoEnv: "1"},
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	tools := client.ListTools()
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Description != "Echo text" {
		t.Fatalf("description = %q", tools[0].Description)
	}

	result, err := client.CallTool("echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool() = %v", err)
	}
	if result != "hi" {
		t.Fatalf("result = %#v", result)
	}

	if _, err := client.CallTool("missing", nil); err == nil {
		t.Fatal("CallTool(unknown) = nil error")
	}
}

const mcpEchoEnv = "GO_WANT_MCP_STDIO_ECHO"

// Parity-exempt: child-process fixture implementing the MCP stdio server that
func TestMCPStdioEchoProcess(t *testing.T) {
	if os.Getenv(mcpEchoEnv) == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout
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
				"serverInfo":      map[string]any{"name": "echo", "version": "0.1.0"},
			}
		case "tools/list":
			reply["result"] = map[string]any{"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "Echo text",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
			}}
		case "tools/call":
			arguments, _ := request.Params["arguments"].(map[string]any)
			text, _ := arguments["text"].(string)
			reply["result"] = map[string]any{"content": []any{
				map[string]any{"type": "text", "text": text},
			}}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		payload, _ := json.Marshal(reply)
		_, _ = writer.Write(append(payload, '\n'))
	}
}
