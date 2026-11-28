package toolcatalog

// Parity: framework/tasks/tools/register.py::register_task_tools
//
// Session task tools: the four tools (task_create / task_update / task_list /
// task_get) that let an agent maintain its own task plan. internal/tasks builds
// them; this file binds one set to one chat session so the catalogue can
// publish them through the per-session tool surface.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// TaskToolAgentID is the identity task_update stamps on a task it moves to
// in_progress. A CLI chat session is a queen, and upstream falls back to
// "queen" when the session carries no explicit agent id
// (framework/server/queen_orchestrator.py).
const TaskToolAgentID = "queen"

// taskToolExecutionMu serialises the process-wide execution context the task
// executors read (tasks.CurrentSessionID). It is held for one tool call at a
// time, so two sessions running concurrently cannot observe each other's
// session id; each executor is a short file operation.
var taskToolExecutionMu sync.Mutex

// TaskToolSet is one session's binding of the task tools: the ordered tools
// from internal/tasks plus the session id their executors must act on.
type TaskToolSet struct {
	sessionID string
	store     *tasks.TaskStore
	pivot     *tasks.PivotHandler
	ordered   []tasks.Tool
	byName    map[string]tasks.Tool
}

// NewTaskToolSet binds the task tools to the session directory. root is the
// session's own directory (queens/<queen>/sessions/<session_id>); its base
// name is the session id the store resolves back to that directory under the
// hive root.
func NewTaskToolSet(root string, home hivehome.Home) *TaskToolSet {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	sessionID := filepath.Base(filepath.Clean(root))
	if sessionID == "" || sessionID == "." || sessionID == string(filepath.Separator) {
		return nil
	}
	// Resolve the store against the session directory before it exists: a
	// session's first turn may create a task plan before it writes a
	// transcript, and the store's fallback (_misc/<id>) would then split the
	// list away from the session's real directory.
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: task tools cannot prepare %s: %v\n", root, err)
	}

	store := tasks.NewTaskStore(home.Root)
	set := &TaskToolSet{sessionID: sessionID, store: store}
	set.rebuild(nil)
	return set
}

// SetPivot installs (or clears) the queen's pivot handler and rebuilds the
// four tools. task_create gains the pivot field + handoff argument when a
// handler is set; a worker catalogue stays with the two-argument version,
// exactly like upstream's build_session_tools(pivot_handler=None).
func (s *TaskToolSet) SetPivot(handler *tasks.PivotHandler) {
	if s == nil || s.pivot == handler {
		return
	}
	s.pivot = handler
	s.rebuild(handler)
}

func (s *TaskToolSet) rebuild(handler *tasks.PivotHandler) {
	built := tasks.BuildSessionTools(s.store, handler)
	s.ordered = built
	s.byName = make(map[string]tasks.Tool, len(built))
	for _, tool := range built {
		s.byName[tool.Name] = tool
	}
}

// CreateDef returns the task_create definition currently in force.
func (s *TaskToolSet) CreateDef() (llm.ToolDef, bool) {
	if s == nil {
		return llm.ToolDef{}, false
	}
	tool, ok := s.byName[tasks.TaskCreate]
	if !ok {
		return llm.ToolDef{}, false
	}
	return llm.ToolDef{
		Name:            tool.Name,
		Description:     tool.Description,
		Parameters:      tool.Parameters,
		ConcurrencySafe: tool.ConcurrencySafe,
	}, true
}

// Defs returns the tool definitions advertised to the model.
func (s *TaskToolSet) Defs() []llm.ToolDef {
	if s == nil {
		return nil
	}
	defs := make([]llm.ToolDef, 0, len(s.ordered))
	for _, tool := range s.ordered {
		defs = append(defs, llm.ToolDef{
			Name:            tool.Name,
			Description:     tool.Description,
			Parameters:      tool.Parameters,
			ConcurrencySafe: tool.ConcurrencySafe,
		})
	}
	return defs
}

// Has reports whether name is one of the session task tools.
func (s *TaskToolSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.byName[name]
	return ok
}

// Execute runs one task tool for this session. The result is serialised the
// same way the integration registry serialises tool results, and a rejected
// operation stays a normal (non-error) result carrying success=false, matching
// upstream's json.dumps payload with is_error=False.
//
// Argument validation is the exception: the Python task tools are pydantic
// models, so a bad `id` or an unknown `status` raises before the store is
// touched and reaches the model as an is_error result. The port validates the
// same two fields here (the tool-surface layer, where the upstream schema
// lives) so the is_error semantics match instead of silently coercing the
// value — a coerced id used to answer "Task #0 not found.".
func (s *TaskToolSet) Execute(name string, input map[string]any) (string, bool, error) {
	tool, ok := s.byName[name]
	if !ok {
		return "", true, fmt.Errorf("unknown task tool %q", name)
	}
	if problem := taskToolArgumentProblem(name, input); problem != "" {
		return tools.PythonJSONDumps(map[string]any{"error": problem}), true, nil
	}
	taskToolExecutionMu.Lock()
	defer taskToolExecutionMu.Unlock()
	restore := tasks.SetExecContext(tasks.ExecContext{
		AgentID:   TaskToolAgentID,
		SessionID: s.sessionID,
	})
	defer restore()
	return tools.PythonJSONDumps(tool.Execute(input)), false, nil
}

// taskToolArgumentProblem mirrors the pydantic validation the Python task tools
// get for free: `id` must be an integer (the schema says so; Python converts
// the value and raises on anything else) and `status` must be a TaskStatus
// member. Returns "" when the call is well-formed.
func taskToolArgumentProblem(name string, input map[string]any) string {
	if name != tasks.TaskGet && name != tasks.TaskUpdate {
		return ""
	}
	if raw, present := input["id"]; present {
		if _, ok := taskToolInt(raw); !ok {
			return fmt.Sprintf("invalid task id: %s (expected an integer)", taskToolRenderValue(raw))
		}
	} else {
		// The schema declares `id` required; a call without it must not fall
		// through to task #0.
		return "invalid task id: missing (expected an integer)"
	}
	if name == tasks.TaskUpdate {
		if raw, present := input["status"]; present && raw != nil {
			status, ok := raw.(string)
			if !ok {
				return fmt.Sprintf("invalid task status: %s (expected a string)", taskToolRenderValue(raw))
			}
			if !tasks.ValidTaskStatus(status) {
				// Byte-identical to the StrEnum error the Python tool raises.
				return fmt.Sprintf("'%s' is not a valid TaskStatus", status)
			}
		}
	}
	return ""
}

// taskToolInt accepts every integer spelling a model can produce for a JSON
// integer: a number, an integral float, or a numeric string (Python's int()).
func taskToolInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
		return 0, false
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// taskToolRenderValue quotes strings the way the Python error messages do and
// falls back to the Go rendering for anything else.
func taskToolRenderValue(value any) string {
	if text, ok := value.(string); ok {
		return fmt.Sprintf("'%s'", text)
	}
	return fmt.Sprintf("%v", value)
}
