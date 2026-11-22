package server

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
)

// Parity: tests/test_adaptive_tool_budget.py::test_workers_route_serializes_budget_telemetry
func TestWorkerInfoToDictSerializesBudgetTelemetry(t *testing.T) {
	info := WorkerInfoView{
		ID:     "w1",
		Task:   "t",
		Status: colony.WorkerCompleted,
		Result: &WorkerResultView{Status: "success", ToolCallsUsed: 12, BudgetLimited: true},
	}
	result := WorkerInfoToDict(info)["result"].(map[string]any)
	if result["tool_calls_used"] != 12 {
		t.Errorf("tool_calls_used = %v, want 12", result["tool_calls_used"])
	}
	if result["budget_limited"] != true {
		t.Errorf("budget_limited = %v, want true", result["budget_limited"])
	}

	legacy := WorkerInfoView{
		ID:     "w2",
		Task:   "t",
		Status: colony.WorkerCompleted,
		Result: &WorkerResultView{Status: "success", TokensUsed: 1, DurationSeconds: 1.0},
	}
	result = WorkerInfoToDict(legacy)["result"].(map[string]any)
	if result["tool_calls_used"] != 0 {
		t.Errorf("legacy tool_calls_used = %v, want 0", result["tool_calls_used"])
	}
	if result["budget_limited"] != false {
		t.Errorf("legacy budget_limited = %v, want false", result["budget_limited"])
	}
}
