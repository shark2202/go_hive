package queenmail

// Parity: framework/server/queen_mail_mcp.py
//
// The `hive` MCP server a Claude Code queen's runs get: list_queens and
// message_queen. Claude Code starts it (--mcp-config, written by the runner)
// with HIVE_QUEEN_MAIL_URL, HIVE_QUEEN_MAIL_TOKEN and HIVE_QUEEN_MAIL_FROM
// (the conversation she writes from) in its environment, and it calls back
// into `hive serve`'s /api/queen-mail/... routes. Only the standard library,
// so it starts quickly and builds for every platform.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/version"
)

// queenMailMCPTimeout bounds one call back into Hive. Handing a letter to a
// queen who has to start first can take a while; a timeout here would report a
// letter as failed that then arrives, and invite a duplicate.
const queenMailMCPTimeout = 180 * time.Second

// queenMailMCPProtocol is the MCP revision used when the client names none.
const queenMailMCPProtocol = "2024-11-05"

// ServeQueenMailMCP reads JSON-RPC messages from stdin and answers on stdout
// until stdin closes. getenv supplies HIVE_QUEEN_MAIL_URL/TOKEN/FROM and is
// os.Getenv in production (overridable in tests).
func ServeQueenMailMCP(stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request mcpRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		if len(request.ID) == 0 || string(request.ID) == "null" {
			// A notification: no answer, whatever its method.
			continue
		}
		response := answerMCPRequest(request, getenv)
		data, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

func answerMCPRequest(request mcpRequest, getenv func(string) string) mcpResponse {
	response := mcpResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		protocol := params.ProtocolVersion
		if protocol == "" {
			protocol = queenMailMCPProtocol
		}
		response.Result = map[string]any{
			"protocolVersion": protocol,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "hive", "version": version.Version},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": queenMailMCPTools()}
	case "tools/call":
		result, problem := callQueenMailTool(request.Params, getenv)
		if problem != "" {
			response.Error = &mcpError{Code: -32602, Message: problem}
			break
		}
		response.Result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": result}},
		}
	default:
		response.Error = &mcpError{Code: -32601, Message: "Method not found"}
	}
	return response
}

// queenMailMCPTools is the tool catalogue, with the descriptions upstream's
// FastMCP server registers.
func queenMailMCPTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_queens",
			"description": "The other queens you can write to with message_queen: each one's id, name and title.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "message_queen",
			"description": "Write to another queen. It returns at once; her answer arrives later as a new message in this " +
				"conversation, \"[Name(queen_id)]: ...\". She can't see your conversation, so say what she needs to know. " +
				"Returns {sent, to, depth}, or {error} (no such queen, today's limit reached, or the chain of letters " +
				"that led here is too long).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queen_id": map[string]any{"type": "string"},
					"message":  map[string]any{"type": "string"},
				},
				"required": []string{"queen_id", "message"},
			},
		},
	}
}

// callQueenMailTool runs one tool call and returns the JSON text Claude Code
// shows her.
func callQueenMailTool(params json.RawMessage, getenv func(string) string) (string, string) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return "", "Invalid tool call"
	}
	baseURL := getenv("HIVE_QUEEN_MAIL_URL")
	token := getenv("HIVE_QUEEN_MAIL_TOKEN")
	from := getenv("HIVE_QUEEN_MAIL_FROM")
	switch call.Name {
	case "list_queens":
		query := url.Values{"from": []string{from}}
		answer := queenMailCall(baseURL, token, http.MethodGet, "/api/queen-mail/queens?"+query.Encode(), nil)
		return encodeJSONText(answer), ""
	case "message_queen":
		body := map[string]any{
			"from":    from,
			"to":      argumentString(call.Arguments, "queen_id"),
			"message": argumentString(call.Arguments, "message"),
		}
		answer := queenMailCall(baseURL, token, http.MethodPost, "/api/queen-mail/letters", body)
		return encodeJSONText(answer), ""
	}
	return "", "Unknown tool: " + call.Name
}

func argumentString(arguments map[string]any, key string) string {
	if text, ok := arguments[key].(string); ok {
		return text
	}
	return ""
}

// queenMailCall makes one request and returns the JSON object Hive answered,
// with transport and HTTP errors reported the way upstream's _call does.
func queenMailCall(baseURL, token, method, path string, body any) map[string]any {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return map[string]any{"error": "Can't reach Hive: " + err.Error()}
		}
		reader = strings.NewReader(string(data))
	}
	request, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		return map[string]any{"error": "Can't reach Hive: " + err.Error()}
	}
	request.Header.Set("X-Queen-Mail-Token", token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: queenMailMCPTimeout}
	response, err := client.Do(request)
	if err != nil {
		return map[string]any{"error": "Can't reach Hive: " + err.Error()}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return map[string]any{"error": "Can't reach Hive: " + err.Error()}
	}
	var answer map[string]any
	if err := json.Unmarshal(raw, &answer); err == nil && answer != nil {
		if response.StatusCode >= 400 && answer["error"] == nil {
			return map[string]any{"error": fmt.Sprintf("Hive answered %d", response.StatusCode)}
		}
		return answer
	}
	if response.StatusCode >= 400 {
		return map[string]any{"error": fmt.Sprintf("Hive answered %d", response.StatusCode)}
	}
	return map[string]any{"error": "Hive answered something that isn't JSON"}
}

// encodeJSONText renders one tool answer as the text content Claude Code
// shows her (json.dumps without HTML escaping, as upstream does).
func encodeJSONText(value map[string]any) string {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `{"error":"can't encode the answer"}`
	}
	return strings.TrimSuffix(builder.String(), "\n")
}
