package builtin

import (
	"context"
	"fmt"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Canonical terminal tool names.
const (
	ToolTerminalRG        = "terminal_rg"
	ToolTerminalGlob      = "terminal_glob"
	ToolTerminalOutputGet = "terminal_output_get"
	ToolTerminalJobStart  = "terminal_job_start"
	ToolTerminalJobLogs   = "terminal_job_logs"
	ToolTerminalJobManage = "terminal_job_manage"
	ToolTerminalPTYOpen   = "terminal_pty_open"
	ToolTerminalPTYRun    = "terminal_pty_run"
	ToolTerminalPTYClose  = "terminal_pty_close"
)

// terminalToolHandler runs one terminal tool. Every handler reports expected
// failures as a JSON envelope, so the error return is reserved for
// infrastructure faults.
type terminalToolHandler func(t *Toolbox, ctx context.Context, input map[string]any) (Result, error)

// terminalToolHandlers is the single registration point for the terminal tool
// surface.
var terminalToolHandlers = map[string]terminalToolHandler{
	ToolTerminalRG:        (*Toolbox).terminalRG,
	ToolTerminalGlob:      (*Toolbox).terminalGlob,
	ToolTerminalOutputGet: (*Toolbox).terminalOutputGet,
	ToolTerminalJobStart:  (*Toolbox).terminalJobStart,
	ToolTerminalJobLogs:   (*Toolbox).terminalJobLogs,
	ToolTerminalJobManage: (*Toolbox).terminalJobManage,
	ToolTerminalPTYOpen:   (*Toolbox).terminalPTYOpen,
	ToolTerminalPTYRun:    (*Toolbox).terminalPTYRun,
	ToolTerminalPTYClose:  (*Toolbox).terminalPTYClose,
}

// executeTerminalTool dispatches a terminal tool, or reports the same
// unknown-tool error the builtin toolbox has always used.
func (t *Toolbox) executeTerminalTool(ctx context.Context, name string, input map[string]any) (Result, error) {
	if handler, ok := terminalToolHandlers[name]; ok {
		return handler(t, ctx, input)
	}
	return Result{}, fmt.Errorf("builtin: unknown tool %q", name)
}

// terminalToolDefs returns the terminal-tools harness group in the stable
// order the tool library expects: search, output, jobs, PTY.
func terminalToolDefs() []llm.ToolDef {
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(description string) map[string]any {
		return map[string]any{"type": "integer", "description": description}
	}
	number := func(description string) map[string]any {
		return map[string]any{"type": "number", "description": description}
	}
	boolean := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	nullableString := func(description string) map[string]any {
		return map[string]any{"type": []string{"string", "null"}, "description": description}
	}
	return []llm.ToolDef{
		{
			Name: ToolTerminalRG,
			Description: "Run ripgrep on path for pattern — the content-search tool. Use it for all regex " +
				"search, project code or raw paths (/etc, /var/log); the full rg flag surface is exposed. " +
				"Relative paths resolve against the session workdir. Returns matches, context lines, " +
				"total, truncated, exit_code and stderr; a missing rg reports ripgrep_required unless " +
				"allow_fallback=True opts into an approximate walk without .gitignore support.",
			Parameters: object(map[string]any{
				"pattern":        str("Regex pattern."),
				"path":           str("Directory or file to search. Default is the session workdir."),
				"glob":           nullableString(`Filename glob (e.g. "*.py").`),
				"type_filter":    nullableString(`rg filetype shortcut (e.g. "py", "rust", "md").`),
				"ignore_case":    boolean("Case-insensitive search."),
				"context":        integer("Lines of context above and below each match."),
				"max_count":      integer("Stop after N matches per file."),
				"max_depth":      integer("Limit directory recursion depth."),
				"hidden":         boolean("Include hidden files (rg ignores them by default)."),
				"no_ignore":      boolean("Do not respect .gitignore."),
				"extra_args":     map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}, "description": "Raw rg flags appended verbatim."},
				"session_cwd":    nullableString("Session workdir injected by the host; relative paths resolve against it."),
				"allow_fallback": boolean("If rg is missing, accept an approximate search without .gitignore support."),
			}, "pattern"),
		},
		{
			Name: ToolTerminalGlob,
			Description: "Find files by name or glob under path — the Glob tool. Bare stems and bare " +
				`filenames recurse: "lk_scan" becomes **/*lk_scan* and "*.py" becomes **/*.py, and the ` +
				"glob actually run is returned as expanded_pattern. For mtime/size predicates use " +
				`terminal_exec("find ...") instead.`,
			Parameters: object(map[string]any{
				"pattern":         str("Filename glob or bare substring."),
				"path":            str("Directory to search under. Default is the session workdir."),
				"max_results":     integer("Cap on returned paths; the search stops early at the cap."),
				"include_ignored": boolean("Include .gitignored, hidden and build-cache files."),
				"session_cwd":     nullableString("Session workdir injected by the host; relative paths resolve against it."),
			}, "pattern"),
		},
		{
			Name: ToolTerminalOutputGet,
			Description: "Retrieve a slice of truncated output by handle. When a terminal tool reports more " +
				`output than fits inline you get output_handle: "out_<hex>"; pass it here with successive ` +
				"offsets to paginate. The full output is preserved for 5 minutes.",
			Parameters: object(map[string]any{
				"output_handle": str("Handle from a prior tool envelope."),
				"since_offset":  integer("Pass 0 first, then next_offset from the previous call."),
				"max_kb":        integer("Max KB to return per call. Default 64."),
			}, "output_handle"),
		},
		{
			Name: ToolTerminalJobStart,
			Description: "Spawn a background process and return a job_id to poll with terminal_job_logs. " +
				"Use it for work that may run longer than a minute, for streaming logs, or to keep working " +
				"while it runs. Jobs die with the host — they are not persistent across reboots.",
			Parameters: object(map[string]any{
				"command":      str("Shell command to run. With shell=false it is split on whitespace; use shell=true for quoting."),
				"cwd":          nullableString("Working directory. Defaults to the session workdir."),
				"env":          map[string]any{"type": []string{"object", "null"}, "additionalProperties": map[string]any{"type": "string"}, "description": "Environment override merged into a sanitized base."},
				"merge_stderr": boolean("Interleave stderr into stdout in a single buffer."),
				"shell":        boolean("Run through the platform shell (POSIX bash). zsh is refused."),
				"name":         nullableString("Human label surfaced by terminal_job_manage(action=\"list\")."),
				"limits":       map[string]any{"type": []string{"object", "null"}, "description": "Optional resource caps (cpu_sec, rss_mb, fsize_mb, nofile). Accepted for parity; not enforced on this host."},
				"session_cwd":  nullableString("Session workdir injected by the host."),
			}, "command"),
		},
		{
			Name: ToolTerminalJobLogs,
			Description: "Read job output at an offset — one call covering read, status and wait. Track " +
				"next_offset across calls so nothing replays. wait_until_exit=True blocks until the job " +
				"exits or the wait times out, then returns the logs and final status.",
			Parameters: object(map[string]any{
				"job_id":           str("From terminal_job_start, or auto-promoted from terminal_exec."),
				"stream":           str(`"stdout" | "stderr" | "merged". Use "merged" only for merged jobs.`),
				"since_offset":     integer("Absolute byte offset to start reading from; pass next_offset afterwards."),
				"max_bytes":        integer("Max bytes of decoded output to return inline. Default 64000."),
				"wait_until_exit":  boolean("Wait for the job to exit before reading."),
				"wait_timeout_sec": number("Longest to wait in seconds (up to 600, capped at 45 per call)."),
				"tail":             boolean("Ignore since_offset and return the last max_bytes."),
			}, "job_id"),
		},
		{
			Name: ToolTerminalJobManage,
			Description: "List jobs, send signals, or write to a job's stdin. The action argument picks the " +
				"operation: capabilities, list, signal_term/signal_kill/signal_int/signal_hup/signal_usr1/" +
				"signal_usr2, stdin or close_stdin. Call capabilities first to see what this platform supports.",
			Parameters: object(map[string]any{
				"action": str("Operation: capabilities | list | signal_* | stdin | close_stdin."),
				"job_id": nullableString("Required for signals and stdin actions."),
				"data":   nullableString("Text to write for action=stdin."),
			}, "action"),
		},
		{
			Name: ToolTerminalPTYOpen,
			Description: "Open a persistent /bin/bash session in a PTY. Use a session when state must survive " +
				"across calls — environment, cd, REPLs or interactive prompts. The session runs vanilla bash " +
				"(--norc --noprofile) with a unique prompt sentinel so run can detect completion. POSIX only.",
			Parameters: object(map[string]any{
				"cwd":              nullableString("Initial working directory. Defaults to the session workdir."),
				"env":              map[string]any{"type": []string{"object", "null"}, "additionalProperties": map[string]any{"type": "string"}, "description": "Environment override merged into a sanitized base."},
				"cols":             integer("Terminal columns. Default 120."),
				"rows":             integer("Terminal rows. Default 40."),
				"idle_timeout_sec": integer("Drop the session after this many idle seconds. Default 1800."),
				"session_cwd":      nullableString("Session workdir injected by the host."),
			}),
		},
		{
			Name: ToolTerminalPTYRun,
			Description: "Run a command in a PTY session, send raw input, or drain output. Default mode sends " +
				"the command and waits for the prompt sentinel or the expect regex. raw_send=True writes " +
				"without waiting (REPLs, sudo prompts, vim). read_only=True drains whatever is buffered.",
			Parameters: object(map[string]any{
				"session_id":  str("From terminal_pty_open."),
				"command":     nullableString("Text to send. Omit only when read_only=True."),
				"expect":      nullableString("Regex to wait for instead of the default prompt sentinel."),
				"raw_send":    boolean("Write without waiting for the prompt."),
				"read_only":   boolean("Send nothing; drain the buffer."),
				"timeout_sec": number("Max wait in seconds. Default 60."),
			}, "session_id"),
		},
		{
			Name: ToolTerminalPTYClose,
			Description: "Terminate a PTY session. Always close sessions you are done with — leaked sessions " +
				"count against the per-host PTY cap.",
			Parameters: object(map[string]any{
				"session_id": str("From terminal_pty_open."),
				"force":      boolean("Skip the graceful exit and SIGTERM/SIGKILL the shell."),
			}, "session_id"),
		},
	}
}
