package agentloop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_loopconfig_prefix_timeout_resolution
func TestLoopConfigPrefixTimeoutResolution(t *testing.T) {
	loop := NewAgentLoop(DefaultLoopConfig(), nil)
	for name, want := range map[string]float64{
		"browser_evaluate": 180.0,
		"browser_status":   180.0,
		"terminal_exec":    60.0,
		"tracker_query":    60.0,
	} {
		if got := loop.ResolveToolTimeout(name); got != want {
			t.Fatalf("ResolveToolTimeout(%q) = %v, want %v", name, got, want)
		}
	}

	cfg := DefaultLoopConfig()
	cfg.ToolTimeoutOverrides = map[string]float64{"browser_": 180.0, "browser_evaluate": 300.0}
	loop = NewAgentLoop(cfg, nil)
	if got := loop.ResolveToolTimeout("browser_evaluate"); got != 300.0 {
		t.Fatalf("longest prefix: got %v, want 300", got)
	}
	if got := loop.ResolveToolTimeout("browser_open"); got != 180.0 {
		t.Fatalf("shorter prefix: got %v, want 180", got)
	}

	cfg.ToolTimeoutOverrides = map[string]float64{}
	loop = NewAgentLoop(cfg, nil)
	if got := loop.ResolveToolTimeout("browser_evaluate"); got != 60.0 {
		t.Fatalf("empty overrides: got %v, want 60", got)
	}

	cfg = DefaultLoopConfig()
	ceiling := tools.MCPCallResultTimeout().Seconds()
	if !(cfg.ToolCallTimeoutSeconds < ceiling) {
		t.Fatalf("tool_call_timeout_seconds %v >= MCP ceiling %v", cfg.ToolCallTimeoutSeconds, ceiling)
	}
	for prefix, value := range cfg.ToolTimeoutOverrides {
		if !(value < ceiling) {
			t.Fatalf("override %q = %v >= MCP ceiling %v", prefix, value, ceiling)
		}
	}
}

// Parity-exempt: 该用例验证 Go 侧 callExecutor 确实执行了已解析的预算（超时即放弃并
func TestToolCallTimeoutAbandonsHungCall(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	cfg := DefaultLoopConfig()
	cfg.ToolCallTimeoutSeconds = 0.05
	cfg.ToolTimeoutOverrides = map[string]float64{}

	loop := NewAgentLoop(cfg, func(req ToolCallRequest) (ToolResult, error) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return ToolResult{Content: "late"}, nil
	})

	start := time.Now()
	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "slow_tool", Input: map[string]any{}})
	elapsed := time.Since(start)

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if !strings.Contains(result.Content, "timed out after") {
		t.Fatalf("timeout message missing telemetry needle: %q", result.Content)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("budget was not enforced: call took %v", elapsed)
	}
	if got := loop.ResolveToolTimeout("slow_tool"); got != 0.05 {
		t.Fatalf("ResolveToolTimeout = %v, want 0.05", got)
	}
}
