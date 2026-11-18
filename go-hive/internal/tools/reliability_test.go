package tools

// Reliability probes for the shared tool executor: giving up on a call must
// cancel the executor's context so the tool stops and its pool slot is
// released, rather than leaving a goroutine behind until some inner deadline.

import (
	"context"
	"testing"
	"time"
)

// Parity-exempt: fault-injection probe for a hung tool; timeout must cancel
// the executor and free the pool slot.
func TestExecuteToolTimeoutCancelsExecutorAndFreesPool(t *testing.T) {
	previous := SharedToolPool()
	SetToolPool(NewToolPool(1, "hive-tool-reliability"))
	defer SetToolPool(previous)

	started := make(chan struct{})
	exited := make(chan struct{})
	slow := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return ToolResult{Content: "late"}, ctx.Err()
	})

	result, err := ExecuteTool(context.Background(), slow, testToolCall("slow", nil), 150*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("timed-out tool was not flagged as an error: %+v", result)
	}
	select {
	case <-started:
	default:
		t.Fatal("executor never started")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("executor was not cancelled after the tool timeout; it is still running and holding a pool slot")
	}

	// A single-slot pool: if the timed-out call still held the semaphore, this
	// second call would time out instead of executing.
	quick := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		return NewToolResult(toolUse.ID, "ok"), nil
	})
	second, err := ExecuteTool(context.Background(), quick, testToolCall("quick", nil), time.Second, nil)
	if err != nil {
		t.Fatalf("second ExecuteTool: %v", err)
	}
	if second.IsError || second.Content != "ok" {
		t.Fatalf("second call = %+v; the timed-out call leaked its pool slot", second)
	}
}
