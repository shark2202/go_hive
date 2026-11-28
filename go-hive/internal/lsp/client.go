package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NotificationHandler receives server notifications. It is called from the
// client's reader goroutine and must not block.
type NotificationHandler func(method string, params json.RawMessage)

// ClientOptions describe one language-server process.
type ClientOptions struct {
	Name           string
	Command        string
	Args           []string
	Env            []string
	Dir            string
	Root           string
	InitOptions    map[string]any
	Settings       map[string]any
	Warmup         time.Duration
	MaxFrameBytes  int
	OnNotification NotificationHandler
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

func (m rpcMessage) hasID() bool {
	trimmed := strings.TrimSpace(string(m.ID))
	return trimmed != "" && trimmed != "null"
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type rpcCall struct {
	ch chan rpcResult
}

// Client is one initialized JSON-RPC session with a language server.
type Client struct {
	opts ClientOptions
	tr   *transport

	name       string
	serverName string
	serverVer  string
	caps       ServerCapabilities

	nextID   atomic.Int64
	lastUsed atomic.Int64

	mu      sync.Mutex
	pending map[int64]*rpcCall
	done    chan struct{}
	exitErr error

	closeOnce sync.Once
}

// StartClient spawns the server, performs initialize/initialized and returns a
// ready client. The caller owns Shutdown.
func StartClient(ctx context.Context, opts ClientOptions) (*Client, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("lsp: client root is empty")
	}
	tr, err := startProcess(processSpec{
		Command: opts.Command,
		Args:    opts.Args,
		Dir:     opts.Dir,
		Env:     opts.Env,
	}, opts.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	c := &Client{
		opts:    opts,
		tr:      tr,
		name:    opts.Name,
		pending: map[int64]*rpcCall{},
		done:    make(chan struct{}),
	}
	c.Touch()
	go c.readLoop()

	warmup := opts.Warmup
	if warmup <= 0 {
		warmup = 5 * time.Second
	}
	initCtx, cancel := context.WithTimeout(ctx, warmup)
	defer cancel()
	var initResult InitializeResult
	if err := c.Request(initCtx, "initialize", c.initializeParams(), &initResult); err != nil {
		_ = c.Kill()
		return nil, fmt.Errorf("lsp: initialize %q: %w", opts.Name, err)
	}
	c.caps = initResult.Capabilities
	c.serverName = initResult.ServerInfo.Name
	c.serverVer = initResult.ServerInfo.Version
	if err := c.Notify("initialized", map[string]any{}); err != nil {
		_ = c.Kill()
		return nil, fmt.Errorf("lsp: initialized notification %q: %w", opts.Name, err)
	}
	if len(opts.Settings) > 0 {
		if err := c.Notify("workspace/didChangeConfiguration", map[string]any{"settings": opts.Settings}); err != nil {
			_ = c.Kill()
			return nil, fmt.Errorf("lsp: didChangeConfiguration %q: %w", opts.Name, err)
		}
	}
	return c, nil
}

func (c *Client) initializeParams() map[string]any {
	root := c.opts.Root
	params := map[string]any{
		"processId": os.Getpid(),
		"clientInfo": map[string]any{
			"name":    "go-hive-lsp",
			"version": Version,
		},
		"rootUri":      fileURI(root),
		"rootPath":     root,
		"capabilities": clientCapabilities(),
		"workspaceFolders": []WorkspaceFolder{{
			URI:  fileURI(root),
			Name: filepath.Base(root),
		}},
		"trace": "off",
	}
	if c.opts.InitOptions != nil {
		params["initializationOptions"] = c.opts.InitOptions
	}
	return params
}

// clientCapabilities advertises exactly the features the read-only client
// implements. Missing capabilities keep servers from sending unsupported
// requests.
func clientCapabilities() map[string]any {
	return map[string]any{
		"workspace": map[string]any{
			"workspaceFolders": true,
			"configuration":    true,
			"applyEdit":        true,
			"symbol": map[string]any{
				"dynamicRegistration": false,
			},
		},
		"textDocument": map[string]any{
			"synchronization": map[string]any{
				"dynamicRegistration": false,
				"didSave":             false,
				"willSave":            false,
				"willSaveWaitUntil":   false,
			},
			"definition": map[string]any{
				"linkSupport": true,
			},
			"typeDefinition": map[string]any{
				"linkSupport": true,
			},
			"implementation": map[string]any{
				"linkSupport": true,
			},
			"references": map[string]any{},
			"hover": map[string]any{
				"contentFormat": []string{"markdown", "plaintext"},
			},
			"documentSymbol": map[string]any{
				"hierarchicalDocumentSymbolSupport": true,
			},
			"diagnostic": map[string]any{
				"dynamicRegistration": false,
			},
			"publishDiagnostics": map[string]any{
				"relatedInformation": true,
				"versionSupport":     true,
			},
		},
		"window": map[string]any{
			"workDoneProgress": true,
		},
		"general": map[string]any{
			"positionEncodings": []string{"utf-16"},
		},
	}
}

// Name is the configured server name.
func (c *Client) Name() string { return c.name }

// Capabilities is the negotiated server capability object.
func (c *Client) Capabilities() ServerCapabilities { return c.caps }

// ServerInfo returns the serverInfo name/version reported by the server.
func (c *Client) ServerInfo() (string, string) { return c.serverName, c.serverVer }

// Alive reports whether the reader has not failed.
func (c *Client) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Touch records activity for idle sweeps.
func (c *Client) Touch() { c.lastUsed.Store(time.Now().UnixNano()) }

// IdleSince returns how long the client has been unused.
func (c *Client) IdleSince(now time.Time) time.Duration {
	last := c.lastUsed.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

// ExitError returns the failure that ended the reader, if any.
func (c *Client) ExitError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitErr
}

// Request sends a JSON-RPC request and waits for its response or the context.
func (c *Client) Request(ctx context.Context, method string, params any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.done:
		return c.ExitError()
	default:
	}
	id := c.nextID.Add(1)
	call := &rpcCall{ch: make(chan rpcResult, 1)}
	c.mu.Lock()
	if c.exitErr != nil {
		err := c.exitErr
		c.mu.Unlock()
		return err
	}
	c.pending[id] = call
	c.mu.Unlock()

	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.send(payload); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case res := <-call.ch:
		if res.err != nil {
			return res.err
		}
		if out != nil && len(res.result) > 0 && string(res.result) != "null" {
			if err := json.Unmarshal(res.result, out); err != nil {
				return fmt.Errorf("lsp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		// The server may still answer; a late response for an unknown id is
		// ignored by the reader.
		_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &TimeoutError{Operation: method}
		}
		return fmt.Errorf("lsp: %s canceled: %w", method, ctx.Err())
	case <-c.done:
		return c.ExitError()
	}
}

// Notify sends a notification without waiting for a response.
func (c *Client) Notify(method string, params any) error {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		payload["params"] = params
	}
	return c.send(payload)
}

func (c *Client) send(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("lsp: encode message: %w", err)
	}
	if err := c.tr.Write(body); err != nil {
		return fmt.Errorf("lsp: write message: %w", err)
	}
	return nil
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	for {
		body, err := c.tr.Read()
		if err != nil {
			// Give the process waiter a moment so a crash report carries the
			// real exit status instead of "connection closed".
			select {
			case <-c.tr.Done():
			case <-time.After(200 * time.Millisecond):
			}
			reason := c.tr.ExitReason()
			if !errors.Is(err, io.EOF) {
				reason = fmt.Sprintf("%v (%s)", err, reason)
			}
			exitErr := &ExitError{Reason: reason, Stderr: c.tr.StderrTail()}
			c.failAll(exitErr)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			c.failAll(fmt.Errorf("lsp: decode message from %q: %w", c.name, err))
			_ = c.tr.Close()
			return
		}
		switch {
		case msg.Method != "" && msg.hasID():
			c.handleServerRequest(msg)
		case msg.Method != "":
			if c.opts.OnNotification != nil {
				c.opts.OnNotification(msg.Method, msg.Params)
			}
		case msg.hasID():
			c.handleResponse(msg)
		}
	}
}

func (c *Client) handleResponse(msg rpcMessage) {
	id, ok := parseIntID(msg.ID)
	if !ok {
		return
	}
	c.mu.Lock()
	call := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if call == nil {
		return
	}
	if msg.Error != nil {
		call.ch <- rpcResult{err: msg.Error}
		return
	}
	call.ch <- rpcResult{result: msg.Result}
}

func (c *Client) failAll(err error) {
	if err == nil {
		err = errors.New("lsp: client stopped")
	}
	c.mu.Lock()
	if c.exitErr != nil {
		c.mu.Unlock()
		return
	}
	c.exitErr = err
	pending := c.pending
	c.pending = map[int64]*rpcCall{}
	close(c.done)
	c.mu.Unlock()
	for _, call := range pending {
		select {
		case call.ch <- rpcResult{err: err}:
		default:
		}
	}
}

func (c *Client) handleServerRequest(msg rpcMessage) {
	switch msg.Method {
	case "workspace/configuration":
		var params struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		reply := make([]any, len(params.Items))
		for i, item := range params.Items {
			if len(c.opts.Settings) == 0 {
				reply[i] = nil
				continue
			}
			if item.Section == "" {
				reply[i] = c.opts.Settings
				continue
			}
			if value, ok := c.opts.Settings[item.Section]; ok {
				reply[i] = value
			}
		}
		c.respond(msg.ID, reply)
	case "workspace/workspaceFolders":
		c.respond(msg.ID, []WorkspaceFolder{{URI: fileURI(c.opts.Root), Name: filepath.Base(c.opts.Root)}})
	case "client/registerCapability", "client/unregisterCapability",
		"window/workDoneProgress/create", "window/showMessageRequest":
		c.respond(msg.ID, nil)
	case "workspace/applyEdit":
		c.respond(msg.ID, map[string]any{
			"applied":       false,
			"failureReason": "lsp read-only v1 refuses workspace edits",
		})
	default:
		c.respondError(msg.ID, RPCMethodNotFound, "method not found: "+msg.Method)
	}
}

func (c *Client) respond(id json.RawMessage, result any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
	if err := c.send(payload); err != nil {
		// Nothing actionable: the reader will notice the broken pipe.
		return
	}
}

func (c *Client) respondError(id json.RawMessage, code int, message string) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	}
	_ = c.send(payload)
}

// Shutdown performs the graceful LSP shutdown/exit handshake, then closes the
// process. It is idempotent.
func (c *Client) Shutdown(ctx context.Context) error {
	var shutdownErr error
	c.closeOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var ignored json.RawMessage
		shutdownErr = c.Request(shutdownCtx, "shutdown", nil, &ignored)
		_ = c.Notify("exit", nil)
		select {
		case <-c.tr.Done():
		case <-time.After(2 * time.Second):
		}
		_ = c.tr.Close()
	})
	return shutdownErr
}

// Kill closes the process without the LSP handshake. It is idempotent.
func (c *Client) Kill() error {
	c.closeOnce.Do(func() {
		return
	})
	return c.tr.Close()
}
