package cli

// Parity: framework/tasks/tools/register.py::register_task_tools
//
// Session task tools: the four tools (task_create / task_update / task_list /
// task_get) that let an agent maintain its own task plan. internal/tasks builds
// them; this file binds one set to one chat session and publishes it through
// the per-session tool catalogue.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tasks"
	"github.com/aden-hive/hive-go/internal/tools"
)

// taskToolAgentID is the identity task_update stamps on a task it moves to
// in_progress. A CLI chat session is a queen, and upstream falls back to
// "queen" when the session carries no explicit agent id
// (framework/server/queen_orchestrator.py).
const taskToolAgentID = "queen"

// taskToolExecutionMu serialises the process-wide execution context the task
// executors read (tasks.CurrentSessionID). It is held for one tool call at a
// time, so two sessions running concurrently cannot observe each other's
// session id; each executor is a short file operation.
var taskToolExecutionMu sync.Mutex

// taskToolSet is one session's binding of the task tools: the ordered tools
// from internal/tasks plus the session id their executors must act on.
type taskToolSet struct {
	sessionID string
	store     *tasks.TaskStore
	pivot     *tasks.PivotHandler
	ordered   []tasks.Tool
	byName    map[string]tasks.Tool
}

// newTaskToolSet binds the task tools to the session directory. root is the
// session's own directory (queens/<queen>/sessions/<session_id>); its base
// name is the session id the store resolves back to that directory under the
// hive root.
func newTaskToolSet(root string, home hivehome.Home) *taskToolSet {
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
	set := &taskToolSet{sessionID: sessionID, store: store}
	set.rebuild(nil)
	return set
}

// setPivot installs (or clears) the queen's pivot handler and rebuilds the
// four tools. task_create gains the pivot field + handoff argument when a
// handler is set; a worker catalogue stays with the two-argument version,
// exactly like upstream's build_session_tools(pivot_handler=None).
func (s *taskToolSet) setPivot(handler *tasks.PivotHandler) {
	if s == nil || s.pivot == handler {
		return
	}
	s.pivot = handler
	s.rebuild(handler)
}

func (s *taskToolSet) rebuild(handler *tasks.PivotHandler) {
	built := tasks.BuildSessionTools(s.store, handler)
	s.ordered = built
	s.byName = make(map[string]tasks.Tool, len(built))
	for _, tool := range built {
		s.byName[tool.Name] = tool
	}
}

// createDef returns the task_create definition currently in force.
func (s *taskToolSet) createDef() (llm.ToolDef, bool) {
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
func (s *taskToolSet) Defs() []llm.ToolDef {
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
func (s *taskToolSet) Has(name string) bool {
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
func (s *taskToolSet) Execute(name string, input map[string]any) (string, bool, error) {
	tool, ok := s.byName[name]
	if !ok {
		return "", true, fmt.Errorf("unknown task tool %q", name)
	}
	taskToolExecutionMu.Lock()
	defer taskToolExecutionMu.Unlock()
	restore := tasks.SetExecContext(tasks.ExecContext{
		AgentID:   taskToolAgentID,
		SessionID: s.sessionID,
	})
	defer restore()
	return tools.PythonJSONDumps(tool.Execute(input)), false, nil
}
