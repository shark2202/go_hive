package tasks

// Parity: framework/tasks/tools/names.py

const (
	TaskCreate = "task_create"
	TaskUpdate = "task_update"
	TaskList   = "task_list"
	TaskGet    = "task_get"
)

var TaskWriteTools = []string{TaskCreate, TaskUpdate}

var AllTaskTools = []string{TaskCreate, TaskUpdate, TaskList, TaskGet}

func IsTaskWriteTool(name string) bool {
	return name == TaskCreate || name == TaskUpdate
}

func IsTaskTool(name string) bool {
	return IsTaskWriteTool(name) || name == TaskList || name == TaskGet
}
