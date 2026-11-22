package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// Parity: core/tests/test_tool_registry.py::test_session_cwd_is_context_param_and_stripped_from_schema
func TestSessionToolDefsHideContextParams(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(""), hivehome.Home{Root: root})

	// Model-visible builtin schemas: the four file tools plus the terminal
	// family. Other integration tools are registered with their upstream raw
	// schemas and are outside this strip's contract.
	inScope := map[string]struct{}{
		builtin.ToolReadFile:          {},
		builtin.ToolWriteFile:         {},
		builtin.ToolEditFile:          {},
		builtin.ToolSearchFiles:       {},
		builtin.ToolTerminalExec:      {},
		builtin.ToolTerminalGlob:      {},
		builtin.ToolTerminalRG:        {},
		builtin.ToolTerminalJobStart:  {},
		builtin.ToolTerminalJobLogs:   {},
		builtin.ToolTerminalJobManage: {},
		builtin.ToolTerminalPTYOpen:   {},
		builtin.ToolTerminalPTYRun:    {},
		builtin.ToolTerminalPTYClose:  {},
		builtin.ToolTerminalOutputGet: {},
	}
	checked := 0
	for _, def := range catalogue.Defs() {
		if _, ok := inScope[def.Name]; !ok {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			continue
		}
		checked++
		for name := range tools.ContextParams {
			if _, leaked := properties[name]; leaked {
				t.Fatalf("%s model-visible schema leaks context param %q", def.Name, name)
			}
		}
		if required, ok := def.Parameters["required"].([]string); ok {
			for _, name := range required {
				if _, leaked := tools.ContextParams[name]; leaked {
					t.Fatalf("%s required list leaks context param %q", def.Name, name)
				}
			}
		}
	}
	if checked != len(inScope) {
		t.Fatalf("inspected %d builtin schemas, want %d", checked, len(inScope))
	}
	for _, name := range []string{builtin.ToolTerminalExec, builtin.ToolTerminalGlob, builtin.ToolTerminalRG, builtin.ToolTerminalJobStart, builtin.ToolTerminalPTYOpen} {
		def := findToolDef(t, catalogue, name)
		properties := def.Parameters["properties"].(map[string]any)
		if _, leaked := properties["session_cwd"]; leaked {
			t.Fatalf("%s leaks session_cwd", name)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_session_cwd_used_when_cwd_omitted
func TestSessionTerminalExecUsesInjectedSessionCwd(t *testing.T) {
	root := testTempDir(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// The chat loader builds its toolbox with an empty root; the session
	// directory only reaches the tool through the injected context param.
	catalogue := newSessionToolCatalogue(root, builtin.New(""), hivehome.Home{Root: root})

	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolTerminalExec, map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if isError {
		t.Fatalf("terminal_exec failed: %s", content)
	}
	stdout := contextStdout(content)
	if !strings.Contains(stdout, realRoot) {
		t.Fatalf("stdout = %q, want the injected session workdir %s", stdout, realRoot)
	}

	// Framework values are authoritative: an LLM-supplied session_cwd (a
	// stale prompt, a replayed transcript) cannot redirect the call.
	content, isError, err = catalogue.Execute(context.Background(), builtin.ToolTerminalExec, map[string]any{
		"command":     "pwd",
		"session_cwd": string(filepath.Separator) + "tmp",
	})
	if err != nil {
		t.Fatalf("Execute with forged context: %v", err)
	}
	if isError || !strings.Contains(contextStdout(content), realRoot) {
		t.Fatalf("forged session_cwd was not overridden: %s", content)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_by_name
func TestSessionTerminalGlobUsesInjectedSessionCwd(t *testing.T) {
	root := testTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "injected.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(""), hivehome.Home{Root: root})
	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolTerminalGlob, map[string]any{
		"pattern": "*.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if isError || !strings.Contains(content, "injected.txt") {
		t.Fatalf("terminal_glob = %q, want injected.txt from the session workdir", content)
	}
}

func findToolDef(t *testing.T, catalogue *sessionToolCatalogue, name string) llm.ToolDef {
	t.Helper()
	for _, candidate := range catalogue.Defs() {
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not in catalogue", name)
	return llm.ToolDef{}
}

// contextStdout returns the stdout field of a builtin terminal_exec envelope
// (a JSON object since the output-shape alignment).
func contextStdout(content string) string {
	var payload struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	return payload.Stdout
}
