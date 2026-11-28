package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/lsp"
)

type recordingRunner struct {
	req         lsp.Request
	hasDeadline bool
	deadline    time.Time
	result      lsp.Result
	err         error
	closed      bool
}

func (r *recordingRunner) Execute(ctx context.Context, req lsp.Request) (lsp.Result, error) {
	r.req = req
	r.deadline, r.hasDeadline = ctx.Deadline()
	return r.result, r.err
}

func (r *recordingRunner) Close() error {
	r.closed = true
	return nil
}

func toolboxWithRunner(t *testing.T, runner *recordingRunner) *LSPToolbox {
	t.Helper()
	toolbox := NewLSPToolbox(t.TempDir(), t.TempDir())
	toolbox.factory = func(root, home string) (lspRunner, error) { return runner, nil }
	return toolbox
}

// Parity-exempt: the lsp tool schema is frozen by docs/lsp-design.json R1/A1
// and has no Python counterpart.
func TestLSPToolSchemaGolden(t *testing.T) {
	def := LSPToolDef()
	if def.Name != ToolLSP {
		t.Fatalf("name = %q", def.Name)
	}
	if ToolLSP != "lsp" {
		t.Fatalf("ToolLSP = %q", ToolLSP)
	}
	if !strings.Contains(def.Description, "read-only v1") {
		t.Fatalf("description must state the read-only scope: %q", def.Description)
	}
	params := def.Parameters
	if params["type"] != "object" {
		t.Fatalf("type = %v", params["type"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %v, want false", params["additionalProperties"])
	}
	if got := params["required"]; !reflect.DeepEqual(got, []string{"action"}) {
		t.Fatalf("required = %#v", got)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", params["properties"])
	}
	wantProps := []string{"action", "column", "file", "line", "query", "symbol", "timeout"}
	var names []string
	for name := range props {
		names = append(names, name)
	}
	if !reflect.DeepEqual(sortedStrings(names), wantProps) {
		t.Fatalf("properties = %v, want %v", sortedStrings(names), wantProps)
	}
	for _, forbidden := range []string{"method", "request", "new_name", "apply", "command", "args"} {
		if _, ok := props[forbidden]; ok {
			t.Fatalf("forbidden property %q exposed in read-only v1", forbidden)
		}
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema = %#v", props["action"])
	}
	enum, ok := action["enum"].([]string)
	if !ok || !reflect.DeepEqual(enum, lsp.SupportedActions()) {
		t.Fatalf("action enum = %#v, want %v", action["enum"], lsp.SupportedActions())
	}
	for _, writeAction := range []string{"rename", "code_actions", "rename_file", "reload", "request"} {
		for _, exposed := range enum {
			if exposed == writeAction {
				t.Fatalf("write/control action %q exposed", writeAction)
			}
		}
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	wantAction := `{"description":"Read-only action to run.","enum":["status","capabilities","diagnostics","definition","type_definition","implementation","references","hover","symbols"],"type":"string"}`
	if string(encoded) != wantAction {
		t.Fatalf("action schema golden:\n%s\nwant:\n%s", encoded, wantAction)
	}
	timeout := props["timeout"].(map[string]any)
	if timeout["minimum"] != 5 || timeout["maximum"] != 300 {
		t.Fatalf("timeout bounds = %v", timeout)
	}
	line := props["line"].(map[string]any)
	if line["minimum"] != 1 {
		t.Fatalf("line minimum = %v", line["minimum"])
	}
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Parity-exempt: approval metadata is part of the frozen lsp contract
// (docs/lsp-design.json D1/A1) and has no Python source.
func TestLSPApprovalMetadata(t *testing.T) {
	approvals := LSPApprovals()
	if len(approvals) != len(lsp.SupportedActions()) {
		t.Fatalf("approvals = %d, want %d", len(approvals), len(lsp.SupportedActions()))
	}
	for i, info := range approvals {
		if info.Action != lsp.SupportedActions()[i] {
			t.Fatalf("approval order = %s, want %s", info.Action, lsp.SupportedActions()[i])
		}
		if info.Mode != LSPApprovalAuto || info.Reason == "" {
			t.Fatalf("approval = %+v", info)
		}
	}
	if _, ok := LSPApprovalFor("rename"); ok {
		t.Fatal("rename must not have approval metadata in v1")
	}
	meta := LSPToolApprovalMetadata()
	if meta["read_only"] != true || meta["approval"] != LSPApprovalAuto {
		t.Fatalf("metadata = %#v", meta)
	}
	if requires, ok := meta["requires_approval"].([]string); !ok || len(requires) != 0 {
		t.Fatalf("requires_approval = %#v", meta["requires_approval"])
	}
	actions := meta["actions"].(map[string]any)
	if len(actions) != len(lsp.SupportedActions()) {
		t.Fatalf("actions = %#v", actions)
	}
}

// Parity-exempt: action validation fails closed per docs/lsp-design.json
// R9/A2; unknown and write actions are rejected by the adapter.
func TestLSPActionValidationFailsClosed(t *testing.T) {
	toolbox := NewLSPToolbox(t.TempDir(), t.TempDir())
	for _, action := range []string{"rename", "code_actions", "rename_file", "reload", "request"} {
		_, err := toolbox.Execute(context.Background(), map[string]any{"action": action, "file": "main.go", "line": 1})
		var toolErr *LSPToolError
		if !errors.As(err, &toolErr) {
			t.Fatalf("%s err = %v, want *LSPToolError", action, err)
		}
		if toolErr.Code != string(lsp.CodeUnsupportedAction) {
			t.Fatalf("%s code = %s", action, toolErr.Code)
		}
	}
	_, err := toolbox.Execute(context.Background(), map[string]any{"action": "teleport"})
	var toolErr *LSPToolError
	if !errors.As(err, &toolErr) || toolErr.Code != string(lsp.CodeInvalidArgument) {
		t.Fatalf("unknown action err = %v", err)
	}
	if _, err := toolbox.Execute(context.Background(), map[string]any{"action": "definition", "file": "main.go"}); err == nil {
		t.Fatal("missing line was accepted")
	}
	if _, err := toolbox.Execute(context.Background(), map[string]any{"action": "definition", "file": "main.go", "line": 1, "timeout": 2}); err == nil {
		t.Fatal("timeout below 5s was accepted")
	}
	if _, err := toolbox.Execute(context.Background(), map[string]any{"action": "status", "timeout": 301}); err == nil {
		t.Fatal("timeout above 300s was accepted")
	}
	if _, err := toolbox.Execute(context.Background(), map[string]any{"action": "status", "timeout": 5.5}); err == nil {
		t.Fatal("fractional timeout was accepted")
	}
}

// Parity-exempt: handler-to-core mapping (request fields, timeout, errors) is
// a new adapter boundary from docs/lsp-design.json T7/D1.
func TestLSPHandlerMapsRequestAndTimeout(t *testing.T) {
	runner := &recordingRunner{result: lsp.Result{Content: "definition [fake] 1 result(s)"}}
	toolbox := toolboxWithRunner(t, runner)
	result, err := toolbox.Execute(context.Background(), map[string]any{
		"action":  "definition",
		"file":    "main.go",
		"line":    4,
		"symbol":  "Foo",
		"timeout": 7,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != runner.result.Content || result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if runner.req.Action != lsp.ActionDefinition || runner.req.File != "main.go" || runner.req.Line != 4 || runner.req.Symbol != "Foo" || runner.req.Column != 1 {
		t.Fatalf("request = %+v", runner.req)
	}
	if !runner.hasDeadline {
		t.Fatal("handler did not bound the call context")
	}
	remaining := time.Until(runner.deadline)
	if remaining < 6*time.Second || remaining > 8*time.Second {
		t.Fatalf("deadline remaining = %s, want about 7s", remaining)
	}

	// Default timeout is 20s.
	runner2 := &recordingRunner{result: lsp.Result{Content: "ok"}}
	toolbox2 := toolboxWithRunner(t, runner2)
	if _, err := toolbox2.Execute(context.Background(), map[string]any{"action": "status"}); err != nil {
		t.Fatal(err)
	}
	remaining = time.Until(runner2.deadline)
	if remaining < 18*time.Second || remaining > 21*time.Second {
		t.Fatalf("default deadline remaining = %s, want about 20s", remaining)
	}
}

// Parity-exempt: typed error mapping is a new adapter concern (T7); no Python
// counterpart.
func TestLSPHandlerMapsTypedErrors(t *testing.T) {
	runner := &recordingRunner{err: lsp.NewError(lsp.CodeNoServer, "no available server")}
	toolbox := toolboxWithRunner(t, runner)
	result, err := toolbox.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("runtime failure must be a tool error result, got %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "no_server") {
		t.Fatalf("result = %+v", result)
	}

	runner.err = lsp.NewError(lsp.CodeInvalidArgument, "bad file")
	_, err = toolbox.Execute(context.Background(), map[string]any{"action": "status"})
	var toolErr *LSPToolError
	if !errors.As(err, &toolErr) || toolErr.Code != string(lsp.CodeInvalidArgument) {
		t.Fatalf("invalid argument err = %v", err)
	}

	runner.err = &lsp.TimeoutError{Operation: "textDocument/definition"}
	runner.result = lsp.Result{}
	result, err = toolbox.Execute(context.Background(), map[string]any{"action": "status", "timeout": 5})
	if err != nil {
		t.Fatalf("timeout err = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "timed out after 5s") {
		t.Fatalf("timeout result = %+v", result)
	}
}

// Parity-exempt: registration gating on settings.enabled is frozen by
// docs/lsp-design.json D1; no Python counterpart.
func TestLSPEnabledGate(t *testing.T) {
	home := t.TempDir()
	if !LSPEnabled(home) {
		t.Fatal("default settings.enabled should be true")
	}
	user := filepath.Join(home, "lsp.json")
	if err := os.WriteFile(user, []byte(`{"settings":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if LSPEnabled(home) {
		t.Fatal("$HIVE_HOME/lsp.json settings.enabled=false did not gate registration")
	}

	// A session-directory file is agent-writable and must not gate the tool
	// (it can name the server command, so it must never be a config source).
	session := t.TempDir()
	if err := os.MkdirAll(filepath.Join(session, ".hive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, ".hive", "lsp.json"), []byte(`{"settings":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if LSPEnabled(home) {
		t.Fatal("session-dir lsp.json overrode the operator-level enabled=false")
	}
}

// Parity-exempt: real status/no-server behavior through the standalone tool
// adapter; no Python source test exists.
func TestLSPToolStatusAndNoServer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolbox := NewLSPToolbox(root, t.TempDir())
	defer toolbox.Close()
	result, err := toolbox.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "lsp status") {
		t.Fatalf("status result = %+v", result)
	}
	result, err = toolbox.Execute(context.Background(), map[string]any{"action": "diagnostics", "file": "main.go"})
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "no_server") {
		t.Fatalf("no-server result = %+v", result)
	}
}
