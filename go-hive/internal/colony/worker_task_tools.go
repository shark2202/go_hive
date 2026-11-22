package colony

// Worker task lists.
//
// Upstream gives every worker the same four task tools the queen has
// (framework/agents/queen/worker_definition.py: "You have task tools
// (task_create, task_update, task_list, task_get)"). A worker uses them to
// break its assignment into steps and track its own progress; the queen never
// polls the list — the worker's report is the channel back.
//
// The Go port had them only on the queen's chat catalogue, so a spawned worker
// could not plan at all. Here each worker gets its own binding: the task store
// is rooted at the worker's own directory and keyed by its worker id, so two
// workers in the same colony — and the same worker across colonies — never
// share a list. Execution is serialised process-wide because the task
// executors read a package-level execution context (tasks.SetExecContext).

import (
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// workerTaskExecutionMu serialises the process-wide execution context the task
// executors read. Same reason as the queen side (internal/cli/task_tools.go):
// one tool call at a time observes one session id.
var workerTaskExecutionMu sync.Mutex

// workerTaskToolSet is one worker's binding of the four task tools.
type workerTaskToolSet struct {
	workerID string
	ordered  []tasks.Tool
	byName   map[string]tasks.Tool
}

// newWorkerTaskToolSet binds the task tools to one worker of one colony.
// workerDir is the worker's own directory (<colony>/workers/<id>); its task
// list lands in <workerDir>/_misc/<id>/tasks.json, the store's documented
// fallback for a session directory it cannot resolve by name.
func newWorkerTaskToolSet(workerDir, workerID string) *workerTaskToolSet {
	if workerID == "" {
		return nil
	}
	built := tasks.BuildSessionTools(tasks.NewTaskStore(workerDir), nil)
	set := &workerTaskToolSet{
		workerID: workerID,
		ordered:  built,
		byName:   make(map[string]tasks.Tool, len(built)),
	}
	for _, tool := range built {
		set.byName[tool.Name] = tool
	}
	return set
}

// Defs returns the tool definitions advertised to the worker's model.
func (s *workerTaskToolSet) Defs() []llm.ToolDef {
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

// workerTaskToolDefs is the definition list without a worker binding, for the
// surfaces assembled before a concrete worker exists (spawn tool lists).
func workerTaskToolDefs() []llm.ToolDef {
	return newWorkerTaskToolSet("", "worker").Defs()
}

// Has reports whether name is one of the worker task tools.
func (s *workerTaskToolSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.byName[name]
	return ok
}

// Execute runs one task tool against this worker's own list. A rejected
// operation is a normal result carrying success=false, as on the queen side.
func (s *workerTaskToolSet) Execute(name string, input map[string]any) (content string, isError bool, handled bool) {
	if s == nil {
		return "", false, false
	}
	tool, ok := s.byName[name]
	if !ok {
		return "", false, false
	}
	workerTaskExecutionMu.Lock()
	defer workerTaskExecutionMu.Unlock()
	restore := tasks.SetExecContext(tasks.ExecContext{
		AgentID:   s.workerID,
		SessionID: s.workerID,
	})
	defer restore()
	return tools.PythonJSONDumps(tool.Execute(input)), false, true
}

// workerTaskNames lists the four tool names, for the tier keep-set and for
// tests that assert the worker surface.
func workerTaskNames() []string {
	defs := workerTaskToolDefs()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}
