package tasks

// Parity: framework/server/routes_tasks.py

import (
	"encoding/json"
	"math"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func readJSONObjectBody(r *http.Request) map[string]any {
	if r.Body == nil {
		return map[string]any{}
	}
	var parsed any
	if err := json.NewDecoder(r.Body).Decode(&parsed); err != nil {
		return map[string]any{}
	}
	if m, ok := parsed.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func HandleGetTaskList(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	store := GetTaskStore()
	if !store.ListExists(raw) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":      "Task list for session " + reprString(raw) + " not found",
			"session_id": raw,
			"tasks":      []any{},
		})
		return
	}

	if swept, err := store.SweepIdleTasks(raw); err == nil {
		for _, record := range swept {
			EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
		}
	}

	meta := store.GetMeta(raw)
	role := "session"
	if meta != nil && meta.Role != "" {
		role = meta.Role
	}
	records := store.ListTasks(raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"role":       role,
		"meta":       meta,
		"tasks":      serializeTasks(records),
	})
}

func jsonIntList(v any) ([]int, bool) {
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

func HandleUnarchiveTaskList(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	body := readJSONObjectBody(r)
	taskIDs, ok := jsonIntList(body["task_ids"])
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":      "task_ids must be a list of integers",
			"session_id": raw,
		})
		return
	}

	store := GetTaskStore()
	restored, _ := store.UnarchiveTasks(raw, taskIDs)
	if restored == nil {
		restored = []int{}
	}
	if len(restored) > 0 {
		ids := make(map[int]struct{}, len(restored))
		for _, id := range restored {
			ids[id] = struct{}{}
		}
		for _, record := range store.ListTasks(raw) {
			if _, hit := ids[record.ID]; hit {
				EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"restored":   restored,
	})
}

func HandleClearCompletedTasks(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("session_id")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}

	store := GetTaskStore()
	archived, _ := store.ArchiveCompletedTasks(raw)
	ids := make([]int, 0, len(archived))
	for _, record := range archived {
		EmitTaskUpdated(raw, record, []string{"status"}, "", nil)
		ids = append(ids, record.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": raw,
		"archived":   ids,
	})
}

func RegisterTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{session_id}/tasks", HandleGetTaskList)
	mux.HandleFunc("POST /api/sessions/{session_id}/tasks/unarchive", HandleUnarchiveTaskList)
	mux.HandleFunc("POST /api/sessions/{session_id}/tasks/clear-completed", HandleClearCompletedTasks)
}
