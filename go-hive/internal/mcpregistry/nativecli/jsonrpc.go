package nativecli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// protocolVersion is the MCP revision this client offers; servers answer with
// the revision they will speak and the answer wins.
const protocolVersion = "2025-11-25"

// transport carries JSON-RPC messages for one MCP server connection.
type transport interface {
	// Send writes one serialized JSON-RPC message.
	Send(message []byte) error
	// Receive returns the next JSON-RPC message from the server.
	Receive() ([]byte, error)
	// Close releases the connection.
	Close() error
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("JSON-RPC error %d", e.Code)
}

// session is one initialized MCP session over a transport.
type session struct {
	transport transport
	source    string
	roots     []string
	nextID    int64
	version   string
}

// call sends a request and waits for its response, answering any server
// requests (roots/list, ping) that arrive first.
func (s *session) call(method string, params any) (any, error) {
	s.nextID++
	id := s.nextID
	request := Ordered{
		{Key: "jsonrpc", Value: "2.0"},
		{Key: "id", Value: id},
		{Key: "method", Value: method},
	}
	if params != nil {
		request = append(request, Field{Key: "params", Value: params})
	}
	if err := s.transport.Send([]byte(MarshalPy(request))); err != nil {
		return nil, err
	}
	for {
		raw, err := s.transport.Receive()
		if err != nil {
			return nil, err
		}
		message, err := ParseJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON-RPC message: %w", err)
		}
		object, ok := message.(Ordered)
		if !ok {
			continue
		}
		methodValue, hasMethod := object.Lookup("method")
		if hasMethod {
			if messageID, hasID := object.Lookup("id"); hasID {
				if err := s.answer(stringOf(methodValue), messageID, object); err != nil {
					return nil, err
				}
			}
			continue
		}
		messageID, hasID := object.Lookup("id")
		if !hasID || !sameRPCID(messageID, id) {
			continue
		}
		if errorValue, found := object.Lookup("error"); found {
			return nil, decodeRPCError(errorValue)
		}
		result, _ := object.Lookup("result")
		return result, nil
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (s *session) notify(method string, params any) error {
	notification := Ordered{
		{Key: "jsonrpc", Value: "2.0"},
		{Key: "method", Value: method},
	}
	if params != nil {
		notification = append(notification, Field{Key: "params", Value: params})
	}
	return s.transport.Send([]byte(MarshalPy(notification)))
}

// answer replies to a server-initiated request.
func (s *session) answer(method string, id any, request Ordered) error {
	switch method {
	case "roots/list":
		roots := []any{}
		for _, raw := range s.roots {
			uri := raw
			name := filepath.Base(raw)
			if parsed, err := url.Parse(raw); err == nil {
				if path := parsed.Path; path != "" {
					name = filepath.Base(path)
				}
			}
			roots = append(roots, Ordered{
				{Key: "uri", Value: uri},
				{Key: "name", Value: name},
			})
		}
		return s.transport.Send([]byte(MarshalPy(Ordered{
			{Key: "jsonrpc", Value: "2.0"},
			{Key: "id", Value: id},
			{Key: "result", Value: Ordered{{Key: "roots", Value: roots}}},
		})))
	case "ping":
		return s.transport.Send([]byte(MarshalPy(Ordered{
			{Key: "jsonrpc", Value: "2.0"},
			{Key: "id", Value: id},
			{Key: "result", Value: Ordered{}},
		})))
	default:
		return s.transport.Send([]byte(MarshalPy(Ordered{
			{Key: "jsonrpc", Value: "2.0"},
			{Key: "id", Value: id},
			{Key: "error", Value: Ordered{
				{Key: "code", Value: -32601},
				{Key: "message", Value: "Method not found: " + method},
			}},
		})))
	}
}

// initialize performs the MCP handshake.
func (s *session) initialize() error {
	result, err := s.call("initialize", Ordered{
		{Key: "protocolVersion", Value: protocolVersion},
		{Key: "capabilities", Value: Ordered{}},
		{Key: "clientInfo", Value: Ordered{
			{Key: "name", Value: "mcp2cli"},
			{Key: "version", Value: Version},
		}},
	})
	if err != nil {
		return err
	}
	if object, ok := result.(Ordered); ok {
		if value, found := object.Lookup("protocolVersion"); found {
			s.version = stringOf(value)
		}
	}
	return s.notify("notifications/initialized", nil)
}

// listTools fetches every tool, following nextCursor to exhaustion.
func (s *session) listTools() ([]any, error) {
	tools := []any{}
	cursor := ""
	for {
		var params any
		if cursor != "" {
			params = Ordered{{Key: "cursor", Value: cursor}}
		}
		result, err := s.call("tools/list", params)
		if err != nil {
			return nil, err
		}
		object, ok := result.(Ordered)
		if !ok {
			return tools, nil
		}
		if value, found := object.Lookup("tools"); found {
			if items, ok := value.([]any); ok {
				tools = append(tools, items...)
			}
		}
		next, _ := object.Lookup("nextCursor")
		cursor = stringOf(next)
		if cursor == "" {
			return tools, nil
		}
	}
}

// callTool invokes a tool and returns its CallToolResult.
func (s *session) callTool(name string, arguments Ordered) (Ordered, error) {
	result, err := s.call("tools/call", Ordered{
		{Key: "name", Value: name},
		{Key: "arguments", Value: arguments},
	})
	if err != nil {
		return nil, err
	}
	object, ok := result.(Ordered)
	if !ok {
		return Ordered{}, nil
	}
	return object, nil
}

// decodeRPCError turns a JSON-RPC error object into an error.
func decodeRPCError(value any) error {
	object, ok := value.(Ordered)
	if !ok {
		return &rpcError{Message: fmt.Sprint(value)}
	}
	rpcErr := &rpcError{}
	if code, found := object.Lookup("code"); found {
		if number, ok := code.(json.Number); ok {
			if parsed, err := number.Int64(); err == nil {
				rpcErr.Code = int(parsed)
			}
		}
	}
	if message, found := object.Lookup("message"); found {
		rpcErr.Message = stringOf(message)
	}
	return rpcErr
}

func sameRPCID(left, right any) bool {
	leftNumber, leftOK := left.(json.Number)
	rightNumber, rightOK := right.(json.Number)
	if leftOK && rightOK {
		return string(leftNumber) == string(rightNumber)
	}
	if leftText, ok := left.(string); ok {
		if rightValue, ok := right.(json.Number); ok {
			return leftText == string(rightValue)
		}
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

// stringOf renders a decoded JSON scalar as a string.
func stringOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

// normalizeRoots validates --root values into file URIs.
func normalizeRoots(values []string) ([]string, error) {
	roots := make([]string, 0, len(values))
	for _, raw := range values {
		if strings.Contains(raw, "://") {
			if !strings.HasPrefix(strings.ToLower(raw), "file://") {
				return nil, fmt.Errorf("--root expects a filesystem path or file:// URI, got %s", pyRepr(raw))
			}
			roots = append(roots, raw)
			continue
		}
		absolute, err := filepath.Abs(expandHome(raw))
		if err != nil {
			return nil, err
		}
		roots = append(roots, fileURI(absolute))
	}
	return roots, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// fileURI renders an absolute filesystem path as a file:// URI.
func fileURI(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	escaped := (&url.URL{Path: slashed}).EscapedPath()
	return "file://" + escaped
}
