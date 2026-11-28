package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// Parity-exempt: upstream's store silently returns a stale token when no
// provider can refresh it; hive-cred must surface reauth_required instead of
// printing the expired value. Existing CLI tests cover the provider-failure
// path with a registered provider.
func TestTokenUnrefreshableOAuthIsReauthExit(t *testing.T) {
	// Expired OAuth2 credential, auto-refresh enabled, but the store has no
	// provider registered — the local-only production boundary.
	store := credentials.NewCredentialStore(
		credentials.WithStorage(newAliasStorage(googleCred("email3", -5*time.Minute))),
		credentials.WithAutoRefresh(true),
	)
	useCredAdapter(t, &fakeCredAdapter{store: store})
	useLocalRegistry(t, emptyLocalRegistry())

	code, stdout, _ := runCred(t, "token", "google", "--account", "email3", "--json")
	if code != tracker.ExitDomain {
		t.Fatalf("Run(token) = %d, want %d (EXIT_REAUTH)", code, tracker.ExitDomain)
	}
	body := decodeCredJSON(t, stdout)
	if body["ok"] != false || credErrorBody(t, body)["code"] != "reauth_required" {
		t.Fatalf("body = %#v, want ok=false and error.code=reauth_required", body)
	}
	if strings.Contains(stdout, "ya29") {
		t.Fatalf("stdout = %q, must not leak the stale token", stdout)
	}
}

// Parity: tests/test_hive_cred_cli.py::test_console_script_registered
// hive-cred exposes token/browse/inspect only because upstream's CLI exposes
// the same three leaves; there is no login/authorize subcommand to port.
func TestCredHelpExposesNoLoginOrAuthorize(t *testing.T) {
	code, stdout, stderr := runCred(t, "--help")
	if code != tracker.ExitOK {
		t.Fatalf("Run(--help) = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range []string{"token", "browse", "inspect"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help output missing %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"login", "authorize"} {
		if strings.Contains(stdout, absent) {
			t.Fatalf("help output advertises unsupported %q:\n%s", absent, stdout)
		}
	}

	code, _, stderr = runCred(t, "login")
	if code != tracker.ExitValidation {
		t.Fatalf("Run(login) = %d, want %d (validation)", code, tracker.ExitValidation)
	}
	if !strings.Contains(stderr, "invalid") && !strings.Contains(stderr, "unrecognized") {
		t.Fatalf("stderr = %q, want an argparse-style rejection", stderr)
	}
}
