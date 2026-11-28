package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// bufferWriteCloser lets a test install an in-memory stdin on a stdio client.
type bufferWriteCloser struct{ io.Writer }

func (bufferWriteCloser) Close() error { return nil }

func decodeMCPTestFrames(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame %q is not valid JSON: %v", line, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func decodeMCPTestFrameArray(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	if err := json.Unmarshal([]byte(raw), &frames); err != nil {
		t.Fatalf("frame array %q is not valid JSON: %v", raw, err)
	}
	return frames
}

func mcpTestFrameError(t *testing.T, frame map[string]any) (float64, string, string) {
	t.Helper()
	if _, ok := frame["result"]; ok {
		t.Fatalf("error response also carries a result: %#v", frame)
	}
	payload, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no JSON-RPC error: %#v", frame)
	}
	code, _ := payload["code"].(float64)
	message, _ := payload["message"].(string)
	data, _ := payload["data"].(string)
	return code, message, data
}

func readMCPTestLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	type readResult struct {
		line string
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		done <- readResult{line: line, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("reading client output: %v", result.err)
		}
		return strings.TrimSpace(result.line)
	case <-time.After(5 * time.Second):
		t.Fatal("client did not write the expected response to a server request")
		return ""
	}
}

// Parity: core/framework/loader/mcp_client.py::MCPClient
// Python's ClientSession (mcp 1.26.0 _received_request) answers a server ping
// with EmptyResult and returns ErrorData for roots/sampling/elicitation and
// unknown methods. The old Go read loop dropped all of these frames.
func TestMCPStdioAnswersServerRequests(t *testing.T) {
	var out bytes.Buffer
	client := newStdioMCPClient(&MCPServerConfig{Name: "server-requests", Transport: "stdio"})
	client.stdin = bufferWriteCloser{&out}

	client.respondToServerRequest(json.RawMessage(`7`), "ping")
	client.respondToServerRequest(json.RawMessage(`"roots-1"`), "roots/list")
	client.respondToServerRequest(json.RawMessage(`8`), "sampling/createMessage")
	client.respondToServerRequest(json.RawMessage(`"elicit-1"`), "elicitation/create")
	client.respondToServerRequest(json.RawMessage(`9`), "future/unknown")

	frames := decodeMCPTestFrames(t, out.String())
	if len(frames) != 5 {
		t.Fatalf("wrote %d frames, want 5: %s", len(frames), out.String())
	}

	ping := frames[0]
	if ping["jsonrpc"] != "2.0" || ping["id"] != float64(7) {
		t.Fatalf("ping envelope = %#v", ping)
	}
	result, ok := ping["result"].(map[string]any)
	if !ok || len(result) != 0 {
		t.Fatalf("ping result = %#v, want {}", ping["result"])
	}
	if _, hasError := ping["error"]; hasError {
		t.Fatalf("ping carried an error: %#v", ping)
	}

	roots := frames[1]
	if roots["id"] != "roots-1" {
		t.Fatalf("roots id = %#v, want the string id echoed verbatim", roots["id"])
	}
	if code, message, _ := mcpTestFrameError(t, roots); code != -32600 || message != "List roots not supported" {
		t.Fatalf("roots error = %v %q", code, message)
	}

	sampling := frames[2]
	if sampling["id"] != float64(8) {
		t.Fatalf("sampling id = %#v", sampling["id"])
	}
	if code, message, _ := mcpTestFrameError(t, sampling); code != -32600 || message != "Sampling not supported" {
		t.Fatalf("sampling error = %v %q", code, message)
	}

	elicitation := frames[3]
	if elicitation["id"] != "elicit-1" {
		t.Fatalf("elicitation id = %#v", elicitation["id"])
	}
	if code, message, _ := mcpTestFrameError(t, elicitation); code != -32600 || message != "Elicitation not supported" {
		t.Fatalf("elicitation error = %v %q", code, message)
	}

	unknown := frames[4]
	if unknown["id"] != float64(9) {
		t.Fatalf("unknown id = %#v", unknown["id"])
	}
	if code, message, data := mcpTestFrameError(t, unknown); code != -32602 ||
		message != "Invalid request parameters" || data != "" {
		t.Fatalf("unknown method error = %v %q data %q", code, message, data)
	}
}

// Parity: core/framework/loader/mcp_client.py::MCPClient
// A server request that reuses a pending client id must be answered, not
// delivered to the pending call; notifications and client responses stay
// write-free. The old read loop dropped the ping and would resolve pending[1]
// with the ping frame, which is exactly the regression asserted here.
func TestMCPStdioReadLoopDistinguishesServerRequests(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	client := newStdioMCPClient(&MCPServerConfig{Name: "loop", Transport: "stdio"})
	client.stdin = outW

	pending := make(chan mcpRPCResponse, 1)
	client.mu.Lock()
	client.pending[1] = pending
	client.mu.Unlock()

	done := make(chan struct{})
	go func() {
		client.readLoop(inR)
		close(done)
	}()

	wrote := make(chan struct{})
	go func() {
		for _, line := range []string{
			`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"ignored"}}`,
			`{"jsonrpc":"2.0","id":999,"result":{"stale":true}}`,
			`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			`{"jsonrpc":"2.0","id":"roots-1","method":"roots/list"}`,
		} {
			if _, err := io.WriteString(inW, line+"\n"); err != nil {
				break
			}
		}
		close(wrote)
	}()

	reader := bufio.NewReader(outR)
	pingLine := readMCPTestLine(t, reader)
	rootsLine := readMCPTestLine(t, reader)
	<-wrote

	pingFrames := decodeMCPTestFrames(t, pingLine)
	if len(pingFrames) != 1 || pingFrames[0]["id"] != float64(1) {
		t.Fatalf("ping reply = %s", pingLine)
	}
	if result, ok := pingFrames[0]["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("ping reply result = %#v", pingFrames[0]["result"])
	}

	rootsFrames := decodeMCPTestFrames(t, rootsLine)
	if len(rootsFrames) != 1 || rootsFrames[0]["id"] != "roots-1" {
		t.Fatalf("roots reply = %s", rootsLine)
	}
	if code, message, _ := mcpTestFrameError(t, rootsFrames[0]); code != -32600 || message != "List roots not supported" {
		t.Fatalf("roots reply = %s", rootsLine)
	}

	select {
	case response := <-pending:
		t.Fatalf("server request resolved the pending client call: %+v", response)
	default:
	}
	client.mu.Lock()
	_, stillPending := client.pending[1]
	client.mu.Unlock()
	if !stillPending {
		t.Fatal("server request removed the pending client call")
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not stop after the input pipe closed")
	}
	_ = outW.Close()
	rest, _ := io.ReadAll(outR)
	if strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("client wrote unexpected frames for notifications/responses: %q", rest)
	}
}

const mcpServerRequestEnv = "GO_WANT_MCP_STDIO_SERVER_REQUESTS"

// Parity: core/framework/loader/mcp_client.py::MCPClient
// End to end against a fake stdio server: a server request that reuses the
// initialize id must not break the handshake, every server request gets a
// shaped reply, and a later tools/call still succeeds. With the old drop
// behaviour the handshake answers the ping frame as the initialize result and
// fails, and the bounded result timeout keeps that regression fast.
func TestMCPStdioServerRequestsEndToEnd(t *testing.T) {
	t.Setenv("HIVE_MCP_CALL_RESULT_TIMEOUT", "5")
	client := NewMCPClient(&MCPServerConfig{
		Name:      "server-requests",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMCPStdioServerRequestProcess"},
		Env:       map[string]string{mcpServerRequestEnv: "1"},
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	result, err := client.CallTool("echo", map[string]any{"text": "after-server-requests"})
	if err != nil {
		t.Fatalf("CallTool() = %v", err)
	}
	text, ok := result.(string)
	if !ok {
		t.Fatalf("CallTool() result = %#v, want text", result)
	}
	frames := decodeMCPTestFrameArray(t, text)
	if len(frames) != 5 {
		t.Fatalf("server captured %d responses, want 5: %s", len(frames), text)
	}

	byID := map[string]map[string]any{}
	for _, frame := range frames {
		rawID, err := json.Marshal(frame["id"])
		if err != nil {
			t.Fatalf("id marshal: %v", err)
		}
		byID[string(rawID)] = frame
	}

	ping := byID["1"]
	if ping == nil {
		t.Fatalf("no ping response in %s", text)
	}
	if result, ok := ping["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("ping response result = %#v", ping["result"])
	}

	roots := byID[`"roots-1"`]
	if roots == nil {
		t.Fatalf("no roots/list response in %s", text)
	}
	if code, message, _ := mcpTestFrameError(t, roots); code != -32600 || message != "List roots not supported" {
		t.Fatalf("roots response = %#v", roots)
	}

	sampling := byID["11"]
	if sampling == nil {
		t.Fatalf("no sampling response in %s", text)
	}
	if code, message, _ := mcpTestFrameError(t, sampling); code != -32600 || message != "Sampling not supported" {
		t.Fatalf("sampling response = %#v", sampling)
	}

	elicitation := byID[`"elicit-1"`]
	if elicitation == nil {
		t.Fatalf("no elicitation response in %s", text)
	}
	if code, message, _ := mcpTestFrameError(t, elicitation); code != -32600 || message != "Elicitation not supported" {
		t.Fatalf("elicitation response = %#v", elicitation)
	}

	unknown := byID["13"]
	if unknown == nil {
		t.Fatalf("no unknown-method response in %s", text)
	}
	if code, message, data := mcpTestFrameError(t, unknown); code != -32602 ||
		message != "Invalid request parameters" || data != "" {
		t.Fatalf("unknown response = %#v", unknown)
	}

	second, err := client.CallTool("echo", map[string]any{"text": "still-works"})
	if err != nil || second != text {
		t.Fatalf("second CallTool() = %#v, %v", second, err)
	}
}

// Parity-exempt: child-process fixture implementing an MCP stdio server that
// issues server-initiated requests; no Python counterpart drives raw frames.
func TestMCPStdioServerRequestProcess(t *testing.T) {
	if os.Getenv(mcpServerRequestEnv) == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout
	write := func(message map[string]any) {
		payload, err := json.Marshal(message)
		if err != nil {
			return
		}
		_, _ = writer.Write(append(payload, '\n'))
	}

	const expectedResponses = 5
	var captured []map[string]any
	var callID json.RawMessage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		id, hasID := frame["id"]
		if !hasID {
			continue
		}
		var method string
		if raw, ok := frame["method"]; ok {
			_ = json.Unmarshal(raw, &method)
		}
		switch method {
		case "initialize":
			// Reuse the client's initialize id for the first server request.
			// Request ids are scoped per direction, so this must be answered
			// and must not resolve the pending initialize call.
			write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "server-requests", "version": "0.1.0"},
			}})
		case "tools/list":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "Echo text",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
			}}})
			// A notification must not be answered; the requests that follow
			// cover the supported, unsupported and unknown method shapes.
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/message",
				"params":  map[string]any{"level": "info", "data": "ignored"},
			})
			write(map[string]any{"jsonrpc": "2.0", "id": "roots-1", "method": "roots/list", "params": map[string]any{}})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      11,
				"method":  "sampling/createMessage",
				"params": map[string]any{
					"messages": []any{map[string]any{
						"role":    "user",
						"content": map[string]any{"type": "text", "text": "hi"},
					}},
					"maxTokens": 16,
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      "elicit-1",
				"method":  "elicitation/create",
				"params": map[string]any{
					"message":         "ok?",
					"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				},
			})
			write(map[string]any{"jsonrpc": "2.0", "id": 13, "method": "future/unknown", "params": map[string]any{}})
		case "":
			var envelope map[string]any
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				continue
			}
			captured = append(captured, envelope)
		case "tools/call":
			callID = id
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{
				"code": -32601, "message": "method not found",
			}})
		}
		if callID != nil && len(captured) >= expectedResponses {
			payload, err := json.Marshal(captured)
			if err != nil {
				os.Exit(1)
			}
			write(map[string]any{"jsonrpc": "2.0", "id": callID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": string(payload)}},
			}})
			callID = nil
		}
	}
}
