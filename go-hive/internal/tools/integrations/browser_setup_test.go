package integrations

// browser_setup guards: the discovery tool must stay static (no network, no
// subprocess), return the upstream envelope, and record itself under the
// `browser` group so the browser categories can see it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

func browserSetupEnvelope(t *testing.T, registry *tools.ToolRegistry) (map[string]any, bool) {
	t.Helper()
	result, err := registry.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name:  BrowserSetupToolName,
		Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("content %q: %v", result.Content, err)
	}
	return payload, result.IsError
}

// Parity-exempt: browser_tools.py is a static discovery payload upstream ships
// without a dedicated test; the port pins the envelope it must keep verbatim.
func TestBrowserSetupReturnsDiscoveryEnvelope(t *testing.T) {
	probe := tools.NewToolRegistry()
	if count := InstallBrowserSetup(probe); count != 1 {
		t.Fatalf("InstallBrowserSetup registered %d tools, want 1", count)
	}

	payload, isError := browserSetupEnvelope(t, probe)
	if isError {
		t.Fatalf("browser_setup reported an error: %v", payload)
	}
	if payload["ok"] != true || payload["cli"] != "hive-browser" {
		t.Fatalf("cli/ok = %v/%v", payload["cli"], payload["ok"])
	}
	how, _ := payload["how"].(string)
	if !strings.Contains(how, "terminal_exec") || !strings.Contains(how, "--json") {
		t.Fatalf("how does not teach the terminal workflow: %q", how)
	}
	firstStep, _ := payload["first_step"].(string)
	if !strings.Contains(firstStep, "hive-browser setup --json") {
		t.Fatalf("first_step = %q", firstStep)
	}
	coordinates, _ := payload["coordinates"].(string)
	if !strings.Contains(coordinates, "FRACTIONS") || !strings.Contains(coordinates, "0..1") {
		t.Fatalf("coordinates = %q; coordinates must be taught as 0..1 fractions", coordinates)
	}
	exitCodes, _ := payload["exit_codes"].(string)
	for _, code := range []string{"0 ok", "2 not_connected", "3 not_started", "4 not_found", "6 bad_args", "7 pending_dialog", "8 rate_limited"} {
		if !strings.Contains(exitCodes, code) {
			t.Fatalf("exit_codes misses %q: %q", code, exitCodes)
		}
	}
	screenshots, _ := payload["screenshots"].(string)
	if !strings.Contains(screenshots, "saved_to") || !strings.Contains(screenshots, "attach_file") {
		t.Fatalf("screenshots guidance = %q", screenshots)
	}

	commands, _ := payload["commands"].(map[string]any)
	for _, name := range []string{
		"setup", "status", "open <url>", "navigate <url>", "reload",
		"interact --action <a>", "select <sel> --value V", "upload <sel> --file P",
		"screenshot --intent <p>", "evaluate --js <src|@file|->",
		"script --skill S --script F.py --args '{...}'", "tab list|close|activate",
		"page html|snapshot|text|shadow-query|console|resize", "dialog respond accept|dismiss",
	} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("command surface misses %q: %v", name, commands)
		}
	}
	if len(commands) != 14 {
		t.Fatalf("command surface has %d entries, want 14", len(commands))
	}

	// Static: a second call returns the same envelope (nothing is discovered or
	// dialled at call time).
	again, _ := browserSetupEnvelope(t, probe)
	if again["first_step"] != payload["first_step"] || len(again["commands"].(map[string]any)) != len(commands) {
		t.Fatalf("envelope changed between calls: %v / %v", again, payload)
	}
}

// Parity-exempt: the browser group record mirrors upstream
// registry.record_builtin_tools("browser", ...); the harness-group gate has no
// browser member.
func TestBrowserSetupRecordsBrowserGroup(t *testing.T) {
	probe := tools.NewToolRegistry()
	if count := InstallBrowserSetup(probe); count != 1 {
		t.Fatalf("install count = %d", count)
	}
	names := probe.GetServerToolNames("browser")
	if _, ok := names[BrowserSetupToolName]; !ok {
		t.Fatalf("browser group = %v, want %s", names, BrowserSetupToolName)
	}
	entries := probe.GetFullMCPCatalog()["browser"]
	if len(entries) != 1 {
		t.Fatalf("browser catalog = %v", entries)
	}
	schema, _ := entries[0]["input_schema"].(map[string]any)
	if schema == nil || schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("browser_setup input_schema = %#v", entries[0]["input_schema"])
	}
}

// Parity-exempt: assembly guard. Re-installing must not duplicate the tool or
// reset an already-recorded group.
func TestInstallBrowserSetupIsIdempotent(t *testing.T) {
	probe := tools.NewToolRegistry()
	if count := InstallBrowserSetup(probe); count != 1 {
		t.Fatalf("first install = %d, want 1", count)
	}
	if count := InstallBrowserSetup(probe); count != 0 {
		t.Fatalf("second install = %d, want 0", count)
	}
	if len(probe.GetFullMCPCatalog()["browser"]) != 1 {
		t.Fatalf("browser catalog duplicated: %v", probe.GetFullMCPCatalog()["browser"])
	}
	if len(probe.RegisteredNames()) != 1 {
		t.Fatalf("registered names = %v", probe.RegisteredNames())
	}
}
