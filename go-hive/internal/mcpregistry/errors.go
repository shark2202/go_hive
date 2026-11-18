package mcpregistry

import "fmt"

const (
	CodeInstallFailed    = "MCP_INSTALL_FAILED"
	CodeAuthMissing      = "MCP_AUTH_MISSING"
	CodeConnectTimeout   = "MCP_CONNECT_TIMEOUT"
	CodeToolNotFound     = "MCP_TOOL_NOT_FOUND"
	CodeProtocolMismatch = "MCP_PROTOCOL_MISMATCH"
	CodeVersionConflict  = "MCP_VERSION_CONFLICT"
	CodeHealthFailed     = "MCP_HEALTH_FAILED"
)

type Error struct {
	Code string
	What string
	Why  string
	Fix  string
}

func (e *Error) Error() string {
	return "[" + e.Code + "]\nWhat failed: " + e.What + "\nWhy: " + e.Why + "\nFix: " + e.Fix
}

func installError(server, why, fix string) *Error {
	return &Error{
		Code: CodeInstallFailed,
		What: fmt.Sprintf("Could not install MCP server '%s'", server),
		Why:  why,
		Fix:  fix,
	}
}
