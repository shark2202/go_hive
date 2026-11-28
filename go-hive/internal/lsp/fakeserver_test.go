package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	fakeEnv     = "GO_HIVE_FAKE_LSP"
	fakeMarker  = "GO_HIVE_FAKE_MARKER"
	fakeTimeout = 10 * time.Second
)

// Parity-exempt: TestMain only re-execs the test binary as a fake LSP server;
// it is a test harness, not a ported behavior. The fake-server tests it enables
// are Go-only acceptance work for docs/lsp-design.json A3/A4.
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeEnv); scenario != "" {
		os.Exit(runFakeServer(scenario))
	}
	os.Exit(m.Run())
}

type fakeMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func runFakeServer(scenario string) int {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	docVersions := map[string]int{}
	waitingForResponse := false
	var pendingID json.RawMessage

	for {
		body, err := readFrame(reader, MaxFrameBytes)
		if err != nil {
			return 0
		}
		var msg fakeMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return 1
		}
		if msg.Method == "" && len(msg.ID) > 0 {
			if waitingForResponse {
				if path := os.Getenv(fakeMarker); path != "" {
					payload, _ := json.Marshal(map[string]any{"id": json.RawMessage(msg.ID), "result": json.RawMessage(msg.Result)})
					_ = os.WriteFile(path, payload, 0o600)
				}
				waitingForResponse = false
			}
			continue
		}
		switch msg.Method {
		case "initialize":
			if scenario == "rootprobe" {
				cwd, _ := os.Getwd()
				var params map[string]any
				_ = json.Unmarshal(msg.Params, &params)
				payload, _ := json.Marshal(map[string]any{
					"cwd":      cwd,
					"root_uri": params["rootUri"],
				})
				if path := os.Getenv(fakeMarker); path != "" {
					_ = os.WriteFile(path, payload, 0o600)
				}
			}
			if scenario == "oversized" {
				_, _ = writer.WriteString("Content-Length: 999999999\r\n\r\n")
				_ = writer.Flush()
				return 0
			}
			if scenario == "malformed" {
				body := []byte("{not json")
				_ = writeFakeFrame(writer, body, false)
				_ = writer.Flush()
				continue
			}
			caps := map[string]any{
				"textDocumentSync":        1,
				"definitionProvider":      true,
				"typeDefinitionProvider":  true,
				"implementationProvider":  true,
				"referencesProvider":      true,
				"hoverProvider":           true,
				"documentSymbolProvider":  true,
				"workspaceSymbolProvider": true,
			}
			if scenario == "pull" {
				caps["diagnosticProvider"] = map[string]any{"interFileDependencies": false, "workspaceDiagnostics": false}
				delete(caps, "textDocumentSync")
			}
			if scenario == "minimal" {
				caps = map[string]any{"textDocumentSync": 1, "definitionProvider": true}
			}
			respond(writer, msg.ID, map[string]any{
				"capabilities": caps,
				"serverInfo":   map[string]any{"name": "fake-lsp", "version": "1.0"},
			}, scenario == "split")
			if scenario == "serverrequest" {
				sendServerRequest(writer, "srv-config", "workspace/configuration", map[string]any{
					"items": []map[string]any{{"section": "gopls"}},
				}, false)
				waitingForResponse = true
			}
		case "initialized":
		case "shutdown":
			respond(writer, msg.ID, nil, false)
		case "exit":
			return 0
		case "textDocument/didOpen":
			var params struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			docVersions[params.TextDocument.URI] = params.TextDocument.Version
			if scenario != "nopublish" && scenario != "pull" {
				publishDiagnostics(writer, params.TextDocument.URI, params.TextDocument.Version, fakeDiagnostics(scenario), scenario == "split")
			}
		case "textDocument/didChange":
			var params struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			docVersions[params.TextDocument.URI] = params.TextDocument.Version
			switch scenario {
			case "staleempty":
				// Deliberately publish an old empty set after the version moved
				// on, so a client that trusts it would call the file clean.
				publishDiagnostics(writer, params.TextDocument.URI, params.TextDocument.Version-1, nil, false)
			case "nopublish", "pull":
			default:
				publishDiagnostics(writer, params.TextDocument.URI, params.TextDocument.Version, fakeDiagnostics(scenario), scenario == "split")
			}
		case "textDocument/didClose":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			publishDiagnostics(writer, params.TextDocument.URI, -1, nil, false)
		case "textDocument/definition", "textDocument/typeDefinition", "textDocument/implementation":
			if scenario == "crash" {
				_, _ = os.Stderr.WriteString("fatal: index corrupt\napi_token=supersecret\n")
				os.Exit(2)
			}
			if scenario == "hang" {
				pendingID = msg.ID
				continue
			}
			if scenario == "rpcerror" {
				respondError(writer, msg.ID, RPCMethodNotFound, "method not found: textDocument/definition")
				continue
			}
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if scenario == "nomatch" {
				respond(writer, msg.ID, []any{}, false)
				continue
			}
			respond(writer, msg.ID, fakeLocations(params.TextDocument.URI), scenario == "split")
		case "textDocument/references":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if scenario == "nomatch" {
				respond(writer, msg.ID, []any{}, false)
				continue
			}
			respond(writer, msg.ID, fakeLocations(params.TextDocument.URI), false)
		case "textDocument/hover":
			if scenario == "nomatch" {
				respond(writer, msg.ID, nil, false)
				continue
			}
			respond(writer, msg.ID, map[string]any{
				"contents": map[string]any{"kind": "markdown", "value": "```go\nfunc Foo() int\n```\n\nFoo returns 1."},
				"range": map[string]any{
					"start": map[string]int{"line": 2, "character": 5},
					"end":   map[string]int{"line": 2, "character": 8},
				},
			}, false)
		case "textDocument/documentSymbol":
			if scenario == "nomatch" {
				respond(writer, msg.ID, []any{}, false)
				continue
			}
			respond(writer, msg.ID, fakeDocumentSymbols(), false)
		case "workspace/symbol":
			if scenario == "nomatch" {
				respond(writer, msg.ID, []any{}, false)
				continue
			}
			var params struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			respond(writer, msg.ID, fakeWorkspaceSymbols(), false)
		case "textDocument/diagnostic":
			respond(writer, msg.ID, map[string]any{
				"kind":  "full",
				"items": fakeDiagnostics(scenario),
			}, false)
		case "$/cancelRequest":
			if path := os.Getenv(fakeMarker); path != "" {
				_ = os.WriteFile(path, []byte("cancel:"+string(msg.Params)), 0o600)
			}
			if len(pendingID) > 0 {
				respondError(writer, pendingID, RPCRequestCancelled, "request cancelled")
				pendingID = nil
			}
		default:
			if scenario == "crash" {
				os.Exit(2)
			}
		}
	}
}

func respond(w *bufio.Writer, id json.RawMessage, result any, split bool) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	_ = writeFakeFrame(w, payload, split)
	_ = w.Flush()
}

func sendServerRequest(w *bufio.Writer, id, method string, params any, split bool) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	_ = writeFakeFrame(w, payload, split)
	_ = w.Flush()
}

func respondError(w *bufio.Writer, id json.RawMessage, code int, message string) {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
	_ = writeFakeFrame(w, payload, false)
	_ = w.Flush()
}

func publishDiagnostics(w *bufio.Writer, uri string, version int, items []map[string]any, split bool) {
	params := map[string]any{"uri": uri, "diagnostics": items}
	if version >= 0 {
		params["version"] = version
	}
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": params})
	_ = writeFakeFrame(w, payload, split)
	_ = w.Flush()
}

// writeFakeFrame writes a frame, optionally splitting the header and body into
// separate writes with a delay to exercise the client's framing reader, and
// adding an extra Content-Type header.
func writeFakeFrame(w *bufio.Writer, body []byte, split bool) error {
	if !split {
		return writeFrame(w, body)
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n", len(body)); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)
	half := len(body) / 2
	if _, err := w.Write(body[:half]); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := w.Write(body[half:]); err != nil {
		return err
	}
	return w.Flush()
}

func fakeLocations(uri string) []map[string]any {
	return []map[string]any{
		{
			"uri": uri,
			"range": map[string]any{
				"start": map[string]int{"line": 2, "character": 0},
				"end":   map[string]int{"line": 2, "character": 3},
			},
		},
		{
			"uri": uri,
			"range": map[string]any{
				"start": map[string]int{"line": 5, "character": 1},
				"end":   map[string]int{"line": 5, "character": 4},
			},
		},
	}
}

func fakeDocumentSymbols() []map[string]any {
	return []map[string]any{
		{
			"name":   "Foo",
			"detail": "func Foo() int",
			"kind":   12,
			"range": map[string]any{
				"start": map[string]int{"line": 2, "character": 0},
				"end":   map[string]int{"line": 4, "character": 1},
			},
			"selectionRange": map[string]any{
				"start": map[string]int{"line": 2, "character": 5},
				"end":   map[string]int{"line": 2, "character": 8},
			},
			"children": []map[string]any{
				{
					"name": "Bar",
					"kind": 12,
					"range": map[string]any{
						"start": map[string]int{"line": 3, "character": 1},
						"end":   map[string]int{"line": 3, "character": 10},
					},
					"selectionRange": map[string]any{
						"start": map[string]int{"line": 3, "character": 1},
						"end":   map[string]int{"line": 3, "character": 4},
					},
				},
			},
		},
	}
}

func fakeWorkspaceSymbols() []map[string]any {
	return []map[string]any{
		{
			"name": "Foo",
			"kind": 12,
			"location": map[string]any{
				"uri": "file:///workspace/main.go",
				"range": map[string]any{
					"start": map[string]int{"line": 2, "character": 0},
					"end":   map[string]int{"line": 2, "character": 3},
				},
			},
		},
		{
			"name": "Bar",
			"kind": 12,
			"location": map[string]any{
				"uri": "file:///workspace/main.go",
				"range": map[string]any{
					"start": map[string]int{"line": 5, "character": 1},
					"end":   map[string]int{"line": 5, "character": 4},
				},
			},
		},
	}
}

func fakeDiagnostics(scenario string) []map[string]any {
	items := []map[string]any{
		{
			"range": map[string]any{
				"start": map[string]int{"line": 0, "character": 0},
				"end":   map[string]int{"line": 0, "character": 1},
			},
			"severity": 2,
			"source":   "fake",
			"message":  "unused variable",
		},
		{
			"range": map[string]any{
				"start": map[string]int{"line": 4, "character": 0},
				"end":   map[string]int{"line": 4, "character": 1},
			},
			"severity": 1,
			"source":   "fake",
			"code":     "E0001",
			"message":  "undefined: x",
		},
	}
	if scenario == "dup" {
		items = append(items, items[1])
	}
	return items
}

// fakeScenarioEnv builds the environment for one fake server child.
func fakeScenarioEnv(scenario string, extra ...string) []string {
	env := []string{fakeEnv + "=" + scenario}
	env = append(env, extra...)
	return env
}

// waitForFile polls until path exists or the deadline expires.
func waitForFile(path string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data)), true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", false
}

// waitForNonEmptyFile polls until path exists with a complete non-empty
// payload. The fake server creates marker files before it writes their content;
// callers that need a payload must not treat the empty file as ready.
func waitForNonEmptyFile(path string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return strings.TrimSpace(string(data)), true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", false
}
