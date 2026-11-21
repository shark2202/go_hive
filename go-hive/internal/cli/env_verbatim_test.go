package cli

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// Evidence for the serve log sink and the per-agent env injection: upstream's
// logging.py and agent_loader.py carry no test module for these seams, so the
// tests drive the production wiring functions.

func unsetEnvVar(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "x")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

// Parity: framework/observability/logging.py::configure_logging
func TestInstallRuntimeLogBufferWritesHiveServerLog(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	previous := log.Writer()
	previousFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(previous)
		log.SetFlags(previousFlags)
	})
	unsetEnvVar(t, "HIVE_NO_FILE_LOG")

	installRuntimeLogBuffer(home)
	log.Println("serve-log-verbatim-probe")

	raw, err := os.ReadFile(filepath.Join(home.Root, "logs", "hive-server.log"))
	if err != nil {
		t.Fatalf("hive-server.log missing after installRuntimeLogBuffer: %v", err)
	}
	if !strings.Contains(string(raw), "serve-log-verbatim-probe") {
		t.Fatalf("hive-server.log did not receive the log line: %q", raw)
	}
	// The record is stamped with upstream's human-log timestamp shape
	// ("2006-01-02 15:04:05"), not the std log "/" date.
	if !regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} serve-log-verbatim-probe$`).Match(raw) {
		t.Fatalf("hive-server.log line is not in the upstream timestamp shape: %q", raw)
	}
}

// Parity: framework/observability/logging.py::configure_logging
func TestInstallRuntimeLogBufferHonoursNoFileLog(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	previous := log.Writer()
	previousFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(previous)
		log.SetFlags(previousFlags)
	})
	t.Setenv("HIVE_NO_FILE_LOG", "1")

	installRuntimeLogBuffer(home)
	log.Println("serve-log-should-not-land")

	if _, err := os.Stat(filepath.Join(home.Root, "logs", "hive-server.log")); !os.IsNotExist(err) {
		t.Fatalf("HIVE_NO_FILE_LOG=1 still wrote the file (%v)", err)
	}
}

// Parity: framework/loader/agent_loader.py::AgentLoader
func TestColonyWorkerCatalogueInjectsTheAgentName(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	colonyPath := home.ColonyDir("researcher")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	catalogue := newSessionToolCatalogue(colonyPath, builtin.New(colonyPath), home)
	catalogue.SetAgentName(filepath.Base(colonyPath))

	// The MCP half: every server config built from this registry carries the
	// agent name (upstream's set_mcp_extra_env).
	if got := catalogue.registry.MCPExtraEnv()["HIVE_AGENT_NAME"]; got != "researcher" {
		t.Fatalf("registry MCP env HIVE_AGENT_NAME = %q, want researcher", got)
	}

	// The subprocess half: an env-declaring tool's input gets the name, and
	// the caller's map is not mutated.
	original := map[string]any{"command": "true"}
	merged := catalogue.injectContextParams(builtin.ToolTerminalExec, original)
	env, _ := merged["env"].(map[string]any)
	if env == nil || env["HIVE_AGENT_NAME"] != "researcher" {
		t.Fatalf("terminal_exec env = %#v, want HIVE_AGENT_NAME=researcher", merged["env"])
	}
	if _, leaked := original["env"]; leaked {
		t.Fatalf("caller input was mutated: %#v", original)
	}
	if merged["command"] != "true" {
		t.Fatalf("command lost in merge: %#v", merged)
	}

	// A tool that does not declare env is untouched (upstream's gate).
	other := catalogue.injectContextParams("read_file", map[string]any{"path": "x"})
	if _, present := other["env"]; present {
		t.Fatalf("read_file gained an env input: %#v", other)
	}

	// An explicit env from the caller keeps its own HIVE_AGENT_NAME.
	explicit := catalogue.injectContextParams(builtin.ToolTerminalExec, map[string]any{
		"env": map[string]any{"HIVE_AGENT_NAME": "override", "OTHER": "1"},
	})
	explicitEnv, _ := explicit["env"].(map[string]any)
	if explicitEnv["HIVE_AGENT_NAME"] != "override" || explicitEnv["OTHER"] != "1" {
		t.Fatalf("explicit env overwritten: %#v", explicitEnv)
	}
}

// Parity: core/tests/test_harness_tools.py::test_context_params_hidden_from_schema_and_injected
func TestColonyWorkerToolSurfaceRunsTerminalWithTheAgentName(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	colonyPath := home.ColonyDir("researcher")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	base, declared, execute := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	if !toolDefNamed(base, builtin.ToolTerminalExec) && !toolDefNamed(declared, builtin.ToolTerminalExec) {
		t.Fatal("worker surface has no terminal_exec to observe")
	}
	result, err := execute(agentloop.ToolCallRequest{
		ID:   "call-1",
		Name: builtin.ToolTerminalExec,
		Input: map[string]any{
			"command": "printf %s \"$HIVE_AGENT_NAME\"",
			"shell":   true,
		},
	})
	if err != nil {
		t.Fatalf("terminal_exec: %v", err)
	}
	if result.IsError {
		t.Fatalf("terminal_exec failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "researcher") {
		t.Fatalf("terminal child did not see HIVE_AGENT_NAME: %q", result.Content)
	}
}
