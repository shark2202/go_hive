package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newDebuggerRunner(t *testing.T, home string) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	runner.HomeRoot = func() (string, error) { return home, nil }
	return runner, &stdout, &stderr
}

func writeJSONLLogDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return dir
}

func writeVisualizer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm_debug_log_visualizer.py")
	if err := os.WriteFile(path, []byte("# visualizer\n"), 0o644); err != nil {
		t.Fatalf("write visualizer: %v", err)
	}
	return path
}

// Parity-exempt: framework/debugger/cli.py::register_debugger_commands has no
func TestDebuggerCommandShape(t *testing.T) {
	runner := NewRunner()
	specs := runner.debuggerCommands()
	if len(specs) != 1 {
		t.Fatalf("debuggerCommands() returned %d specs, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Prog != "hive debugger" {
		t.Fatalf("Prog = %q", spec.Prog)
	}
	if spec.Help != "Open the LLM debug log viewer" {
		t.Fatalf("Help = %q", spec.Help)
	}
	wantDescription := "Start a local server that lets you browse LLM debug sessions " +
		"recorded in ~/.hive/llm_logs. Sessions are loaded on demand so the " +
		"browser stays responsive."
	if spec.Description != wantDescription {
		t.Fatalf("Description = %q", spec.Description)
	}

	wantFlags := []struct {
		dest string
		long string
		kind argKind
		def  any
	}{
		{"session", "session", kindString, nil},
		{"port", "port", kindInt, 0},
		{"logs_dir", "logs-dir", kindString, nil},
		{"limit_files", "limit-files", kindInt, nil},
		{"output", "output", kindString, nil},
		{"no_open", "no-open", kindBool, nil},
		{"include_tests", "include-tests", kindBool, nil},
	}
	if len(spec.Flags) != len(wantFlags) {
		t.Fatalf("Flags = %d, want %d", len(spec.Flags), len(wantFlags))
	}
	for i, want := range wantFlags {
		got := spec.Flags[i]
		if got.Dest != want.dest || got.Long != want.long || got.Kind != want.kind || got.Default != want.def {
			t.Fatalf("flag %d = %+v, want %+v", i, got, want)
		}
	}

	order := []string{}
	for _, command := range runner.rootCommand().Subcommands {
		order = append(order, command.Prog)
	}
	skillAt, debuggerAt, mcpAt, janitorAt := -1, -1, -1, -1
	for i, prog := range order {
		switch prog {
		case "hive skill":
			skillAt = i
		case "hive debugger":
			debuggerAt = i
		case "hive mcp":
			mcpAt = i
		case "hive janitor":
			janitorAt = i
		}
	}
	if !(skillAt >= 0 && skillAt < debuggerAt && debuggerAt < mcpAt && mcpAt < janitorAt) {
		t.Fatalf("registration order = %v, want skill < debugger < mcp < janitor", order)
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerNoLogsFound(t *testing.T) {
	home := t.TempDir()
	runner, stdout, stderr := newDebuggerRunner(t, home)

	code := runner.Run([]string{"debugger"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	logsDir := filepath.Join(home, "llm_logs")
	want := fmt.Sprintf("No LLM debug logs found in %s.\n", logsDir) +
		"LLM turn logging is now server-side (LLM proxy datalog); " +
		"set HIVE_ENABLE_LLM_LOGS=1 to capture local debug logs.\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerLogsDirWithoutJSONL(t *testing.T) {
	home := t.TempDir()
	runner, stdout, _ := newDebuggerRunner(t, home)
	emptyDir := filepath.Join(home, "custom_logs")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(emptyDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code := runner.Run([]string{"debugger", "--logs-dir", emptyDir}); code != 0 {
		t.Fatalf("empty dir exit = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "No LLM debug logs found in "+emptyDir+".") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	plainFile := filepath.Join(home, "logs.jsonl")
	if err := os.WriteFile(plainFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout.Reset()
	if code := runner.Run([]string{"debugger", "--logs-dir", plainFile}); code != 0 {
		t.Fatalf("plain file exit = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "No LLM debug logs found in "+plainFile+".") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerMissingVisualizerScript(t *testing.T) {
	home := t.TempDir()
	runner, stdout, _ := newDebuggerRunner(t, home)
	writeJSONLLogDir(t, filepath.Join(home, "llm_logs"))
	missing := filepath.Join(t.TempDir(), "no_such_script.py")
	runner.DebuggerScript = func() string { return missing }

	code := runner.Run([]string{"debugger"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "Visualizer script missing: " + missing + "\n" +
		"It ships with the OSS hive repo (scripts/llm_debug_log_visualizer.py); " +
		"for proxy traffic, use the server-side datalog " +
		"(llm/scripts/datalog_replay.py in hive-backend) instead.\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerForwardsFlagsInOrder(t *testing.T) {
	home := t.TempDir()
	runner, _, _ := newDebuggerRunner(t, home)
	logsDir := writeJSONLLogDir(t, filepath.Join(home, "llm_logs"))
	script := writeVisualizer(t)
	runner.DebuggerScript = func() string { return script }

	var captured []string
	runner.DebuggerExec = func(command []string) int {
		captured = append([]string{}, command...)
		return 7
	}
	output := filepath.Join(t.TempDir(), "viewer.html")

	code := runner.Run([]string{
		"debugger",
		"--logs-dir", logsDir,
		"--session", "exec-42",
		"--port", "8791",
		"--limit-files", "0",
		"--output", output,
		"--no-open",
		"--include-tests",
	})
	if code != 7 {
		t.Fatalf("exit = %d, want the child's 7", code)
	}
	want := []string{
		"python3", script,
		"--session", "exec-42",
		"--port", "8791",
		"--logs-dir", logsDir,
		"--limit-files", "0",
		"--output", output,
		"--no-open",
		"--include-tests",
	}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("command = %#v, want %#v", captured, want)
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerOmitsDefaultFlags(t *testing.T) {
	home := t.TempDir()
	runner, _, _ := newDebuggerRunner(t, home)
	logsDir := writeJSONLLogDir(t, filepath.Join(home, "llm_logs"))
	script := writeVisualizer(t)
	runner.DebuggerScript = func() string { return script }

	var captured []string
	runner.DebuggerExec = func(command []string) int {
		captured = append([]string{}, command...)
		return 0
	}

	code := runner.Run([]string{
		"debugger",
		"--logs-dir", logsDir,
		"--port", "0",
		"--session", "",
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := []string{"python3", script, "--logs-dir", logsDir}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("command = %#v, want %#v", captured, want)
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerDefaultsLogsDirToHiveHome(t *testing.T) {
	home := t.TempDir()
	runner, _, _ := newDebuggerRunner(t, home)
	writeJSONLLogDir(t, filepath.Join(home, "llm_logs"))
	script := writeVisualizer(t)
	runner.DebuggerScript = func() string { return script }

	var captured []string
	runner.DebuggerExec = func(command []string) int {
		captured = append([]string{}, command...)
		return 0
	}

	if code := runner.Run([]string{"debugger"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := []string{"python3", script}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("command = %#v, want %#v", captured, want)
	}
}

// Parity-exempt: framework/debugger/cli.py::cmd_debugger has no upstream test;
func TestDebuggerInvalidPortIsUsageError(t *testing.T) {
	home := t.TempDir()
	runner, stdout, stderr := newDebuggerRunner(t, home)

	code := runner.Run([]string{"debugger", "--port", "abc"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "argument --port: invalid int value: 'abc'\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}
