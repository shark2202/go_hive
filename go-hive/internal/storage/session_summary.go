package storage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const (
	summaryFilename       = "summary.json"
	lastMessageMaxChars   = 120
	summaryStaleTolerance = 0.001
)

type SessionSummary struct {
	MessageCount int     `json:"message_count"`
	LastMessage  *string `json:"last_message"`
	LastActiveAt float64 `json:"last_active_at"`
	LastPartSeq  *int    `json:"last_part_seq"`
}

func IsClientFacing(part map[string]any) bool {
	if jsonTruthy(part["is_transition_marker"]) {
		return false
	}
	role, _ := part["role"].(string)
	if role == "tool" {
		return false
	}
	if role == "assistant" && jsonTruthy(part["tool_calls"]) {
		return false
	}
	return true
}

func ExtractText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		texts := []string{}
		for _, block := range v {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t != "text" {
				continue
			}
			if text, ok := m["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, " ")
	default:
		return ""
	}
}

func summaryPath(sessionDir string) string {
	return filepath.Join(sessionDir, summaryFilename)
}

func ReadSummary(sessionDir string) *SessionSummary {
	path := summaryPath(sessionDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var summary SessionSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil
	}
	return &summary
}

func IsStale(sessionDir string) bool {
	path := summaryPath(sessionDir)
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	summaryMtime := fileMtimeSeconds(info)

	convsDir := filepath.Join(sessionDir, "conversations")
	if _, err := os.Stat(convsDir); err != nil {
		return false
	}

	candidates := []string{filepath.Join(convsDir, "parts")}
	children, err := os.ReadDir(convsDir)
	if err != nil {
		return false
	}
	for _, child := range children {
		if child.IsDir() && child.Name() != "parts" {
			candidates = append(candidates, filepath.Join(convsDir, child.Name(), "parts"))
		}
	}
	for _, dir := range candidates {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if fileMtimeSeconds(dirInfo) > summaryMtime+summaryStaleTolerance {
			return true
		}
	}
	return false
}

func UpdateSummary(sessionDir string, part map[string]any) {
	if !IsClientFacing(part) {
		return
	}
	existing := ReadSummary(sessionDir)
	if existing == nil {
		existing = &SessionSummary{}
	}

	messageCount := existing.MessageCount + 1
	lastActiveAt := existing.LastActiveAt
	lastMessage := existing.LastMessage

	if ts, ok := jsonNumber(part["created_at"]); ok && ts > lastActiveAt {
		lastActiveAt = ts
	}

	if role, _ := part["role"].(string); role == "assistant" {
		text := strings.TrimSpace(ExtractText(part["content"]))
		if text != "" {
			truncated := truncateRunes(text, lastMessageMaxChars)
			lastMessage = &truncated
		}
	}

	lastPartSeq := existing.LastPartSeq
	if seq, ok := jsonInt(part["seq"]); ok {
		lastPartSeq = &seq
	}

	writeSummary(sessionDir, &SessionSummary{
		MessageCount: messageCount,
		LastMessage:  lastMessage,
		LastActiveAt: lastActiveAt,
		LastPartSeq:  lastPartSeq,
	})
}

func RebuildSummary(sessionDir string) *SessionSummary {
	convsDir := filepath.Join(sessionDir, "conversations")
	if _, err := os.Stat(convsDir); err != nil {
		return nil
	}

	allParts := []map[string]any{}
	collect := func(partsDir string) {
		entries, err := os.ReadDir(partsDir)
		if err != nil {
			return
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(partsDir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var part map[string]any
			if err := json.Unmarshal(data, &part); err != nil {
				continue
			}
			if _, ok := part["created_at"]; !ok {
				if info, err := os.Stat(path); err == nil {
					part["created_at"] = fileMtimeSeconds(info)
				}
			}
			allParts = append(allParts, part)
		}
	}

	collect(filepath.Join(convsDir, "parts"))
	children, err := os.ReadDir(convsDir)
	if err == nil {
		for _, child := range children {
			if child.IsDir() && child.Name() != "parts" {
				collect(filepath.Join(convsDir, child.Name(), "parts"))
			}
		}
	}

	clientMsgs := []map[string]any{}
	for _, p := range allParts {
		if IsClientFacing(p) {
			clientMsgs = append(clientMsgs, p)
		}
	}
	sort.SliceStable(clientMsgs, func(i, j int) bool {
		return sortKey(clientMsgs[i]) < sortKey(clientMsgs[j])
	})

	lastActiveAt := 0.0
	var lastMessage *string
	if len(clientMsgs) > 0 {
		latest := clientMsgs[len(clientMsgs)-1]
		if ts, ok := jsonNumber(latest["created_at"]); ok {
			lastActiveAt = ts
		}
		for i := len(clientMsgs) - 1; i >= 0; i-- {
			msg := clientMsgs[i]
			if role, _ := msg["role"].(string); role != "assistant" {
				continue
			}
			text := strings.TrimSpace(ExtractText(msg["content"]))
			if text != "" {
				truncated := truncateRunes(text, lastMessageMaxChars)
				lastMessage = &truncated
				break
			}
		}
	}

	var lastPartSeq *int
	maxSeq := 0
	found := false
	for _, p := range allParts {
		if seq, ok := jsonInt(p["seq"]); ok && (!found || seq > maxSeq) {
			maxSeq = seq
			found = true
		}
	}
	if found {
		lastPartSeq = &maxSeq
	}

	summary := &SessionSummary{
		MessageCount: len(clientMsgs),
		LastMessage:  lastMessage,
		LastActiveAt: lastActiveAt,
		LastPartSeq:  lastPartSeq,
	}
	writeSummary(sessionDir, summary)
	return summary
}

func writeSummary(sessionDir string, summary *SessionSummary) {
	path := summaryPath(sessionDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return
	}
	// session_summary.py::_write_summary uses framework/utils/io.py::atomic_write
	// (mkstemp 0600); the summary carries transcript excerpts, so keep it private.
	_ = atomicfs.WriteFile(path, payload, 0o600)
}

func fileMtimeSeconds(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1e9
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func jsonTruthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0
	case int:
		return value != 0
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	default:
		return true
	}
}

func jsonNumber(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		f, err := value.Float64()
		return f, err == nil
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func jsonInt(v any) (int, bool) {
	f, ok := jsonNumber(v)
	if !ok || math.Trunc(f) != f {
		return 0, false
	}
	return int(f), true
}

func sortKey(part map[string]any) float64 {
	if n, ok := jsonNumber(part["created_at"]); ok {
		return n
	}
	if n, ok := jsonNumber(part["seq"]); ok {
		return n
	}
	return 0
}
