package builtin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// terminalShellDirect is the shell spec used when the caller did not ask for a
// shell: the argv list is exec'd as given.
var terminalShellDirect = terminalShellSpec{kind: "direct"}

// terminalShellSpec is how a shell is invoked: binary, flags between the
// binary and the command string, and the dialect the model sees.
type terminalShellSpec struct {
	executable string
	argvPrefix []string
	kind       string
}

func (s terminalShellSpec) buildArgv(command string) []string {
	if s.executable == "" {
		return []string{command}
	}
	argv := make([]string, 0, len(s.argvPrefix)+2)
	argv = append(argv, s.executable)
	argv = append(argv, s.argvPrefix...)
	argv = append(argv, command)
	return argv
}

// terminalZshRefused is returned for an explicit zsh request. zsh has
// constructs (zmodload, =cmd, zpty, ztcp) that bypass the bash-shaped guard
// checks, so the terminal surface is bash-only on POSIX.
type terminalZshRefusedError struct{ shell string }

func (e terminalZshRefusedError) Error() string {
	return fmt.Sprintf("shell=%q rejected: terminal tools are bash-only on POSIX; use bash or omit the shell parameter", e.shell)
}

func terminalKindForPath(path string) (string, []string) {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
	switch {
	case strings.Contains(base, "pwsh"), strings.Contains(base, "powershell"):
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command"}
	case base == "cmd", base == "cmd.exe":
		return "cmd", []string{"/c"}
	}
	return "bash", []string{"-c"}
}

// terminalResolveShellSpec resolves shell=true or an explicit shell path into
// an invocation spec. Exact-zsh spellings are rejected; any other POSIX shell
// is accepted with -c.
func terminalResolveShellSpec(shell bool, explicit string) (terminalShellSpec, error) {
	if !shell && explicit == "" {
		return terminalShellDirect, nil
	}
	if explicit != "" {
		if strings.Contains(strings.ToLower(explicit), "zsh") {
			return terminalShellSpec{}, terminalZshRefusedError{shell: explicit}
		}
		kind, prefix := terminalKindForPath(explicit)
		return terminalShellSpec{executable: explicit, argvPrefix: prefix, kind: kind}, nil
	}
	if runtime.GOOS == "windows" {
		return terminalWindowsShellSpec(), nil
	}
	return terminalShellSpec{executable: "/bin/bash", argvPrefix: []string{"-c"}, kind: "bash"}, nil
}

func terminalWindowsShellSpec() terminalShellSpec {
	if bash := terminalFindWindowsBash(); bash != "" {
		return terminalShellSpec{executable: bash, argvPrefix: []string{"-c"}, kind: "bash"}
	}
	for _, name := range []string{"pwsh", "powershell"} {
		if found, err := exec.LookPath(name); err == nil {
			return terminalShellSpec{executable: found, argvPrefix: []string{"-NoProfile", "-NonInteractive", "-Command"}, kind: "powershell"}
		}
	}
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		return terminalShellSpec{executable: comspec, argvPrefix: []string{"/c"}, kind: "cmd"}
	}
	return terminalShellDirect
}

// terminalFindWindowsBash locates a real Git Bash. System32\bash.exe is the WSL
// launcher and is skipped: it drops into a Linux filesystem view.
func terminalFindWindowsBash() string {
	if override := os.Getenv("HIVE_BASH_PATH"); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override
		}
	}
	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "Programs", "Git", "bin", "bash.exe"))
	}
	if pf := os.Getenv("ProgramW6432"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Git", "bin", "bash.exe"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if found, err := exec.LookPath("bash"); err == nil && !strings.Contains(strings.ToLower(found), "system32") {
		return found
	}
	return ""
}

// terminalSanitizedEnv returns the process environment without zsh startup
// variables, plus the caller's overrides. ZDOTDIR/ZSH_* are stripped so zsh
// dotfiles cannot leak behaviour into the bash subprocess.
func terminalSanitizedEnv(extra map[string]string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	for _, entry := range env {
		name := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			name = entry[:idx]
		}
		if name == "ZDOTDIR" || strings.HasPrefix(name, "ZSH_") {
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+extra[key])
	}
	return out
}

// terminalEnvMap reads an optional string map argument.
func terminalEnvMap(input map[string]any, key string) (map[string]string, bool) {
	v, ok := input[key]
	if !ok || v == nil {
		return nil, true
	}
	switch typed := v.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for name, value := range typed {
			s, ok := value.(string)
			if !ok {
				return nil, false
			}
			out[name] = s
		}
		return out, true
	}
	return nil, false
}

var (
	terminalProtectedProcesses = `(?:google[-_]?chrome\S*|chromium(?:-browser)?|chrome\S*|crashpad\S*|msedge\S*|brave\S*|electron\S*|bridge_host|gcu\.\w+)`
	terminalBrowserBinary      = `(?:[\w./-]*/)?(?:google-chrome(?:-stable|-beta|-unstable)?|chromium(?:-browser)?|chrome)`
	terminalKillVerb           = `(?:pkill|killall|kill)`
	terminalBlockPatterns      = []struct {
		pattern *regexp.Regexp
		reason  string
	}{
		{regexp.MustCompile(`(?i)\b(?:pkill|killall)\b[^\n;|&]*` + terminalProtectedProcesses), "kills browser/runtime processes by name"},
		{regexp.MustCompile(`(?i)\b` + terminalKillVerb + `\b[^\n]*\$\([^)]*grep[^)]*` + terminalProtectedProcesses), "kills browser/runtime processes via a ps/grep pipeline"},
		{regexp.MustCompile(`(?i)grep\b[^\n]*` + terminalProtectedProcesses + `[^\n]*\|\s*(?:xargs\s+)?` + terminalKillVerb + `\b`), "kills browser/runtime processes via a ps/grep pipeline"},
		{regexp.MustCompile(`(?i)(?:^|[;&|]\s*|\b(?:nohup|setsid|exec|env)\s+)` + terminalBrowserBinary + `(?:\s|$)`), "launches a browser process"},
		{regexp.MustCompile(`(?i)\bstop-process\b[^\n]*` + terminalProtectedProcesses), "kills browser/runtime processes (PowerShell Stop-Process)"},
		{regexp.MustCompile(`(?i)\bget-process\b[^\n]*` + terminalProtectedProcesses + `[^\n]*\|\s*(?:[^|\n]*\|\s*)?stop-process\b`), "kills browser/runtime processes (PowerShell Get-Process | Stop-Process)"},
		{regexp.MustCompile(`(?i)\btaskkill\b[^\n]*` + terminalProtectedProcesses), "kills browser/runtime processes (taskkill)"},
		{regexp.MustCompile(`(?i)\b(?:start-process|start)\b[^\n;|&]*` + terminalBrowserBinary + `\b`), "launches a browser process"},
	}
)

// terminalBlockedMessage is the hard-block text for browser kill/launch
// commands. Unlike the advisory destructive warning this stops the command
// before spawn: the runtime and the user own the browser's lifecycle.
const terminalBlockedMessage = "BLOCKED: this command %s. The Hive runtime and the user own the " +
	"browser's lifecycle — the bridge attaches to the user's ALREADY-RUNNING " +
	"Chrome via an extension, so killing or launching browser processes " +
	"destroys the user's session and every other agent's work, and there is " +
	"nothing for an agent to 'start'. If `hive-browser` commands are timing out " +
	"or erroring, that is a transport issue the runtime recovers from " +
	"automatically: wait ~30s, retry once, then report_to_parent and move on. " +
	"Allowed cleanup is limited to your own tabs via `hive-browser tab close`."

// terminalCheckCommand returns a block message when the command must not run.
func terminalCheckCommand(command string) string {
	for _, entry := range terminalBlockPatterns {
		if entry.pattern.MatchString(command) {
			return fmt.Sprintf(terminalBlockedMessage, entry.reason)
		}
	}
	return ""
}

var errTerminalEmptyCommand = errors.New("command was empty")
