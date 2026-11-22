package server

// Parity: core/framework/server/routes_tasks.py::register_routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// TaskRouteDeps scopes the task-list route table to one hive root. A nil
// Store falls back to the process-wide default store, mirroring
// framework.tasks.get_task_store().
type TaskRouteDeps struct {
	Store *tasks.TaskStore
}

func (d TaskRouteDeps) taskStore() *tasks.TaskStore {
	if d.Store != nil {
		return d.Store
	}
	return tasks.GetTaskStore()
}

// RegisterTaskRoutes wires the per-session task-list REST surface:
//
//	GET  /api/sessions/{session_id}/tasks                  -- snapshot of one list
//	POST /api/sessions/{session_id}/tasks/unarchive        -- restore archived tasks
//	POST /api/sessions/{session_id}/tasks/clear-completed  -- archive completed tasks
//
// Parity: core/framework/server/routes_tasks.py::register_routes
func RegisterTaskRoutes(mux *http.ServeMux, deps TaskRouteDeps) {
	store := deps.taskStore()
	mux.HandleFunc("GET /api/sessions/{session_id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		handleGetTaskList(store, w, r)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/tasks/unarchive", func(w http.ResponseWriter, r *http.Request) {
		handleUnarchiveTaskList(store, w, r)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/tasks/clear-completed", func(w http.ResponseWriter, r *http.Request) {
		handleClearCompletedTasks(store, w, r)
	})
}

// Parity: core/framework/server/routes_tasks.py::handle_get_task_list
func handleGetTaskList(store *tasks.TaskStore, w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	if !store.ListExists(raw) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":      "Task list for session '" + raw + "' not found",
			"session_id": raw,
			"tasks":      []any{},
		})
		return
	}

	// Sweep stale in_progress tasks before serializing so the client always
	// sees the post-sweep view. Each flipped record emits task_updated so
	// other open viewers stay consistent. Cheap no-op when nothing is stale.
	if swept, err := store.SweepIdleTasks(raw); err == nil {
		for _, record := range swept {
			tasks.EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
		}
	}

	meta := store.GetMeta(raw)
	role := "session"
	if meta != nil && meta.Role != "" {
		role = meta.Role
	}
	records := store.ListTasks(raw)
	serialized := make([]any, 0, len(records))
	for _, record := range records {
		serialized = append(serialized, tasks.SerializeRecord(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"role":       role,
		"meta":       meta,
		"tasks":      serialized,
	})
}

// readTaskJSONBody is the best-effort JSON body upstream reads: an
// empty/malformed body (or a non-object) is treated as {}. A body over the
// shared JSON cap is refused instead of buffered, so the task routes answer
// 413 like the other JSON routes rather than accepting unbounded input.
func readTaskJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	if r == nil || r.Body == nil {
		return map[string]any{}, nil
	}
	capped := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	data, err := io.ReadAll(capped)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		// A broken stream is as good as an empty body, matching the previous
		// best-effort decode.
		return map[string]any{}, nil
	}
	var parsed any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&parsed); err != nil {
		return map[string]any{}, nil
	}
	if m, ok := parsed.(map[string]any); ok {
		return m, nil
	}
	return map[string]any{}, nil
}

// taskIDList mirrors Python's isinstance(i, int) over a JSON array: whole
// JSON numbers only, and a non-array fails the whole request.
func taskIDList(v any) ([]int, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		switch n := item.(type) {
		case int:
			out = append(out, n)
		case float64:
			if math.Trunc(n) != n {
				return nil, false
			}
			out = append(out, int(n))
		default:
			return nil, false
		}
	}
	return out, true
}

// Parity: core/framework/server/routes_tasks.py::handle_unarchive_task_list
func handleUnarchiveTaskList(store *tasks.TaskStore, w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	body, err := readTaskJSONBody(w, r)
	if errors.Is(err, errBodyTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":      "Request body too large",
			"session_id": raw,
		})
		return
	}
	taskIDs, ok := taskIDList(body["task_ids"])
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":      "task_ids must be a list of integers",
			"session_id": raw,
		})
		return
	}

	restored, _ := store.UnarchiveTasks(raw, taskIDs)
	if restored == nil {
		restored = []int{}
	}
	if len(restored) > 0 {
		wanted := make(map[int]struct{}, len(restored))
		for _, id := range restored {
			wanted[id] = struct{}{}
		}
		for _, record := range store.ListTasks(raw) {
			if _, hit := wanted[record.ID]; hit {
				tasks.EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"restored":   restored,
	})
}

// Parity: core/framework/server/routes_tasks.py::handle_clear_completed_tasks
func handleClearCompletedTasks(store *tasks.TaskStore, w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	archived, _ := store.ArchiveCompletedTasks(raw)
	ids := make([]int, 0, len(archived))
	for _, record := range archived {
		tasks.EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
		ids = append(ids, record.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"archived":   ids,
	})
}
