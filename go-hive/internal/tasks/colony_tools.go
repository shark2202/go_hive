package tasks

// Parity: framework/tasks/tools/colony_tools.py
//
// Deprecated: the colony template tool family is unregistered legacy code.
// It was introduced with a registrar in 0af597881 (2026-04-26), the registrar
// was removed in 24f864bc4 (2026-05-11), and the current tree carries this
// file without any caller. Do not wire or call it; scheduled for removal.
// See go_hive/docs/hive-package-atlas.html and delivery-backlog.json.

import (
	"fmt"
)

// Deprecated: unregistered legacy tool names; see BuildColonyTemplateTools.
const (
	ColonyTemplateAdd    = "colony_template_add"
	ColonyTemplateUpdate = "colony_template_update"
	ColonyTemplateRemove = "colony_template_remove"
	ColonyTemplateList   = "colony_template_list"
)

func colonyAddSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subject":     map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"active_form": map[string]any{"type": "string"},
			"metadata":    map[string]any{"type": "object"},
		},
		"required": []string{"subject"},
	}
}

func colonyUpdateSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "integer"},
			"subject":     map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"active_form": map[string]any{"type": "string"},
			"owner":       map[string]any{"type": []string{"string", "null"}},
			"status": map[string]any{
				"type": "string",
				"enum": []string{
					string(TaskStatusPending),
					string(TaskStatusInProgress),
					string(TaskStatusCompleted),
				},
			},
			"metadata_patch": map[string]any{"type": "object"},
		},
		"required": []string{"id"},
	}
}

func colonyRemoveSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "integer"},
		},
		"required": []string{"id"},
	}
}

func colonyListSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

const colonyAddDesc = "Append a task to your colony's spawn-plan template. Templates are read " +
	"by `run_parallel_workers` and the UI; workers do not pull from the " +
	"template after spawn. Use this to plan colony work before spawning."

const colonyUpdateDesc = "Update a template entry on your colony's spawn-plan template (e.g., " +
	"stamp completion when a worker reports back, adjust subject/description). " +
	"Only the queen can call this."

const colonyRemoveDesc = "Remove a template entry from your colony's spawn-plan template. The id is reserved (high-water-mark preserved) — never reused."

const colonyListDesc = "List all entries on your colony's spawn-plan template. Each entry " +
	"includes any `metadata.assigned_session` stamp that ties the entry to " +
	"a spawned worker."

func makeColonyAddExecutor(store *TaskStore, listID string) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		activeForm := asOptionalString(inputs["active_form"])
		metadata := inputMap(inputs["metadata"])
		if metadata == nil {
			metadata = map[string]any{}
		}
		rec, err := store.CreateTask(listID, CreateTaskSpec{
			Subject:     asString(inputs["subject"]),
			Description: asString(inputs["description"]),
			ActiveForm:  activeForm,
			Metadata:    metadata,
		})
		if err != nil {
			return map[string]any{
				"success":      false,
				"task_list_id": listID,
				"message":      fmt.Sprintf("Failed to add template entry: %v", err),
			}
		}
		EmitTaskCreated(listID, rec, "", nil)
		return map[string]any{
			"success":      true,
			"task_list_id": listID,
			"task_id":      rec.ID,
			"message":      fmt.Sprintf("Template entry #%d added: %s", rec.ID, rec.Subject),
			"task":         SerializeRecord(rec),
		}
	}
}

func makeColonyUpdateExecutor(store *TaskStore, listID string) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		taskID := inputInt(inputs["id"])
		statusRaw, hasStatus := inputs["status"]
		var statusIn string
		if hasStatus && statusRaw != nil {
			text, ok := statusRaw.(string)
			if !ok {
				return map[string]any{
					"success":      false,
					"task_list_id": listID,
					"task_id":      taskID,
					"error":        InvalidTaskStatusMessage(statusRaw),
				}
			}
			statusIn = text
		}
		if statusIn != "" && !ValidTaskStatus(statusIn) {
			return map[string]any{
				"success":      false,
				"task_list_id": listID,
				"task_id":      taskID,
				"error":        InvalidTaskStatusMessage(statusIn),
			}
		}
		var statusEnum *TaskStatus
		if statusIn != "" {
			candidate := TaskStatus(statusIn)
			statusEnum = &candidate
		}
		ownerVal, hasOwner := inputs["owner"]
		var owner *string
		if hasOwner {
			if s, ok := ownerVal.(string); ok {
				owner = &s
			}
		}
		newRec, fields := store.UpdateTask(listID, taskID, UpdateTaskOptions{
			Subject:       asOptionalString(inputs["subject"]),
			Description:   asOptionalString(inputs["description"]),
			ActiveForm:    asOptionalString(inputs["active_form"]),
			Owner:         owner,
			OwnerSet:      hasOwner,
			Status:        statusEnum,
			MetadataPatch: inputMap(inputs["metadata_patch"]),
		})
		if newRec == nil {
			return map[string]any{
				"success":      false,
				"task_list_id": listID,
				"task_id":      taskID,
				"message":      fmt.Sprintf("Template entry #%d not found.", taskID),
			}
		}
		if len(fields) > 0 {
			EmitTaskUpdated(listID, newRec, fields, "", nil)
		}
		fieldsOut := fields
		if fieldsOut == nil {
			fieldsOut = []string{}
		}
		return map[string]any{
			"success":      true,
			"task_list_id": listID,
			"task_id":      taskID,
			"fields":       fieldsOut,
			"message":      fmt.Sprintf("Template entry #%d updated. Fields: %s.", taskID, joinOrNone(fields)),
			"task":         SerializeRecord(newRec),
		}
	}
}

func makeColonyRemoveExecutor(store *TaskStore, listID string) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		taskID := inputInt(inputs["id"])
		deleted, cascade, err := store.DeleteTask(listID, taskID)
		if err != nil || !deleted {
			return map[string]any{
				"success":      false,
				"task_list_id": listID,
				"task_id":      taskID,
				"message":      fmt.Sprintf("Template entry #%d not found.", taskID),
			}
		}
		if cascade == nil {
			cascade = []int{}
		}
		EmitTaskDeleted(listID, taskID, cascade, "", nil)
		return map[string]any{
			"success":      true,
			"task_list_id": listID,
			"task_id":      taskID,
			"deleted":      true,
			"cascade":      cascade,
			"message":      fmt.Sprintf("Template entry #%d removed.", taskID),
		}
	}
}

func makeColonyListExecutor(store *TaskStore, listID string) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		records := store.ListTasks(listID)
		return map[string]any{
			"success":      true,
			"task_list_id": listID,
			"count":        len(records),
			"tasks":        serializeTasks(records),
		}
	}
}

// BuildColonyTemplateTools returns the colony spawn-plan template tools.
//
// Deprecated: this family is not registered anywhere. It was introduced with
// a registrar in 0af597881, the registrar was removed in 24f864bc4, and the
// current tree carries it without a caller. Do not register or call it;
// scheduled for removal. See docs/hive-package-atlas.html.
func BuildColonyTemplateTools(colonyID string, store *TaskStore) []Tool {
	if store == nil {
		store = GetTaskStore()
	}
	listID := ColonyTaskListID(colonyID)
	return []Tool{
		{
			Name:            ColonyTemplateAdd,
			Description:     colonyAddDesc,
			Parameters:      colonyAddSchema(),
			ConcurrencySafe: false,
			Execute:         makeColonyAddExecutor(store, listID),
		},
		{
			Name:            ColonyTemplateUpdate,
			Description:     colonyUpdateDesc,
			Parameters:      colonyUpdateSchema(),
			ConcurrencySafe: false,
			Execute:         makeColonyUpdateExecutor(store, listID),
		},
		{
			Name:            ColonyTemplateRemove,
			Description:     colonyRemoveDesc,
			Parameters:      colonyRemoveSchema(),
			ConcurrencySafe: false,
			Execute:         makeColonyRemoveExecutor(store, listID),
		},
		{
			Name:            ColonyTemplateList,
			Description:     colonyListDesc,
			Parameters:      colonyListSchema(),
			ConcurrencySafe: true,
			Execute:         makeColonyListExecutor(store, listID),
		},
	}
}
