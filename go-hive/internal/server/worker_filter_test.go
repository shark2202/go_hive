package server

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

func sseStream(id string) *string { return &id }

// Parity: core/tests/test_sse_worker_filter.py::test_queen_stream_events_pass_through
func TestSSEQueenStreamEventsPassThrough(t *testing.T) {
	cases := []struct {
		stream  *string
		evtType eventbus.EventType
	}{
		{sseStream("queen"), eventbus.EventLLMTextDelta},
		{sseStream("queen"), eventbus.EventToolCallStarted},
		{sseStream("overseer"), eventbus.EventLLMTextDelta},
		{sseStream(""), eventbus.EventLLMTextDelta},
		{nil, eventbus.EventLLMTextDelta},
	}
	for _, tc := range cases {
		if IsWorkerNoise(tc.stream, tc.evtType) {
			t.Errorf("stream %v event %s must pass through", tc.stream, tc.evtType)
		}
	}
}

// Parity: core/tests/test_sse_worker_filter.py::test_worker_llm_and_tool_events_are_filtered
func TestSSEWorkerLLMAndToolEventsAreFiltered(t *testing.T) {
	cases := []struct {
		stream  string
		evtType eventbus.EventType
	}{
		{"worker:abc123", eventbus.EventLLMTextDelta},
		{"worker:abc123", eventbus.EventToolCallStarted},
		{"worker:xyz", eventbus.EventToolCallCompleted},
		{"worker:xyz", eventbus.EventNodeLoopIteration},
	}
	for _, tc := range cases {
		if !IsWorkerNoise(sseStream(tc.stream), tc.evtType) {
			t.Errorf("stream %s event %s must be filtered", tc.stream, tc.evtType)
		}
	}
}

// Parity: core/tests/test_sse_worker_filter.py::test_singular_worker_chatter_still_passes
func TestSSESingularWorkerChatterStillPasses(t *testing.T) {
	cases := []struct {
		evtType eventbus.EventType
	}{
		{eventbus.EventLLMTextDelta},
		{eventbus.EventToolCallStarted},
	}
	for _, tc := range cases {
		if IsWorkerNoise(sseStream("worker"), tc.evtType) {
			t.Errorf("bare worker event %s must pass", tc.evtType)
		}
	}
}

// Parity: core/tests/test_sse_worker_filter.py::test_worker_lifecycle_and_report_events_pass_through
func TestSSEWorkerLifecycleAndReportEventsPassThrough(t *testing.T) {
	allowed := []eventbus.EventType{
		eventbus.EventSubagentReport,
		eventbus.EventExecutionCompleted,
		eventbus.EventExecutionFailed,
	}
	for _, stream := range []string{"worker:abc", "worker"} {
		for _, evtType := range allowed {
			if IsWorkerNoise(sseStream(stream), evtType) {
				t.Errorf("stream %s lifecycle event %s must pass", stream, evtType)
			}
		}
	}
}

// Parity: core/tests/test_sse_worker_filter.py::test_handler_module_exposes_allowlist_constant
func TestSSEHandlerExposesAllowlistConstant(t *testing.T) {
	for _, required := range []string{
		eventbus.EventSubagentReport.String(),
		eventbus.EventExecutionCompleted.String(),
		eventbus.EventExecutionFailed.String(),
	} {
		if _, ok := WorkerEventAllowlist[required]; !ok {
			t.Errorf("WorkerEventAllowlist missing %q", required)
		}
	}
}
