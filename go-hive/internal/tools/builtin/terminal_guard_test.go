package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// Parity: tools/tests/test_terminal_tools_security.py::test_resolve_shell_rejects_zsh
func TestTerminalResolveShellRejectsZsh(t *testing.T) {
	for _, path := range []string{"/bin/zsh", "/usr/bin/zsh", "/usr/local/bin/zsh", "ZSH"} {
		if _, err := terminalResolveShellSpec(false, path); err == nil {
			t.Fatalf("shell %q accepted, want refused", path)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_security.py::test_resolve_shell_accepts_bash
func TestTerminalResolveShellAcceptsBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves shell=True to Git Bash/PowerShell/cmd, not /bin/bash")
	}
	spec, err := terminalResolveShellSpec(true, "")
	if err != nil {
		t.Fatalf("shell=true: %v", err)
	}
	if spec.executable != "/bin/bash" || spec.kind != "bash" {
		t.Fatalf("shell=true spec = %+v, want /bin/bash", spec)
	}
	if argv := spec.buildArgv("echo hi"); len(argv) != 3 || argv[0] != "/bin/bash" || argv[1] != "-c" || argv[2] != "echo hi" {
		t.Fatalf("argv = %v", argv)
	}

	explicit, err := terminalResolveShellSpec(true, "/bin/bash")
	if err != nil {
		t.Fatalf("explicit bash: %v", err)
	}
	if explicit.executable != "/bin/bash" {
		t.Fatalf("explicit spec = %+v", explicit)
	}
	direct, err := terminalResolveShellSpec(false, "")
	if err != nil {
		t.Fatalf("shell=false: %v", err)
	}
	if direct.executable != "" || direct.kind != "direct" {
		t.Fatalf("direct spec = %+v", direct)
	}
}

// Parity: tools/tests/test_terminal_tools_security.py::test_sanitized_env_strips_zsh_vars
func TestTerminalSanitizedEnvStripsZshVars(t *testing.T) {
	t.Setenv("ZDOTDIR", "/some/path")
	t.Setenv("ZSH_VERSION", "5.9")
	t.Setenv("ZSH_NAME", "zsh")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := terminalSanitizedEnv(map[string]string{"TERM": "xterm-256color"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, stripped := range []string{"ZDOTDIR=", "ZSH_VERSION=", "ZSH_NAME="} {
		if strings.Contains(joined, "\n"+stripped) {
			t.Fatalf("%s survived sanitization: %v", stripped, env)
		}
	}
	if !strings.Contains(joined, "\nPATH=/usr/bin:/bin\n") {
		t.Fatalf("PATH missing from sanitized env")
	}
	if !strings.Contains(joined, "\nTERM=xterm-256color\n") {
		t.Fatalf("override missing from sanitized env")
	}
	if os.Getenv("ZDOTDIR") != "/some/path" {
		t.Fatal("sanitization mutated the process environment")
	}
}

// Parity: tools/tests/test_terminal_tools_security.py::test_command_guard_blocks_windows_browser_kills
func TestTerminalCommandGuardBlocksBrowserKills(t *testing.T) {
	blocked := []string{
		"pkill -9 -f chrome",
		"killall Google Chrome",
		"kill $(ps aux | grep -i chrome | awk '{print $2}')",
		"ps aux | grep chrome | xargs kill -9",
		"Stop-Process -Name chrome",
		"Stop-Process -Name msedge -Force",
		"Get-Process chrome | Stop-Process -Force",
		"Get-Process chrome | Where-Object { $_.CPU -gt 1 } | Stop-Process",
		"taskkill /IM chrome.exe /F",
		"taskkill /F /IM msedge.exe",
		"Stop-Process -Name bridge_host",
	}
	for _, cmd := range blocked {
		if terminalCheckCommand(cmd) == "" {
			t.Fatalf("should block: %q", cmd)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_security.py::test_command_guard_blocks_windows_browser_launch
func TestTerminalCommandGuardBlocksBrowserLaunch(t *testing.T) {
	for _, cmd := range []string{"google-chrome --profile-directory=Default", "Start-Process chrome", "start chrome", `Start-Process "chrome.exe"`, "nohup chromium --remote-debugging-port=9222 &"} {
		if terminalCheckCommand(cmd) == "" {
			t.Fatalf("should block: %q", cmd)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_security.py::test_command_guard_allows_clean_windows_commands
func TestTerminalCommandGuardAllowsCleanCommands(t *testing.T) {
	allowed := []string{
		"Get-Process | Sort-Object CPU -Descending",
		`Get-ChildItem C:\Users`,
		"taskkill /IM mytool.exe",
		"Stop-Process -Name myworker",
		"echo chrome",
		"Start-Process notepad",
		"ps aux | grep chrome",
		"which google-chrome",
		"rg -n pattern .",
	}
	for _, cmd := range allowed {
		if msg := terminalCheckCommand(cmd); msg != "" {
			t.Fatalf("should allow %q, blocked with %q", cmd, msg)
		}
	}
}
