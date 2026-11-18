package server

// Parity: framework/server/routes_workers.py::handle_live_workers_stream
// Parity: framework/server/routes_workers.py::handle_stop_live_worker
// Parity: framework/server/routes_workers.py::handle_stop_all_live_workers
// Parity: framework/server/routes_workers.py::handle_worker_reap_timeline
//
// routes_workers.py also registers three process-global browser-registry
// routes (/api/browser/contexts, /api/browser/tabs, /api/browser/contexts/
// close). They are outside this lane's worker(4) slice and are not added
// here. The GET /workers list route lives in routes_colony_workers.py and is
// deliberately absent upstream too (a duplicate shadowed it).

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

const defaultLiveWorkersPoll = 2 * time.Second

// LiveWorkerSnapshot is the worker-registry row the stream/stop routes read.
// StartedAt is a monotonic-seconds value, matching upstream's use of
// time.monotonic(); zero means "never started" (duration 0.0).
type LiveWorkerSnapshot struct {
	ID              string
	Task            string
	Status          string
	IsActive        bool
	StartedAt       float64
	ExplicitReport  map[string]any
	ResultStatus    *string
	ResultSummary   *string
	ReportPublished *float64
	DoneCallback    *float64
	ReapScheduled   *float64
	ReapCompleted   *float64
	ReapResult      any
}

// LiveWorkerRuntime is the seam over a session's ColonyRuntime worker
// registry. Python reads colony._workers, calls stop_worker/stop_workers.
type LiveWorkerRuntime interface {
	LiveWorkers() []LiveWorkerSnapshot
	LiveWorker(workerID string) (LiveWorkerSnapshot, bool)
	StopLiveWorker(workerID string) (map[string]any, error)
	StopAllLiveWorkers() map[string]any
}

type WorkerRouteDeps struct {
	Manager *SessionManager
	// Runtime resolves the live worker registry for a session. Nil falls
	// back to a session whose Colony implements LiveWorkerRuntime.
	Runtime func(*Session) LiveWorkerRuntime
	// Now supplies monotonic seconds for duration_seconds. Nil uses
	// time.Now().UnixNano()/1e9.
	Now func() float64
	// PollInterval is the SSE registry poll cadence. Zero uses 2s.
	PollInterval time.Duration
}

func (deps WorkerRouteDeps) runtime(session *Session) LiveWorkerRuntime {
	if deps.Runtime != nil {
		return deps.Runtime(session)
	}
	if session == nil || session.Colony == nil {
		return nil
	}
	runtime, _ := session.Colony.(LiveWorkerRuntime)
	return runtime
}

func (deps WorkerRouteDeps) now() float64 {
	if deps.Now != nil {
		return deps.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (deps WorkerRouteDeps) pollInterval() time.Duration {
	if deps.PollInterval > 0 {
		return deps.PollInterval
	}
	return defaultLiveWorkersPoll
}

func RegisterWorkerRoutes(mux *http.ServeMux, deps WorkerRouteDeps) {
	mux.HandleFunc("GET /api/sessions/{session_id}/workers/stream", func(w http.ResponseWriter, r *http.Request) {
		handleLiveWorkersStream(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/workers/stop-all", func(w http.ResponseWriter, r *http.Request) {
		handleStopAllLiveWorkers(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/workers/{worker_id}/stop", func(w http.ResponseWriter, r *http.Request) {
		handleStopLiveWorker(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/workers/{worker_id}/reap-timeline", func(w http.ResponseWriter, r *http.Request) {
		handleWorkerReapTimeline(w, r, deps)
	})
}

// Parity: framework/server/routes_workers.py::_build_live_workers_payload
func buildLiveWorkersPayload(runtime LiveWorkerRuntime, now float64) []map[string]any {
	payload := []map[string]any{}
	if runtime == nil {
		return payload
	}
	workers := runtime.LiveWorkers()
	sort.SliceStable(workers, func(i, j int) bool {
		left, right := workers[i], workers[j]
		if left.IsActive != right.IsActive {
			return left.IsActive
		}
		return liveWorkerDuration(left, now) > liveWorkerDuration(right, now)
	})
	for _, worker := range workers {
		var resultStatus any
		if worker.ResultStatus != nil {
			resultStatus = *worker.ResultStatus
		}
		var resultSummary any
		if worker.ResultSummary != nil {
			resultSummary = *worker.ResultSummary
		}
		var explicitReport any
		if worker.ExplicitReport != nil {
			explicitReport = worker.ExplicitReport
		}
		payload = append(payload, map[string]any{
			"worker_id":        worker.ID,
			"task":             truncateWorkerTaskRunes(worker.Task, 400),
			"status":           worker.Status,
			"is_active":        worker.IsActive,
			"duration_seconds": liveWorkerDuration(worker, now),
			"explicit_report":  explicitReport,
			"result_status":    resultStatus,
			"result_summary":   resultSummary,
		})
	}
	return payload
}

func liveWorkerDuration(worker LiveWorkerSnapshot, now float64) float64 {
	if worker.StartedAt == 0 {
		return 0
	}
	return math.RoundToEven((now-worker.StartedAt)*10) / 10
}

// paritySignature mirrors routes_workers.py::_payload_change_signature. It
// intentionally excludes duration_seconds, which ticks every poll.
func liveWorkersSignature(payload []map[string]any) string {
	var builder strings.Builder
	for _, worker := range payload {
		fmt.Fprintf(&builder, "%v\x00%v\x00%v\x00%v\x00%v\x00%v\x00",
			worker["worker_id"],
			worker["status"],
			worker["is_active"],
			worker["result_status"],
			worker["result_summary"],
			pyTruthy(worker["explicit_report"]),
		)
	}
	return builder.String()
}

func truncateWorkerTaskRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// Parity: framework/server/routes_workers.py::handle_live_workers_stream
func handleLiveWorkersStream(w http.ResponseWriter, r *http.Request, deps WorkerRouteDeps) {
	session := workerSession(deps, r.PathValue("session_id"))
	if session == nil {
		sessionNotFound(w, r.PathValue("session_id"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lastSignature := ""
	hasSignature := false
	for {
		runtime := deps.runtime(session)
		payload := buildLiveWorkersPayload(runtime, deps.now())
		signature := liveWorkersSignature(payload)
		if !hasSignature || signature != lastSignature {
			writeNamedSSEEvent(w, flusher, "snapshot", map[string]any{"workers": payload})
			lastSignature = signature
			hasSignature = true
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(deps.pollInterval()):
		}
		// Refresh the session pointer in case the colony was loaded/unloaded
		// while the stream was open.
		if refreshed := workerSession(deps, session.ID); refreshed != nil {
			session = refreshed
		}
	}
}

// Parity: framework/server/routes_workers.py::handle_stop_live_worker
func handleStopLiveWorker(w http.ResponseWriter, r *http.Request, deps WorkerRouteDeps) {
	session := workerSession(deps, r.PathValue("session_id"))
	if session == nil {
		sessionNotFound(w, r.PathValue("session_id"))
		return
	}
	workerID := r.PathValue("worker_id")
	if workerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "worker_id required"})
		return
	}
	runtime := deps.runtime(session)
	if runtime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "No active colony on this session"})
		return
	}
	worker, found := runtime.LiveWorker(workerID)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Worker '" + workerID + "' not found"})
		return
	}
	if !worker.IsActive {
		writeJSON(w, http.StatusOK, map[string]any{
			"stopped":   false,
			"reason":    "Worker already terminated",
			"worker_id": workerID,
			"status":    worker.Status,
		})
		return
	}
	summary, err := runtime.StopLiveWorker(workerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"stopped":   false,
			"error":     err.Error(),
			"worker_id": workerID,
		})
		return
	}
	if summary == nil {
		summary = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stopped":   true,
		"worker_id": workerID,
		"detail":    summary,
	})
}

// Parity: framework/server/routes_workers.py::handle_stop_all_live_workers
func handleStopAllLiveWorkers(w http.ResponseWriter, r *http.Request, deps WorkerRouteDeps) {
	session := workerSession(deps, r.PathValue("session_id"))
	if session == nil {
		sessionNotFound(w, r.PathValue("session_id"))
		return
	}
	runtime := deps.runtime(session)
	if runtime == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"stopped": []any{},
			"error":   "No active colony on this session",
		})
		return
	}
	summary := runtime.StopAllLiveWorkers()
	if summary == nil {
		summary = map[string]any{}
	}
	writeJSON(w, http.StatusOK, summary)
}

// Parity: framework/server/routes_workers.py::handle_worker_reap_timeline
func handleWorkerReapTimeline(w http.ResponseWriter, r *http.Request, deps WorkerRouteDeps) {
	session := workerSession(deps, r.PathValue("session_id"))
	if session == nil {
		sessionNotFound(w, r.PathValue("session_id"))
		return
	}
	workerID := r.PathValue("worker_id")
	if workerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "worker_id required"})
		return
	}
	runtime := deps.runtime(session)
	if runtime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "No active colony on this session"})
		return
	}
	worker, found := runtime.LiveWorker(workerID)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Worker '" + workerID + "' not found"})
		return
	}

	orderingOK := true
	pairs := [][2]*float64{
		{worker.ReportPublished, worker.DoneCallback},
		{worker.DoneCallback, worker.ReapScheduled},
		{worker.ReapScheduled, worker.ReapCompleted},
	}
	for _, pair := range pairs {
		if pair[0] == nil || pair[1] == nil {
			continue
		}
		if *pair[0] > *pair[1] {
			orderingOK = false
		}
	}

	var reapResult any
	if worker.ReapResult != nil {
		reapResult = worker.ReapResult
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"worker_id":    workerID,
		"status":       worker.Status,
		"profile_name": workerID,
		"timestamps": map[string]any{
			"report_published_at": worker.ReportPublished,
			"done_callback_at":    worker.DoneCallback,
			"reap_scheduled_at":   worker.ReapScheduled,
			"reap_completed_at":   worker.ReapCompleted,
		},
		"deltas_ms": map[string]any{
			"report_to_done_callback":         deltaMS(worker.ReportPublished, worker.DoneCallback),
			"done_callback_to_reap_scheduled": deltaMS(worker.DoneCallback, worker.ReapScheduled),
			"reap_scheduled_to_completed":     deltaMS(worker.ReapScheduled, worker.ReapCompleted),
			"report_to_reap_completed":        deltaMS(worker.ReportPublished, worker.ReapCompleted),
		},
		"ordering_ok": orderingOK,
		"reap_result": reapResult,
	})
}

func deltaMS(start, end *float64) any {
	if start == nil || end == nil {
		return nil
	}
	return math.Round((*end-*start)*1000*1000) / 1000
}

func workerSession(deps WorkerRouteDeps, sessionID string) *Session {
	if deps.Manager == nil {
		return nil
	}
	return deps.Manager.GetSession(sessionID)
}
