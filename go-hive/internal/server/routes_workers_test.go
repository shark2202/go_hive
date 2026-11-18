package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeLiveWorkerRuntime struct {
	workers  []LiveWorkerSnapshot
	stopped  []string
	stopData map[string]any
	stopErr  error
	allData  map[string]any
}

func (f *fakeLiveWorkerRuntime) LiveWorkers() []LiveWorkerSnapshot {
	return append([]LiveWorkerSnapshot(nil), f.workers...)
}

func (f *fakeLiveWorkerRuntime) LiveWorker(workerID string) (LiveWorkerSnapshot, bool) {
	for _, worker := range f.workers {
		if worker.ID == workerID {
			return worker, true
		}
	}
	return LiveWorkerSnapshot{}, false
}

func (f *fakeLiveWorkerRuntime) StopLiveWorker(workerID string) (map[string]any, error) {
	f.stopped = append(f.stopped, workerID)
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	if f.stopData != nil {
		return f.stopData, nil
	}
	return map[string]any{"stopped": []string{workerID}, "count": 1}, nil
}

func (f *fakeLiveWorkerRuntime) StopAllLiveWorkers() map[string]any {
	if f.allData != nil {
		return f.allData
	}
	return map[string]any{"stopped": []string{}, "count": 0}
}

func workerTestMux(manager *SessionManager, runtime LiveWorkerRuntime, now float64, poll time.Duration) *http.ServeMux {
	deps := WorkerRouteDeps{
		Manager:      manager,
		Runtime:      func(*Session) LiveWorkerRuntime { return runtime },
		Now:          func() float64 { return now },
		PollInterval: poll,
	}
	mux := http.NewServeMux()
	RegisterWorkerRoutes(mux, deps)
	return mux
}

func workerSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_a"})
	return manager
}

// Parity-exempt: routes_workers.py has no Python test for the workers SSE
// stream; this pins the immediate snapshot frame, field shape and ordering.
func TestLiveWorkersStreamEmitsSnapshot(t *testing.T) {
	runtime := &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{
		{ID: "worker_old", Task: "older active", Status: "running", IsActive: true, StartedAt: 100},
		{ID: "worker_new", Task: "newer active", Status: "queued", IsActive: true, StartedAt: 102},
		{
			ID:             "worker_done",
			Task:           "finished",
			Status:         "completed",
			IsActive:       false,
			StartedAt:      90,
			ExplicitReport: map[string]any{"status": "success"},
			ResultStatus:   stringPointerForTest("success"),
			ResultSummary:  stringPointerForTest("all done"),
		},
	}}
	server := httptest.NewServer(workerTestMux(workerSessionManager(t), runtime, 105, 10*time.Millisecond))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sessions/session_a/workers/stream", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(response.Body)
	name, data := readEventFrame(t, reader)
	if name != "snapshot" {
		t.Fatalf("event = %q, want snapshot", name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	workers, ok := payload["workers"].([]any)
	if !ok || len(workers) != 3 {
		t.Fatalf("workers = %#v", payload["workers"])
	}
	first := workers[0].(map[string]any)
	second := workers[1].(map[string]any)
	third := workers[2].(map[string]any)
	if first["worker_id"] != "worker_old" || second["worker_id"] != "worker_new" || third["worker_id"] != "worker_done" {
		t.Fatalf("order = %v, %v, %v (want active oldest-first, then terminated)", first["worker_id"], second["worker_id"], third["worker_id"])
	}
	if first["is_active"] != true || third["is_active"] != false {
		t.Fatalf("is_active = %#v / %#v", first["is_active"], third["is_active"])
	}
	if first["duration_seconds"] != 5.0 || second["duration_seconds"] != 3.0 || third["duration_seconds"] != 15.0 {
		t.Fatalf("durations = %#v / %#v / %#v", first["duration_seconds"], second["duration_seconds"], third["duration_seconds"])
	}
	if third["result_status"] != "success" || third["result_summary"] != "all done" {
		t.Fatalf("result fields = %#v", third)
	}
	if first["result_status"] != nil || first["result_summary"] != nil || first["explicit_report"] != nil {
		t.Fatalf("active worker carried result fields: %#v", first)
	}
	report, ok := third["explicit_report"].(map[string]any)
	if !ok || report["status"] != "success" {
		t.Fatalf("explicit_report = %#v", third["explicit_report"])
	}
	cancel()
}

// Parity-exempt: resolve_session's 404 contract for the worker routes.
func TestLiveWorkersStreamUnknownSession(t *testing.T) {
	mux := workerTestMux(NewSessionManager(), nil, 0, time.Millisecond)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/ghost/workers/stream", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["error"] != "Session 'ghost' not found" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: handle_stop_live_worker's four branches (no colony, unknown,
// already terminated, active) are pinned here through the HTTP mux.
func TestStopLiveWorkerRoutes(t *testing.T) {
	t.Run("no colony", func(t *testing.T) {
		mux := workerTestMux(workerSessionManager(t), nil, 0, time.Millisecond)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/w1/stop", nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", recorder.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if body["error"] != "No active colony on this session" {
			t.Fatalf("body = %#v", body)
		}
	})

	t.Run("unknown worker", func(t *testing.T) {
		mux := workerTestMux(workerSessionManager(t), &fakeLiveWorkerRuntime{}, 0, time.Millisecond)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/w9/stop", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d", recorder.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if body["error"] != "Worker 'w9' not found" {
			t.Fatalf("body = %#v", body)
		}
	})

	t.Run("already terminated", func(t *testing.T) {
		runtime := &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{{ID: "w1", Status: "completed", IsActive: false}}}
		mux := workerTestMux(workerSessionManager(t), runtime, 0, time.Millisecond)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/w1/stop", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if body["stopped"] != false || body["reason"] != "Worker already terminated" || body["status"] != "completed" {
			t.Fatalf("body = %#v", body)
		}
		if len(runtime.stopped) != 0 {
			t.Fatalf("stop_worker called for a terminated worker: %#v", runtime.stopped)
		}
	})

	t.Run("active", func(t *testing.T) {
		runtime := &fakeLiveWorkerRuntime{
			workers:  []LiveWorkerSnapshot{{ID: "w1", Status: "running", IsActive: true}},
			stopData: map[string]any{"stopped": []string{"w1"}, "count": 1},
		}
		mux := workerTestMux(workerSessionManager(t), runtime, 0, time.Millisecond)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/w1/stop", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		detail, ok := body["detail"].(map[string]any)
		if body["stopped"] != true || !ok || detail["count"] != float64(1) {
			t.Fatalf("body = %#v", body)
		}
		if !reflect.DeepEqual(runtime.stopped, []string{"w1"}) {
			t.Fatalf("stopped = %#v", runtime.stopped)
		}
	})
}

// Parity-exempt: a stop_worker exception upstream maps to 500 with the
// stringified error; pinned here.
func TestStopLiveWorkerFailure(t *testing.T) {
	runtime := &fakeLiveWorkerRuntime{
		workers: []LiveWorkerSnapshot{{ID: "w1", Status: "running", IsActive: true}},
		stopErr: errors.New("boom"),
	}
	mux := workerTestMux(workerSessionManager(t), runtime, 0, time.Millisecond)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/w1/stop", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["stopped"] != false || body["error"] != "boom" || body["worker_id"] != "w1" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: stop-all's no-colony envelope and the pass-through summary.
func TestStopAllLiveWorkersRoute(t *testing.T) {
	mux := workerTestMux(workerSessionManager(t), nil, 0, time.Millisecond)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/stop-all", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	stopped, ok := body["stopped"].([]any)
	if !ok || len(stopped) != 0 || body["error"] != "No active colony on this session" {
		t.Fatalf("body = %#v", body)
	}

	runtime := &fakeLiveWorkerRuntime{allData: map[string]any{"stopped": []string{"w1", "w2"}, "count": 2}}
	mux = workerTestMux(workerSessionManager(t), runtime, 0, time.Millisecond)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/workers/stop-all", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body = map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["count"] != float64(2) {
		t.Fatalf("summary = %#v", body)
	}
}

// Parity-exempt: reap-timeline's timestamps, deltas_ms rounding and
// ordering_ok invariant are pinned here (the endpoint exists for e2e tests).
func TestWorkerReapTimeline(t *testing.T) {
	report, done, scheduled, completed := 10.0, 10.1, 10.25, 10.3
	reapResult := map[string]any{"ok": true}
	worker := LiveWorkerSnapshot{
		ID: "w1", Status: "stopped",
		ReportPublished: &report, DoneCallback: &done, ReapScheduled: &scheduled, ReapCompleted: &completed,
		ReapResult: reapResult,
	}
	mux := workerTestMux(workerSessionManager(t), &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{worker}}, 0, time.Millisecond)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/session_a/workers/w1/reap-timeline", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["worker_id"] != "w1" || body["status"] != "stopped" || body["profile_name"] != "w1" {
		t.Fatalf("body = %#v", body)
	}
	timestamps, ok := body["timestamps"].(map[string]any)
	if !ok || timestamps["report_published_at"] != 10.0 || timestamps["reap_completed_at"] != 10.3 {
		t.Fatalf("timestamps = %#v", body["timestamps"])
	}
	deltas, ok := body["deltas_ms"].(map[string]any)
	if !ok {
		t.Fatalf("deltas = %#v", body["deltas_ms"])
	}
	if deltas["report_to_done_callback"] != 100.0 || deltas["report_to_reap_completed"] != 300.0 {
		t.Fatalf("deltas = %#v", deltas)
	}
	if body["ordering_ok"] != true {
		t.Fatalf("ordering_ok = %#v", body["ordering_ok"])
	}
	result, ok := body["reap_result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("reap_result = %#v", body["reap_result"])
	}

	// Out-of-order timestamps flip ordering_ok.
	outOfOrder := worker
	lateReport := 10.5
	outOfOrder.ReportPublished = &lateReport
	mux = workerTestMux(workerSessionManager(t), &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{outOfOrder}}, 0, time.Millisecond)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/session_a/workers/w1/reap-timeline", nil))
	body = map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["ordering_ok"] != false {
		t.Fatalf("ordering_ok = %#v, want false", body["ordering_ok"])
	}

	// Null timestamps leave deltas nil.
	bare := LiveWorkerSnapshot{ID: "w2", Status: "running"}
	mux = workerTestMux(workerSessionManager(t), &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{bare}}, 0, time.Millisecond)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/session_a/workers/w2/reap-timeline", nil))
	body = map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	deltas = body["deltas_ms"].(map[string]any)
	if deltas["report_to_done_callback"] != nil || body["ordering_ok"] != true {
		t.Fatalf("bare timeline = %#v", body)
	}
}

// Parity-exempt: build_live_workers_payload's 400-char task cap, nil result
// projection and duration rounding are pinned here.
func TestBuildLiveWorkersPayloadProjection(t *testing.T) {
	longTask := strings.Repeat("é", 401)
	status := "success"
	summary := "ok"
	runtime := &fakeLiveWorkerRuntime{workers: []LiveWorkerSnapshot{
		{ID: "w1", Task: longTask, Status: "completed", IsActive: false, StartedAt: 1.234, ResultStatus: &status, ResultSummary: &summary},
	}}
	payload := buildLiveWorkersPayload(runtime, 2.0)
	if len(payload) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
	task := payload[0]["task"].(string)
	if len([]rune(task)) != 400 {
		t.Fatalf("task runes = %d, want 400", len([]rune(task)))
	}
	if payload[0]["duration_seconds"] != 0.8 {
		t.Fatalf("duration = %#v, want 0.8", payload[0]["duration_seconds"])
	}
	if payload[0]["result_status"] != "success" || payload[0]["result_summary"] != "ok" {
		t.Fatalf("result fields = %#v", payload[0])
	}
	if empty := buildLiveWorkersPayload(nil, 0); len(empty) != 0 {
		t.Fatalf("nil runtime payload = %#v", empty)
	}
}

// Parity-exempt: _payload_change_signature must ignore duration_seconds but
// see every real state transition (status/result/report presence).
func TestLiveWorkersSignatureIgnoresDuration(t *testing.T) {
	base := []map[string]any{{
		"worker_id":        "w1",
		"status":           "running",
		"is_active":        true,
		"result_status":    nil,
		"result_summary":   nil,
		"explicit_report":  nil,
		"duration_seconds": 1.0,
	}}
	same := []map[string]any{{
		"worker_id":        "w1",
		"status":           "running",
		"is_active":        true,
		"result_status":    nil,
		"result_summary":   nil,
		"explicit_report":  map[string]any{},
		"duration_seconds": 99.0,
	}}
	if liveWorkersSignature(base) != liveWorkersSignature(same) {
		t.Fatal("signature changed on duration or empty report")
	}
	changed := []map[string]any{{
		"worker_id":        "w1",
		"status":           "completed",
		"is_active":        false,
		"result_status":    "success",
		"result_summary":   "done",
		"explicit_report":  map[string]any{"status": "success"},
		"duration_seconds": 1.0,
	}}
	if liveWorkersSignature(base) == liveWorkersSignature(changed) {
		t.Fatal("signature did not change on a status transition")
	}
}

func stringPointerForTest(value string) *string { return &value }
