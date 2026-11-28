package cli

import "testing"

// TestGuardMainAlias pins the entrypoint symbol the three cmd/hive* binaries
// call: the crash presentation itself lives in internal/cli/cliguard, and the
// root alias must keep forwarding normal exit codes untouched.
// Parity-exempt: alias smoke test; crash behavior is covered in cliguard.
func TestGuardMainAlias(t *testing.T) {
	if got := GuardMain(func() int { return 7 }); got != 7 {
		t.Fatalf("guard altered a normal exit code: %d", got)
	}
}
