package server

// Parity: framework/server/routes_colony_workers.py

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	conversationMaxParts   = 2000
	conversationContentCap = 20000
)

// Parity: framework/server/routes_colony_workers.py::_read_worker_from_disk
func ReadWorkerFromDisk(workerID, wdir string) map[string]any {
	meta := readJSONObjectOr(filepath.Join(wdir, "meta.json"), map[string]any{})

	var result any
	if data, err := os.ReadFile(filepath.Join(wdir, "result.json")); err == nil {
		if err := json.Unmarshal(data, &result); err != nil {
			result = nil
		}
	}

	task := meta["task"]
	if !pythonTruthy(task) {
		task = extractHistoricalTask(wdir)
	}
	goal := meta["goal"]
	if !pythonTruthy(goal) {
		goal = nil
	}
	startedAt := meta["spawned_at"]
	if !pythonTruthy(startedAt) {
		startedAt = safeMtime(wdir)
	}
	profileName := any("")
	if v := meta["profile_name"]; pythonTruthy(v) {
		profileName = v
	}

	return map[string]any{
		"worker_id":    workerID,
		"task":         task,
		"goal":         goal,
		"status":       "historical",
		"started_at":   startedAt,
		"result":       result,
		"profile_name": profileName,
		"batch": map[string]any{
			"batch_id":    orDefault(meta["batch_id"], ""),
			"batch_index": orDefault(meta["batch_index"], 0),
			"batch_size":  orDefault(meta["batch_size"], 0),
			"worker_seq":  orDefault(meta["worker_seq"], 0),
		},
	}
}

// Parity: framework/server/routes_colony_workers.py::_read_worker_conversation
func ReadWorkerConversation(wdir string) map[string]any {
	partsDir := filepath.Join(wdir, "conversations", "parts")
	if info, err := os.Stat(partsDir); err != nil || !info.IsDir() {
		return emptyConversation()
	}
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		return emptyConversation()
	}

	var files []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	total := len(files)
	truncated := total > conversationMaxParts
	if total > conversationMaxParts {
		files = files[:conversationMaxParts]
	}

	messages := make([]any, 0, len(files))
	for _, name := range files {
		data, readErr := os.ReadFile(filepath.Join(partsDir, name))
		if readErr != nil {
			continue
		}
		var part map[string]any
		if err := json.Unmarshal(data, &part); err != nil {
			continue
		}

		contentAny := part["content"]
		content, isString := contentAny.(string)
		if !isString {
			if contentAny == nil {
				content = ""
			} else {
				encoded, _ := marshalNoHTMLEscape(contentAny)
				content = string(encoded)
			}
		}
		if utf8.RuneCountInString(content) > conversationContentCap {
			content = string([]rune(content)[:conversationContentCap]) + "\n…[truncated]"
		}

		msg := map[string]any{
			"seq":     orDefault(part["seq"], 0),
			"role":    orDefault(part["role"], ""),
			"content": content,
		}
		if rawCalls, ok := part["tool_calls"].([]any); ok && len(rawCalls) > 0 {
			calls := make([]any, 0, len(rawCalls))
			for _, raw := range rawCalls {
				call, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				calls = append(calls, map[string]any{
					"name":      orDefault(fn["name"], ""),
					"arguments": orDefault(fn["arguments"], ""),
				})
			}
			msg["tool_calls"] = calls
		}
		if toolUseID := part["tool_use_id"]; pythonTruthy(toolUseID) {
			msg["tool_use_id"] = toolUseID
		}
		messages = append(messages, msg)
	}

	return map[string]any{"messages": messages, "total": total, "truncated": truncated}
}

func emptyConversation() map[string]any {
	return map[string]any{"messages": []any{}, "total": 0, "truncated": false}
}

// Parity: framework/server/routes_colony_workers.py::_extract_historical_task
func extractHistoricalTask(workerDir string) string {
	partsDir := filepath.Join(workerDir, "conversations", "parts")
	if _, err := os.Stat(partsDir); err != nil {
		return ""
	}
	for i := 0; i < 5; i++ {
		path := filepath.Join(partsDir, padPartIndex(i))
		if _, err := os.Stat(path); err != nil {
			break
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		var part map[string]any
		if err := json.Unmarshal(data, &part); err != nil {
			return ""
		}
		if role, _ := part["role"].(string); role != "user" {
			continue
		}
		content, ok := part["content"].(string)
		if !ok {
			continue
		}
		text := strings.TrimSpace(content)
		if text == "" || strings.ToLower(text) == "hello" {
			continue
		}
		if utf8.RuneCountInString(text) > 400 {
			text = string([]rune(text)[:400])
		}
		return text
	}
	return ""
}

func safeMtime(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0.0
	}
	return epochSeconds(info.ModTime())
}

func padPartIndex(i int) string {
	digits := []byte("0000000000")
	n := i
	for pos := len(digits) - 1; pos >= 0 && n > 0; pos-- {
		digits[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(digits) + ".json"
}

func readJSONObjectOr(path string, fallback map[string]any) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return fallback
	}
	return out
}

func orDefault(value, fallback any) any {
	if pythonTruthy(value) {
		return value
	}
	return fallback
}

func pythonTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		value, _ := v.Float64()
		return value != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buf.String(), "\n")), nil
}
