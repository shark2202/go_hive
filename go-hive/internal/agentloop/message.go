package agentloop

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

const LegacyRunID = "__legacy_run__"

func IsLegacyRunID(runID *string) bool {
	return runID == nil || *runID == LegacyRunID
}

type Message struct {
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Content string `json:"content"`

	ToolUseID *string          `json:"tool_use_id,omitempty"`
	ToolCalls []map[string]any `json:"tool_calls,omitempty"`
	IsError   bool             `json:"is_error,omitempty"`

	PhaseID            *string `json:"phase_id,omitempty"`
	IsTransitionMarker bool    `json:"is_transition_marker,omitempty"`

	IsClientInput    bool             `json:"is_client_input,omitempty"`
	ImageContent     []map[string]any `json:"image_content,omitempty"`
	IsSkillContent   bool             `json:"is_skill_content,omitempty"`
	RunID            *string          `json:"run_id,omitempty"`
	IsSystemReminder bool             `json:"is_system_reminder,omitempty"`
	Truncated        bool             `json:"truncated,omitempty"`
	InheritedFrom    *string          `json:"inherited_from,omitempty"`
	IsTrigger        bool             `json:"is_trigger,omitempty"`
	ThinkingBlocks   []map[string]any `json:"thinking_blocks,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	Images           []map[string]any `json:"images,omitempty"`
	SpilloverPath    *string          `json:"spillover_path,omitempty"`
}

func (m Message) ToLLMDict() map[string]any {
	switch m.Role {
	case RoleUser:
		if len(m.ImageContent) > 0 {
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			blocks = append(blocks, m.ImageContent...)
			return map[string]any{"role": RoleUser, "content": blocks}
		}
		return map[string]any{"role": RoleUser, "content": m.Content}

	case RoleAssistant:
		d := map[string]any{"role": RoleAssistant}
		if len(m.ToolCalls) > 0 {
			d["tool_calls"] = m.ToolCalls
			if m.Content != "" {
				d["content"] = m.Content
			} else {
				d["content"] = nil
			}
		} else {
			d["content"] = m.Content
		}
		if len(m.ThinkingBlocks) > 0 {
			d["thinking_blocks"] = m.ThinkingBlocks
		}
		if m.ReasoningContent != nil && *m.ReasoningContent != "" {
			d["reasoning_content"] = *m.ReasoningContent
		}
		return d
	}

	content := m.Content
	if m.IsError {
		content = "ERROR: " + content
	}
	if len(m.ImageContent) > 0 {
		blocks := []map[string]any{{"type": "text", "text": content}}
		blocks = append(blocks, m.ImageContent...)
		return map[string]any{
			"role":         RoleTool,
			"tool_call_id": ptrOrNil(m.ToolUseID),
			"content":      blocks,
		}
	}
	return map[string]any{
		"role":         RoleTool,
		"tool_call_id": ptrOrNil(m.ToolUseID),
		"content":      content,
	}
}

func (m Message) ToStorageDict() map[string]any {
	d := map[string]any{
		"seq":     m.Seq,
		"role":    m.Role,
		"content": m.Content,
	}
	if m.ToolUseID != nil {
		d["tool_use_id"] = *m.ToolUseID
	}
	if m.ToolCalls != nil {
		d["tool_calls"] = m.ToolCalls
	}
	if m.IsError {
		d["is_error"] = true
	}
	if m.PhaseID != nil {
		d["phase_id"] = *m.PhaseID
	}
	if m.IsTransitionMarker {
		d["is_transition_marker"] = true
	}
	if m.IsClientInput {
		d["is_client_input"] = true
	}
	if m.ImageContent != nil {
		d["image_content"] = m.ImageContent
	}
	if m.IsSkillContent {
		d["is_skill_content"] = true
	}
	if m.RunID != nil {
		d["run_id"] = *m.RunID
	}
	if m.IsSystemReminder {
		d["is_system_reminder"] = true
	}
	if m.Truncated {
		d["truncated"] = true
	}
	if m.InheritedFrom != nil {
		d["inherited_from"] = *m.InheritedFrom
	}
	if m.IsTrigger {
		d["is_trigger"] = true
	}
	if m.ThinkingBlocks != nil {
		d["thinking_blocks"] = m.ThinkingBlocks
	}
	if m.ReasoningContent != nil {
		d["reasoning_content"] = *m.ReasoningContent
	}
	if m.Images != nil {
		d["images"] = m.Images
	}
	if m.SpilloverPath != nil {
		d["spillover_path"] = *m.SpilloverPath
	}
	return d
}

func MessageFromStorageDict(data map[string]any) Message {
	return Message{
		Seq:                intOr(data["seq"], 0),
		Role:               stringOr(data["role"], ""),
		Content:            stringOr(data["content"], ""),
		ToolUseID:          stringPtr(data["tool_use_id"]),
		ToolCalls:          mapSlice(data["tool_calls"]),
		IsError:            boolOr(data["is_error"], false),
		PhaseID:            stringPtr(data["phase_id"]),
		IsTransitionMarker: boolOr(data["is_transition_marker"], false),
		IsClientInput:      boolOr(data["is_client_input"], false),
		ImageContent:       mapSlice(data["image_content"]),
		IsSkillContent:     boolOr(data["is_skill_content"], false),
		RunID:              stringPtr(data["run_id"]),
		IsSystemReminder:   boolOr(data["is_system_reminder"], boolOr(data["is_system_nudge"], false)),
		Truncated:          boolOr(data["truncated"], false),
		InheritedFrom:      stringPtr(data["inherited_from"]),
		IsTrigger:          boolOr(data["is_trigger"], false),
		ThinkingBlocks:     mapSlice(data["thinking_blocks"]),
		ReasoningContent:   stringPtr(data["reasoning_content"]),
		Images:             mapSlice(data["images"]),
		SpilloverPath:      stringPtr(data["spillover_path"]),
	}
}

func GetCursorNextSeq(cursor map[string]any) *int {
	if cursor == nil {
		return nil
	}
	return intPtr(cursor["next_seq"])
}

func UpdateCursorNextSeq(cursor map[string]any, nextSeq int) map[string]any {
	updated := copyMap(cursor)
	updated["next_seq"] = nextSeq
	return updated
}

func GetRunCursor(cursor map[string]any, _ *string) map[string]any {
	if cursor == nil {
		return nil
	}
	return copyMap(cursor)
}

func UpdateRunCursor(cursor map[string]any, _ *string, values map[string]any) map[string]any {
	updated := copyMap(cursor)
	for k, v := range values {
		updated[k] = v
	}
	return updated
}

var (
	reFullResultAt = regexp.MustCompile(`(?i)full result at:\s*(\S+)`)
	reCharsAtPath  = regexp.MustCompile(`\)\s+at\s+(/\S+)`)
	reSavedAt      = regexp.MustCompile(`(?i)saved at:\s*(\S+)`)
	reSavedTo      = regexp.MustCompile(`(?i)saved to '([^']+)'`)
)

func ExtractSpilloverFilename(content string) *string {
	for _, re := range []*regexp.Regexp{reFullResultAt, reCharsAtPath, reSavedAt} {
		if match := re.FindStringSubmatch(content); match != nil {
			value := strings.TrimRight(match[1], ".,;:")
			return &value
		}
	}
	if match := reSavedTo.FindStringSubmatch(content); match != nil {
		value := match[1]
		return &value
	}
	return nil
}

func extractSpilloverFilenameInline(content string) *string {
	return ExtractSpilloverFilename(content)
}

func tryExtractKey(content, key string) *string {
	if value := jsonKeyValue(content, key); value != nil {
		return value
	}
	if jsonStr := findJSONObject(content); jsonStr != "" {
		if value := jsonKeyValue(jsonStr, key); value != nil {
			return value
		}
	}
	colonRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\s*:\s*(.+)`)
	if match := colonRe.FindStringSubmatch(content); match != nil {
		value := strings.TrimSpace(match[1])
		return &value
	}
	equalsRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\s*=\s*(.+)`)
	if match := equalsRe.FindStringSubmatch(content); match != nil {
		value := strings.TrimSpace(match[1])
		return &value
	}
	return nil
}

func jsonKeyValue(text, key string) *string {
	trimmed := strings.TrimSpace(text)
	if !json.Valid([]byte(trimmed)) {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	if str, ok := raw.(string); ok {
		return &str
	}
	value := pyJSONDumps(raw)
	return &value
}

func pyJSONDumps(value any) string {
	var buf strings.Builder
	writePyJSON(&buf, value)
	return buf.String()
}

func writePyJSON(buf *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		buf.WriteString(pyJSONString(v))
	case json.Number:
		buf.WriteString(v.String())
	case float64:
		encoded, err := json.Marshal(v)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(encoded)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteString("{")
		for i, key := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(pyJSONString(key))
			buf.WriteString(": ")
			writePyJSON(buf, v[key])
		}
		buf.WriteString("}")
	case []any:
		buf.WriteString("[")
		for i, item := range v {
			if i > 0 {
				buf.WriteString(", ")
			}
			writePyJSON(buf, item)
		}
		buf.WriteString("]")
	default:
		buf.WriteString("null")
	}
}

func pyJSONString(value string) string {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func findJSONObject(text string) string {
	start := strings.IndexByte(text, '{')
	if start == -1 {
		return ""
	}
	end := strings.LastIndexByte(text, '}')
	if end == -1 || end < start {
		return ""
	}

	candidate := text[start : end+1]
	if json.Valid([]byte(candidate)) {
		return candidate
	}

	depth := 0
	inString := false
	escapeNext := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if escapeNext {
			escapeNext = false
			continue
		}
		if c == '\\' && inString {
			escapeNext = true
			continue
		}
		if c == '"' && !escapeNext {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func strPtr(value string) *string { return &value }

func ptrOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringPtr(value any) *string {
	if value == nil {
		return nil
	}
	if str, ok := value.(string); ok {
		return &str
	}
	return nil
}

func stringOr(value any, fallback string) string {
	if str, ok := value.(string); ok {
		return str
	}
	return fallback
}

func boolOr(value any, fallback bool) bool {
	if b, ok := value.(bool); ok {
		return b
	}
	return fallback
}

func storedZero(value any) bool {
	if value == nil {
		return false
	}
	_, ok := value.(string)
	if ok {
		return false
	}
	return intOr(value, -1) == 0
}

func intOr(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

func intPtr(value any) *int {
	switch v := value.(type) {
	case int:
		return &v
	case int64:
		n := int(v)
		return &n
	case float64:
		n := int(v)
		return &n
	case json.Number:
		if n, err := v.Int64(); err == nil {
			iv := int(n)
			return &iv
		}
	}
	return nil
}

func floatPtrFromAny(value any) *float64 {
	switch v := value.(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func mapSlice(value any) []map[string]any {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func copyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func marshalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}
