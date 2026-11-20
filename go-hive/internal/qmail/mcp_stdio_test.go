package qmail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
	if text := toolCallResult(t, responses[1]); text != `{"error":"That's your own id"}` {
		t.Fatalf("tool text = %q", text)
	}
}

// Parity-exempt: 上游 FastMCP 自带的协议行为（未知方法、ping、无 id 的通知不回包）
// 没有对应 pytest；这里钉住 Go 版的最小协议面。
func TestTheMCPServerAnswersTheProtocolEdgeCases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queens":[]}`))
	}))
	defer server.Close()
	env := envFromMap(map[string]string{"HIVE_QUEEN_MAIL_URL": server.URL})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeQueenMailMCP(strings.NewReader(input), &output, env); err != nil {
		t.Fatal(err)
	}
	responses := mcpResponses(t, output.String())
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if _, ok := responses[7]["result"]; !ok {
		t.Fatalf("ping = %#v", responses[7])
	}
	if errorValue, _ := responses[8]["error"].(map[string]any); errorValue["code"] != float64(-32601) {
		t.Fatalf("unknown method = %#v", responses[8])
	}
	if errorValue, _ := responses[9]["error"].(map[string]any); errorValue["code"] != float64(-32602) {
		t.Fatalf("unknown tool = %#v", responses[9])
	}
}
