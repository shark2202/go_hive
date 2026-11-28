package lsp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action is one lsp tool action.
type Action string

// Read-only v1 actions (R1).
const (
	ActionStatus         Action = "status"
	ActionCapabilities   Action = "capabilities"
	ActionDiagnostics    Action = "diagnostics"
	ActionDefinition     Action = "definition"
	ActionTypeDefinition Action = "type_definition"
	ActionImplementation Action = "implementation"
	ActionReferences     Action = "references"
	ActionHover          Action = "hover"
	ActionSymbols        Action = "symbols"
)

// Deferred write/control actions from the wider design. They are rejected with
// a typed unsupported-action error and are absent from the adapter schema.
const (
	ActionRename     Action = "rename"
	ActionCodeAction Action = "code_actions"
	ActionRenameFile Action = "rename_file"
	ActionReload     Action = "reload"
	ActionRequest    Action = "request"
)

var readOnlyActions = []Action{
	ActionStatus,
	ActionCapabilities,
	ActionDiagnostics,
	ActionDefinition,
	ActionTypeDefinition,
	ActionImplementation,
	ActionReferences,
	ActionHover,
	ActionSymbols,
}

var knownDeferredActions = map[Action]bool{
	ActionRename:     true,
	ActionCodeAction: true,
	ActionRenameFile: true,
	ActionReload:     true,
	ActionRequest:    true,
}

// SupportedActions returns the read-only action enum in frozen order.
func SupportedActions() []string {
	out := make([]string, 0, len(readOnlyActions))
	for _, action := range readOnlyActions {
		out = append(out, string(action))
	}
	return out
}

// IsReadOnlyAction reports whether action is part of v1.
func IsReadOnlyAction(action Action) bool {
	for _, candidate := range readOnlyActions {
		if candidate == action {
			return true
		}
	}
	return false
}

// ParseAction validates an action string. Deferred write actions get
// CodeUnsupportedAction; unknown strings get CodeInvalidArgument.
func ParseAction(raw string) (Action, error) {
	action := Action(strings.TrimSpace(raw))
	if action == "" {
		return "", NewError(CodeInvalidArgument, "action is required")
	}
	if IsReadOnlyAction(action) {
		return action, nil
	}
	if knownDeferredActions[action] {
		if action == ActionReload {
			return "", NewError(CodeUnsupportedAction, "lsp action %q is not exposed in read-only v1; reload is a control API", action)
		}
		return "", NewError(CodeUnsupportedAction, "lsp action %q is a write/control action and is not available in read-only v1", action)
	}
	return "", NewError(CodeInvalidArgument, "unknown lsp action %q", raw)
}

// Request is the validated core request the adapter forwards.
type Request struct {
	Action Action
	File   string
	Line   int
	Column int
	Symbol string
	Query  string
}

// ParseRequest validates a tool input map. It never accepts command/args or a
// raw LSP method: those are not part of the model-facing contract.
func ParseRequest(input map[string]any) (Request, error) {
	actionRaw, _ := input["action"].(string)
	action, err := ParseAction(actionRaw)
	if err != nil {
		return Request{}, err
	}
	req := Request{Action: action, Column: 1}
	if value, ok := input["file"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return Request{}, NewError(CodeInvalidArgument, "file must be a string")
		}
		req.File = strings.TrimSpace(text)
	}
	if value, ok := input["symbol"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return Request{}, NewError(CodeInvalidArgument, "symbol must be a string")
		}
		req.Symbol = strings.TrimSpace(text)
	}
	if value, ok := input["query"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return Request{}, NewError(CodeInvalidArgument, "query must be a string")
		}
		req.Query = strings.TrimSpace(text)
	}
	if value, ok := input["line"]; ok && value != nil {
		number, err := inputInt(value)
		if err != nil {
			return Request{}, NewError(CodeInvalidArgument, "line must be an integer")
		}
		req.Line = number
	}
	if value, ok := input["column"]; ok && value != nil {
		number, err := inputInt(value)
		if err != nil {
			return Request{}, NewError(CodeInvalidArgument, "column must be an integer")
		}
		req.Column = number
	}
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

// Validate checks action-specific fields.
func (r Request) Validate() error {
	if r.Action == ActionStatus {
		return nil
	}
	switch r.Action {
	case ActionCapabilities:
		return nil
	case ActionDiagnostics, ActionSymbols:
		if r.File == "" {
			return NewError(CodeInvalidArgument, "file is required for action %q (use file=\"*\" for the workspace)", r.Action)
		}
		return nil
	default:
		if r.File == "" {
			return NewError(CodeInvalidArgument, "file is required for action %q", r.Action)
		}
		if r.File == "*" {
			return NewError(CodeInvalidArgument, "file=\"*\" is only valid for diagnostics and symbols")
		}
		if r.Line < 1 {
			return NewError(CodeInvalidArgument, "line is required for action %q (1-based)", r.Action)
		}
		if r.Column < 1 {
			return NewError(CodeInvalidArgument, "column must be >= 1")
		}
	}
	return nil
}

func inputInt(value any) (int, error) {
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

// Result is the content and structured details of one action.
type Result struct {
	Content string
	Data    map[string]any
}
