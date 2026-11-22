package queenmail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/version"
)

// envFromMap is the getenv the MCP server reads its HIVE_QUEEN_MAIL_* with.
func envFromMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// mcpResponses parses the server's answers, keyed by JSON-RPC id.
func mcpResponses(t *testing.T, output string) map[float64]map[string]any {
	t.Helper()
	responses := map[float64]map[string]any{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("answer %q: %v", line, err)
		}
		id, _ := message["id"].(float64)
		responses[id] = message
	}
	return responses
}

// toolCallResult is the text content of one tools/call answer.
func toolCallResult(t *testing.T, message map[string]any) string {
	t.Helper()
	result, _ := message["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("answer = %#v", message)
	}
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

// assertJSONEqual compares two JSON values after both go through Go's encoder
// (sorted keys), so Python's pydantic field order doesn't create noise while
// every field and byte of every string still has to match.
func assertJSONEqual(t *testing.T, label string, got any, want string) {
	t.Helper()
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("%s: want fixture: %v", label, err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal got: %v", label, err)
	}
	wantJSON, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("%s: marshal want: %v", label, err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s:\n got %s\nwant %s", label, gotJSON, wantJSON)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_mcp_server_writes_through_hive_with_the_token
func TestTheMCPServerWritesThroughHiveWithTheToken(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		seen = append(seen, r.Clone(r.Context()))
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sent":"hop_1","to":"queen_ivy","depth":1}`))
	}))
	defer server.Close()

	env := envFromMap(map[string]string{
		"HIVE_QUEEN_MAIL_URL":   server.URL + "/",
		"HIVE_QUEEN_MAIL_TOKEN": "t0ken",
		"HIVE_QUEEN_MAIL_FROM":  "claude_code:queen_claude:abc",
	})
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.329"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"message_queen","arguments":{"queen_id":"queen_ivy","message":"Hi"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, env); err != nil {
		t.Fatal(err)
	}

	responses := mcpResponses(t, output.String())
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	initialized, _ := responses[1]["result"].(map[string]any)
	serverInfo, _ := initialized["serverInfo"].(map[string]any)
	if serverInfo["name"] != "hive" || initialized["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize = %#v", initialized)
	}
	listed, _ := responses[2]["result"].(map[string]any)
	tools, _ := listed["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", listed)
	}
	if text := toolCallResult(t, responses[3]); text != `{"sent":"hop_1","to":"queen_ivy","depth":1}` {
		// json.dumps keeps insertion order; Go marshals maps sorted. The text
		// is JSON either way, so compare what it says.
		var said map[string]any
		if err := json.Unmarshal([]byte(text), &said); err != nil {
			t.Fatalf("tool text = %q", text)
		}
		if said["sent"] != "hop_1" || said["to"] != "queen_ivy" || said["depth"] != float64(1) {
			t.Fatalf("tool text = %q", text)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("requests = %d", len(seen))
	}
	request := seen[0]
	if request.Method != http.MethodPost || request.URL.Path != "/api/queen-mail/letters" {
		t.Fatalf("request = %s %s", request.Method, request.URL.Path)
	}
	if request.Header.Get("X-Queen-Mail-Token") != "t0ken" {
		t.Fatalf("token header = %q", request.Header.Get("X-Queen-Mail-Token"))
	}
	body := bodies[0]
	if body["from"] != "claude_code:queen_claude:abc" || body["to"] != "queen_ivy" || body["message"] != "Hi" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_mcp_server_passes_hives_refusal_on
func TestTheMCPServerPassesHivesRefusalOn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"That's your own id"}`))
	}))
	defer server.Close()

	env := envFromMap(map[string]string{
		"HIVE_QUEEN_MAIL_URL":   server.URL,
		"HIVE_QUEEN_MAIL_TOKEN": "t0ken",
		"HIVE_QUEEN_MAIL_FROM":  "claude_code:queen_claude:abc",
	})
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_queens","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, env); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	text := toolCallResult(t, responses[1])
	if text != `{"error":"That's your own id"}` {
		t.Fatalf("tool text = %q", text)
	}
	// Hive's refusal is still a successful tool call: the tool returns {error}
	// as its value, so FastMCP reports isError false plus structuredContent.
	result, _ := responses[1]["result"].(map[string]any)
	if isError, ok := result["isError"]; !ok || isError != false {
		t.Fatalf("isError = %#v", result["isError"])
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["result"] != text {
		t.Fatalf("structuredContent = %#v", result["structuredContent"])
	}
}

// Parity-exempt: 上游没有覆盖 FastMCP 协议外形的 pytest（Python 测试直接调用
// queen_mail_mcp 的工具函数）；本测试钉住 2026-11-21 双跑实测的 FastMCP 1.26.0
// initialize 输出（docs/e2e-assets/queen-mail-mcp/handshake.json，探针 probe.py）。
func TestTheMCPServerInitializesTheFastMCPWay(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.329"}}}` + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	result, _ := responses[1]["result"].(map[string]any)
	assertJSONEqual(t, "capabilities", result["capabilities"], `{
		"experimental": {},
		"prompts": {"listChanged": false},
		"resources": {"subscribe": false, "listChanged": false},
		"tools": {"listChanged": false}
	}`)
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "hive" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}
	// Parity-exempt (deliberate exception, docs/queen-mail.md §6): upstream
	// reports FastMCP's library version (1.26.0); Go reports its own build.
	if serverInfo["version"] != version.Version {
		t.Fatalf("serverInfo.version = %#v, want %q", serverInfo["version"], version.Version)
	}
}

// Parity-exempt: FastMCP 会话的协商规则（mcp 1.26.0 shared/session.py）没有
// 上游 pytest；按 2026-11-21 实测：支持的版本原样回显，不支持的版本回 latest，
// 缺 protocolVersion 按请求校验错误返回 -32602。
func TestTheMCPServerNegotiatesProtocolVersionsTheFastMCPWay(t *testing.T) {
	cases := []struct{ sent, want string }{
		{"2024-11-05", "2024-11-05"},
		{"2025-03-26", "2025-03-26"},
		{"2025-06-18", "2025-06-18"},
		{"2025-11-25", "2025-11-25"},
		{"1999-01-01", "2025-11-25"},
	}
	lines := make([]string, 0, len(cases))
	for i, c := range cases {
		lines = append(lines, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{}}}`, i+1, c.sent))
	}
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(strings.Join(lines, "\n")+"\n"), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	for i, c := range cases {
		result, _ := responses[float64(i+1)]["result"].(map[string]any)
		if result["protocolVersion"] != c.want {
			t.Fatalf("protocolVersion(%q) = %#v, want %q", c.sent, result["protocolVersion"], c.want)
		}
	}

	var missing bytes.Buffer
	input := `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"capabilities":{}}}` + "\n"
	if err := ServeQueenMailMCP(strings.NewReader(input), &missing, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	errorValue, _ := mcpResponses(t, missing.String())[7]["error"].(map[string]any)
	if errorValue["code"] != float64(-32602) || errorValue["message"] != "Invalid request parameters" || errorValue["data"] != "" {
		t.Fatalf("missing protocolVersion = %#v", errorValue)
	}
}

// Parity-exempt: golden 对照——testdata/fastmcp_tools_list.json 是 FastMCP
// 1.26.0 实测 tools/list 结果的逐字拷贝（docs/e2e-assets/queen-mail-mcp/
// handshake.json）；上游没有针对 FastMCP 生成字段的 pytest。
func TestTheMCPServerListsTheToolsFastMCPLists(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	listed, _ := mcpResponses(t, output.String())[1]["result"].(map[string]any)
	want, err := os.ReadFile("testdata/fastmcp_tools_list.json")
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, "tools/list", listed, string(want))

	// The descriptions are the raw __doc__ strings, byte for byte:
	// message_queen's keeps the docstring's "\n    " continuation indent.
	tools, _ := listed["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", listed)
	}
	message, _ := tools[1].(map[string]any)
	description, _ := message["description"].(string)
	if !strings.Contains(description, "new message in this\n    conversation,") ||
		!strings.HasSuffix(description, "that led here is too long).") {
		t.Fatalf("message_queen description = %q", description)
	}
}

// Parity-exempt: FastMCP 的 CallToolResult 外形（structuredContent/isError）
// 是库行为，上游无 pytest；按 2026-11-21 实测（handshake.json 的 call_* 字段）。
func TestTheMCPServerWrapsToolCallsTheFastMCPWay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sent":"hop_1","to":"queen_ivy","depth":1}`))
	}))
	defer server.Close()

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"message_queen","arguments":{"queen_id":"queen_ivy","message":"Hi"}}}` + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(map[string]string{"HIVE_QUEEN_MAIL_URL": server.URL})); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	result, _ := responses[1]["result"].(map[string]any)
	if isError, ok := result["isError"]; !ok || isError != false {
		t.Fatalf("isError = %#v", result["isError"])
	}
	text := toolCallResult(t, responses[1])
	structured, _ := result["structuredContent"].(map[string]any)
	if len(structured) != 1 || structured["result"] != text {
		t.Fatalf("structuredContent = %#v, text = %q", result["structuredContent"], text)
	}
	var said map[string]any
	if err := json.Unmarshal([]byte(text), &said); err != nil ||
		said["sent"] != "hop_1" || said["to"] != "queen_ivy" || said["depth"] != float64(1) {
		t.Fatalf("tool text = %q", text)
	}
}

// Parity-exempt: FastMCP 把未知工具报成工具级错误结果（isError true、无
// structuredContent），不是 JSON-RPC 错误；按 2026-11-21 实测（handshake.json
// 的 call_unknown_tool）。
func TestTheMCPServerReportsUnknownToolsAsToolErrors(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	message := mcpResponses(t, output.String())[1]
	if _, isRPCError := message["error"]; isRPCError {
		t.Fatalf("unknown tool answered a JSON-RPC error: %#v", message)
	}
	result, _ := message["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError = %#v", result["isError"])
	}
	if _, hasStructured := result["structuredContent"]; hasStructured {
		t.Fatalf("unknown tool has structuredContent: %#v", result)
	}
	if text := toolCallResult(t, message); text != "Unknown tool: no_such_tool" {
		t.Fatalf("tool text = %q", text)
	}
}

// Parity-exempt: 上游 FastMCP 自带的协议行为（ping、无 id 的通知不回包）
// 没有对应 pytest；这里钉住 Go 版的最小协议面。
func TestTheMCPServerAnswersTheProtocolEdgeCases(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	if len(responses) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	if result, ok := responses[7]["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("ping = %#v", responses[7])
	}
}

// Parity-exempt: FastMCP 的方法表是库行为（mcp 1.26.0），上游无 pytest；按
// 2026-11-21 实测：未知方法 -32602 "Invalid request parameters" data ""，已知
// 但没有 handler 的方法 -32601 "Method not found"。
func TestTheMCPServerAnswersUnknownMethodsTheFastMCPWay(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"bogus/method","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"roots/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"logging/setLevel","params":{"level":"info"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/subscribe","params":{"uri":"hive://x"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tasks/list","params":{}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":"not-an-object"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	for _, id := range []float64{1, 2, 6} {
		errorValue, _ := responses[id]["error"].(map[string]any)
		if errorValue["code"] != float64(-32602) || errorValue["message"] != "Invalid request parameters" {
			t.Fatalf("method %v = %#v", id, responses[id])
		}
		if data, ok := errorValue["data"]; !ok || data != "" {
			t.Fatalf("method %v data = %#v", id, errorValue["data"])
		}
	}
	for _, id := range []float64{3, 4, 5} {
		errorValue, _ := responses[id]["error"].(map[string]any)
		if errorValue["code"] != float64(-32601) || errorValue["message"] != "Method not found" {
			t.Fatalf("method %v = %#v", id, responses[id])
		}
		if _, hasData := errorValue["data"]; hasData {
			t.Fatalf("method %v carries data: %#v", id, errorValue)
		}
	}
}

// Parity-exempt: FastMCP 对 prompts/resources 空面的响应（列表为空、未知对象
// 报 error code 0）是 mcp 1.26.0 库行为，上游无 pytest；按实测钉住。
func TestTheMCPServerAnswersTheEmptyPromptAndResourceSurface(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"hive://nope"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, envFromMap(nil)); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	assertJSONEqual(t, "resources/list", responses[1]["result"], `{"resources": []}`)
	assertJSONEqual(t, "resources/templates/list", responses[2]["result"], `{"resourceTemplates": []}`)
	assertJSONEqual(t, "prompts/list", responses[3]["result"], `{"prompts": []}`)
	promptError, _ := responses[4]["error"].(map[string]any)
	if promptError["code"] != float64(0) || promptError["message"] != "Unknown prompt: nope" {
		t.Fatalf("prompts/get = %#v", responses[4])
	}
	resourceError, _ := responses[5]["error"].(map[string]any)
	if resourceError["code"] != float64(0) || resourceError["message"] != "Unknown resource: hive://nope" {
		t.Fatalf("resources/read = %#v", responses[5])
	}
}
