package queenmail

import (
	"strings"
	"testing"
)

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_claude_code_queen_takes_part_whatever_queens_says
func TestHerEntryKeepsUpstreamDefaultsAndTakesPart(t *testing.T) {
	hive := decodeSection(t, `{"queen_mail": {"enabled": true, "queens": ["queen_ava", "queen_ivy"],
		"claude_code": {"queen_claude": {"name": "Claude", "title": "Engineer", "timeout_minutes": 5}}}}`)
	config := ConfigFromHive(hive)

	if !config.Includes(claude) || config.Includes(zed) {
		t.Fatalf("includes: claude=%v zed=%v", config.Includes(claude), config.Includes(zed))
	}
	entry, ok := config.Claude(claude)
	if !ok {
		t.Fatal("queen_claude missing")
	}
	if entry.Name != "Claude" || entry.Title != "Engineer" || entry.TimeoutMinutes != 5 || entry.PermissionMode != "default" {
		t.Fatalf("entry = %+v", entry)
	}
	if got := strings.Join(entry.Command, " "); got != "claude" {
		t.Fatalf("command = %q", got)
	}
	if entry.Auth != "api_key" {
		t.Fatalf("auth = %q", entry.Auth)
	}
}

// Parity-exempt: 上游只在 monkeypatch 中覆盖少量键；框架侧 config.py::_claude_code_queen
// 的完整解析（command 的两种形态、auth 过滤、env、工具列表、timeout 校验）没有逐一 pytest，
// 这里把每个键的行为钉住。
func TestClaudeQueenEntryParsesEveryField(t *testing.T) {
	hive := decodeSection(t, `{"queen_mail": {"enabled": true, "claude_code": {"q": {
		"name": "Claude", "title": "Engineer",
		"command": ["/usr/local/bin/claude", "--debug"],
		"auth": "subscription", "api_key": "sk-x", "api_base": "https://gw.example",
		"config_dir": "/tmp/cfg", "cwd": "/tmp/cwd", "permission_mode": "acceptEdits",
		"allowed_tools": ["Bash", "", "Read"], "disallowed_tools": ["WebFetch"],
		"add_dirs": ["/data"], "model": "m-1", "timeout_minutes": 7,
		"system_prompt": "Be terse.",
		"env": {"FOO": "bar", "N": 1, "K": true}
	}}}}`)
	entry := ConfigFromHive(hive).ClaudeCode["q"]

	if entry.Name != "Claude" || entry.Title != "Engineer" {
		t.Fatalf("name/title = %q/%q", entry.Name, entry.Title)
	}
	if strings.Join(entry.Command, " ") != "/usr/local/bin/claude --debug" {
		t.Fatalf("command = %#v", entry.Command)
	}
	if entry.Auth != "subscription" || entry.APIKey != "sk-x" || entry.APIBase != "https://gw.example" {
		t.Fatalf("auth/key/base = %q/%q/%q", entry.Auth, entry.APIKey, entry.APIBase)
	}
	if entry.ConfigDir != "/tmp/cfg" || entry.Cwd != "/tmp/cwd" || entry.PermissionMode != "acceptEdits" {
		t.Fatalf("dirs/mode = %q/%q/%q", entry.ConfigDir, entry.Cwd, entry.PermissionMode)
	}
	if strings.Join(entry.AllowedTools, ",") != "Bash,Read" || strings.Join(entry.DisallowedTools, ",") != "WebFetch" ||
		strings.Join(entry.AddDirs, ",") != "/data" {
		t.Fatalf("tools = %#v/%#v/%#v", entry.AllowedTools, entry.DisallowedTools, entry.AddDirs)
	}
	if entry.Model != "m-1" || entry.TimeoutMinutes != 7 || entry.SystemPrompt != "Be terse." {
		t.Fatalf("model/timeout/prompt = %q/%d/%q", entry.Model, entry.TimeoutMinutes, entry.SystemPrompt)
	}
	if len(entry.Env) != 1 || entry.Env["FOO"] != "bar" {
		t.Fatalf("env = %#v (non-string values must be left out)", entry.Env)
	}

	// A blank command string, an unknown auth, a bool timeout and a non-list
	// of tools all fall back the way upstream's guards do.
	hive = decodeSection(t, `{"queen_mail": {"enabled": true, "claude_code": {"q": {
		"command": "  ", "auth": "token", "timeout_minutes": true,
		"allowed_tools": "Bash", "add_dirs": [1, "ok", ""]
	}}}}`)
	entry = ConfigFromHive(hive).ClaudeCode["q"]
	if strings.Join(entry.Command, " ") != "claude" || entry.Auth != "api_key" || entry.TimeoutMinutes != DefaultClaudeTimeoutMinutes {
		t.Fatalf("fallbacks = %#v / %q / %d", entry.Command, entry.Auth, entry.TimeoutMinutes)
	}
	if entry.AllowedTools != nil || strings.Join(entry.AddDirs, ",") != "ok" {
		t.Fatalf("lists = %#v / %#v", entry.AllowedTools, entry.AddDirs)
	}

	// A single string command is the program on its own.
	hive = decodeSection(t, `{"queen_mail": {"enabled": true, "claude_code": {"q": {"command": "/opt/claude"}}}}`)
	if got := ConfigFromHive(hive).ClaudeCode["q"]; len(got.Command) != 1 || got.Command[0] != "/opt/claude" {
		t.Fatalf("string command = %#v", got.Command)
	}
}
