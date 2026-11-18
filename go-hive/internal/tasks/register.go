package tasks

// Parity: framework/tasks/tools/register.py

func RegisterTaskTools(registry *Registry, store *TaskStore, pivotHandler *PivotHandler) {
	if store == nil {
		store = GetTaskStore()
	}
	for _, tool := range BuildSessionTools(store, pivotHandler) {
		registry.Register(tool)
	}
}
