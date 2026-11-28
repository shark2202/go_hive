package cli

// Session task tools: the four tools (task_create / task_update / task_list /
// task_get) that let an agent maintain its own task plan. The implementation
// lives in internal/cli/toolcatalog (no Runner dependency); this file keeps
// the composition root's historical type and constructor names so the
// catalogue, the pivot wiring and the tests keep using them unchanged.

import (
	"github.com/shark2202/go_hive/go-hive/internal/cli/toolcatalog"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// taskToolAgentID is the identity task_update stamps on a task it moves to
// in_progress (upstream's queen fallback).
const taskToolAgentID = toolcatalog.TaskToolAgentID

// taskToolSet is one session's binding of the task tools. A nil set means the
// session has no task tools; every method keeps the original nil-receiver
// behaviour.
type taskToolSet struct {
	inner *toolcatalog.TaskToolSet
}

// newTaskToolSet binds the task tools to the session directory. root is the
// session's own directory (queens/<queen>/sessions/<session_id>); its base
// name is the session id the store resolves back to that directory under the
// hive root.
func newTaskToolSet(root string, home hivehome.Home) *taskToolSet {
	inner := toolcatalog.NewTaskToolSet(root, home)
	if inner == nil {
		return nil
	}
	return &taskToolSet{inner: inner}
}

// setPivot installs (or clears) the queen's pivot handler and rebuilds the
// four tools. task_create gains the pivot field + handoff argument when a
// handler is set; a worker catalogue stays with the two-argument version,
// exactly like upstream's build_session_tools(pivot_handler=None).
func (s *taskToolSet) setPivot(handler *tasks.PivotHandler) {
	if s == nil {
		return
	}
	s.inner.SetPivot(handler)
}

// createDef returns the task_create definition currently in force.
func (s *taskToolSet) createDef() (llm.ToolDef, bool) {
	if s == nil {
		return llm.ToolDef{}, false
	}
	return s.inner.CreateDef()
}

// Defs returns the tool definitions advertised to the model.
func (s *taskToolSet) Defs() []llm.ToolDef {
	if s == nil {
		return nil
	}
	return s.inner.Defs()
}

// Has reports whether name is one of the session task tools.
func (s *taskToolSet) Has(name string) bool {
	if s == nil {
		return false
	}
	return s.inner.Has(name)
}

// Execute runs one task tool for this session.
func (s *taskToolSet) Execute(name string, input map[string]any) (string, bool, error) {
	return s.inner.Execute(name, input)
}
