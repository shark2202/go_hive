// Package queenmail carries letters between queens: a durable letter store, a
// pending-delivery queue, delivery to Hive queens (through a port the server
// adapter implements) and the audit log at queens/mail.jsonl.
//
// Parity: framework/server/queen_mail.py
package queenmail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
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
	// claudeCodeOrder is the queen_mail.claude_code key order read from the
	// JSON source. Go maps lose insertion order, while upstream's
	// config.claude_code is an ordered dict: correspondents() and the
	// dashboard list render her entries in the order the user wrote them.
	// ConfigFromHive cannot recover it and falls back to sorted keys;
	// ConfigFromJSON/LoadConfig preserve it.
	claudeCodeOrder []string
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
	// A map has no insertion order: use sorted keys so a caller that only has
	// the decoded section still renders deterministically. LoadConfig replaces
	// this with the source order.
	config.claudeCodeOrder = sortedClaudeIDs(config.ClaudeCode)
	return config
}

// ConfigFromJSON reads configuration.json bytes and preserves the
// queen_mail.claude_code key order, which get_queen_mail_config keeps in its
// ordered dict. It applies the same tolerance as hiveconf.Load: a BOM is
// stripped, a non-object or trailing-data document reads as the default
// configuration.
//
// Parity: framework/config.py::get_queen_mail_config (dict insertion order)
func ConfigFromJSON(raw []byte) Config {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var hive map[string]any
	if err := decoder.Decode(&hive); err != nil || hive == nil {
		return DefaultConfig()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return DefaultConfig()
	}
	config := ConfigFromHive(hive)
	config.setClaudeCodeOrder(orderedKeysAtPath(raw, "queen_mail", "claude_code"))
	return config
}

// LoadConfig reads <HIVE_HOME>/configuration.json with source order intact.
func LoadConfig(home hivehome.Home) Config {
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return DefaultConfig()
	}
	return ConfigFromJSON(data)
}

// ClaudeQueens returns the Claude Code queens in configuration order. Entries
// the order cannot name (a programmatically built Config) are appended by id
// so no configured queen drops out.
func (c Config) ClaudeQueens() []ClaudeQueen {
	if len(c.ClaudeCode) == 0 {
		return nil
	}
	out := make([]ClaudeQueen, 0, len(c.ClaudeCode))
	seen := make(map[string]struct{}, len(c.ClaudeCode))
	for _, id := range c.claudeCodeOrder {
		queen, ok := c.ClaudeCode[id]
		if !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, queen)
	}
	for _, id := range sortedClaudeIDs(c.ClaudeCode) {
		if _, done := seen[id]; done {
			continue
		}
		out = append(out, c.ClaudeCode[id])
	}
	return out
}

// setClaudeCodeOrder installs the source order, keeping only configured ids
// and appending any map entries the source order did not name.
func (c *Config) setClaudeCodeOrder(order []string) {
	if c == nil {
		return
	}
	out := make([]string, 0, len(c.ClaudeCode))
	seen := make(map[string]struct{}, len(c.ClaudeCode))
	for _, id := range order {
		if _, present := c.ClaudeCode[id]; !present {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range sortedClaudeIDs(c.ClaudeCode) {
		if _, done := seen[id]; done {
			continue
		}
		out = append(out, id)
	}
	c.claudeCodeOrder = out
}

func sortedClaudeIDs(configured map[string]ClaudeQueen) []string {
	ids := make([]string, 0, len(configured))
	for id := range configured {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// orderedKeysAtPath walks the raw JSON document down path and returns the
// final object's keys in source order. A missing/malformed path yields nil.
func orderedKeysAtPath(raw []byte, path ...string) []string {
	current := raw
	for _, key := range path {
		current = rawObjectMember(current, key)
		if len(current) == 0 {
			return nil
		}
	}
	return orderedObjectKeys(current)
}

// rawObjectMember returns one JSON object member's raw value without decoding
// the object into a map (which would lose its order).
func rawObjectMember(raw []byte, key string) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		name, ok := keyToken.(string)
		if !ok {
			return nil
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
		if name == key {
			return value
		}
	}
	return nil
}

// orderedObjectKeys lists one JSON object's keys in source order.
func orderedObjectKeys(raw []byte) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	keys := []string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return keys
		}
		key, ok := keyToken.(string)
		if !ok {
			return keys
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return keys
		}
		keys = append(keys, key)
	}
	return keys
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
