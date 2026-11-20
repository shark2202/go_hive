package tasks

// Parity: framework/tasks/tools/session_tools.py

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

var CRMInThisBuild = false

type PivotHandler struct {
	FieldName        string
	FieldDescription string
	// HandoffDescription overrides the generated handoff argument docs. The
	// queen's DM pivot passes the Python contract text
	// (task_create_contract.go); an empty value keeps the generic text below
	// for handlers that only care about routing.
	HandoffDescription string
	Handle             func(goal string, handoff any, tasks []any) map[string]any
}

var taskStatusValues = []string{"pending", "in_progress", "completed", "archived", "deleted"}

func updateSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "integer", "description": "Task id (the #N from task_list)."},
			"subject":     map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"active_form": map[string]any{"type": "string"},
			"owner": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Agent id of the owner. Null clears ownership.",
			},
			"status": map[string]any{
				"type": "string",
				"enum": taskStatusValues,
				"description": "pending / in_progress / completed track work. " +
					"'archived' moves a finished or no-longer-relevant task " +
					"out of the active plan into the user's History " +
					"(preserved, unlike 'deleted' which removes it " +
					"permanently). Archiving is reversible: set an archived " +
					"task back to pending/in_progress/completed to restore " +
					"it to the active plan (use task_list " +
					"include_archived=true to find archived ids).",
			},
			"add_blocks": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Add task ids that this task blocks (bidirectional).",
			},
			"add_blocked_by": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Add task ids that block this task (bidirectional).",
			},
			"metadata_patch": map[string]any{
				"type":        "object",
				"description": "Merge into metadata. Null values delete keys.",
			},
		},
		"required": []string{"id"},
	}
}

func listSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"include_archived": map[string]any{
				"type": "boolean",
				"description": "Include archived tasks (default false). Set true to " +
					"review what you archived — e.g. before restoring one " +
					"by editing its status back to pending/in_progress.",
			},
		},
		// The Python request layer publishes the key even when empty
		// (framework/llm/litellm.py::_tool_to_openai_format).
		"required": []string{},
	}
}

func getSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "integer"}},
		"required":   []string{"id"},
	}
}

func createSchema(pivotHandler *PivotHandler) map[string]any {
	properties := map[string]any{
		"goal": map[string]any{
			"type": "string",
			"description": "One-sentence statement of what this plan is for, in the " +
				"user's own terms. Required on the FIRST task_create of a " +
				"session — anchors the work so a later batch that drifts " +
				"from this can be recognised as a real pivot rather than " +
				"absorbed silently. On follow-up task_create calls in the " +
				"same session, you MAY omit it (the existing goal is " +
				"kept) OR re-state it unchanged. Provide a NEW goal only " +
				"when you are also setting the pivot field below — the " +
				"new goal then anchors the forked context.",
		},
		"tasks": map[string]any{
			"type":        "array",
			"minItems":    1,
			"description": "Array of task specs. Each becomes one task with a sequential id. Atomic — all created or none.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subject": map[string]any{
						"type":        "string",
						"description": "Imperative title (e.g. 'Crawl target URL').",
					},
					"description": map[string]any{"type": "string"},
					"active_form": map[string]any{
						"type":        "string",
						"description": "Present-continuous label shown while in_progress.",
					},
					"metadata": map[string]any{"type": "object"},
				},
				"required": []string{"subject"},
			},
		},
	}
	if pivotHandler != nil {
		properties[pivotHandler.FieldName] = map[string]any{
			"type":        "boolean",
			"description": pivotHandler.FieldDescription,
		}
		handoffDescription := pivotHandler.HandoffDescription
		if handoffDescription == "" {
			handoffDescription = "Required when " + pivotHandler.FieldName + "=true. The new " +
				"context inherits NOTHING from this conversation except " +
				"what you write here — no transcript, no summary, no " +
				"scrollback. So write a COMPLETE, self-contained " +
				"handoff: everything the new context needs to know to " +
				"carry the task plan through to done.\n\n" +
				"Be descriptive and objective — state facts, not " +
				"impressions. Cover, as applicable:\n" +
				"- The user's actual goal and intent for this work, in " +
				"their own terms.\n" +
				"- Concrete facts and data established so far: names, " +
				"URLs, IDs, file paths, the account/alias to use, " +
				"numbers, exact requirements and wording.\n" +
				"- Decisions already made, and options already " +
				"considered and ruled out (with the reason).\n" +
				"- Constraints, preferences, deadlines, and what 'done' " +
				"looks like.\n" +
				"- Anything already attempted or partially completed " +
				"that the new context must not redo.\n\n" +
				"The new context cannot look back — if a detail matters " +
				"for finishing the task and is not already in a task " +
				"subject/description, it MUST be in this handoff. Write " +
				"it in full now, while you still have the whole " +
				"conversation in view. Ignored when " +
				pivotHandler.FieldName + " is false."
		}
		properties["handoff"] = map[string]any{
			"type":        "string",
			"description": handoffDescription,
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"tasks"},
	}
}

const updateDescription = "Update ONE task on your own session task list. There is no batch " +
	"update tool by design — every `completed` transition is a discrete " +
	"progress signal to the user.\n\n" +
	"Workflow:\n" +
	"- Mark a task `in_progress` BEFORE you start working on it.\n" +
	"- Mark it `completed` AS SOON as you finish it — do not let " +
	"multiple finished tasks pile up unmarked before flushing them at " +
	"the end of the run.\n" +
	"- Send these in the same response as your next real tool call (one " +
	"response can carry several calls): a response that only updates " +
	"tasks makes the user wait a whole model turn for nothing.\n" +
	"- Archive tasks: when the user asks you to refresh/tidy the plan, set " +
	"status='archived' on finished or no-longer-relevant tasks to move " +
	"them out of the active plan into the user's History. Archived tasks " +
	"are preserved and hidden from the default `task_list` — pass " +
	"include_archived=true to see them. Archiving is reversible: edit an " +
	"archived task's status back to pending/in_progress/completed to pull " +
	"it back into the active plan.\n" +
	"- Delete tasks: when a task is no longer relevant or was created " +
	"in error. Setting status='deleted' **permanently** removes the " +
	"task — the id is retired and cannot be reused. Prefer 'archived' " +
	"over 'deleted' unless the task was a genuine mistake.\n\n" +
	"ONLY mark `completed` when the task is FULLY done. If you hit errors, " +
	"blockers, or partial state, keep it `in_progress` and create a new " +
	"task describing what's blocking. Never mark completed with caveats; " +
	"if it's not done, it's not done.\n\n" +
	"Setting status='in_progress' without owner auto-fills your agent_id."

const listDescription = "Show your session task list, sorted by id ascending. Internal tasks " +
	"(metadata._internal=true), resolved blockers, and archived tasks are " +
	"filtered out by default; pass include_archived=true to also see " +
	"archived tasks (e.g. to restore one). **Prefer working on tasks in id " +
	"order** (lowest first) — earlier tasks usually set up context for " +
	"later ones."

const getDescription = "Read the full record of one task (description, metadata, timestamps) " +
	"from your own session task list. Use this to refresh your view of a " +
	"task before updating it if you're not sure of current fields."

const createDescriptionCRM = "**GTM work:** if the plan is about people / leads / accounts / " +
	"outreach, include a CLAIM task (dedup leads in the global CRM before " +
	"outreach) and a PROMOTE task (write results to the CRM after) — the " +
	"shared CRM is a planned deliverable, not an afterthought.\n\n"

const createDescriptionBase = "Use this tool to create a structured task list for your current " +
	"session. The list renders as a live widget in the user's right-rail " +
	"panel — it helps the user understand progress on their request and " +
	"demonstrates thoroughness.\n\n" +
	"**Always takes a `tasks` array — one entry or many, created " +
	"atomically (all-or-none).** Replying to 5 posts is ONE `task_create` " +
	"with 5 entries. A single one-off mid-run addition is ONE " +
	"`task_create` with a 1-entry array.\n\n"

const createDescriptionTail = "## When to use this tool\n\n" +
	"Use this tool proactively in these scenarios:\n" +
	"- **Multi-step requests** — when a request requires 2+ distinct " +
	"atomic actions\n" +
	"- **Non-trivial work** — anything involving careful planning or " +
	"multiple operations (browser automation, file edits, API calls, " +
	"fan-out to workers)\n" +
	"- **User provides multiple tasks** — a list of things to do " +
	"(numbered or comma-separated)\n" +
	"- **After receiving REFINING instructions** — if the new request " +
	"refines the existing plan (same goal, more detail), capture the " +
	"changes as tasks and delete prior ones via `task_update` " +
	"status='deleted'. If the new request is OFF-GOAL — different goal " +
	"from this list's anchor — DON'T wipe and redo here; use the pivot " +
	"field on this same tool instead.\n" +
	"- **Mid-run additions** — when you discover unplanned work after " +
	"the initial plan is laid out, add it with a 1-entry array\n" +
	"- **User explicitly requests a plan or task list**\n\n" +
	"## When NOT to use this tool\n\n" +
	"Skip this tool when:\n" +
	"- There is only a single, straightforward action (one terminal_exec, " +
	"one `hive-browser open`, one tracker_query)\n" +
	"- The task is trivial and tracking it provides no organizational " +
	"benefit\n" +
	"- The request is purely conversational or informational (greetings, " +
	"questions, opinions)\n" +
	"- The user has explicitly asked you not to use it\n\n" +
	"## Task fields\n\n" +
	"- **subject**: short imperative title (e.g. 'Crawl target URL')\n" +
	"- **description**: optional, slightly longer 'what to do' note\n" +
	"- **active_form**: present-continuous label shown while in_progress " +
	"(e.g. 'Crawling target URL'). If omitted, the spinner shows the " +
	"subject\n" +
	"- **metadata**: optional KV. Set _internal=true to hide from " +
	"`task_list`\n\n" +
	"All tasks are created with status `pending`.\n\n" +
	"## Tips\n\n" +
	"- Subjects should be clear and outcome-focused\n" +
	"- **Granularity: one task/batch per atomic action, not one umbrella per " +
	"project** — \"Scrape 5 LinkedIn profiles\" is 5 entries, not 1\n" +
	"- Include a final **verification step** for any non-trivial plan " +
	"(re-read the diff, query the tracker for gaps, screenshot the " +
	"result, run the script end-to-end)\n" +
	"- Check `task_list` first to avoid creating duplicate tasks"

func createDescription() string {
	out := createDescriptionBase
	if CRMInThisBuild {
		out += createDescriptionCRM
	}
	return out + createDescriptionTail
}

func inputInt(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return n
		}
	}
	return 0
}

func inputMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func inputSpecs(v any) []map[string]any {
	items, ok := v.([]any)
	if !ok {
		if typed, ok := v.([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		} else {
			out = append(out, map[string]any{})
		}
	}
	return out
}

func serializeTasks(records []*TaskRecord) []any {
	out := make([]any, 0, len(records))
	for _, r := range records {
		out = append(out, SerializeRecord(r))
	}
	return out
}

func agentPtr(agentID string) *string { return &agentID }

func makeCreateExecutor(store *TaskStore, pivotHandler *PivotHandler) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		sessionID := CurrentSessionID()
		if sessionID == "" {
			return map[string]any{"success": false, "error": "No session_id resolved for this agent."}
		}
		agentID := CurrentAgentID()
		specMaps := inputSpecs(inputs["tasks"])
		if len(specMaps) == 0 {
			return map[string]any{
				"success": false,
				"error":   "task_create requires a non-empty `tasks` array.",
			}
		}

		var goal *string
		if raw, ok := inputs["goal"].(string); ok {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" {
				goal = &trimmed
			}
		}

		existingMeta := store.GetMeta(sessionID)
		isFirstCreate := existingMeta == nil || existingMeta.Goal == nil
		if isFirstCreate && goal == nil {
			return map[string]any{
				"success": false,
				"error": "task_create on a fresh task list requires a `goal` — " +
					"one sentence describing what this plan is for, in " +
					"the user's terms. The goal anchors the plan so a " +
					"later batch that drifts from it can be recognised " +
					"as a real pivot. Call task_create again with `goal` " +
					"set.",
			}
		}

		if pivotHandler != nil && truthyAny(inputs[pivotHandler.FieldName]) {
			if goal == nil {
				return map[string]any{
					"success": false,
					"error": pivotHandler.FieldName + "=true requires a " +
						"`goal` — state the new context's goal in one " +
						"sentence, in the user's terms. The new context " +
						"anchors on this.",
				}
			}
			tasks := make([]any, 0, len(specMaps))
			for _, m := range specMaps {
				tasks = append(tasks, m)
			}
			return pivotHandler.Handle(*goal, inputs["handoff"], tasks)
		}

		specs := make([]TaskSpec, 0, len(specMaps))
		for _, m := range specMaps {
			specs = append(specs, SpecFromMap(m))
		}
		recs, err := store.CreateTasksBatch(sessionID, specs, goal)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}

		for _, rec := range recs {
			hookErr := RunTaskHooks(HookTaskCreated, TaskHookContext{
				SessionID: sessionID,
				Task:      rec,
				AgentID:   agentPtr(agentID),
			})
			if hookErr == nil {
				continue
			}
			if _, blocking := AsBlockingHookError(hookErr); blocking {
				log.Printf("task_created hook blocked batch on task #%d: %v", rec.ID, hookErr)
				for _, r := range recs {
					_, _, _ = store.DeleteTask(sessionID, r.ID)
				}
				return map[string]any{
					"success": false,
					"error": fmt.Sprintf("Hook blocked task #%d ('%s'); entire batch rolled back: %v",
						rec.ID, rec.Subject, hookErr),
				}
			}
		}

		for _, rec := range recs {
			EmitTaskCreated(sessionID, rec, "", nil)
		}

		ids := make([]int, 0, len(recs))
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		rangeLabel := ""
		if len(ids) == 1 {
			rangeLabel = fmt.Sprintf("#%d", ids[0])
		} else if contiguous(ids) {
			rangeLabel = fmt.Sprintf("#%d-#%d", ids[0], ids[len(ids)-1])
		} else {
			parts := make([]string, 0, len(ids))
			for _, id := range ids {
				parts = append(parts, fmt.Sprintf("#%d", id))
			}
			rangeLabel = strings.Join(parts, ", ")
		}
		return map[string]any{
			"success":    true,
			"session_id": sessionID,
			"task_ids":   ids,
			"message": fmt.Sprintf("Created %d task(s): %s. Mark #%d in_progress before starting it.",
				len(ids), rangeLabel, ids[0]),
			"tasks": serializeTasks(recs),
		}
	}
}

func contiguous(ids []int) bool {
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			return false
		}
	}
	return true
}

func truthyAny(v any) bool {
	switch typed := v.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

func makeUpdateExecutor(store *TaskStore) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		sessionID := CurrentSessionID()
		if sessionID == "" {
			return map[string]any{"success": false, "error": "No session_id resolved for this agent."}
		}
		agentID := CurrentAgentID()
		taskID := inputInt(inputs["id"])

		current := store.GetTask(sessionID, taskID)

		statusIn, _ := inputs["status"].(string)
		if statusIn == "deleted" {
			deleted, cascade, _ := store.DeleteTask(sessionID, taskID)
			if !deleted {
				return map[string]any{
					"success":    false,
					"session_id": sessionID,
					"task_id":    taskID,
					"message":    fmt.Sprintf("Task #%d not found (already deleted?)", taskID),
				}
			}
			EmitTaskDeleted(sessionID, taskID, cascade, "", nil)
			if cascade == nil {
				cascade = []int{}
			}
			return map[string]any{
				"success":    true,
				"session_id": sessionID,
				"task_id":    taskID,
				"deleted":    true,
				"cascade":    cascade,
				"message":    fmt.Sprintf("Task #%d deleted.", taskID),
			}
		}

		ownerVal, hasOwner := inputs["owner"]
		var owner *string
		if hasOwner {
			if s, ok := ownerVal.(string); ok {
				owner = &s
			}
		}
		var statusEnum *TaskStatus
		if statusIn != "" {
			candidate := TaskStatus(statusIn)
			statusEnum = &candidate
		}
		if statusEnum != nil && *statusEnum == TaskStatusInProgress && !hasOwner && agentID != "" {
			owner = agentPtr(agentID)
			hasOwner = true
		}

		if statusEnum != nil && *statusEnum == TaskStatusCompleted {
			if current == nil {
				return map[string]any{
					"success":    false,
					"session_id": sessionID,
					"task_id":    taskID,
					"message":    fmt.Sprintf("Task #%d not found.", taskID),
				}
			}
			if current.Status != TaskStatusCompleted {
				preview := current.Clone()
				preview.Status = TaskStatusCompleted
				hookErr := RunTaskHooks(HookTaskCompleted, TaskHookContext{
					SessionID: sessionID,
					Task:      preview,
					AgentID:   agentPtr(agentID),
				})
				if hookErr != nil {
					if _, blocking := AsBlockingHookError(hookErr); blocking {
						log.Printf("task_completed hook blocked #%d: %v", taskID, hookErr)
						return map[string]any{
							"success":    false,
							"session_id": sessionID,
							"task_id":    taskID,
							"message":    fmt.Sprintf("Hook blocked completion of #%d: %v", taskID, hookErr),
							"task":       SerializeRecord(current),
						}
					}
				}
			}
		}

		opts := UpdateTaskOptions{
			Subject:       asOptionalString(inputs["subject"]),
			Description:   asOptionalString(inputs["description"]),
			ActiveForm:    asOptionalString(inputs["active_form"]),
			Owner:         owner,
			OwnerSet:      hasOwner,
			Status:        statusEnum,
			AddBlocks:     asIntSlice(inputs["add_blocks"]),
			AddBlockedBy:  asIntSlice(inputs["add_blocked_by"]),
			MetadataPatch: inputMap(inputs["metadata_patch"]),
		}
		newRec, fields := store.UpdateTask(sessionID, taskID, opts)
		if newRec == nil {
			return map[string]any{
				"success":    false,
				"session_id": sessionID,
				"task_id":    taskID,
				"message":    fmt.Sprintf("Task #%d not found.", taskID),
			}
		}
		if len(fields) > 0 {
			EmitTaskUpdated(sessionID, newRec, fields, "", nil)
		}

		fieldsOut := fields
		if fieldsOut == nil {
			fieldsOut = []string{}
		}
		message := fmt.Sprintf("Task #%d updated. Fields changed: %s.", taskID, joinOrNone(fields))
		if statusEnum != nil && *statusEnum == TaskStatusCompleted && containsString(fields, "status") {
			others := store.ListTasks(sessionID)
			completedIDs := map[int]bool{}
			for _, r := range others {
				if r.Status == TaskStatusCompleted {
					completedIDs[r.ID] = true
				}
			}
			var nextPending *TaskRecord
			for _, r := range others {
				if r.Status != TaskStatusPending {
					continue
				}
				unresolved := false
				for _, b := range r.BlockedBy {
					if !completedIDs[b] {
						unresolved = true
						break
					}
				}
				if !unresolved {
					nextPending = r
					break
				}
			}
			inProgress := []*TaskRecord{}
			for _, r := range others {
				if r.Status == TaskStatusInProgress {
					inProgress = append(inProgress, r)
				}
			}
			if len(inProgress) > 0 {
				limit := len(inProgress)
				if limit > 3 {
					limit = 3
				}
				parts := make([]string, 0, limit)
				for _, r := range inProgress[:limit] {
					parts = append(parts, fmt.Sprintf("#%d", r.ID))
				}
				message += fmt.Sprintf(" Still in progress: %s.", strings.Join(parts, ", "))
			} else if nextPending != nil {
				message += fmt.Sprintf(" Next pending: #%d — \"%s\". Mark it in_progress before starting.", nextPending.ID, nextPending.Subject)
			} else {
				meta := store.GetMeta(sessionID)
				if meta != nil && meta.Goal != nil && *meta.Goal != "" {
					message += fmt.Sprintf(" All tasks complete (goal: %s). Wrap up: ", *meta.Goal) +
						"report results to the user and stop. If the user " +
						"follows up with work that doesn't fit this goal, " +
						"treat it as a pivot, not a continuation."
				} else {
					message += " All tasks complete. Wrap up: report results to the user and stop."
				}
			}
		}

		return map[string]any{
			"success":    true,
			"session_id": sessionID,
			"task_id":    taskID,
			"fields":     fieldsOut,
			"message":    message,
			"task":       SerializeRecord(newRec),
		}
	}
}

func joinOrNone(fields []string) string {
	if len(fields) == 0 {
		return "(none)"
	}
	return strings.Join(fields, ", ")
}

func makeListExecutor(store *TaskStore) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		sessionID := CurrentSessionID()
		if sessionID == "" {
			return map[string]any{"success": false, "error": "No session_id resolved for this agent."}
		}
		includeArchived := truthyAny(inputs["include_archived"])
		records := store.ListTasks(sessionID)
		if !includeArchived {
			filtered := make([]*TaskRecord, 0, len(records))
			for _, r := range records {
				if r.Status != TaskStatusArchived {
					filtered = append(filtered, r)
				}
			}
			records = filtered
		}
		meta := store.GetMeta(sessionID)
		var goal any
		if meta != nil && meta.Goal != nil {
			goal = *meta.Goal
		}
		completedIDs := map[int]bool{}
		for _, r := range records {
			if r.Status == TaskStatusCompleted {
				completedIDs[r.ID] = true
			}
		}
		rendered := []string{}
		if goalText, ok := goal.(string); ok && goalText != "" {
			rendered = append(rendered, "Goal: "+goalText)
		}
		for _, r := range records {
			unresolved := []int{}
			for _, b := range r.BlockedBy {
				if !completedIDs[b] {
					unresolved = append(unresolved, b)
				}
			}
			parts := []string{fmt.Sprintf("#%d", r.ID), "[" + string(r.Status) + "]", r.Subject}
			if r.Owner != nil && *r.Owner != "" {
				parts = append(parts, "("+*r.Owner+")")
			}
			if len(unresolved) > 0 {
				blocked := make([]string, 0, len(unresolved))
				for _, b := range unresolved {
					blocked = append(blocked, fmt.Sprintf("#%d", b))
				}
				parts = append(parts, "[blocked by "+strings.Join(blocked, ", ")+"]")
			}
			rendered = append(rendered, strings.Join(parts, " "))
		}
		return map[string]any{
			"success":    true,
			"session_id": sessionID,
			"count":      len(records),
			"goal":       goal,
			"lines":      rendered,
			"tasks":      serializeTasks(records),
		}
	}
}

func makeGetExecutor(store *TaskStore) func(map[string]any) map[string]any {
	return func(inputs map[string]any) map[string]any {
		sessionID := CurrentSessionID()
		if sessionID == "" {
			return map[string]any{"success": false, "error": "No session_id resolved for this agent."}
		}
		taskID := inputInt(inputs["id"])
		rec := store.GetTask(sessionID, taskID)
		if rec == nil {
			return map[string]any{
				"success":    false,
				"session_id": sessionID,
				"task_id":    taskID,
				"message":    fmt.Sprintf("Task #%d not found.", taskID),
			}
		}
		return map[string]any{
			"success":    true,
			"session_id": sessionID,
			"task_id":    taskID,
			"task":       SerializeRecord(rec),
		}
	}
}

func BuildSessionTools(store *TaskStore, pivotHandler *PivotHandler) []Tool {
	if store == nil {
		store = GetTaskStore()
	}
	return []Tool{
		{
			Name:            TaskCreate,
			Description:     createDescription(),
			Parameters:      createSchema(pivotHandler),
			ConcurrencySafe: false,
			Execute:         makeCreateExecutor(store, pivotHandler),
		},
		{
			Name:            TaskUpdate,
			Description:     updateDescription,
			Parameters:      updateSchema(),
			ConcurrencySafe: false,
			Execute:         makeUpdateExecutor(store),
		},
		{
			Name:            TaskList,
			Description:     listDescription,
			Parameters:      listSchema(),
			ConcurrencySafe: true,
			Execute:         makeListExecutor(store),
		},
		{
			Name:            TaskGet,
			Description:     getDescription,
			Parameters:      getSchema(),
			ConcurrencySafe: true,
			Execute:         makeGetExecutor(store),
		},
	}
}
