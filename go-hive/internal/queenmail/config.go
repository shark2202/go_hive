// Package queenmail carries letters between queens: a durable letter store, a
// pending-delivery queue, delivery to Hive queens (through a port the server
// adapter implements) and the audit log at queens/mail.jsonl.
//
// Parity: framework/server/queen_mail.py
package queenmail

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	// DefaultDailyLimit is how many letters one queen may send a day when the
	// configuration says nothing (queen_mail.daily_limit).
	DefaultDailyLimit = 100
	// DefaultMaxDepth is how many hops one chain of letters may run.
	DefaultMaxDepth = 8
	// DefaultMaxParallelTurns mirrors queen_mail.max_parallel_turns.
	DefaultMaxParallelTurns = 2
	// DefaultClaudeTitle is the title a Claude Code queen shows when her entry
	// names none.
	DefaultClaudeTitle = "Claude Code"
	// DefaultClaudeCommand is the Claude Code program an entry runs when it
	// names none.
	DefaultClaudeCommand = "claude"
	// DefaultClaudePermissionMode refuses, in a headless run, every tool that
	// would ask first.
	DefaultClaudePermissionMode = "default"
	// DefaultClaudeTimeoutMinutes is the longest one turn of hers may run.
	DefaultClaudeTimeoutMinutes = 30
)

// ClaudeQueen is one queen run by Claude Code, as queen_mail.claude_code
// configures her.
//
// Parity: framework/config.py::ClaudeCodeQueenConfig
type ClaudeQueen struct {
	ID    string
	Name  string
	Title string
	// Command is the Claude Code program and any arguments to put before
	// Hive's own.
	Command []string
	// Auth is how she pays: "api_key" (the supported way) or "subscription"
	// (a Claude plan signed in in her config folder, at the user's risk).
	Auth   string
	APIKey string
	// APIBase is ANTHROPIC_BASE_URL for her runs: a gateway in front of the
	// API.
	APIBase string
	// ConfigDir is her CLAUDE_CONFIG_DIR; empty uses
	// HIVE_HOME/claude_code/<id>/config.
	ConfigDir string
	// Cwd is the folder she works in; empty uses
	// HIVE_HOME/claude_code/<id>/workspace.
	Cwd string
	// PermissionMode is passed to --permission-mode as is.
	PermissionMode string
	AllowedTools   []string
	// DisallowedTools and AddDirs are passed to --disallowedTools/--add-dir.
	DisallowedTools []string
	AddDirs         []string
	// Model is passed to --model; empty leaves her own default.
	Model string
	// TimeoutMinutes is the longest one turn of hers may run.
	TimeoutMinutes int
	// SystemPrompt is appended to her system prompt after Hive's note on how
	// letters reach her and where her answers go.
	SystemPrompt string
	// Env holds extra environment variables for her runs.
	Env map[string]string
}

// String redacts the API key, as upstream's repr=False does, so a logged or
// dumped configuration never prints a secret.
func (q ClaudeQueen) String() string {
	redacted := q
	if redacted.APIKey != "" {
		redacted.APIKey = "«redacted»"
	}
	type plain ClaudeQueen
	return fmt.Sprintf("%+v", plain(redacted))
}

// Config is the queen_mail section, with the defaults upstream applies to
// anything missing or invalid.
type Config struct {
	Enabled          bool
	Queens           []string
	DailyLimit       int
	MaxDepth         int
	MaxParallelTurns int
	ClaudeCode       map[string]ClaudeQueen
}

// DefaultConfig is queen_mail's "off" configuration.
func DefaultConfig() Config {
	return Config{
		DailyLimit:       DefaultDailyLimit,
		MaxDepth:         DefaultMaxDepth,
		MaxParallelTurns: DefaultMaxParallelTurns,
		ClaudeCode:       map[string]ClaudeQueen{},
	}
}

// ConfigFromHive reads the queen_mail section out of an already-parsed
// configuration.json. Missing or wrongly typed values keep their defaults, and
// `queens` that is not a list means every queen takes part.
func ConfigFromHive(hive map[string]any) Config {
	config := DefaultConfig()
	section, _ := hive["queen_mail"].(map[string]any)
	if section == nil {
		return config
	}

	// Parity: framework/config.py::get_queen_mail_config (enabled is `is True`)
	config.Enabled = section["enabled"] == true

	if rawQueens, present := section["queens"]; present {
		if list, ok := rawQueens.([]any); ok {
			queens := make([]string, 0, len(list))
			for _, item := range list {
				if text, ok := item.(string); ok && text != "" {
					queens = append(queens, text)
				}
			}
			config.Queens = queens
		}
	}

	config.DailyLimit = positiveInt(section["daily_limit"], config.DailyLimit)
	config.MaxDepth = positiveInt(section["max_depth"], config.MaxDepth)
	config.MaxParallelTurns = positiveInt(section["max_parallel_turns"], config.MaxParallelTurns)

	if rawClaude, ok := section["claude_code"].(map[string]any); ok {
		for queenID, rawEntry := range rawClaude {
			entry, ok := rawEntry.(map[string]any)
			if !ok || queenID == "" {
				continue
			}
			config.ClaudeCode[queenID] = ClaudeQueenFromEntry(queenID, entry)
		}
	}
	return config
}

// ClaudeQueenFromEntry reads one queen_mail.claude_code entry, with upstream's
// defaults for anything missing or wrongly typed.
//
// Parity: framework/config.py::_claude_code_queen
func ClaudeQueenFromEntry(queenID string, entry map[string]any) ClaudeQueen {
	queen := ClaudeQueen{
		ID:              queenID,
		Name:            textOr(entry["name"], queenID),
		Title:           textOr(entry["title"], DefaultClaudeTitle),
		Command:         commandOf(entry["command"]),
		Auth:            authOf(entry["auth"]),
		APIKey:          textOr(entry["api_key"], ""),
		APIBase:         textOr(entry["api_base"], ""),
		ConfigDir:       textOr(entry["config_dir"], ""),
		Cwd:             textOr(entry["cwd"], ""),
		PermissionMode:  textOr(entry["permission_mode"], DefaultClaudePermissionMode),
		AllowedTools:    stringsList(entry["allowed_tools"]),
		DisallowedTools: stringsList(entry["disallowed_tools"]),
		AddDirs:         stringsList(entry["add_dirs"]),
		Model:           textOr(entry["model"], ""),
		TimeoutMinutes:  positiveInt(entry["timeout_minutes"], DefaultClaudeTimeoutMinutes),
		SystemPrompt:    textOr(entry["system_prompt"], ""),
		Env:             envMap(entry["env"]),
	}
	return normalizeClaudeQueen(queen)
}

// normalizeClaudeQueen fills the defaults a programmatically built entry may
// leave out, so the runner reads one shape whichever way the config arrived.
func normalizeClaudeQueen(queen ClaudeQueen) ClaudeQueen {
	if queen.Name == "" {
		queen.Name = queen.ID
	}
	if queen.Title == "" {
		queen.Title = DefaultClaudeTitle
	}
	if len(queen.Command) == 0 {
		queen.Command = []string{DefaultClaudeCommand}
	}
	if queen.Auth != "api_key" && queen.Auth != "subscription" {
		queen.Auth = "api_key"
	}
	if queen.PermissionMode == "" {
		queen.PermissionMode = DefaultClaudePermissionMode
	}
	if queen.TimeoutMinutes < 1 {
		queen.TimeoutMinutes = DefaultClaudeTimeoutMinutes
	}
	return queen
}

// commandOf is upstream's command parsing: a non-blank string is the program
// on its own; a list is the program and its arguments; anything else falls
// back to "claude".
func commandOf(value any) []string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return []string{text}
	}
	if command := stringsList(value); len(command) > 0 {
		return command
	}
	return []string{DefaultClaudeCommand}
}

func authOf(value any) string {
	if auth, ok := value.(string); ok && (auth == "api_key" || auth == "subscription") {
		return auth
	}
	return "api_key"
}

// stringsList keeps the non-empty strings of a JSON list, the way upstream's
// texts() does; a non-list is no list at all.
func stringsList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

// envMap keeps the string-to-string pairs of a JSON object, as upstream's env
// parsing does.
func envMap(value any) map[string]string {
	entries, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(entries))
	for key, raw := range entries {
		if text, ok := raw.(string); ok && key != "" {
			out[key] = text
		}
	}
	return out
}

// Includes reports whether queenID may write and be written to: the feature is
// on, and `queens` is unset, names her, or lists her among the Claude Code
// queens (who take part whatever `queens` says).
func (c Config) Includes(queenID string) bool {
	if !c.Enabled {
		return false
	}
	if _, claude := c.ClaudeCode[queenID]; claude {
		return true
	}
	if c.Queens == nil {
		return true
	}
	for _, listed := range c.Queens {
		if listed == queenID {
			return true
		}
	}
	return false
}

// Claude returns the configured Claude Code queen queenID names.
func (c Config) Claude(queenID string) (ClaudeQueen, bool) {
	queen, ok := c.ClaudeCode[queenID]
	return queen, ok
}

// positiveInt is Python's `isinstance(value, int) and value >= 1` guard with a
// fallback; json.Number keeps configuration.json's integer spelling.
func positiveInt(value any, fallback int) int {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed >= 1 {
			return int(parsed)
		}
	case float64:
		if typed >= 1 && typed == math.Trunc(typed) {
			return int(typed)
		}
	case float32:
		return positiveInt(float64(typed), fallback)
	case int:
		if typed >= 1 {
			return typed
		}
	case int64:
		if typed >= 1 {
			return int(typed)
		}
	}
	return fallback
}

func textOr(value any, fallback string) string {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}
