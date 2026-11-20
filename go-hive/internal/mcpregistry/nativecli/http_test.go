package nativecli

import (
	"strings"
	"testing"
)

// Parity-exempt: native CLI transport test; HTTP parity against the reference binary
// lives in TestParityWithReferenceHTTP.
func TestNativeCLIStreamableHTTPListsAndCalls(t *testing.T) {
	server, state := newFakeStreamableServer(t, false)

	list := runCLI(t, []string{"--mcp", server.URL, "--list"}, "", nil)
	if list.code != 0 {
		t.Fatalf("list code = %d, stderr = %s", list.code, list.stderr)
	}
	if !strings.Contains(list.stdout, "echo") || !strings.Contains(list.stdout, "tag-list") {
		t.Fatalf("list stdout = %q", list.stdout)
	}
	if got := state.headerValue("header:Mcp-Session-Id"); got != "fixture-session" {
		t.Fatalf("session id echoed = %q, want fixture-session", got)
	}

	call := runCLI(t, []string{"--mcp", server.URL, "add", "--a", "2", "--b", "3.5"}, "", nil)
	if call.code != 0 {
		t.Fatalf("call code = %d, stderr = %s", call.code, call.stderr)
	}
	if call.stdout != "5.5\n" {
		t.Fatalf("call stdout = %q", call.stdout)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLIStreamableHTTPAcceptsSSEResponses(t *testing.T) {
	server, _ := newFakeStreamableServer(t, true)
	result := runCLI(t, []string{"--mcp", server.URL, "--list", "--compact"}, "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "echo") {
		t.Fatalf("stdout = %q", result.stdout)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLISSEListAndCall(t *testing.T) {
	server := newFakeSSEServer(t)

	list := runCLI(t, []string{"--mcp", server.url(), "--list", "--compact"}, "", nil)
	if list.code != 0 {
		t.Fatalf("list code = %d, stderr = %s", list.code, list.stderr)
	}
	if !strings.Contains(list.stdout, "add") {
		t.Fatalf("list stdout = %q", list.stdout)
	}

	call := runCLI(t, []string{"--mcp", server.url(), "--transport", "sse", "echo", "--text", "over sse"}, "", nil)
	if call.code != 0 {
		t.Fatalf("call code = %d, stderr = %s", call.code, call.stderr)
	}
	if call.stdout != "over sse\n" {
		t.Fatalf("call stdout = %q", call.stdout)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLIAutoTransportFallsBackToSSE(t *testing.T) {
	server := newFakeSSEServer(t)
	result := runCLI(t, []string{"--mcp", server.url(), "echo", "--text", "auto"}, "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if result.stdout != "auto\n" {
		t.Fatalf("stdout = %q", result.stdout)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLIForwardsAuthHeaders(t *testing.T) {
	server, state := newFakeStreamableServer(t, false)
	result := runCLI(t, []string{
		"--mcp", server.URL,
		"--auth-header", "Authorization:env:HIVE_TEST_TOKEN",
		"--auth-header", "X-Fixture:plain-value",
		"--list", "--compact",
	}, "", map[string]string{"HIVE_TEST_TOKEN": "Bearer fixture-token"})
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if got := state.headerValue("header:Authorization"); got != "Bearer fixture-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := state.headerValue("header:X-Fixture"); got != "plain-value" {
		t.Fatalf("X-Fixture = %q", got)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLISSECarriesAuthHeader(t *testing.T) {
	server := newFakeSSEServer(t)
	result := runCLI(t, []string{
		"--mcp", server.url(),
		"--transport", "sse",
		"--auth-header", "Authorization:Bearer sse-token",
		"echo", "--text", "with header",
	}, "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if got := server.state.headerValue("header:Authorization"); got != "Bearer sse-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: native CLI transport test; no upstream Python case for this fixture.
func TestNativeCLIHTTPStatusErrorIsReported(t *testing.T) {
	server, _ := newFakeStreamableServer(t, false)
	result := runCLI(t, []string{"--mcp", server.URL + "/missing", "--transport", "streamable", "--list"}, "", nil)
	if result.code != 1 {
		t.Fatalf("code = %d, stdout = %q", result.code, result.stdout)
	}
	if !strings.Contains(result.stderr, "Error: cannot use MCP server at") {
		t.Fatalf("stderr = %q", result.stderr)
	}
}
