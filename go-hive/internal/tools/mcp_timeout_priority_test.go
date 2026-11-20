package tools

import (
	"testing"
	"time"
)

// TestMCPCallResultTimeoutPrecedence pins the resolution order for the MCP
// tools/call timeout: env > configuration.json mcp.timeout_seconds > built-in
// 240s. The file value used to win outright, which made the documented
// environment variable a no-op whenever the config pinned a timeout.
//
// Parity-exempt: the Python client reads HIVE_MCP_CALL_RESULT_TIMEOUT once at
// import from a module-level literal and has no configuration.json reader, so
// there is no upstream test for this precedence. The Go port carries both
// sources and must keep the general env > file > default rule.
func TestMCPCallResultTimeoutPrecedence(t *testing.T) {
	previous := mcpCallResultTimeoutOverride.Load()
	t.Cleanup(func() { mcpCallResultTimeoutOverride.Store(previous) })

	cases := []struct {
		name string
		env  string
		file time.Duration
		want time.Duration
	}{
		{name: "default only", env: "", file: 0, want: 240 * time.Second},
		{name: "file over default", env: "", file: 90 * time.Second, want: 90 * time.Second},
		{name: "env over file", env: "12", file: 90 * time.Second, want: 12 * time.Second},
		{name: "env over default", env: "12", file: 0, want: 12 * time.Second},
		{name: "invalid env falls through to file", env: "abc", file: 90 * time.Second, want: 90 * time.Second},
		{name: "non-positive env falls through to file", env: "0", file: 90 * time.Second, want: 90 * time.Second},
		{name: "whitespace env is unset", env: "   ", file: 33 * time.Second, want: 33 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HIVE_MCP_CALL_RESULT_TIMEOUT", tc.env)
			mcpCallResultTimeoutOverride.Store(0)
			SetMCPCallResultTimeout(tc.file)
			if got := MCPCallResultTimeout(); got != tc.want {
				t.Fatalf("MCPCallResultTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}
