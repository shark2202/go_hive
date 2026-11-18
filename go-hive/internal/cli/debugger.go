package cli

// Parity: framework/debugger/cli.py

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// Parity: framework/debugger/cli.py::register_debugger_commands
func (r *Runner) debuggerCommands() []*commandSpec {
	return []*commandSpec{{
		Prog: "hive debugger",
		Help: "Open the LLM debug log viewer",
		Description: "Start a local server that lets you browse LLM debug sessions " +
			"recorded in ~/.hive/llm_logs. Sessions are loaded on demand so the " +
			"browser stays responsive.",
		Flags: []argSpec{
			{Dest: "session", Long: "session", Kind: kindString,
				Help: "Execution ID to select initially."},
			{Dest: "port", Long: "port", Kind: kindInt, Default: 0,
				Help: "Port for the local server (0 = auto-pick a free port)."},
			{Dest: "logs_dir", Long: "logs-dir", Kind: kindString,
				Help: "Directory containing JSONL log files (default: ~/.hive/llm_logs)."},
			{Dest: "limit_files", Long: "limit-files", Kind: kindInt,
				Help: "Maximum number of newest log files to scan (default: 200)."},
			{Dest: "output", Long: "output", Kind: kindString,
				Help: "Write a static HTML file instead of starting a server."},
			{Dest: "no_open", Long: "no-open", Kind: kindBool,
				Help: "Start the server but do not open a browser."},
			{Dest: "include_tests", Long: "include-tests", Kind: kindBool,
				Help: "Show test/mock sessions (hidden by default)."},
		},
		Run: r.cmdDebugger,
	}}
}

// Parity: framework/debugger/cli.py::cmd_debugger
func (r *Runner) cmdDebugger(args *parsedArgs) int {
	logsDir := args.str("logs_dir")
	if logsDir == "" {
		home, err := r.home()
		if err != nil {
			fmt.Fprintf(r.Stderr, "%s\n", err.Error())
			return 1
		}
		logsDir = filepath.Join(home.Root, "llm_logs")
	}

	if !debuggerHasJSONLLogs(logsDir) {
		fmt.Fprintf(r.Stdout, "No LLM debug logs found in %s.\n", logsDir)
		fmt.Fprint(r.Stdout, "LLM turn logging is now server-side (LLM proxy datalog); "+
			"set HIVE_ENABLE_LLM_LOGS=1 to capture local debug logs.\n")
		return 0
	}

	script := r.debuggerScript()
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		fmt.Fprintf(r.Stdout, "Visualizer script missing: %s\n", script)
		fmt.Fprint(r.Stdout, "It ships with the OSS hive repo (scripts/llm_debug_log_visualizer.py); "+
			"for proxy traffic, use the server-side datalog "+
			"(llm/scripts/datalog_replay.py in hive-backend) instead.\n")
		return 1
	}

	command := []string{r.pythonExecutable(), script}
	if value := args.str("session"); value != "" {
		command = append(command, "--session", value)
	}
	if value := args.intValue("port"); value != 0 {
		command = append(command, "--port", strconv.Itoa(value))
	}
	if value := args.str("logs_dir"); value != "" {
		command = append(command, "--logs-dir", value)
	}
	if args.wasProvided("limit_files") {
		command = append(command, "--limit-files", strconv.Itoa(args.intValue("limit_files")))
	}
	if value := args.str("output"); value != "" {
		command = append(command, "--output", value)
	}
	if args.boolean("no_open") {
		command = append(command, "--no-open")
	}
	if args.boolean("include_tests") {
		command = append(command, "--include-tests")
	}
	return r.DebuggerExec(command)
}

func debuggerHasJSONLLogs(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if matched, _ := filepath.Match("*.jsonl", entry.Name()); matched {
			return true
		}
	}
	return false
}

func (r *Runner) debuggerScript() string {
	if r.DebuggerScript != nil {
		return r.DebuggerScript()
	}
	return defaultDebuggerScript()
}

func (r *Runner) pythonExecutable() string {
	if r.DebuggerPython != "" {
		return r.DebuggerPython
	}
	return "python3"
}

func defaultDebuggerScript() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Dir(file)
	for i := 0; i < 4; i++ {
		root = filepath.Dir(root)
	}
	return filepath.Join(root, "scripts", "llm_debug_log_visualizer.py")
}

func (r *Runner) defaultDebuggerExec(command []string) int {
	if len(command) == 0 {
		return 1
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintln(r.Stderr, err.Error())
	return 1
}
