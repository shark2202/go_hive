// Package qmail carries letters between queens: a durable letter store, a
// pending-delivery queue, delivery to Hive queens (through a port the server
// adapter implements) and the audit log at queens/mail.jsonl.
//
// Parity: framework/server/queen_mail.py
package qmail

import (
	"encoding/json"
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
)

// ClaudeQueen is one queen run by Claude Code, as queen_mail.claude_code
// configures her. The backend only needs who she is; her runner is separate.
type ClaudeQueen struct {
	ID    string
	Name  string
	Title string
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
			config.ClaudeCode[queenID] = ClaudeQueen{
				ID:    queenID,
				Name:  textOr(entry["name"], queenID),
				Title: textOr(entry["title"], DefaultClaudeTitle),
			}
		}
	}
	return config
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
