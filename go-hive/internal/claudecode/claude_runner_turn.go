package claudecode

// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._turn
//
// One turn of hers: build her environment, arguments and MCP config, run
// Claude Code to the end (killing its process tree if it runs out of time),
// and read the result object --output-format json prints. A failed turn still
// answers: the writer is told what went wrong, the way upstream does.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/queenmail"
)

// claudeTurnRun is one execution of Claude Code.
type claudeTurnRun struct {
	code     int
	stdout   string
	stderr   string
	timedOut bool
	startErr error
}

// turn runs one turn on prompt; it returns what she ended it with, or why it
// failed (the writer is told either way).
func (r *Runner) turn(conversation *Conversation, prompt string) string {
	config, ok := r.mail.Config().Claude(conversation.QueenID)
	if !ok {
		r.logf(
			"claude code queen %s is no longer in queen_mail.claude_code; what %s wrote to her waits unanswered",
			conversation.QueenID,
			conversation.Peer,
		)
		return ""
	}
	env, problem := r.turnEnv(config)
	if problem != "" {
		return problem
	}
	home := r.store.Home(conversation.QueenID)
	cwd := config.Cwd
	if cwd == "" {
		cwd = filepath.Join(home, "workspace")
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return "(Her reply failed: Claude Code couldn't start: " + err.Error() + ")"
	}
	mcpPath := r.claudeMCPConfig(conversation)
	if mcpPath != "" {
		defer func() { _ = os.Remove(mcpPath) }()
	}

	started := conversation.Started
	var result map[string]any
	code := 0
	var out, stderrText string
	for attempt := 1; attempt <= 2; attempt++ {
		run := r.runClaude(r.turnArgs(config, conversation.SessionID, started, mcpPath), prompt, cwd, env, r.turnTimeout(config))
		if run.timedOut {
			return fmt.Sprintf("(Her reply failed: her turn ran out of time after %d min)", config.TimeoutMinutes)
		}
		if run.startErr != nil {
			return "(Her reply failed: Claude Code couldn't start: " + run.startErr.Error() + ")"
		}
		code, out, stderrText = run.code, run.stdout, run.stderr
		result = parseClaudeResult(out)
		// Our record of whether the session exists can be wrong: its file was
		// deleted (resuming finds nothing), or Hive stopped mid-turn before
		// noting it began (starting it again collides). Claude Code then exits
		// with an error and no result; correct the record and try once more.
		if result == nil && code != 0 && attempt == 1 {
			if started && strings.Contains(stderrText, "No conversation found") {
				started = false
				r.store.SetStarted(conversation.Ref, false)
				continue
			}
			if !started && strings.Contains(stderrText, "already in use") {
				started = true
				r.store.SetStarted(conversation.Ref, true)
				continue
			}
		}
		break
	}
	if result == nil {
		tail := tailChars(strings.TrimSpace(firstNonEmpty(stderrText, out)), 500)
		return fmt.Sprintf("(Her reply failed: Claude Code ended with %d: %s)", code, tail)
	}
	if !boolValue(result["is_error"]) || intValue(result["num_turns"]) > 0 {
		// The session exists now; from here on it is resumed.
		r.store.SetStarted(conversation.Ref, true)
	}
	if boolValue(result["is_error"]) {
		return "(Her reply failed: " + firstNonEmpty(stringValue(result["result"]), stringValue(result["subtype"]), "error") + ")"
	}
	return strings.TrimSpace(stringValue(result["result"]))
}

// runClaude starts Claude Code, waits for it (killing its process tree when
// the clock runs out) and reports its exit code and output.
func (r *Runner) runClaude(args []string, prompt, cwd string, env []string, timeout time.Duration) claudeTurnRun {
	run := r.executeClaude(args, prompt, cwd, env, timeout)
	if r.observe != nil {
		r.observe(run)
	}
	return run
}

// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._run
func (r *Runner) executeClaude(args []string, prompt, cwd string, env []string, timeout time.Duration) claudeTurnRun {
	if len(args) == 0 {
		return claudeTurnRun{startErr: errors.New("no command")}
	}
	command := exec.Command(args[0], args[1:]...)
	command.Dir = cwd
	command.Env = env
	command.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	configureProcessGroup(command)
	// 杀掉进程树后 Wait() 仍要收尾 I/O 拷贝 goroutine：若某个后代短暂持有
	// stdout/stderr 管道，默认会一直等 EOF。WaitDelay 给出确定上限（上游用
	// communicate(timeout)+kill 同样有界），保证被停止的回合不会悬挂。
	command.WaitDelay = 5 * time.Second
	if err := command.Start(); err != nil {
		return claudeTurnRun{startErr: err}
	}
	r.track(command)
	defer r.untrack(command)
	if r.stopped() {
		killProcessTree(command)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
		code := -1
		if command.ProcessState != nil {
			code = command.ProcessState.ExitCode()
		}
		return claudeTurnRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
	case <-timer.C:
		// The turn ran out of time: kill the whole tree and tell the writer
		// promptly. Do NOT block on Wait here: after SIGKILL its I/O copy
		// goroutines can linger up to WaitDelay (5s) when a descendant briefly
		// holds the pipe, which would delay the timeout reply (wave-38 flake).
		// Partial stdout/stderr are intentionally not read -- the copy
		// goroutines may still be writing (data race), and upstream discards
		// them on timeout too.
		killProcessTree(command)
		select {
		case <-done:
		case <-time.After(1 * time.Second):
		}
		return claudeTurnRun{timedOut: true}
	}
}

// turnEnv is the environment of her runs: an allowlist of what a program
// needs to run and reach the network, never Hive's own secrets. Empty when she
// should pay with an API key and has none; the second return is then the reply
// the writer is told.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._env
func (r *Runner) turnEnv(config queenmail.ClaudeQueen) ([]string, string) {
	values := inheritedClaudeEnv(os.Environ())
	configDir := config.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(r.store.Home(config.ID), "config")
	}
	values["CLAUDE_CONFIG_DIR"] = configDir
	switch {
	case config.Auth == "subscription":
		if r.warnOnce(config.ID) {
			r.logf(
				"claude code queen %s runs on the Claude subscription signed in at %s. Running a plan unattended for other agents may break its terms; that is at your own risk. An API key is the supported way.",
				config.ID,
				configDir,
			)
		}
	case config.APIKey != "":
		values["ANTHROPIC_API_KEY"] = config.APIKey
		if config.APIBase != "" {
			values["ANTHROPIC_BASE_URL"] = config.APIBase
		}
	default:
		return nil, "(Her reply failed: she has no API key. Set queen_mail.claude_code." + config.ID +
			".api_key in configuration.json, or \"auth\": \"subscription\" to use a Claude plan signed in at your own risk.)"
	}
	for key, value := range config.Env {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env, ""
}

// inheritedClaudeEnv keeps only the variables a program needs from Hive's
// environment: paths, locale, temp folders and proxies. Nothing that signs in.
var claudeEnvInherited = map[string]bool{
	"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true,
	"COMSPEC": true, "OS": true, "TEMP": true, "TMP": true, "TMPDIR": true,
	"HOME": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true,
	"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true,
	"PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "PROGRAMW6432": true,
	"COMMONPROGRAMFILES": true, "COMMONPROGRAMFILES(X86)": true,
	"NUMBER_OF_PROCESSORS": true, "PROCESSOR_ARCHITECTURE": true,
	"USERNAME": true, "USERDOMAIN": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "TERM": true, "LANG": true, "LANGUAGE": true, "TZ": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
}

// claudeEnvPrefixes are inherited whatever their full name.
var claudeEnvPrefixes = []string{"LC_", "XDG_"}

func inheritedClaudeEnv(environ []string) map[string]string {
	out := map[string]string{}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		upper := strings.ToUpper(key)
		inherited := claudeEnvInherited[upper]
		if !inherited {
			for _, prefix := range claudeEnvPrefixes {
				if strings.HasPrefix(upper, prefix) {
					inherited = true
					break
				}
			}
		}
		if inherited {
			out[key] = value
		}
	}
	return out
}

// turnArgs is the command line of one turn.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._args
func (r *Runner) turnArgs(config queenmail.ClaudeQueen, sessionID string, started bool, mcpPath string) []string {
	command := config.Command
	if len(command) == 0 {
		command = []string{queenmail.DefaultClaudeCommand}
	}
	program := command[0]
	if resolved, err := exec.LookPath(program); err == nil {
		program = resolved
	}
	args := make([]string, 0, len(command)+14)
	args = append(args, program)
	args = append(args, command[1:]...)
	args = append(args, "-p", "--output-format", "json")
	if started {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, "--permission-mode", config.PermissionMode)
	allowed := append([]string(nil), config.AllowedTools...)
	if mcpPath != "" {
		args = append(args, "--mcp-config", mcpPath, "--strict-mcp-config")
		if config.PermissionMode != "bypassPermissions" {
			for _, tool := range claudeMCPTools {
				if !containsString(allowed, tool) {
					allowed = append(allowed, tool)
				}
			}
		}
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, allowed...)
	}
	if len(config.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, config.DisallowedTools...)
	}
	if len(config.AddDirs) > 0 {
		args = append(args, "--add-dir")
		args = append(args, config.AddDirs...)
	}
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	note := claudeSystemNote(config.Name)
	if config.SystemPrompt != "" {
		note += "\n\n" + config.SystemPrompt
	}
	args = append(args, "--append-system-prompt", note)
	return args
}

// claudeMCPConfig writes the MCP config that gives this run the `hive` server:
// a token good for this conversation alone, a file only its owner can read,
// deleted when the run ends. Empty while Hive has no address yet.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._mcp_config
func (r *Runner) claudeMCPConfig(conversation *Conversation) string {
	baseURL := r.mail.BaseURL()
	if baseURL == "" {
		r.logf("claude code queen: no server address yet, so %s can't write letters this turn", conversation.QueenID)
		return ""
	}
	server := map[string]any{
		"type":    "stdio",
		"command": claudeMCPCommand(),
		"args":    []string{"queen-mail-mcp"},
		"env": map[string]string{
			"HIVE_QUEEN_MAIL_URL":   baseURL,
			"HIVE_QUEEN_MAIL_TOKEN": r.mail.TokenFor(conversation.Ref),
			"HIVE_QUEEN_MAIL_FROM":  conversation.Ref,
		},
	}
	payload := map[string]any{"mcpServers": map[string]any{"hive": server}}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		r.logf("claude code queen: couldn't write the MCP config for %s: %v", conversation.QueenID, err)
		return ""
	}
	path := filepath.Join(r.store.Home(conversation.QueenID), "mcp", conversation.SessionID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.logf("claude code queen: couldn't write the MCP config for %s: %v", conversation.QueenID, err)
		return ""
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		r.logf("claude code queen: couldn't write the MCP config for %s: %v", conversation.QueenID, err)
		return ""
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		r.logf("claude code queen: couldn't write the MCP config for %s: %v", conversation.QueenID, err)
		return ""
	}
	if err := file.Close(); err != nil {
		r.logf("claude code queen: couldn't write the MCP config for %s: %v", conversation.QueenID, err)
		return ""
	}
	return path
}

// claudeMCPCommand is the program Claude Code starts for the `hive` MCP
// server: this binary, in its queen-mail-mcp mode.
func claudeMCPCommand() string {
	if executable, err := os.Executable(); err == nil && executable != "" {
		return executable
	}
	return "hive"
}

// claudeSystemNote is what Hive tells her about letters, before her own
// system_prompt is appended.
//
// Parity: framework/server/claude_code_queens.py::_system_prompt
func claudeSystemNote(name string) string {
	return "You are " + name + ", one of the queens of a Hive: agents who write to each other. Other queens write you " +
		"letters; each arrives as \"[Name(queen_id)]: text\", and so does a reply to a letter of yours. What you end " +
		"your turn with goes back to the queen this conversation is with, so just answer. To write to a queen " +
		"yourself, use list_queens and message_queen (the hive MCP server); her answer comes back later as a new " +
		"message in this conversation."
}

// parseClaudeResult is the result object --output-format json prints: the
// whole stdout, or its last JSON line should anything precede it.
//
// Parity: framework/server/claude_code_queens.py::_parse_result
func parseClaudeResult(stdout string) map[string]any {
	trimmed := strings.TrimSpace(stdout)
	if trimmed != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	lines := strings.Split(stdout, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	return nil
}

func boolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// tailChars keeps the last limit characters of text, as upstream's [-500:]
// does.
func tailChars(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[len(runes)-limit:])
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
