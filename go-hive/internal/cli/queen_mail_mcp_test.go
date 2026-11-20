package cli

// `hive queen-mail-mcp` is the stdio MCP server a Claude Code queen's runs
// are given; this checks the command wiring itself.
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity-exempt: 上游以 pytest 直接调用 queen_mail_mcp 模块；Go 侧另测命令装配
// —— `hive queen-mail-mcp` 必须真的把 stdio JSON-RPC 接到 qmail.ServeQueenMailMCP。
func TestQueenMailMCPCommandServesJSONRPC(t *testing.T) {
	var seenToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken = r.Header.Get("X-Queen-Mail-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"queens":[]}`))
	}))
	defer server.Close()

	t.Setenv("HIVE_QUEEN_MAIL_URL", server.URL)
	t.Setenv("HIVE_QUEEN_MAIL_TOKEN", "t0ken")
	t.Setenv("HIVE_QUEEN_MAIL_FROM", "claude_code:queen_claude:abc")

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_queens","arguments":{}}}` + "\n"
	var stdout, stderr bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(input)}
	if code := runner.Run([]string{"queen-mail-mcp"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &answer); err != nil {
		t.Fatalf("answer %q: %v", stdout.String(), err)
	}
	result, _ := answer["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 || seenToken != "t0ken" {
		t.Fatalf("answer = %#v, token = %q", answer, seenToken)
	}
}
