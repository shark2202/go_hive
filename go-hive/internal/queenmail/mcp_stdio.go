package queenmail

// Parity: framework/server/queen_mail_mcp.py
//
// The `hive` MCP server a Claude Code queen's runs get: list_queens and
// message_queen. Claude Code starts it (--mcp-config, written by the runner)
// with HIVE_QUEEN_MAIL_URL, HIVE_QUEEN_MAIL_TOKEN and HIVE_QUEEN_MAIL_FROM
// (the conversation she writes from) in its environment, and it calls back
// into `hive serve`'s /api/queen-mail/... routes. Only the standard library,
// so it starts quickly and builds for every platform.
//
// The JSON-RPC surface mirrors the FastMCP 1.26.0 server upstream registers:
// initialize capabilities and protocol-version negotiation, tool definitions
// (descriptions, titles, outputSchema), tools/call envelopes and error codes.
// Measured field by field by docs/e2e-assets/queen-mail-mcp/probe.py; the
// result and the one deliberate exception (serverInfo.version) are recorded
// in docs/queen-mail.md §6.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/version"
)

// queenMailMCPTimeout bounds one call back into Hive. Handing a letter to a
// queen who has to start first can take a while; a timeout here would report a
// letter as failed that then arrives, and invite a duplicate.
const queenMailMCPTimeout = 180 * time.Second

// queenMailMCPProtocols are the MCP revisions upstream's FastMCP session
// accepts (mcp.shared.version.SUPPORTED_PROTOCOL_VERSIONS at 1.26.0).
var queenMailMCPProtocols = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

// queenMailMCPLatestProtocol is what FastMCP answers a client whose requested
// revision it doesn't know (mcp.types.LATEST_PROTOCOL_VERSION at 1.26.0).
const queenMailMCPLatestProtocol = "2025-11-25"

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
	Code    int     `json:"code"`
	Message string  `json:"message"`
	Data    *string `json:"data,omitempty"`
}

// invalidParams is the error FastMCP's session answers with when a request
// parses as no known method or its parameters fail validation: -32602 with an
// explicit empty data string.
func invalidParams() *mcpError {
	empty := ""
	return &mcpError{Code: -32602, Message: "Invalid request parameters", Data: &empty}
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
			ProtocolVersion *string `json:"protocolVersion"`
		}
		// FastMCP validates the request before answering; a missing
		// protocolVersion is an invalid-params error, not a default.
		if err := json.Unmarshal(request.Params, &params); err != nil || params.ProtocolVersion == nil {
			response.Error = invalidParams()
			break
		}
		protocol := *params.ProtocolVersion
		if !slices.Contains(queenMailMCPProtocols, protocol) {
			protocol = queenMailMCPLatestProtocol
		}
		response.Result = map[string]any{
			"protocolVersion": protocol,
			// FastMCP advertises all four blocks even when the server has no
			// prompts or resources.
			"capabilities": map[string]any{
				"experimental": map[string]any{},
				"prompts":      map[string]any{"listChanged": false},
				"resources":    map[string]any{"subscribe": false, "listChanged": false},
				"tools":        map[string]any{"listChanged": false},
			},
			// Parity-exempt (deliberate exception, docs/queen-mail.md §6):
			// upstream reports the version of the FastMCP library it happens
			// to run (1.26.0, via importlib.metadata). A Go binary has no such
			// library version, so it reports its own.
			"serverInfo": map[string]any{"name": "hive", "version": version.Version},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": queenMailMCPTools()}
	case "tools/call":
		text, toolError, problem := callQueenMailTool(request.Params, getenv)
		if problem != nil {
			response.Error = problem
			break
		}
		// FastMCP wraps every answer in a CallToolResult: the text content
		// Claude Code shows, plus structuredContent {result: <text>}
		// (validated against outputSchema) and isError. An unknown tool is a
		// tool-level error result, not a JSON-RPC error.
		result := map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": toolError,
		}
		if !toolError {
			result["structuredContent"] = map[string]any{"result": text}
		}
		response.Result = result
	case "resources/list":
		response.Result = map[string]any{"resources": []map[string]any{}}
	case "resources/templates/list":
		response.Result = map[string]any{"resourceTemplates": []map[string]any{}}
	case "prompts/list":
		response.Result = map[string]any{"prompts": []map[string]any{}}
	case "prompts/get":
		var params struct {
			Name *string `json:"name"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == nil {
			response.Error = invalidParams()
			break
		}
		// FastMCP raises a plain ValueError for a prompt it doesn't have,
		// which its session turns into error code 0; replicated as measured.
		response.Error = &mcpError{Code: 0, Message: "Unknown prompt: " + *params.Name}
	case "resources/read":
		var params struct {
			URI *string `json:"uri"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.URI == nil {
			response.Error = invalidParams()
			break
		}
		// Same code-0 path as prompts/get: this server has no resources.
		response.Error = &mcpError{Code: 0, Message: "Unknown resource: " + *params.URI}
	case "logging/setLevel", "completion/complete", "resources/subscribe", "resources/unsubscribe",
		"tasks/list", "tasks/get", "tasks/result", "tasks/cancel":
		// MCP requests a bare FastMCP server knows but has no handler for.
		response.Error = &mcpError{Code: -32601, Message: "Method not found"}
	default:
		// Anything that parses as no known server request (including
		// client-to-server methods) gets FastMCP's validation error.
		response.Error = invalidParams()
	}
	return response
}

// queenMailMCPTools is the tool catalogue exactly as FastMCP 1.26.0 registers
// it for upstream's module: descriptions are the raw function __doc__ strings
// (message_queen's keeps its "\n    " continuation indent, byte for byte),
// the schema and parameter titles are the ones pydantic/FastMCP generate,
// and the outputSchema is the wrapper FastMCP derives from the `-> str`
// return annotation. Measured copy:
// docs/e2e-assets/queen-mail-mcp/handshake.json (probe.py).
func queenMailMCPTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_queens",
			"description": "The other queens you can write to with message_queen: each one's id, name and title.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"title":      "list_queensArguments",
			},
			"outputSchema": queenMailMCPOutputSchema("list_queens"),
		},
		{
			"name":        "message_queen",
			"description": "Write to another queen. It returns at once; her answer arrives later as a new message in this\n    conversation, \"[Name(queen_id)]: ...\". She can't see your conversation, so say what she needs to know.\n    Returns {sent, to, depth}, or {error} (no such queen, today's limit reached, or the chain of letters\n    that led here is too long).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					// pydantic titles each parameter: name.title().replace('_', ' ').
					"queen_id": map[string]any{"type": "string", "title": "Queen Id"},
					"message":  map[string]any{"type": "string", "title": "Message"},
				},
				"required": []string{"queen_id", "message"},
				"title":    "message_queenArguments",
			},
			"outputSchema": queenMailMCPOutputSchema("message_queen"),
		},
	}
}

// queenMailMCPOutputSchema is FastMCP's structured-output schema for a tool
// annotated `-> str`: a named wrapper model with one required "result" field.
func queenMailMCPOutputSchema(tool string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result": map[string]any{"type": "string", "title": "Result"},
		},
		"required": []string{"result"},
		"title":    tool + "Output",
	}
}

// callQueenMailTool runs one tools/call and returns the JSON text Claude Code
// shows her, whether FastMCP would report it as a tool-level error (an unknown
// tool), and the invalid-params error for arguments it can't read.
func callQueenMailTool(params json.RawMessage, getenv func(string) string) (string, bool, *mcpError) {
	var call struct {
		Name      *string        `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || call.Name == nil {
		return "", false, invalidParams()
	}
	baseURL := getenv("HIVE_QUEEN_MAIL_URL")
	token := getenv("HIVE_QUEEN_MAIL_TOKEN")
	from := getenv("HIVE_QUEEN_MAIL_FROM")
	switch *call.Name {
	case "list_queens":
		query := url.Values{"from": []string{from}}
		answer := queenMailCall(baseURL, token, http.MethodGet, "/api/queen-mail/queens?"+query.Encode(), nil)
		return encodeJSONText(answer), false, nil
	case "message_queen":
		body := map[string]any{
			"from":    from,
			"to":      argumentString(call.Arguments, "queen_id"),
			"message": argumentString(call.Arguments, "message"),
		}
		answer := queenMailCall(baseURL, token, http.MethodPost, "/api/queen-mail/letters", body)
		return encodeJSONText(answer), false, nil
	}
	return "Unknown tool: " + *call.Name, true, nil
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
