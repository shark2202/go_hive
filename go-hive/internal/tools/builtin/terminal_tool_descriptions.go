package builtin

// Model-visible descriptions for the ten terminal-tools tools, byte-exact from
// the Python terminal-tools docstrings frozen in
// go_hive/contracts/tool-definitions.json (source docs: tools/src/terminal_tools,
// captured through inspect.getdoc on the FastMCP tool functions).
//
// They live in their own file because they are contract data, not code: the
// docstrings are long, the recheck lane compares them byte-for-byte, and a
// stray edit here changes what the model reads. The per-parameter docs that the
// Go port used to advertise separately were removed from the schema on purpose
// — Python's pydantic schemas carry no property descriptions, and the same
// guidance is already in these Args paragraphs.
const (
	// terminal_exec
	TerminalExecDescription = "Run a shell command and capture its output.\n" +
		"\n" +
		"Past auto_background_after_sec, the call auto-promotes to a background\n" +
		"job and returns immediately with `auto_backgrounded=True, job_id=...`\n" +
		"— poll with terminal_job_logs(job_id, since_offset=...) to read the rest.\n" +
		"Set auto_background_after_sec=0 to force pure foreground (kill on\n" +
		"timeout_sec).\n" +
		"\n" +
		"The agent loop may first return a bg_* handle; use collect_result for\n" +
		"that handle, then terminal_job_logs for any job_id in the result.\n" +
		"POSIX uses bash when shell=True; Windows selects Git Bash, PowerShell,\n" +
		"then cmd. Read shell_kind before choosing platform-specific syntax.\n" +
		"\n" +
		"Args:\n" +
		"    command: The command. With shell=False POSIX uses shlex splitting;\n" +
		"        shell syntax is detected automatically. Use shell=True for scripts.\n" +
		"    cwd: Working directory. Defaults to the session workdir when\n" +
		"        omitted; pass an absolute path to override (loose default,\n" +
		"        not a sandbox — you can point anywhere).\n" +
		"    env: Environment override (merged into a sanitized base — zsh\n" +
		"        dotfile vars are stripped).\n" +
		"    timeout_sec: Total deadline from command start, including background\n" +
		"        execution. Expiry terminates the process tree and reports\n" +
		"        timed_out=True. 0 means unlimited, requiring auto-promotion.\n" +
		"    auto_background_after_sec: Inline budget. Past this, promote to\n" +
		"        a background job and return. 0 disables auto-promotion. The\n" +
		"        foreground wait must be finite and at most 220 seconds.\n" +
		"    shell: True to use the platform shell. zsh refused.\n" +
		"    stdin: Optional stdin payload (string).\n" +
		"    limits: Optional setrlimit caps. Keys: cpu_sec, rss_mb,\n" +
		"        fsize_mb, nofile.\n" +
		"    max_output_kb: Inline output cap. Overflow stashes to an\n" +
		"        output_handle for retrieval via terminal_output_get.\n" +
		"\n" +
		"Returns the standard envelope: see `terminal-tools-foundations` skill."

	// terminal_glob
	TerminalGlobDescription = "Find files by name/glob under `path` — the Glob tool.\n" +
		"\n" +
		"Use this to locate files by name, in the project tree or raw/arbitrary\n" +
		"paths (/var/log, /etc, archive contents). Relative paths resolve against\n" +
		"the session workdir. For mtime/size/type predicate queries, drop to\n" +
		"terminal_exec(\"find ...\") — see references/find_predicates.md.\n" +
		"\n" +
		"Pattern handling (so bare stems Just Work):\n" +
		"    \"lk_scan_post_reactors\" -> matched as **/*lk_scan_post_reactors*\n" +
		"    \"*.py\"                  -> matched as **/*.py (recursive)\n" +
		"    \"src/**/*.py\"           -> used verbatim\n" +
		"The glob actually run is returned as `expanded_pattern`.\n" +
		"\n" +
		"Args:\n" +
		"    pattern: Filename glob or bare substring.\n" +
		"    path: Directory to search under. Relative paths resolve against the\n" +
		"        session workdir; default is the session workdir itself.\n" +
		"    max_results: Cap on returned paths (search stops early at the cap).\n" +
		"    include_ignored: Include .gitignored / hidden / build-cache files.\n" +
		"\n" +
		"Returns: {paths, count, truncated, timed_out, expanded_pattern, command}\n" +
		"    Without rg, returns fallback=\"python-walk\" and a note: the\n" +
		"    approximate filename walk does not honor .gitignore."

	// terminal_rg
	TerminalRgDescription = "Run ripgrep on `path` for `pattern` — the content-search tool.\n" +
		"\n" +
		"Use this for all content (regex) search, whether project code or raw\n" +
		"paths (system configs, /var/log, archive contents); it exposes the full\n" +
		"rg flag surface. Relative paths resolve against the session workdir.\n" +
		"\n" +
		"Args:\n" +
		"    pattern: Regex pattern.\n" +
		"    path: Directory or file to search. Relative paths resolve against\n" +
		"        the session workdir; default is the session workdir itself.\n" +
		"    glob: Filename glob (e.g. \"*.py\").\n" +
		"    type_filter: rg filetype shortcut (e.g. \"py\", \"rust\", \"md\").\n" +
		"    ignore_case: Case-insensitive search.\n" +
		"    context: Lines of context above and below each match.\n" +
		"    max_count: Stop after N matches per file.\n" +
		"    max_depth: Limit directory recursion depth.\n" +
		"    hidden: Include hidden files (rg ignores them by default).\n" +
		"    no_ignore: Don't respect .gitignore.\n" +
		"    extra_args: Raw flags to append (use sparingly — most needs are covered above).\n" +
		"    allow_fallback: If rg is missing, explicitly accept approximate Python\n" +
		"        search without .gitignore support and with Python regex syntax.\n" +
		"        Context, extra_args, unknown filetypes and complex globs still\n" +
		"        require rg; unsupported parameters return an error without searching.\n" +
		"\n" +
		"Returns: {matches: [...], context: [...], total, truncated, exit_code, stderr}.\n" +
		"    Context entries have the same path/line/text shape as matches;\n" +
		"    total counts only matches. Missing rg returns ripgrep_required by default."

	// terminal_job_start
	TerminalJobStartDescription = "Spawn a background process. Returns a job_id you poll with terminal_job_logs.\n" +
		"\n" +
		"Use this when work might run >1 minute, when you want to keep doing\n" +
		"other things while it runs, or when you need to stream logs as they\n" +
		"arrive. Jobs die when the terminal-tools server restarts — they are NOT\n" +
		"persistent across reboots.\n" +
		"\n" +
		"Args:\n" +
		"    command: Shell command to run. With shell=False, pass argv via the\n" +
		"        command string and we'll split on whitespace; for complex\n" +
		"        quoting use shell=True with the selected platform's syntax.\n" +
		"    cwd: Working directory. Defaults to the session workdir when\n" +
		"        omitted; pass an absolute path to override.\n" +
		"    env: Environment override. Merged into a sanitized base env (with\n" +
		"        zsh dotfile vars stripped).\n" +
		"    merge_stderr: When True, stderr is interleaved into stdout in a\n" +
		"        single ring buffer. Convenient for log-shaped output where\n" +
		"        ordering matters.\n" +
		"    shell: True to use the platform shell (POSIX bash; Windows Git Bash,\n" +
		"        PowerShell, then cmd). Refuses zsh.\n" +
		"    name: Optional human label surfaced in terminal_job_manage(action=\"list\").\n" +
		"    limits: Optional resource caps applied via setrlimit before exec.\n" +
		"        Keys: cpu_sec, rss_mb, fsize_mb, nofile.\n" +
		"\n" +
		"Returns: {job_id, pid, started_at}"

	// terminal_job_logs
	TerminalJobLogsDescription = "Read job output at an offset. Combined read + status + wait primitive.\n" +
		"\n" +
		"Track next_offset across calls to avoid replaying data. When\n" +
		"wait_until_exit=True, waits until the job exits, a new message for you\n" +
		"arrives, or wait_timeout_sec elapses, then returns logs and status.\n" +
		"\n" +
		"Args:\n" +
		"    job_id: From terminal_job_start (or auto-promoted from terminal_exec).\n" +
		"    stream: \"stdout\" | \"stderr\" | \"merged\". Use \"merged\" only when the\n" +
		"        job was started with merge_stderr=True.\n" +
		"    since_offset: Absolute byte offset to start reading from. Pass 0\n" +
		"        on first call; pass next_offset on subsequent calls.\n" +
		"    max_bytes: Max bytes of decoded output to return inline.\n" +
		"    wait_until_exit: When True, waits for the job to exit before reading.\n" +
		"        Prefer one long wait to repeated short ones: each repeat costs a\n" +
		"        whole model turn. If it returns status=\"running\" because a new\n" +
		"        message arrived, deal with the message, then wait again.\n" +
		"    wait_timeout_sec: Longest to wait, in seconds (up to 600). Does not\n" +
		"        change the job's execution deadline.\n" +
		"    tail: When True, ignores since_offset and returns the last max_bytes.\n" +
		"\n" +
		"Returns: {data, offset, next_offset, status, exit_code, timed_out, eof, truncated_bytes_dropped}"

	// terminal_job_manage
	TerminalJobManageDescription = "List jobs, send signals, or write to job stdin.\n" +
		"\n" +
		"Single tool covering job-control side effects. The action argument\n" +
		"picks the operation:\n" +
		"\n" +
		"- \"capabilities\": return platform, supported_actions and signal semantics.\n" +
		"No job_id required. Check this before choosing a signal.\n" +
		"- \"list\": list active + recently-exited jobs. job_id ignored.\n" +
		"- \"signal_term\" | \"signal_kill\" | \"signal_int\" | \"signal_hup\"\n" +
		"| \"signal_usr1\" | \"signal_usr2\": send the named signal. Requires job_id.\n" +
		"- \"stdin\": write `data` to the job's stdin. Requires job_id and data.\n" +
		"- \"close_stdin\": close the job's stdin pipe (e.g. to flush a tool that\n" +
		"reads until EOF). Requires job_id.\n" +
		"\n" +
		"On POSIX, signal_int requests an interrupt; signal_term requests\n" +
		"termination and forces cleanup after up to 2 seconds. On Windows,\n" +
		"only signal_term and signal_kill are supported: both forcefully\n" +
		"terminate the job process tree, without application cleanup handlers.\n" +
		"Windows signal_int returns unsupported_action without signaling the job.\n" +
		"\n" +
		"Returns vary by action. List → {jobs: [...], capabilities}.\n" +
		"Signals → {ok, signal, semantics}.\n" +
		"Stdin → {bytes_written}."

	// terminal_output_get
	TerminalOutputGetDescription = "Retrieve a slice of truncated output by handle.\n" +
		"\n" +
		"When terminal_exec or terminal_job_logs returns more output than fits inline,\n" +
		"you'll see `output_handle: \"out_<hex>\"`. Pass it here with successive\n" +
		"offsets to paginate. The full output is preserved (combined stdout+stderr\n" +
		"with `--- stdout ---` / `--- stderr ---` separators) for 5 minutes.\n" +
		"\n" +
		"Args:\n" +
		"    output_handle: From a prior tool's envelope.\n" +
		"    since_offset: Pass 0 first, then next_offset from the previous call.\n" +
		"    max_kb: Max KB to return per call.\n" +
		"\n" +
		"Returns: {data, offset, next_offset, eof, expired}"

	// terminal_pty_open
	TerminalPtyOpenDescription = "Open a persistent /bin/bash session in a PTY.\n" +
		"\n" +
		"Use a session when you need state across calls — building env vars,\n" +
		"navigating with cd, driving REPLs, or responding to interactive\n" +
		"prompts (sudo, ssh, mysql). For one-shot work, use terminal_exec\n" +
		"instead.\n" +
		"\n" +
		"The session runs vanilla bash (--norc --noprofile) so dotfiles\n" +
		"don't surprise you. A unique PS1 sentinel is set so terminal_pty_run\n" +
		"can unambiguously detect command completion. macOS users: this\n" +
		"is /bin/bash, not zsh, by deliberate policy — explicit\n" +
		"shell=\"/bin/zsh\" overrides are rejected.\n" +
		"\n" +
		"Args:\n" +
		"    cwd: Initial working directory. Defaults to the session workdir\n" +
		"        when omitted; pass an absolute path to override.\n" +
		"    env: Environment override (zsh dotfile vars are stripped).\n" +
		"    cols, rows: Terminal size.\n" +
		"    idle_timeout_sec: Drop the session after this many seconds idle.\n" +
		"\n" +
		"Returns: {session_id, pid, shell}"

	// terminal_pty_run
	TerminalPtyRunDescription = "Run a command in a session, send raw input, or drain output.\n" +
		"\n" +
		"Three modes:\n" +
		"- Default: pass a command. The session sends it, waits for the\n" +
		"    unique prompt sentinel (or `expect` regex if provided), and\n" +
		"    returns the output between submission and prompt.\n" +
		"- raw_send=True: pass a command. The text is written without\n" +
		"    waiting for prompt. Use for REPL input (\"p('hi')\\n\"), for\n" +
		"    password prompts (sudo), or for vim keystrokes.\n" +
		"- read_only=True: drains whatever's currently buffered.\n" +
		"    Typically follows raw_send.\n" +
		"\n" +
		"Args:\n" +
		"    session_id: From terminal_pty_open.\n" +
		"    command: The text to send. None when read_only=True.\n" +
		"    expect: Regex to wait for INSTEAD of the default prompt sentinel.\n" +
		"        Useful when the command launches a REPL with its own prompt.\n" +
		"    raw_send: Don't wait for prompt; just write.\n" +
		"    read_only: Don't send anything; drain the buffer.\n" +
		"    timeout_sec: Max wait. On timeout, returns whatever's buffered\n" +
		"        with timed_out=True (the command may still be running —\n" +
		"        check with another _run call).\n" +
		"\n" +
		"Returns: {output, prompt_after, timed_out, ...}"

	// terminal_pty_close
	TerminalPtyCloseDescription = "Terminate a PTY session. Always do this when you're done — leaked\n" +
		"sessions count against the per-server PTY cap.\n" +
		"\n" +
		"Args:\n" +
		"    session_id: From terminal_pty_open.\n" +
		"    force: Skip the graceful \"exit\\n\" attempt and SIGTERM/SIGKILL.\n" +
		"\n" +
		"Returns: {exit_code, final_output, already_closed}"
)
