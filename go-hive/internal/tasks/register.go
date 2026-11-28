package tasks

// Parity: framework/tasks/tools/register.py

// RegisterTaskTools registers the four session task tools.
//
// The colony_template_* family (colony_tools.go) is intentionally NOT
// registered: it is unregistered legacy code, marked deprecated 2026-11-26
// and scheduled for removal. Do not add BuildColonyTemplateTools here.
func RegisterTaskTools(registry *Registry, store *TaskStore, pivotHandler *PivotHandler) {
	if store == nil {
		store = GetTaskStore()
	}
	for _, tool := range BuildSessionTools(store, pivotHandler) {
		registry.Register(tool)
	}
}
