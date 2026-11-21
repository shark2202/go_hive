package integrationkit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action is one upstream MCP tool function.
type Action struct {
	Name     string
	Summary  string
	Source   string
	Params   map[string]any
	Required []string
	Requires []string
}

// Tool is one upstream integration module exposed as a single Go tool with
// an "action" selector (implicit when the module has exactly one action).
type Tool struct {
	ID          string
	Name        string
	DisplayName string
	Summary     string
	Actions     []Action
}

// ActionNames returns the action names in catalog order.
func (t *Tool) ActionNames() []string {
	out := make([]string, 0, len(t.Actions))
	for _, a := range t.Actions {
		out = append(out, a.Name)
	}
	return out
}

// Action looks up an action by name.
func (t *Tool) Action(name string) (Action, bool) {
	for _, a := range t.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

// Schema is the JSON schema published to the model. Parameters are the union
// of every action's parameters; action-specific required arguments are listed
// in the description of each action and enforced at execution time.
//
// A single-action tool carries that action's upstream required list directly
// (upstream registers it as its own MCP tool, so its schema has `required`
// with no `action` selector). A multi-action tool only requires `action`:
// the per-action required arguments stay in the description because one JSON
// object is shared by every action.
func (t *Tool) Schema() map[string]any {
	props := map[string]any{}
	for _, a := range t.Actions {
		for name, schema := range a.Params {
			if _, exists := props[name]; exists {
				continue
			}
			props[name] = schema
		}
	}
	actionNames := t.ActionNames()
	if len(actionNames) > 1 {
		props["action"] = map[string]any{
			"type":        "string",
			"enum":        actionNames,
			"description": "Action to invoke. See the tool description for per-action arguments.",
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(actionNames) > 1 {
		schema["required"] = []string{"action"}
	} else if len(t.Actions) == 1 && len(t.Actions[0].Required) > 0 {
		schema["required"] = append([]string(nil), t.Actions[0].Required...)
	}
	return schema
}

// Description renders the model-facing description: summary plus the action
// menu with required arguments.
func (t *Tool) Description() string {
	if len(t.Actions) == 1 {
		return t.Actions[0].Summary
	}
	var b strings.Builder
	b.WriteString(t.Summary)
	b.WriteString("\n\nActions:")
	for _, a := range t.Actions {
		b.WriteString("\n- ")
		b.WriteString(a.Name)
		if len(a.Required) > 0 {
			b.WriteString(" (required: ")
			b.WriteString(strings.Join(a.Required, ", "))
			b.WriteString(")")
		}
		if a.Summary != "" {
			b.WriteString(": ")
			b.WriteString(a.Summary)
		}
	}
	return b.String()
}

// RestSpec is a declarative description of one upstream REST action.
type RestSpec struct {
	Method string
	// Path is appended to the module base URL. "{param}" segments are filled
	// from the action input and removed from the query/body payload.
	Path string
	// Query names parameters sent in the query string. When empty, every
	// non-path parameter is sent in the query for GET/DELETE and in the body
	// for other methods.
	Query []string
	// Body names parameters sent in the body. When empty, every non-path,
	// non-query parameter is sent in the body.
	Body []string
	// Rename maps input names to provider field names.
	Rename map[string]string
	// Constants are merged into the body (or query for GET/DELETE).
	Constants map[string]any
	// Unwrap returns the named response field instead of the envelope
	// (Telegram's {"ok":true,"result":...}).
	Unwrap string
	// ListKey reshapes {"data":[...]} into {"has_more":bool,ListKey:[...]}.
	ListKey string
	// HasMore includes the provider's has_more flag with ListKey.
	HasMore bool
	// FirstOf returns the first element of the named provider array
	// (customer lookup by email, subscription status).
	FirstOf string
	// JSONParams lists string inputs whose contents are JSON and must be
	// embedded structurally (Stripe line_items).
	JSONParams []string
	// JoinLists lists array inputs that the provider expects as a
	// comma-separated string (Slack user_ids).
	JoinLists []string
	// Build optionally overrides the derived body/query payload.
	Build func(input map[string]any) map[string]any
	// Pagination follows the provider's cursor across pages and merges the
	// result lists. Nil means the action returns exactly one page.
	Pagination *PaginationSpec
}

// DefaultMaxPages caps a declarative pagination walk when the spec does not
// set its own limit.
const DefaultMaxPages = 10

// PaginationSpec is declarative pagination for one REST action: the client
// follows one of three cursors, sends the next cursor back in CursorParam (or
// follows a Link URL directly), and concatenates the ItemsPath arrays.
//
// Paths are dot-separated field names ("records", "response_metadata.
// next_cursor"). The walk stops when the cursor is missing, empty, repeated,
// advertised has_more is false, or MaxPages pages have been read.
type PaginationSpec struct {
	// ItemsPath is the dot path to the page's item array. Empty means the page
	// itself is the array (GitHub list endpoints return a bare JSON array).
	ItemsPath string
	// NextCursorPath is the dot path to the next-page cursor in the response
	// payload.
	NextCursorPath string
	// CursorParam is the request parameter carrying the body cursor: query for
	// GET/DELETE, body otherwise.
	CursorParam string
	// LinkHeader follows the rel="next" URL advertised in the response Link
	// header instead of a body cursor (GitHub list endpoints). The walked
	// request reuses the advertised URL as-is, including its query string.
	LinkHeader bool
	// LastItemField derives the next cursor from this field of the last item on
	// the page, then sends it in CursorParam (Stripe starting_after). The walk
	// also stops early when the page advertises has_more=false.
	LastItemField string
	// MaxPages caps the walk; 0 uses DefaultMaxPages.
	MaxPages int
}

func (p *PaginationSpec) maxPages() int {
	if p == nil || p.MaxPages <= 0 {
		return DefaultMaxPages
	}
	return p.MaxPages
}

// WithDefaults fills omitted parameters from the catalog defaults so action
// handlers can rely on upstream defaults.
func WithDefaults(action Action, input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	for name, raw := range action.Params {
		if _, present := out[name]; present {
			continue
		}
		schema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if def, ok := schema["default"]; ok {
			out[name] = def
		}
	}
	return out
}

// Guard converts a panic in an action implementation into a Go error so one
// bad provider cannot take down the agent loop.
func Guard(fn func() (any, error)) (result any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result, err = nil, fmt.Errorf("integrations: panic: %v", rec)
		}
	}()
	return fn()
}

// JSONString marshals a value, falling back to its fmt form.
func JSONString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
