package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFakeClient(t *testing.T, scenario string, extraEnv ...string) *Client {
	t.Helper()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := StartClient(ctx, ClientOptions{
		Name:    "fake",
		Command: os.Args[0],
		Root:    root,
		Warmup:  4 * time.Second,
		Env:     fakeScenarioEnv(scenario, extraEnv...),
	})
	if err != nil {
		t.Fatalf("StartClient(%s): %v", scenario, err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = client.Shutdown(shutdownCtx)
	})
	return client
}

// Parity-exempt: fake-server lifecycle tests are the Go-only acceptance path
// for docs/lsp-design.json R2/A3; no Python LSP client exists.
func TestClientInitializeAndDefinition(t *testing.T) {
	client := startFakeClient(t, "basic")
	if !client.Capabilities().Has("definitionProvider") {
		t.Fatal("definitionProvider not negotiated")
	}
	if kind := client.Capabilities().SyncKind(); kind != 1 {
		t.Fatalf("sync kind = %d, want 1", kind)
	}
	name, version := client.ServerInfo()
	if name != "fake-lsp" || version != "1.0" {
		t.Fatalf("serverInfo = %q %q", name, version)
	}
	root := client.opts.Root
	uri := fileURI(filepath.Join(root, "main.go"))
	var raw json.RawMessage
	err := client.Request(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 2, "character": 0},
	}, &raw)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	locations, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations: %v", err)
	}
	if len(locations) != 2 {
		t.Fatalf("locations = %d, want 2", len(locations))
	}
	if locations[0].URI != uri {
		t.Fatalf("uri = %s, want %s", locations[0].URI, uri)
	}
}

// Parity-exempt: split-frame handling is a transport acceptance case from
// docs/lsp-design.json A4; no Python source test exists.
func TestClientReadsSplitFramesAndExtraHeaders(t *testing.T) {
	client := startFakeClient(t, "split")
	var hover Hover
	err := client.Request(context.Background(), "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 2, "character": 5},
	}, &hover)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if !strings.Contains(string(hover.Contents), "Foo returns 1") {
		t.Fatalf("hover contents = %s", hover.Contents)
	}
}

// Parity-exempt: timeout and $/cancelRequest are bounded-transport guarantees
// from docs/lsp-design.json R8/A10 with no upstream counterpart.
func TestClientTimesOutAndSendsCancel(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cancel.txt")
	client := startFakeClient(t, "hang", fakeMarker+"="+marker)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	var raw json.RawMessage
	err := client.Request(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 0, "character": 0},
	}, &raw)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	payload, ok := waitForNonEmptyFile(marker, 2*time.Second)
	if !ok {
		t.Fatal("fake server never received $/cancelRequest")
	}
	if !strings.HasPrefix(payload, "cancel:") || !strings.Contains(payload, "id") {
		t.Fatalf("cancel payload = %q", payload)
	}
}

// Parity-exempt: crash-with-pending-request behavior is R2/A3 plus the stderr
// redaction requirement from R10/A11.
func TestClientCrashFailsPendingWithRedactedStderr(t *testing.T) {
	client := startFakeClient(t, "crash")
	var raw json.RawMessage
	err := client.Request(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 0, "character": 0},
	}, &raw)
	if err == nil {
		t.Fatal("expected a crash error")
	}
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err type = %T (%v), want *ExitError", err, err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("stderr secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted stderr line]") {
		t.Fatalf("stderr tail missing redaction evidence: %v", err)
	}
	if client.Alive() {
		t.Fatal("client still reports alive after crash")
	}
}

// Parity-exempt: bounded restart after a crash is R2/A3; no Python client.
func TestClientRestartAfterCrash(t *testing.T) {
	broken := startFakeClient(t, "crash")
	var raw json.RawMessage
	_ = broken.Request(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 0, "character": 0},
	}, &raw)
	fresh := startFakeClient(t, "basic")
	if err := fresh.Request(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 0, "character": 0},
	}, &raw); err != nil {
		t.Fatalf("restarted client request: %v", err)
	}
}

// Parity-exempt: malformed JSON must fail the client cleanly instead of
// panicking (A4).
func TestClientMalformedJSONFailsCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := StartClient(ctx, ClientOptions{
		Name:    "fake",
		Command: os.Args[0],
		Root:    t.TempDir(),
		Warmup:  2 * time.Second,
		Env:     fakeScenarioEnv("malformed"),
	})
	if err == nil {
		t.Fatal("expected malformed JSON to fail initialize")
	}
}

// Parity-exempt: oversized-frame refusal is a resource-bound case (R8/A10).
func TestClientOversizedFrameFailsCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := StartClient(ctx, ClientOptions{
		Name:    "fake",
		Command: os.Args[0],
		Root:    t.TempDir(),
		Warmup:  2 * time.Second,
		Env:     fakeScenarioEnv("oversized"),
	})
	if err == nil {
		t.Fatal("expected oversized frame to fail initialize")
	}
}

// Parity-exempt: server-initiated workspace/configuration is part of the
// JSON-RPC lifecycle in R2/A3 with no upstream test.
func TestClientAnswersWorkspaceConfiguration(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "config.json")
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := StartClient(ctx, ClientOptions{
		Name:     "fake",
		Command:  os.Args[0],
		Root:     root,
		Warmup:   4 * time.Second,
		Env:      fakeScenarioEnv("serverrequest", fakeMarker+"="+marker),
		Settings: map[string]any{"gopls": map[string]any{"staticcheck": true}},
	})
	if err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = client.Shutdown(shutdownCtx)
	}()
	payload, ok := waitForFile(marker, 3*time.Second)
	if !ok {
		t.Fatal("configuration response not recorded")
	}
	var recorded struct {
		ID     json.RawMessage   `json:"id"`
		Result []json.RawMessage `json:"result"`
	}
	// The fake child creates the marker before it finishes flushing the JSON;
	// poll until the complete payload is parseable.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if payload == "" {
			payload, ok = waitForFile(marker, time.Until(deadline))
		}
		if ok && json.Unmarshal([]byte(payload), &recorded) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker JSON: %v (%s)", json.Unmarshal([]byte(payload), &recorded), payload)
		}
		time.Sleep(10 * time.Millisecond)
		payload, ok = waitForFile(marker, time.Until(deadline))
	}
	if len(recorded.Result) != 1 {
		t.Fatalf("result = %s", payload)
	}
	var section map[string]any
	if err := json.Unmarshal(recorded.Result[0], &section); err != nil {
		t.Fatalf("section JSON: %v", err)
	}
	if section["staticcheck"] != true {
		t.Fatalf("section = %v", section)
	}
}

// Parity-exempt: notification routing is exercised through a real subprocess
// because the fake server is the Go-only acceptance harness (R2/A3).
func TestClientNotificationHandlerReceivesDiagnostics(t *testing.T) {
	notifications := make(chan PublishDiagnosticsParams, 4)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := StartClient(ctx, ClientOptions{
		Name:    "fake",
		Command: os.Args[0],
		Root:    root,
		Warmup:  4 * time.Second,
		Env:     fakeScenarioEnv("basic"),
		OnNotification: func(method string, params json.RawMessage) {
			if method != "textDocument/publishDiagnostics" {
				return
			}
			var published PublishDiagnosticsParams
			if json.Unmarshal(params, &published) == nil {
				select {
				case notifications <- published:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = client.Shutdown(shutdownCtx)
	}()
	uri := fileURI(filepath.Join(root, "main.go"))
	if err := client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": "go", "version": 1, "text": "package main\n",
		},
	}); err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	select {
	case published := <-notifications:
		if published.URI != uri {
			t.Fatalf("uri = %s", published.URI)
		}
		if published.Version == nil || *published.Version != 1 {
			t.Fatalf("version = %v", published.Version)
		}
		if len(published.Diagnostics) != 2 {
			t.Fatalf("diagnostics = %d, want 2", len(published.Diagnostics))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no publishDiagnostics notification")
	}
}

// Parity-exempt: JSON-RPC error responses are part of A3; no upstream test.
func TestClientPropagatesRPCError(t *testing.T) {
	client := startFakeClient(t, "rpcerror")
	var raw json.RawMessage
	err := client.Request(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///tmp/main.go"},
		"position":     map[string]any{"line": 0, "character": 0},
	}, &raw)
	var rpcErr *ResponseError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *ResponseError", err)
	}
	if rpcErr.Code != RPCMethodNotFound {
		t.Fatalf("code = %d", rpcErr.Code)
	}
}

// Parity-exempt: URI conversion for non-ASCII/space paths is a new Go LSP
// concern (R4/A6); no Python counterpart.
func TestFileURIRoundTripEscapesPaths(t *testing.T) {
	for _, path := range []string{
		"/tmp/main.go",
		"/tmp/with space/你好.go",
		"/tmp/emoji-😀/file.go",
	} {
		uri := fileURI(path)
		if !strings.HasPrefix(uri, "file:///") {
			t.Fatalf("fileURI(%q) = %q", path, uri)
		}
		got, err := pathFromURI(uri)
		if err != nil {
			t.Fatalf("pathFromURI(%q): %v", uri, err)
		}
		if got != filepath.FromSlash(path) {
			t.Fatalf("round trip %q -> %q", path, got)
		}
	}
	if _, err := pathFromURI("https://example.com/a.go"); err == nil {
		t.Fatal("non-file URI accepted")
	}
}

// Parity-exempt: sanity guard for the fake-server harness itself; it asserts
// no behavior of the LSP client.
func TestFakeServerHarnessUsesTestBinary(t *testing.T) {
	if os.Getenv(fakeEnv) != "" {
		t.Fatal("parent test unexpectedly has fake env set")
	}
	if !filepath.IsAbs(os.Args[0]) {
		t.Fatalf("test binary path is not absolute: %s", os.Args[0])
	}
}
