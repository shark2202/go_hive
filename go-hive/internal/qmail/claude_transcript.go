package qmail

// Parity: framework/server/claude_code_queens.py::_read_transcript
//
// The dashboard's read-only view of a Claude Code queen reads what was said
// and done in one of her conversations out of Claude Code's own session file
// (CLAUDE_CONFIG_DIR/projects/<project>/<session id>.jsonl), which is what
// holds the letters she was handed. This file turns one of those files into
// the entries the routes serve.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/mcpregistry/nativecli"
)

// TranscriptEntry is one thing in one of her conversations: what she was
// handed ("letter"), what she said ("text"), a tool she called ("tool", named
// in Tool) or what it returned ("result", Error when it failed).
type TranscriptEntry struct {
	At    string
	Kind  string
	Text  string
	Tool  string
	Error bool
}

// How much of one tool call's input, and of one tool result, a transcript
// keeps (upstream _TOOL_INPUT_CHARS / _TOOL_RESULT_CHARS).
const (
	claudeToolInputChars  = 600
	claudeToolResultChars = 2000
)

// SessionFile is Claude Code's file for one of a queen's sessions, under the
// project folder of wherever it ran; "" when there is none. Her config folder
// is config_dir, or HIVE_HOME/claude_code/<id>/config as the runner uses it.
//
// Parity: framework/server/claude_code_queens.py::_session_file
func (c *ClaudeStore) SessionFile(queen ClaudeQueen, sessionID string) string {
	configDir := queen.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(c.Home(queen.ID), "config")
	}
	if !validClaudePathSegment(sessionID) {
		return ""
	}
	projects := filepath.Join(configDir, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return ""
	}
	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(projects, entry.Name(), sessionID+".jsonl")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			matches = append(matches, path)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	// Sorted like upstream's sorted(projects.glob(...)): the first of them.
	sort.Strings(matches)
	return matches[0]
}

// ReadTranscript reads one of Claude Code's session files as the entries the
// dashboard shows, in order. Her thinking, her subagents' own turns and Claude
// Code's bookkeeping are left out. A missing or unreadable file reads as no
// entries.
//
// Parity: framework/server/claude_code_queens.py::_read_transcript
func ReadTranscript(path string) []TranscriptEntry {
	entries := []TranscriptEntry{}
	if path == "" {
		return entries
	}
	file, err := os.Open(path)
	if err != nil {
		return entries
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			entries = appendTranscriptRecord(entries, line)
		}
		if readErr != nil {
			break
		}
	}
	return entries
}

// appendTranscriptRecord keeps the entries of one session-file line, skipping
// anything that is not a user or assistant turn.
func appendTranscriptRecord(entries []TranscriptEntry, line []byte) []TranscriptEntry {
	if strings.TrimSpace(string(line)) == "" {
		return entries
	}
	value, err := nativecli.ParseJSON(line)
	if err != nil {
		return entries
	}
	record, ok := value.(nativecli.Ordered)
	if !ok {
		return entries
	}
	recordType := orderedStringField(record, "type")
	if recordType != "user" && recordType != "assistant" {
		return entries
	}
	if orderedTruthy(record, "isMeta") || orderedTruthy(record, "isSidechain") {
		return entries
	}
	message, ok := orderedValue(record, "message").(nativecli.Ordered)
	if !ok {
		return entries
	}
	at := claudeTextValue(orderedValue(record, "timestamp"))
	for _, part := range transcriptParts(orderedValue(message, "content")) {
		entries = appendTranscriptPart(entries, at, recordType, part)
	}
	return entries
}

// transcriptParts is one record's content as parts: a bare string is one text
// part, a list keeps the objects in it, anything else has none.
func transcriptParts(content any) []nativecli.Ordered {
	switch typed := content.(type) {
	case string:
		text := nativecli.Ordered{nativecli.Field{Key: "type", Value: "text"}, nativecli.Field{Key: "text", Value: typed}}
		return []nativecli.Ordered{text}
	case []any:
		parts := make([]nativecli.Ordered, 0, len(typed))
		for _, raw := range typed {
			if part, ok := raw.(nativecli.Ordered); ok {
				parts = append(parts, part)
			}
		}
		return parts
	}
	return nil
}

func appendTranscriptPart(entries []TranscriptEntry, at, recordType string, part nativecli.Ordered) []TranscriptEntry {
	switch orderedStringField(part, "type") {
	case "text":
		text := claudeTextValue(orderedValue(part, "text"))
		if strings.TrimSpace(text) == "" {
			return entries
		}
		kind := "text"
		if recordType == "user" {
			kind = "letter"
		}
		return append(entries, TranscriptEntry{At: at, Kind: kind, Text: text})
	case "tool_use":
		// json.dumps of the call's input, the way Python renders it (key order
		// and separators included).
		return append(entries, TranscriptEntry{
			At:   at,
			Kind: "tool",
			Tool: claudeTextValue(orderedValue(part, "name")),
			Text: clipTranscriptText(nativecli.MarshalPy(orderedValue(part, "input")), claudeToolInputChars),
		})
	case "tool_result":
		return append(entries, TranscriptEntry{
			At:    at,
			Kind:  "result",
			Text:  clipTranscriptText(claudeResultText(orderedValue(part, "content")), claudeToolResultChars),
			Error: orderedValue(part, "is_error") == true,
		})
	}
	return entries
}

// claudeResultText is what one tool result holds: its content as text, the
// text parts joined by newlines when it is a list of parts.
func claudeResultText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		texts := make([]string, 0, len(typed))
		for _, raw := range typed {
			part, ok := raw.(nativecli.Ordered)
			if !ok || orderedStringField(part, "type") != "text" {
				continue
			}
			texts = append(texts, claudeTextValue(orderedValue(part, "text")))
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// clipTranscriptText keeps text as Python's _clip does: the whole text when it
// is short enough, else its first limit-1 code points and an ellipsis.
func clipTranscriptText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// claudeTextValue is Python's str(value or "") for the values a session file's
// text fields carry.
func claudeTextValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		return typed.String()
	}
	return ""
}

func orderedValue(object nativecli.Ordered, key string) any {
	value, _ := object.Lookup(key)
	return value
}

// orderedStringField is object[key] when it is a string, "" otherwise; the
// fields upstream compares to string constants read this way.
func orderedStringField(object nativecli.Ordered, key string) string {
	text, _ := orderedValue(object, key).(string)
	return text
}

// orderedTruthy is Python truthiness for isMeta/isSidechain: an absent or
// falsy value leaves a record in.
func orderedTruthy(object nativecli.Ordered, key string) bool {
	switch typed := orderedValue(object, key).(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		number, err := typed.Float64()
		return err != nil || number != 0
	case []any:
		return len(typed) > 0
	case nativecli.Ordered:
		return len(typed) > 0
	}
	return fmt.Sprint(orderedValue(object, key)) != ""
}
