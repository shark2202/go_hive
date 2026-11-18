package tools

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const mcpSSETimeout = 30 * time.Second

type mcpSSEDialTarget struct {
	URL     string
	Headers map[string]string
	Timeout time.Duration
	Server  string
}

type mcpSSESession interface {
	Initialize() error
	ListTools() ([]MCPTool, error)
	CallTool(name string, arguments map[string]any) (any, error)
	Close() error
}

var newMCPSSESession = func(target mcpSSEDialTarget) (mcpSSESession, error) {
	return dialMCPSSESession(target)
}

type sseMCPClient struct {
	config *MCPServerConfig

	mu            sync.Mutex
	session       mcpSSESession
	tools         map[string]MCPTool
	conn          bool
	probeFailures int

	sessionFn   func(target mcpSSEDialTarget) (mcpSSESession, error)
	reconnectFn func()
}

func newSSEMCPClient(config *MCPServerConfig) *sseMCPClient {
	return &sseMCPClient{config: config, tools: map[string]MCPTool{}}
}

func (c *sseMCPClient) Config() *MCPServerConfig { return c.config }

func (c *sseMCPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn {
		return nil
	}
	if c.config.URL == "" {
		return errors.New("url is required for SSE transport")
	}

	dial := c.sessionFn
	if dial == nil {
		dial = newMCPSSESession
	}
	session, err := dial(mcpSSEDialTarget{
		URL:     c.config.URL,
		Headers: c.config.Headers,
		Timeout: mcpSSETimeout,
		Server:  c.config.Name,
	})
	if err != nil {
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}
	if err := session.Initialize(); err != nil {
		_ = session.Close()
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}
	tools, err := session.ListTools()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}
	byName := make(map[string]MCPTool, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		if tool.ServerName == "" {
			tool.ServerName = c.config.Name
		}
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{}
		}
		byName[tool.Name] = tool
	}
	c.session = session
	c.tools = byName
	c.conn = true
	c.probeFailures = 0
	return nil
}

func (c *sseMCPClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
	c.conn = false
	c.tools = map[string]MCPTool{}
	return nil
}

func (c *sseMCPClient) ListTools() []MCPTool {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return sortedMCPTools(c.tools)
}

func (c *sseMCPClient) CallTool(name string, arguments map[string]any) (any, error) {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	_, known := c.tools[name]
	session := c.session
	c.mu.Unlock()
	if !known {
		return nil, &MCPToolNotFoundError{Server: c.config.Name, ToolName: name}
	}
	if session == nil {
		return nil, &mcpTransportClosedError{msg: "MCP SSE transport closed: session not initialized"}
	}

	call := func() (any, error) { return session.CallTool(name, arguments) }
	result, err := call()
	if err == nil {
		return result, nil
	}
	if !isMCPDeadSessionError(err) {
		return nil, err
	}
	original := err
	if c.reconnectFn != nil {
		c.reconnectFn()
	} else if reconnectErr := c.Reconnect(); reconnectErr != nil {
		return nil, original
	}
	retried, retryErr := call()
	if retryErr != nil {
		return nil, original
	}
	return retried, nil
}

func (c *sseMCPClient) Reconnect() error {
	_ = c.Disconnect()
	return c.Connect()
}

func (c *sseMCPClient) ProbeLiveness() bool {
	if !c.IsConnected() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probeFailures = 0
	return true
}

func (c *sseMCPClient) ConsecutiveProbeFailures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probeFailures
}

func (c *sseMCPClient) InflightCalls() int { return 0 }

func (c *sseMCPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

func (c *sseMCPClient) LifecycleBusy() bool { return false }

type realSSESession struct {
	server  string
	url     string
	headers map[string]string
	client  *http.Client
	body    io.ReadCloser

	mu       sync.Mutex
	endpoint string
	nextID   int64
	pending  map[int64]chan mcpRPCResponse
	closed   chan struct{}

	closeOnce sync.Once
}

func dialMCPSSESession(target mcpSSEDialTarget) (mcpSSESession, error) {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: target.Timeout}).DialContext,
	}
	client := &http.Client{Transport: transport}

	req, err := http.NewRequest(http.MethodGet, target.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyMCPHTTPError(err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	session := &realSSESession{
		server:  target.Server,
		url:     target.URL,
		headers: target.Headers,
		client:  client,
		body:    resp.Body,
		pending: map[int64]chan mcpRPCResponse{},
		closed:  make(chan struct{}),
	}

	endpointCh := make(chan string, 1)
	go session.readLoop(endpointCh)

	select {
	case endpoint := <-endpointCh:
		session.mu.Lock()
		session.endpoint = endpoint
		session.mu.Unlock()
		return session, nil
	case <-session.closed:
		return nil, fmt.Errorf("SSE event stream closed before the endpoint event (server %q)", target.Server)
	case <-time.After(target.Timeout):
		_ = session.Close()
		return nil, fmt.Errorf("Timed out waiting for MCP SSE connection to initialize (server %q)", target.Server)
	}
}

func (s *realSSESession) resolveEndpoint(raw string) (string, bool) {
	base, err := url.Parse(s.url)
	if err != nil {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	return base.ResolveReference(ref).String(), true
}

func (s *realSSESession) readLoop(endpointCh chan<- string) {
	reader := bufio.NewReaderSize(s.body, 64*1024)
	event := "message"
	data := make([]string, 0, 4)

	dispatch := func() {
		if len(data) == 0 {
			return
		}
		payload := strings.Join(data, "\n")
		switch event {
		case "endpoint":
			if endpoint, ok := s.resolveEndpoint(payload); ok {
				select {
				case endpointCh <- endpoint:
				default:
				}
			}
		case "message":
			var response mcpRPCResponse
			if err := json.Unmarshal([]byte(payload), &response); err != nil || response.ID == nil {
				return
			}
			if id, ok := parseRPCID(*response.ID); ok {
				s.deliver(id, response)
			}
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			s.failAll(err)
			s.shutdown()
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			dispatch()
			event = "message"
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
}

func (s *realSSESession) deliver(id int64, response mcpRPCResponse) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if ok {
		select {
		case ch <- response:
		default:
		}
	}
}

func (s *realSSESession) failAll(err error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[int64]chan mcpRPCResponse{}
	s.mu.Unlock()
	closedErr := &mcpTransportClosedError{msg: "MCP SSE transport closed: " + err.Error()}
	for _, ch := range pending {
		select {
		case ch <- mcpRPCResponse{Error: &mcpRPCError{Code: -32000, Message: closedErr.Error()}}:
		default:
		}
	}
}

func (s *realSSESession) shutdown() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.body.Close()
		s.client.CloseIdleConnections()
	})
}

func (s *realSSESession) post(payload map[string]any) (*mcpHTTPResponse, error) {
	s.mu.Lock()
	endpoint := s.endpoint
	s.mu.Unlock()
	if endpoint == "" {
		return nil, &mcpTransportClosedError{msg: "MCP SSE transport closed: session not initialized"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, classifyMCPHTTPError(err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return &mcpHTTPResponse{Status: resp.StatusCode, Body: raw}, nil
}

func (s *realSSESession) notify(method string, params map[string]any) error {
	_, err := s.post(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	return err
}

func (s *realSSESession) rpc(method string, params map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.endpoint == "" {
		s.mu.Unlock()
		return nil, &mcpTransportClosedError{msg: "MCP SSE transport closed: session not initialized"}
	}
	s.nextID++
	id := s.nextID
	ch := make(chan mcpRPCResponse, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	_, err := s.post(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(mcpCallResultTimeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		if response.Error != nil {
			return nil, response.Error
		}
		return response.Result, nil
	case <-s.closed:
		return nil, &mcpTransportClosedError{msg: "MCP SSE transport closed: connection reset"}
	case <-timer.C:
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, &mcpTransportClosedError{msg: fmt.Sprintf(
			"MCP call exceeded %s and was abandoned (transport closed): server=%s",
			mcpCallResultTimeout, s.server)}
	}
}

func (s *realSSESession) Initialize() error {
	if _, err := s.rpc("initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "hive-go", "version": "0.1.0"},
	}); err != nil {
		return err
	}
	return s.notify("notifications/initialized", map[string]any{})
}

func (s *realSSESession) ListTools() ([]MCPTool, error) {
	raw, err := s.rpc("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	tools := make([]MCPTool, 0, len(result.Tools))
	for _, t := range result.Tools {
		if t.Name == "" {
			continue
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{}
		}
		tools = append(tools, MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
			ServerName:  s.server,
		})
	}
	return tools, nil
}

func (s *realSSESession) CallTool(name string, arguments map[string]any) (any, error) {
	raw, err := s.rpc("tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	return mcpCallResultValue(s.server, "sse", name, raw)
}

func (s *realSSESession) Close() error {
	s.shutdown()
	return nil
}
