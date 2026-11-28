package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/lsp"
)

// ToolLSP is the model-facing tool name.
const ToolLSP = "lsp"

// LSP tool timeouts. The frozen contract is 5..300 seconds with a 20s default.
const (
	DefaultLSPTimeout = 20 * time.Second
	MinLSPTimeout     = 5 * time.Second
	MaxLSPTimeout     = 300 * time.Second
)

// Approval modes for the lsp tool.
const (
	LSPApprovalAuto     = "auto"
	LSPApprovalRequired = "required"
)

// LSPApprovalInfo is the approval metadata for one action. Read-only v1
// exposes only auto-run actions; write actions are absent, not "auto".
type LSPApprovalInfo struct {
	Action string `json:"action"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

// lspApprovalReasons documents why each read-only action is safe to auto-run.
var lspApprovalReasons = map[string]string{
	"status":          "reads config/discovery state; starts no server",
	"capabilities":    "reads negotiated server capabilities; may start the configured server",
	"diagnostics":     "reads diagnostics from the configured language server",
	"definition":      "reads a definition location",
	"type_definition": "reads a type definition location",
	"implementation":  "reads implementation locations",
	"references":      "reads reference locations",
	"hover":           "reads hover text",
	"symbols":         "reads document/workspace symbols",
}

// LSPApprovals returns approval metadata for every exposed action in frozen
// order. All v1 actions are auto-run and read-only.
func LSPApprovals() []LSPApprovalInfo {
	actions := lsp.SupportedActions()
	out := make([]LSPApprovalInfo, 0, len(actions))
	for _, action := range actions {
		out = append(out, LSPApprovalInfo{
			Action: action,
			Mode:   LSPApprovalAuto,
			Reason: lspApprovalReasons[action],
		})
	}
	return out
}

// LSPApprovalFor returns the approval metadata for one action.
func LSPApprovalFor(action string) (LSPApprovalInfo, bool) {
	for _, info := range LSPApprovals() {
		if info.Action == action {
			return info, true
		}
	}
	return LSPApprovalInfo{}, false
}

// LSPToolError is the adapter's typed error for schema/action validation.
// Runtime failures are returned as Result.IsError so the model can recover;
// a malformed call is a programming error the caller should see.
type LSPToolError struct {
	Code    string
	Message string
}

func (e *LSPToolError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return "lsp " + e.Code + ": " + e.Message
}

// LSPToolDef is the standalone tool definition. It is intentionally not added
// to builtin.defs: registration is gated on lsp.json settings.enabled and is
// wired by the coordinator, outside this package's shared registry.
func LSPToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: ToolLSP,
		Description: "Semantic code intelligence through a language server installed for this project " +
			"(read-only v1). Use action=status to see which servers are configured and available. " +
			"Use definition/type_definition/implementation/references/hover at a 1-based line plus a " +
			"symbol name (or a 1-based column) inside a file. Use diagnostics for one file or file=\"*\" " +
			"for the workspace, and symbols for document or workspace symbols. Rename, code actions, " +
			"file renames and raw requests are not available in this version.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        lsp.SupportedActions(),
					"description": "Read-only action to run.",
				},
				"file": map[string]any{
					"type":        "string",
					"description": "Workspace-relative or absolute file path. Required for all actions except status and capabilities. Use \"*\" for workspace diagnostics/symbols.",
				},
				"line": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "1-based line for positional actions.",
				},
				"column": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "1-based column used when symbol is omitted. Defaults to 1.",
				},
				"symbol": map[string]any{
					"type":        "string",
					"description": "Identifier on the line. Suffix #N selects the Nth occurrence. Case-sensitive first, then case-insensitive.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Workspace symbol query for action=symbols with file=\"*\".",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"minimum":     5,
					"maximum":     300,
					"description": "Overall tool timeout in seconds (5..300, default 20).",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

// LSPToolApprovalMetadata is a stable map for registries that carry approval
// metadata as data instead of typed fields.
func LSPToolApprovalMetadata() map[string]any {
	actions := map[string]any{}
	for _, info := range LSPApprovals() {
		actions[info.Action] = info.Mode
	}
	return map[string]any{
		"tool":              ToolLSP,
		"read_only":         true,
		"approval":          LSPApprovalAuto,
		"actions":           actions,
		"requires_approval": []string{},
	}
}

// LSPEnabled reports whether lsp is enabled for home according to
// <home>/lsp.json (or HIVE_HOME when home is empty). There is no
// project/session-level file: the session directory is agent-writable, and
// lsp.json can name the server command, so reading it would let the model
// start a process the operator disabled. See go_hive/docs/config-roots.html.
// Registration uses this; a config read failure keeps the tool unregistered
// instead of exposing a broken surface.
func LSPEnabled(home string) bool {
	cfg, err := lsp.LoadConfig(home)
	if err != nil {
		return false
	}
	return cfg.Settings.Enabled
}

// lspRunner is the slice of lsp.Service the adapter needs. It is an interface
// so adapter tests can assert request mapping and timeouts without a server.
type lspRunner interface {
	Execute(ctx context.Context, req lsp.Request) (lsp.Result, error)
	Close() error
}

// LSPToolbox executes the lsp tool for one workspace.
type LSPToolbox struct {
	Root string
	Home string

	mu      sync.Mutex
	service lspRunner
	factory func(root, home string) (lspRunner, error)
}

// NewLSPToolbox returns a toolbox rooted at root. The language-server service
// is created lazily on first non-status call.
func NewLSPToolbox(root, home string) *LSPToolbox {
	return &LSPToolbox{
		Root: root,
		Home: home,
		factory: func(root, home string) (lspRunner, error) {
			return lsp.NewService(lsp.Options{Root: root, Home: home})
		},
	}
}

func (t *LSPToolbox) runner() (lspRunner, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.service != nil {
		return t.service, nil
	}
	factory := t.factory
	if factory == nil {
		factory = func(root, home string) (lspRunner, error) {
			return lsp.NewService(lsp.Options{Root: root, Home: home})
		}
	}
	service, err := factory(t.Root, t.Home)
	if err != nil {
		return nil, err
	}
	t.service = service
	return service, nil
}

// Close shuts the underlying language servers down.
func (t *LSPToolbox) Close() error {
	t.mu.Lock()
	service := t.service
	t.service = nil
	t.mu.Unlock()
	if service != nil {
		return service.Close()
	}
	return nil
}

// Execute runs one lsp tool call.
func (t *LSPToolbox) Execute(ctx context.Context, input map[string]any) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := lsp.ParseRequest(input)
	if err != nil {
		return Result{}, toolErrorFromCore(err)
	}
	timeout, err := lspTimeout(input)
	if err != nil {
		return Result{}, err
	}
	runner, err := t.runner()
	if err != nil {
		return Result{Content: "Error: lsp unavailable: " + err.Error(), IsError: true}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := runner.Execute(callCtx, req)
	if err != nil {
		if errorsIsTimeout(err) {
			return Result{Content: fmt.Sprintf("Error: lsp %s timed out after %s", req.Action, timeout), IsError: true}, nil
		}
		if code, message, ok := coreErrorParts(err); ok {
			if code == string(lsp.CodeInvalidArgument) || code == string(lsp.CodeUnsupportedAction) {
				return Result{}, &LSPToolError{Code: code, Message: message}
			}
			return Result{Content: "Error: lsp " + code + ": " + message, IsError: true}, nil
		}
		return Result{Content: "Error: lsp: " + err.Error(), IsError: true}, nil
	}
	return Result{Content: result.Content, IsError: false}, nil
}

func lspTimeout(input map[string]any) (time.Duration, error) {
	value, ok := input["timeout"]
	if !ok || value == nil {
		return DefaultLSPTimeout, nil
	}
	number, err := timeoutInt(value)
	if err != nil {
		return 0, &LSPToolError{Code: string(lsp.CodeInvalidArgument), Message: "timeout must be an integer number of seconds"}
	}
	if number < 5 || number > 300 {
		return 0, &LSPToolError{Code: string(lsp.CodeInvalidArgument), Message: "timeout must be within 5..300 seconds"}
	}
	return time.Duration(number) * time.Second, nil
}

func timeoutInt(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(typed), nil
	case json.Number:
		n, err := typed.Int64()
		if err != nil {
			return 0, err
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("not a number")
	}
}

func toolErrorFromCore(err error) error {
	if code, message, ok := coreErrorParts(err); ok {
		return &LSPToolError{Code: code, Message: message}
	}
	return &LSPToolError{Code: string(lsp.CodeInvalidArgument), Message: err.Error()}
}

func coreErrorParts(err error) (string, string, bool) {
	var typed *lsp.Error
	if errors.As(err, &typed) {
		return string(typed.Code), typed.Message, true
	}
	return "", "", false
}

func errorsIsTimeout(err error) bool {
	for err != nil {
		if _, ok := err.(*lsp.TimeoutError); ok {
			return true
		}
		if err == context.DeadlineExceeded {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// ExecuteLSP runs the lsp tool against root using a short-lived toolbox.
// Callers that serve many calls should keep an LSPToolbox instead so language
// servers stay warm.
func ExecuteLSP(ctx context.Context, root, home string, input map[string]any) (Result, error) {
	toolbox := NewLSPToolbox(root, home)
	defer toolbox.Close()
	return toolbox.Execute(ctx, input)
}

// LSPToolName normalizes a configured tool name for registration helpers.
func LSPToolName() string { return strings.TrimSpace(ToolLSP) }
