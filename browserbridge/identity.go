package browserbridge

import (
	"os"
	"strings"
)

// identity.go ports the transportable half of tools/src/gcu/browser/identity.py.
// A Go bridge process has no per-agent contextvar, so the acting session id and
// display label arrive as explicit command params; these env fallbacks match the
// CLI/runtime hand-down used upstream when no explicit value is present.
const (
	SessionEnv     = "HIVE_BROWSER_SESSION"
	DisplayNameEnv = "HIVE_BROWSER_PROFILE_DISPLAY_NAME"
)

// ResolveSession returns the explicit session id, else HIVE_BROWSER_SESSION.
func ResolveSession(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	return strings.TrimSpace(os.Getenv(SessionEnv))
}

// ResolveDisplayName returns the human tab-group label from the environment.
func ResolveDisplayName() string {
	return strings.TrimSpace(os.Getenv(DisplayNameEnv))
}
