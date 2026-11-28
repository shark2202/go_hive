package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var mcpConnectReadyTimeout = 10 * time.Second

// mcpCallResultTimeoutOverride carries the serve-owned config value
// (configuration.json mcp.timeout_seconds). Zero means "no file value".
var mcpCallResultTimeoutOverride atomic.Int64

// mcpCallResultTimeoutDefault is the built-in ceiling used when neither the
// environment nor configuration.json states a timeout.
const mcpCallResultTimeoutDefault = 240 * time.Second

// SetMCPCallResultTimeout records the configuration.json mcp.timeout_seconds
// value for the whole process. serve calls it once while reading the MCP config,
// before any client connects; a non-positive duration is ignored so a bad config
// cannot make MCP calls fail instantly. The environment still outranks this
// value: the precedence is env > file > 240s default.
func SetMCPCallResultTimeout(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	mcpCallResultTimeoutOverride.Store(int64(timeout))
}

// MCPCallResultTimeout resolves the tools/call timeout with the general
// configuration rule — env > configuration.json > built-in default. Python reads
// HIVE_MCP_CALL_RESULT_TIMEOUT once at import; reading it per call here keeps a
// long-lived process honest after an operator edits the environment of a reload.
func MCPCallResultTimeout() time.Duration {
	if timeout, ok := mcpTimeoutFromEnv(); ok {
		return timeout
	}
	if override := mcpCallResultTimeoutOverride.Load(); override > 0 {
		return time.Duration(override)
	}
	return mcpCallResultTimeoutDefault
}

func mcpTimeoutFromEnv() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("HIVE_MCP_CALL_RESULT_TIMEOUT"))
	if raw == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

const mcpReconnectCooldown = 30 * time.Second

// mcpProtocolVersion is the revision the client offers in initialize. The MCP
// SDK upstream ships (mcp.types.LATEST_PROTOCOL_VERSION) is the offer, so this
// tracks the newest revision rather than pinning an older one; the server's
// answer is validated against supportedMCPProtocolVersions below.
const mcpProtocolVersion = "2025-11-25"

// supportedMCPProtocolVersions mirrors mcp.shared.version.
// SUPPORTED_PROTOCOL_VERSIONS: a server may negotiate any of these, and any
// other value is a hard connect error ("Unsupported protocol version from the
// server: <v>").
var supportedMCPProtocolVersions = map[string]struct{}{
	"2024-11-05": {},
	"2025-03-26": {},
	"2025-06-18": {},
	"2025-11-25": {},
}

// validateMCPProtocolVersion checks the initialize result's protocolVersion the
// way ClientSession.initialize does after a server answers. An empty or
// unparseable result fails like an unsupported version, because upstream's
// InitializeResult requires the field.
func validateMCPProtocolVersion(result json.RawMessage) error {
	version := ""
	if len(result) > 0 {
		var payload struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(result, &payload); err == nil {
			version = payload.ProtocolVersion
		}
	}
	if _, ok := supportedMCPProtocolVersions[version]; ok {
		return nil
	}
	return fmt.Errorf("Unsupported protocol version from the server: %s", version)
}

type MCPToolNotFoundError struct {
	Server   string
	ToolName string
}

func (e *MCPToolNotFoundError) Error() string {
	return fmt.Sprintf("Tool '%s' not found on server '%s'", e.ToolName, e.Server)
}

type mcpTransportClosedError struct{ msg string }

func (e *mcpTransportClosedError) Error() string { return e.msg }

type mcpHTTPConnectError struct{ err error }

func (e *mcpHTTPConnectError) Error() string { return e.err.Error() }
func (e *mcpHTTPConnectError) Unwrap() error { return e.err }

type mcpHTTPTimeoutError struct{ err error }

func (e *mcpHTTPTimeoutError) Error() string { return e.err.Error() }
func (e *mcpHTTPTimeoutError) Unwrap() error { return e.err }

func NewMCPClient(config *MCPServerConfig) MCPClient {
	switch config.Transport {
	case "stdio":
		return newStdioMCPClient(config)
	case "http", "unix":
		return newHTTPMCPClient(config)
	case "sse":
		return newSSEMCPClient(config)
	default:
		return &unsupportedMCPClient{config: config}
	}
}

// SplitMCPContent splits an MCP tool result's content list into joined text and
// OpenAI-style image/file blocks.
//
// The decision tree is a literal transcription of
// core/framework/loader/mcp_client.py::_split_mcp_content, which keys on
// attribute presence (hasattr), not on value types:
//
//   - a block carrying `text` is a text block;
//   - a block carrying both `data` and `mimeType` is image/file content — even
//     when `mimeType` is null, because the SDK model always carries the field.
//     The data URL renders the value the way Python's f-string renders it
//     ("None" for null, "True"/"False" for booleans, str() for scalars);
//   - a block carrying `data` but no `mimeType` falls back to str(data) text;
//   - everything else (resource_link, embedded resource, unknown blocks) is
//     dropped.
//
// Only the camelCase `mimeType` field is recognised; Python reads the SDK field
// name and has no snake_case alias, so `mime_type` is not a second spelling.
// Frozen probe: go_hive/docs/e2e-assets/mcp-namespace/python-output.json
// (`split.null_mime`, `split.data_only`, `split.resource_link_only`).
func SplitMCPContent(items []any) (string, []map[string]any) {
	textParts := make([]string, 0, len(items))
	imageParts := make([]map[string]any, 0)

	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := item["text"].(string); ok {
			textParts = append(textParts, text)
			continue
		}

		data, hasData := item["data"]
		mime, hasMime := item["mimeType"]
		if hasData && hasMime {
			mimeText := mcpContentString(mime)
			if strings.ToLower(mimeText) == "application/pdf" {
				filename := "document.pdf"
				if meta, ok := item["_meta"].(map[string]any); ok {
					if name, ok := meta["filename"].(string); ok {
						filename = name
					}
				}
				imageParts = append(imageParts, map[string]any{
					"type": "file",
					"file": map[string]any{
						"file_data": "data:application/pdf;base64," + mcpContentString(data),
						"filename":  filename,
					},
				})
			} else {
				imageParts = append(imageParts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + mimeText + ";base64," + mcpContentString(data),
					},
				})
			}
			continue
		}
		if hasData {
			textParts = append(textParts, mcpContentString(data))
		}
	}

	text := ""
	if len(textParts) > 0 {
		text = strings.Join(textParts, "\n")
	}
	return text, imageParts
}

// mcpContentString renders a JSON-decoded content field the way Python's str()
// renders the corresponding SDK value. Only the malformed-payload cases need
// the Python spellings: a real TextContent/ImageContent carries str fields, but
// Go decodes raw JSON, so null/number/bool values reach the splitter. Python's
// str() spells null "None" and capitalises booleans; containers (unreachable
// through the SDK's str-typed fields) fall back to Go's fmt.Sprint.
func mcpContentString(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		// Shortest decimal spelling: JSON decodes every number to float64, and
		// Python's json layer keeps integral literals as int, whose str() has
		// no exponent ("1000000", not "1e+06"). 'f' matches that for the
		// integral and ordinary decimal cases that can appear in a payload.
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	}
	return fmt.Sprint(value)
}

func isMCPDeadSessionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var closed *mcpTransportClosedError
	if errors.As(err, &closed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"broken pipe",
		"connection closed",
		"connection reset",
		"stream closed",
		"session not initialized",
		"transport closed",
		"read operation was cancelled",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func isRetryableMCPHTTPError(err error) bool {
	if err == nil {
		return false
	}
	var connectErr *mcpHTTPConnectError
	var timeoutErr *mcpHTTPTimeoutError
	if errors.As(err, &connectErr) || errors.As(err, &timeoutErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func mergeMCPEnv(overrides map[string]string) []string {
	env := os.Environ()
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func resolveStdioArgs(args []string, cwd string) []string {
	if runtime.GOOS != "windows" || cwd == "" {
		return args
	}
	out := append([]string(nil), args...)
	for i, arg := range out {
		if strings.HasSuffix(arg, ".py") && !strings.HasPrefix(arg, "/") && !strings.Contains(arg, ":") {
			candidate := strings.TrimSuffix(cwd, "/") + "/" + arg
			if _, err := os.Stat(candidate); err == nil {
				out[i] = candidate
			}
		}
	}
	return out
}

type mcpHTTPResponse struct {
	Status int
	Body   []byte
}

type mcpHTTPClient interface {
	Get(path string) (*mcpHTTPResponse, error)
	Post(path string, body any) (*mcpHTTPResponse, error)
	Close() error
}

type realMCPHTTPClient struct {
	baseURL string
	headers map[string]string
	client  *http.Client
}

var newMCPHTTPClient = func(config *MCPServerConfig) mcpHTTPClient {
	return newRealMCPHTTPClient(config)
}

func newRealMCPHTTPClient(config *MCPServerConfig) mcpHTTPClient {
	transport := &http.Transport{}
	if config.Transport == "unix" && config.SocketPath != "" {
		socketPath := config.SocketPath
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
	}
	return &realMCPHTTPClient{
		baseURL: strings.TrimSuffix(config.URL, "/"),
		headers: config.Headers,
		client:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *realMCPHTTPClient) url(path string) string {
	return c.baseURL + path
}

func (c *realMCPHTTPClient) do(req *http.Request) (*mcpHTTPResponse, error) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, classifyMCPHTTPError(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, classifyMCPHTTPError(readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &mcpHTTPResponse{Status: resp.StatusCode, Body: body}, nil
}

func classifyMCPHTTPError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &mcpHTTPTimeoutError{err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &mcpHTTPTimeoutError{err: err}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return &mcpHTTPConnectError{err: err}
	}
	return err
}

func (c *realMCPHTTPClient) Get(path string) (*mcpHTTPResponse, error) {
	req, err := http.NewRequest(http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *realMCPHTTPClient) Post(path string, body any) (*mcpHTTPResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.url(path), strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *realMCPHTTPClient) Close() error {
	c.client.CloseIdleConnections()
	return nil
}

type httpMCPClient struct {
	config *MCPServerConfig

	mu           sync.Mutex
	http         mcpHTTPClient
	tools        map[string]MCPTool
	conn         bool
	blockedUntil time.Time

	probeFailures int

	callFn      func(name string, args map[string]any) (any, error)
	reconnectFn func()
}

func newHTTPMCPClient(config *MCPServerConfig) *httpMCPClient {
	return &httpMCPClient{config: config, tools: map[string]MCPTool{}}
}

func (c *httpMCPClient) Config() *MCPServerConfig { return c.config }

func (c *httpMCPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *httpMCPClient) connectLocked() error {
	if c.conn {
		return nil
	}
	if c.config.Transport == "http" && c.config.URL == "" {
		return errors.New("url is required for HTTP transport")
	}
	if c.config.Transport == "unix" {
		if c.config.URL == "" {
			return errors.New("url is required for UNIX transport")
		}
		if c.config.SocketPath == "" {
			return errors.New("socket_path is required for UNIX transport")
		}
	}

	c.http = newMCPHTTPClient(c.config)
	if _, err := c.http.Get("/health"); err != nil {
		_ = err
	}

	tools, err := c.listToolsLocked()
	if err != nil {
		_ = c.http.Close()
		c.http = nil
		return err
	}
	c.tools = tools
	c.conn = true
	c.probeFailures = 0
	return nil
}

func (c *httpMCPClient) listToolsLocked() (map[string]MCPTool, error) {
	// Follow nextCursor like mcp2cli 3.8.0 (and the native CLI): the first page
	// carries no cursor, later pages send the previous page's opaque cursor.
	tools, err := mcpListToolsPaged(func(cursor string) (json.RawMessage, error) {
		request := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/list",
			"params":  mcpToolsListParams(cursor),
		}
		resp, err := c.http.Post("/mcp/v1", request)
		if err != nil {
			return nil, fmt.Errorf("Failed to list tools via HTTP: %w", err)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(resp.Body, &envelope); err != nil {
			return nil, fmt.Errorf("Failed to list tools via HTTP: invalid JSON-RPC response: %w", err)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("MCP error: %v", envelope.Error)
		}
		return envelope.Result, nil
	})
	if err != nil {
		return nil, err
	}

	byName := make(map[string]MCPTool, len(tools))
	for _, tool := range tools {
		tool.ServerName = c.config.Name
		byName[tool.Name] = tool
	}
	return byName, nil
}

func (c *httpMCPClient) ListTools() []MCPTool {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return sortedMCPTools(c.tools)
}

func sortedMCPTools(tools map[string]MCPTool) []MCPTool {
	out := make([]MCPTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, t)
	}
	sortMCPTools(out)
	return out
}

func (c *httpMCPClient) CallTool(name string, arguments map[string]any) (any, error) {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	_, known := c.tools[name]
	transport := c.config.Transport
	c.mu.Unlock()
	if !known {
		return nil, &MCPToolNotFoundError{Server: c.config.Name, ToolName: name}
	}

	call := func() (any, error) {
		if c.callFn != nil {
			return c.callFn(name, arguments)
		}
		return c.callToolHTTP(name, arguments)
	}

	if transport != "unix" {
		return call()
	}
	return c.callWithRetry(call)
}

func (c *httpMCPClient) callWithRetry(call func() (any, error)) (any, error) {
	result, err := call()
	if err == nil {
		return result, nil
	}
	if !isRetryableMCPHTTPError(err) {
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

func (c *httpMCPClient) callToolHTTP(name string, arguments map[string]any) (any, error) {
	c.mu.Lock()
	httpClient := c.http
	c.mu.Unlock()
	if httpClient == nil {
		return nil, errors.New("HTTP client not initialized")
	}

	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	resp, err := httpClient.Post("/mcp/v1", request)
	if err != nil {
		return nil, c.wrapCallError(name, err)
	}
	data, err := decodeMCPRPCBody(resp.Body)
	if err != nil {
		return nil, c.wrapCallError(name, err)
	}
	if errObj, ok := data["error"]; ok && errObj != nil {
		return nil, c.wrapCallError(name, fmt.Errorf("%v", errObj))
	}
	result, _ := data["result"].(map[string]any)
	content := result["content"]
	if content == nil {
		return []any{}, nil
	}
	if list, ok := content.([]any); ok {
		// Python forwards result["content"] as json.loads built it, so a
		// first non-text block must keep the server's key order (L3).
		return mcpOrderFirstNonTextContentBlock(resp.Body, list), nil
	}
	return content, nil
}

func (c *httpMCPClient) wrapCallError(toolName string, err error) error {
	return fmt.Errorf("[Server: %s] [Transport: %s] Failed to call tool via HTTP: Tool '%s' failed: %w",
		c.config.Name, c.config.Transport, toolName, err)
}

func (c *httpMCPClient) Reconnect() error {
	c.mu.Lock()
	if time.Now().Before(c.blockedUntil) {
		remaining := int(time.Until(c.blockedUntil).Seconds())
		c.mu.Unlock()
		return fmt.Errorf("MCP server '%s' reconnect is in cooldown after a recent failure (%ds left); transport closed",
			c.config.Name, remaining)
	}
	c.mu.Unlock()

	_ = c.Disconnect()

	if err := c.Connect(); err != nil {
		c.mu.Lock()
		c.blockedUntil = time.Now().Add(mcpReconnectCooldown)
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.probeFailures = 0
	c.mu.Unlock()
	return nil
}

func (c *httpMCPClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.http != nil {
		_ = c.http.Close()
		c.http = nil
	}
	c.conn = false
	return nil
}

func (c *httpMCPClient) ProbeLiveness() bool {
	c.mu.Lock()
	httpClient := c.http
	connected := c.conn
	c.mu.Unlock()
	if !connected || httpClient == nil {
		return false
	}
	done := make(chan error, 1)
	go func() {
		_, err := httpClient.Post("/mcp/v1", map[string]any{
			"jsonrpc": "2.0",
			"id":      99,
			"method":  "ping",
			"params":  map[string]any{},
		})
		done <- err
	}()
	select {
	case err := <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.probeFailures++
			return false
		}
		c.probeFailures = 0
		return true
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		c.probeFailures++
		c.mu.Unlock()
		return false
	}
}

func (c *httpMCPClient) ConsecutiveProbeFailures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probeFailures
}

func (c *httpMCPClient) InflightCalls() int { return 0 }

func (c *httpMCPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

func (c *httpMCPClient) LifecycleBusy() bool { return false }

func decodeMCPRPCBody(body []byte) (map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON-RPC response: %w", err)
	}
	return data, nil
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *mcpRPCError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

type mcpRPCResponse struct {
	ID     *json.RawMessage `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  *mcpRPCError     `json:"error"`
}

type stdioMCPClient struct {
	config *MCPServerConfig

	connectMu sync.Mutex
	mu        sync.Mutex
	// writeMu keeps one JSON-RPC line per stdin write. The read loop answers
	// server-initiated requests while callers write their own requests, so
	// without it two concurrent frames could interleave on the pipe.
	writeMu sync.Mutex
	cmd     *execCmd
	stdin   io.WriteCloser
	tools   map[string]MCPTool
	conn    bool
	nextID  int64
	pending map[int64]chan mcpRPCResponse
	closed  chan struct{}
	readErr error

	probeFailures int
	calls         atomic.Int64

	reconnectFn func()
}

func newStdioMCPClient(config *MCPServerConfig) *stdioMCPClient {
	return &stdioMCPClient{
		config:  config,
		tools:   map[string]MCPTool{},
		pending: map[int64]chan mcpRPCResponse{},
	}
}

func (c *stdioMCPClient) Config() *MCPServerConfig { return c.config }

func (c *stdioMCPClient) Connect() error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.Lock()
	if c.conn {
		c.mu.Unlock()
		return nil
	}
	if c.config.Command == "" {
		c.mu.Unlock()
		return errors.New("command is required for STDIO transport")
	}

	args := resolveStdioArgs(c.config.Args, c.config.Cwd)
	cwd := c.config.Cwd
	if runtime.GOOS == "windows" {
		cwd = ""
	}
	cmd, err := startMCPProcess(c.config.Command, args, mergeMCPEnv(c.config.Env), cwd)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}
	stdin, stdout, err := cmd.pipes()
	if err != nil {
		c.mu.Unlock()
		cmd.kill()
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.closed = make(chan struct{})
	c.readErr = nil
	c.mu.Unlock()
	go c.readLoop(stdout)

	initResult := make(chan error, 1)
	go func() {
		result, initErr := c.rpc("initialize", map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "hive-go", "version": "0.1.0"},
		})
		if initErr != nil {
			initResult <- initErr
			return
		}
		if versionErr := validateMCPProtocolVersion(result); versionErr != nil {
			initResult <- versionErr
			return
		}
		initResult <- c.writeMessage(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/initialized",
		})
	}()

	select {
	case err := <-initResult:
		if err != nil {
			_ = c.Disconnect()
			return fmt.Errorf("Failed to connect to MCP server: %w", err)
		}
	case <-time.After(mcpConnectReadyTimeout):
		_ = c.Disconnect()
		return fmt.Errorf("Failed to connect to MCP server: Timed out waiting for MCP stdio connection to initialize (server %q)", c.config.Name)
	}

	tools, err := c.listToolsRPC()
	if err != nil {
		_ = c.Disconnect()
		return fmt.Errorf("Failed to connect to MCP server: %w", err)
	}
	c.mu.Lock()
	c.tools = tools
	c.conn = true
	c.probeFailures = 0
	c.mu.Unlock()
	return nil
}

func (c *stdioMCPClient) readLoop(stdout io.Reader) {
	reader := newMCPLineReader(stdout)
	for {
		line, err := reader.ReadLine()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			pending := c.pending
			c.pending = map[int64]chan mcpRPCResponse{}
			c.mu.Unlock()
			closedErr := &mcpTransportClosedError{msg: "MCP stdio transport closed: " + err.Error()}
			for _, ch := range pending {
				select {
				case ch <- mcpRPCResponse{Error: &mcpRPCError{Code: -32000, Message: closedErr.Error()}}:
				default:
				}
			}
			return
		}
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		var response mcpRPCResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			continue
		}
		if response.ID == nil {
			// Notifications carry no id and are never answered.
			continue
		}
		if method := mcpFrameMethod(line); method != "" {
			// An id plus a non-empty method is a request the server is making
			// of this client, not the answer a caller is waiting for. Answer
			// it on the same transport; it must never resolve a pending call.
			c.respondToServerRequest(*response.ID, method)
			continue
		}
		id, ok := parseRPCID(*response.ID)
		if !ok {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if ok {
			ch <- response
		}
	}
}

// JSON-RPC error codes used when answering server-initiated requests. They are
// the codes mcp 1.26.0 puts in ErrorData on the Python side.
const (
	mcpJSONRPCInvalidRequest = -32600
	mcpJSONRPCInvalidParams  = -32602
)

// mcpFrameMethod reports the method of a JSON-RPC frame, or "" when the frame
// has none (a response). Callers have already unmarshalled the frame, so a
// method field that is not a JSON string is treated as absent and keeps the
// frame on its pre-existing response path.
func mcpFrameMethod(line string) string {
	var frame struct {
		Method json.RawMessage `json:"method"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		return ""
	}
	var method string
	if err := json.Unmarshal(frame.Method, &method); err != nil {
		return ""
	}
	return method
}

// respondToServerRequest answers a server-initiated request the way Python's
// mcp.ClientSession does (mcp 1.26.0, mcp/client/session.py
// _received_request). A ping gets an empty result; the client-side features
// Hive does not implement get the SDK's ErrorData instead of being dropped.
//
// Verified against the installed SDK, not assumed: _default_list_roots_callback
// returns ErrorData(INVALID_REQUEST, "List roots not supported") when no
// list_roots_callback is configured, which is the case for
// core/framework/loader/mcp_client.py. Sampling and elicitation default the
// same way, and a method outside types.ServerRequest fails validation in
// BaseSession._receive_loop, which answers INVALID_PARAMS "Invalid request
// parameters" with data "". So roots/list is an error, not {"roots": []}.
//
// Parity: core/framework/loader/mcp_client.py::MCPClient
func (c *stdioMCPClient) respondToServerRequest(id json.RawMessage, method string) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	switch method {
	case "ping":
		reply["result"] = map[string]any{}
	case "roots/list":
		reply["error"] = map[string]any{
			"code":    mcpJSONRPCInvalidRequest,
			"message": "List roots not supported",
		}
	case "sampling/createMessage":
		reply["error"] = map[string]any{
			"code":    mcpJSONRPCInvalidRequest,
			"message": "Sampling not supported",
		}
	case "elicitation/create":
		reply["error"] = map[string]any{
			"code":    mcpJSONRPCInvalidRequest,
			"message": "Elicitation not supported",
		}
	default:
		reply["error"] = map[string]any{
			"code":    mcpJSONRPCInvalidParams,
			"message": "Invalid request parameters",
			"data":    "",
		}
	}
	// A write failure means the transport is gone; readLoop observes EOF on
	// its next read, so there is no pending caller to report it to here.
	_ = c.writeMessage(reply)
}

func parseRPCID(raw json.RawMessage) (int64, bool) {
	var id int64
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, true
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		parsed, parseErr := strconv.ParseInt(asString, 10, 64)
		if parseErr == nil {
			return parsed, true
		}
	}
	return 0, false
}

func (c *stdioMCPClient) writeMessage(message map[string]any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin == nil {
		return &mcpTransportClosedError{msg: "MCP stdio transport closed: stdin unavailable"}
	}
	payload = append(payload, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = stdin.Write(payload)
	return err
}

func (c *stdioMCPClient) rpc(method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.stdin == nil {
		c.mu.Unlock()
		return nil, &mcpTransportClosedError{msg: "MCP stdio transport closed: session not initialized"}
	}
	c.nextID++
	id := c.nextID
	ch := make(chan mcpRPCResponse, 1)
	c.pending[id] = ch
	closed := c.closed
	c.mu.Unlock()

	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeMessage(request); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(MCPCallResultTimeout())
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-closed:
		return nil, &mcpTransportClosedError{msg: "MCP stdio transport closed: connection reset"}
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, &mcpTransportClosedError{msg: fmt.Sprintf(
			"MCP call exceeded %s and was abandoned (transport closed): server=%s",
			MCPCallResultTimeout(), c.config.Name)}
	}
}

func (c *stdioMCPClient) listToolsRPC() (map[string]MCPTool, error) {
	// Follow nextCursor to exhaustion. The Python SDK's ClientSession.list_tools
	// is single-page, but MCP defines pagination and mcp2cli 3.8.0 (the upstream
	// CLI this client mirrors) walks it; a paginating server must not lose tools.
	tools, err := mcpListToolsPaged(func(cursor string) (json.RawMessage, error) {
		return c.rpc("tools/list", mcpToolsListParams(cursor))
	})
	if err != nil {
		return nil, err
	}
	byName := make(map[string]MCPTool, len(tools))
	for _, tool := range tools {
		tool.ServerName = c.config.Name
		byName[tool.Name] = tool
	}
	return byName, nil
}

func (c *stdioMCPClient) ListTools() []MCPTool {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return sortedMCPTools(c.tools)
}

func (c *stdioMCPClient) CallTool(name string, arguments map[string]any) (any, error) {
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	_, known := c.tools[name]
	c.mu.Unlock()
	if !known {
		return nil, &MCPToolNotFoundError{Server: c.config.Name, ToolName: name}
	}

	call := func() (any, error) {
		c.calls.Add(1)
		defer c.calls.Add(-1)
		return c.callToolOnce(name, arguments)
	}

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

func (c *stdioMCPClient) callToolOnce(name string, arguments map[string]any) (any, error) {
	raw, err := c.rpc("tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	return mcpCallResultValue(c.config.Name, c.config.Transport, name, raw)
}

func mcpCallResultValue(server, transport, toolName string, raw json.RawMessage) (any, error) {
	var result struct {
		IsError bool  `json:"isError"`
		Content []any `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.IsError {
		errorText := ""
		if len(result.Content) > 0 {
			if item, ok := result.Content[0].(map[string]any); ok {
				if text, ok := item["text"].(string); ok {
					errorText = text
				}
			}
		}
		return nil, fmt.Errorf("[Server: %s] [Transport: %s] Tool '%s' failed: %s",
			server, transport, toolName, errorText)
	}

	if len(result.Content) == 0 {
		return nil, nil
	}
	text, imageParts := SplitMCPContent(result.Content)
	if len(imageParts) > 0 {
		return map[string]any{"_text": text, "_images": imageParts}, nil
	}
	if text == "" {
		return nil, nil
	}
	return text, nil
}

func (c *stdioMCPClient) Reconnect() error {
	_ = c.Disconnect()
	if err := c.Connect(); err != nil {
		return err
	}
	return nil
}

func (c *stdioMCPClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.teardownLocked()
	return nil
}

func (c *stdioMCPClient) teardownLocked() {
	if c.closed != nil {
		select {
		case <-c.closed:
		default:
			close(c.closed)
		}
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.cmd != nil {
		c.cmd.kill()
		c.cmd = nil
	}
	c.conn = false
	c.tools = map[string]MCPTool{}
	pending := c.pending
	c.pending = map[int64]chan mcpRPCResponse{}
	for _, ch := range pending {
		select {
		case ch <- mcpRPCResponse{Error: &mcpRPCError{Code: -32000, Message: "MCP stdio transport closed"}}:
		default:
		}
	}
}

func (c *stdioMCPClient) ProbeLiveness() bool {
	if !c.IsConnected() {
		return false
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.rpc("ping", map[string]any{})
		done <- err
	}()
	select {
	case err := <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.probeFailures++
			return false
		}
		c.probeFailures = 0
		return true
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		c.probeFailures++
		c.mu.Unlock()
		return false
	}
}

func (c *stdioMCPClient) ConsecutiveProbeFailures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probeFailures
}

func (c *stdioMCPClient) InflightCalls() int { return int(c.calls.Load()) }

func (c *stdioMCPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

func (c *stdioMCPClient) LifecycleBusy() bool { return false }
