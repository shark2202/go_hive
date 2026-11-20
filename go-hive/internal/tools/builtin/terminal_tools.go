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
//
// Schemas and descriptions mirror the model-visible surface generated from the
// upstream docstrings (tools/src/terminal_tools). session_cwd is a framework
// CONTEXT_PARAM: it is declared on the Python functions but stripped from the
// LLM-facing schema, injected at execution time instead, so it must never
// appear in Defs().
func terminalToolDefs() []llm.ToolDef {
	defs := terminalSearchToolDefs()
	defs = append(defs, terminalJobToolDefs()...)
	return append(defs, terminalPTYToolDefs()...)
}

func terminalSearchToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: ToolTerminalRG,
			Description: "Run ripgrep on `path` for `pattern` — the content-search tool." + "\n" +
				"" + "\n" +
				"Use this for all content (regex) search, whether project code or raw" + "\n" +
				"paths (system configs, /var/log, archive contents); it exposes the full" + "\n" +
				"rg flag surface. Relative paths resolve against the session workdir." + "\n" +
				"" + "\n" +
				"Args:" + "\n" +
				"    pattern: Regex pattern." + "\n" +
				"    path: Directory or file to search. Relative paths resolve against" + "\n" +
				"        the session workdir; default is the session workdir itself." + "\n" +
				"    glob: Filename glob (e.g. \"*.py\")." + "\n" +
				"    type_filter: rg filetype shortcut (e.g. \"py\", \"rust\", \"md\")." + "\n" +
				"    ignore_case: Case-insensitive search." + "\n" +
				"    context: Lines of context above and below each match." + "\n" +
				"    max_count: Stop after N matches per file." + "\n" +
				"    max_depth: Limit directory recursion depth." + "\n" +
				"    hidden: Include hidden files (rg ignores them by default)." + "\n" +
				"    no_ignore: Don't respect .gitignore." + "\n" +
				"    extra_args: Raw flags to append (use sparingly — most needs are covered above)." + "\n" +
				"    allow_fallback: If rg is missing, explicitly accept approximate Python" + "\n" +
				"        search without .gitignore support and with Python regex syntax." + "\n" +
				"        Context, extra_args, unknown filetypes and complex globs still" + "\n" +
				"        require rg; unsupported parameters return an error without searching." + "\n" +
				"" + "\n" +
				"Returns: {matches: [...], context: [...], total, truncated, exit_code, stderr}." + "\n" +
				"    Context entries have the same path/line/text shape as matches;" + "\n" +
				"    total counts only matches. Missing rg returns ripgrep_required by default.",
			Parameters: schemaObject(map[string]any{
				"pattern":        schemaString("Regex pattern."),
				"path":           schemaDefault(schemaString("Directory or file to search. Default is the session workdir."), "."),
				"glob":           schemaNullableString(`Filename glob (e.g. "*.py").`),
				"type_filter":    schemaNullableString(`rg filetype shortcut (e.g. "py", "rust", "md").`),
				"ignore_case":    schemaDefault(schemaBoolean("Case-insensitive search."), false),
				"context":        schemaDefault(schemaInteger("Lines of context above and below each match."), 0),
				"max_count":      schemaNullableInt("Stop after N matches per file."),
				"max_depth":      schemaNullableInt("Limit directory recursion depth."),
				"hidden":         schemaDefault(schemaBoolean("Include hidden files (rg ignores them by default)."), false),
				"no_ignore":      schemaDefault(schemaBoolean("Do not respect .gitignore."), false),
				"extra_args":     schemaNullableStringList("Raw rg flags appended verbatim."),
				"allow_fallback": schemaDefault(schemaBoolean("If rg is missing, accept an approximate search without .gitignore support."), false),
				// Framework-injected CONTEXT_PARAM, stripped before the model
				// sees the schema (tools.StripContextParams).
				"session_cwd": schemaNullableString("Session workdir injected by the host; relative paths resolve against it."),
			}, "pattern"),
		},
		{
			Name: ToolTerminalGlob,
			Description: "Find files by name/glob under `path` — the Glob tool." + "\n" +
				"" + "\n" +
				"Use this to locate files by name, in the project tree or raw/arbitrary" + "\n" +
				"paths (/var/log, /etc, archive contents). Relative paths resolve against" + "\n" +
				"the session workdir. For mtime/size/type predicate queries, drop to" + "\n" +
				"terminal_exec(\"find ...\") — see references/find_predicates.md." + "\n" +
				"" + "\n" +
				"Pattern handling (so bare stems Just Work):" + "\n" +
				"    \"lk_scan_post_reactors\" -> matched as **/*lk_scan_post_reactors*" + "\n" +
				"    \"*.py\"                  -> matched as **/*.py (recursive)" + "\n" +
				"    \"src/**/*.py\"           -> used verbatim" + "\n" +
				"The glob actually run is returned as `expanded_pattern`." + "\n" +
				"" + "\n" +
				"Args:" + "\n" +
				"    pattern: Filename glob or bare substring." + "\n" +
				"    path: Directory to search under. Relative paths resolve against the" + "\n" +
				"        session workdir; default is the session workdir itself." + "\n" +
				"    max_results: Cap on returned paths (search stops early at the cap)." + "\n" +
				"    include_ignored: Include .gitignored / hidden / build-cache files." + "\n" +
				"" + "\n" +
				"Returns: {paths, count, truncated, timed_out, expanded_pattern, command}" + "\n" +
				"    Without rg, returns fallback=\"python-walk\" and a note: the" + "\n" +
				"    approximate filename walk does not honor .gitignore.",
			Parameters: schemaObject(map[string]any{
				"pattern":         schemaString("Filename glob or bare substring."),
				"path":            schemaDefault(schemaString("Directory to search under. Default is the session workdir."), "."),
				"max_results":     schemaDefault(schemaInteger("Cap on returned paths; the search stops early at the cap."), 1000),
				"include_ignored": schemaDefault(schemaBoolean("Include .gitignored, hidden and build-cache files."), false),
				// Framework-injected CONTEXT_PARAM (stripped before the model).
				"session_cwd": schemaNullableString("Session workdir injected by the host; relative paths resolve against it."),
			}, "pattern"),
		},
	}
}

func terminalJobToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: ToolTerminalOutputGet,
			Description: "Retrieve a slice of truncated output by handle. When a terminal tool reports more " +
				`output than fits inline you get output_handle: "out_<hex>"; pass it here with successive ` +
				"offsets to paginate. The full output is preserved for 5 minutes.",
			Parameters: schemaObject(map[string]any{
				"output_handle": schemaString("Handle from a prior tool envelope."),
				"since_offset":  schemaInteger("Pass 0 first, then next_offset from the previous call."),
				"max_kb":        schemaInteger("Max KB to return per call. Default 64."),
			}, "output_handle"),
		},
		{
			Name: ToolTerminalJobStart,
			Description: "Spawn a background process. Returns a job_id you poll with terminal_job_logs." + "\n" +
				"" + "\n" +
				"Use this when work might run >1 minute, when you want to keep doing" + "\n" +
				"other things while it runs, or when you need to stream logs as they" + "\n" +
				"arrive. Jobs die when the terminal-tools server restarts — they are NOT" + "\n" +
				"persistent across reboots." + "\n" +
				"" + "\n" +
				"Args:" + "\n" +
				"    command: Shell command to run. With shell=False, pass argv via the" + "\n" +
				"        command string and we'll split on whitespace; for complex" + "\n" +
				"        quoting use shell=True with the selected platform's syntax." + "\n" +
				"    cwd: Working directory. Defaults to the session workdir when" + "\n" +
				"        omitted; pass an absolute path to override." + "\n" +
				"    env: Environment override. Merged into a sanitized base env (with" + "\n" +
				"        zsh dotfile vars stripped)." + "\n" +
				"    merge_stderr: When True, stderr is interleaved into stdout in a" + "\n" +
				"        single ring buffer. Convenient for log-shaped output where" + "\n" +
				"        ordering matters." + "\n" +
				"    shell: True to use the platform shell (POSIX bash; Windows Git Bash," + "\n" +
				"        PowerShell, then cmd). Refuses zsh." + "\n" +
				"    name: Optional human label surfaced in terminal_job_manage(action=\"list\")." + "\n" +
				"    limits: Optional resource caps applied via setrlimit before exec." + "\n" +
				"        Keys: cpu_sec, rss_mb, fsize_mb, nofile." + "\n" +
				"" + "\n" +
				"Returns: {job_id, pid, started_at}",
			Parameters: schemaObject(map[string]any{
				"command":       schemaString("Shell command to run. With shell=false it is split on whitespace; use shell=true for quoting."),
				"cwd":           schemaNullableString("Working directory. Defaults to the session workdir."),
				"env":           schemaNullableStringMap("Environment override merged into a sanitized base."),
				"merge_stderr":  schemaDefault(schemaBoolean("Interleave stderr into stdout in a single buffer."), false),
				"shell":         schemaDefault(schemaBoolean("Run through the platform shell (POSIX bash). zsh is refused."), false),
				"name":          schemaNullableString("Human label surfaced by terminal_job_manage(action=\"list\")."),
				"limits":        schemaNullableIntMap("Optional resource caps (cpu_sec, rss_mb, fsize_mb, nofile)."),
				"crm_principal": schemaNullableString("Acting CRM principal forwarded to child processes as HIVE_CRM_PRINCIPAL."),
				// Framework-injected CONTEXT_PARAM (stripped before the model).
				"session_cwd": schemaNullableString("Session workdir injected by the host."),
			}, "command"),
		},
		{
			Name: ToolTerminalJobLogs,
			Description: "Read job output at an offset — one call covering read, status and wait. Track " +
				"next_offset across calls so nothing replays. wait_until_exit=True blocks until the job " +
				"exits or the wait times out, then returns the logs and final status.",
			Parameters: schemaObject(map[string]any{
				"job_id":           schemaString("From terminal_job_start, or auto-promoted from terminal_exec."),
				"stream":           schemaString(`"stdout" | "stderr" | "merged". Use "merged" only for merged jobs.`),
				"since_offset":     schemaInteger("Absolute byte offset to start reading from; pass next_offset afterwards."),
				"max_bytes":        schemaInteger("Max bytes of decoded output to return inline. Default 64000."),
				"wait_until_exit":  schemaBoolean("Wait for the job to exit before reading."),
				"wait_timeout_sec": schemaNumber("Longest to wait in seconds (up to 600, capped at 45 per call)."),
				"tail":             schemaBoolean("Ignore since_offset and return the last max_bytes."),
			}, "job_id"),
		},
		{
			Name: ToolTerminalJobManage,
			Description: "List jobs, send signals, or write to a job's stdin. The action argument picks the " +
				"operation: capabilities, list, signal_term/signal_kill/signal_int/signal_hup/signal_usr1/" +
				"signal_usr2, stdin or close_stdin. Call capabilities first to see what this platform supports.",
			Parameters: schemaObject(map[string]any{
				"action": schemaString("Operation: capabilities | list | signal_* | stdin | close_stdin."),
				"job_id": schemaNullableString("Required for signals and stdin actions."),
				"data":   schemaNullableString("Text to write for action=stdin."),
			}, "action"),
		},
	}
}

func terminalPTYToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: ToolTerminalPTYOpen,
			Description: "Open a persistent /bin/bash session in a PTY." + "\n" +
				"" + "\n" +
				"Use a session when you need state across calls — building env vars," + "\n" +
				"navigating with cd, driving REPLs, or responding to interactive" + "\n" +
				"prompts (sudo, ssh, mysql). For one-shot work, use terminal_exec" + "\n" +
				"instead." + "\n" +
				"" + "\n" +
				"The session runs vanilla bash (--norc --noprofile) so dotfiles" + "\n" +
				"don't surprise you. A unique PS1 sentinel is set so terminal_pty_run" + "\n" +
				"can unambiguously detect command completion. macOS users: this" + "\n" +
				"is /bin/bash, not zsh, by deliberate policy — explicit" + "\n" +
				"shell=\"/bin/zsh\" overrides are rejected." + "\n" +
				"" + "\n" +
				"Args:" + "\n" +
				"    cwd: Initial working directory. Defaults to the session workdir" + "\n" +
				"        when omitted; pass an absolute path to override." + "\n" +
				"    env: Environment override (zsh dotfile vars are stripped)." + "\n" +
				"    cols, rows: Terminal size." + "\n" +
				"    idle_timeout_sec: Drop the session after this many seconds idle." + "\n" +
				"" + "\n" +
				"Returns: {session_id, pid, shell}",
			Parameters: schemaObject(map[string]any{
				"cwd":              schemaNullableString("Initial working directory. Defaults to the session workdir."),
				"env":              schemaNullableStringMap("Environment override merged into a sanitized base."),
				"cols":             schemaDefault(schemaInteger("Terminal columns. Default 120."), 120),
				"rows":             schemaDefault(schemaInteger("Terminal rows. Default 40."), 40),
				"idle_timeout_sec": schemaDefault(schemaInteger("Drop the session after this many idle seconds. Default 1800."), 1800),
				// Framework-injected CONTEXT_PARAM (stripped before the model).
				"session_cwd": schemaNullableString("Session workdir injected by the host."),
			}),
		},
		{
			Name: ToolTerminalPTYRun,
			Description: "Run a command in a PTY session, send raw input, or drain output. Default mode sends " +
				"the command and waits for the prompt sentinel or the expect regex. raw_send=True writes " +
				"without waiting (REPLs, sudo prompts, vim). read_only=True drains whatever is buffered.",
			Parameters: schemaObject(map[string]any{
				"session_id":  schemaString("From terminal_pty_open."),
				"command":     schemaNullableString("Text to send. Omit only when read_only=True."),
				"expect":      schemaNullableString("Regex to wait for instead of the default prompt sentinel."),
				"raw_send":    schemaBoolean("Write without waiting for the prompt."),
				"read_only":   schemaBoolean("Send nothing; drain the buffer."),
				"timeout_sec": schemaNumber("Max wait in seconds. Default 60."),
			}, "session_id"),
		},
		{
			Name: ToolTerminalPTYClose,
			Description: "Terminate a PTY session. Always close sessions you are done with — leaked sessions " +
				"count against the per-host PTY cap.",
			Parameters: schemaObject(map[string]any{
				"session_id": schemaString("From terminal_pty_open."),
				"force":      schemaBoolean("Skip the graceful exit and SIGTERM/SIGKILL the shell."),
			}, "session_id"),
		},
	}
}

// schemaObject builds one JSON-schema object; required stays absent when
// every property is optional, matching pydantic's generated schema.
func schemaObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func schemaString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func schemaInteger(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func schemaNumber(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func schemaBoolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func schemaNullableString(description string) map[string]any {
	return map[string]any{"type": []string{"string", "null"}, "description": description, "default": nil}
}

func schemaNullableInt(description string) map[string]any {
	return map[string]any{"type": []string{"integer", "null"}, "description": description, "default": nil}
}

func schemaNullableStringMap(description string) map[string]any {
	return map[string]any{
		"type":                 []string{"object", "null"},
		"additionalProperties": map[string]any{"type": "string"},
		"description":          description,
		"default":              nil,
	}
}

func schemaNullableIntMap(description string) map[string]any {
	return map[string]any{
		"type":                 []string{"object", "null"},
		"additionalProperties": map[string]any{"type": "integer"},
		"description":          description,
		"default":              nil,
	}
}

func schemaNullableStringList(description string) map[string]any {
	return map[string]any{
		"type":        []string{"array", "null"},
		"items":       map[string]any{"type": "string"},
		"description": description,
		"default":     nil,
	}
}

func schemaDefault(schema map[string]any, value any) map[string]any {
	schema["default"] = value
	return schema
}
