package soul

// Parity: framework/config.py::get_soul_config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type SoulSection struct {
	Key        string
	Title      string
	Limit      int
	MaxAgeDays *int
	Evidence   int
}

type SoulConfig struct {
	QueenID           string
	Mode              string
	Hour              int
	Model             string
	MaxEdits          int
	BootstrapEdits    int
	IncludeTitles     []string
	ExcludeTitles     []string
	SourceFile        *string
	SourceKeys        []string
	BootstrapMemories []string
	Sections          []SoulSection
	Header            string
	SessionChars      int
	MessageChars      int
}

const DefaultSoulHeader = "These are your own notes about yourself, written by you from what has happened to you. " +
	"Where they disagree with your identity above, your identity comes first."

func DefaultSoulSections() []SoulSection {
	fortnight := 14
	return []SoulSection{
		{Key: "now", Title: "Now: what's on my mind, what I'm working on", Limit: 300, MaxAgeDays: &fortnight, Evidence: 1},
		{Key: "people", Title: "Me and them", Limit: 800, Evidence: 2},
		{Key: "threads", Title: "Open threads and promises", Limit: 600, Evidence: 1},
		{Key: "views", Title: "What I think, and why", Limit: 600, Evidence: 2},
		{Key: "changes", Title: "How I've changed", Limit: 400, Evidence: 2},
	}
}

func PyString(value any) string {
	if value == nil {
		return "None"
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func pyStringOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func OrString(value any) string {
	text, ok := value.(string)
	if !ok || text == "" {
		return ""
	}
	return text
}

func pyFalsy(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return !v
	case string:
		return v == ""
	case json.Number:
		f, err := v.Float64()
		return err == nil && f == 0
	case int:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func AsInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint64:
		return int(v), true
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	}
	return 0, false
}

func pyCount(value any, fallback, low int, high *int) int {
	parsed, ok := AsInt(value)
	if !ok || parsed < low || (high != nil && parsed > *high) {
		return fallback
	}
	return parsed
}

func asStringList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if text, isString := entry.(string); isString && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func patterns(raw map[string]any, key string) []string {
	return asStringList(raw[key])
}

func GetSoulConfig(config map[string]any, queenID string) *SoulConfig {
	soul, _ := config["soul"].(map[string]any)
	queens, _ := soul["queens"].(map[string]any)
	if queens == nil || queenID == "" {
		return nil
	}
	raw, _ := queens[queenID].(map[string]any)
	if raw == nil {
		return nil
	}
	if enabled, present := raw["enabled"]; present && enabled == false {
		return nil
	}
	defaults := &SoulConfig{
		QueenID:        queenID,
		Mode:           "review",
		Hour:           4,
		Model:          "queen",
		MaxEdits:       5,
		BootstrapEdits: 30,
		Sections:       DefaultSoulSections(),
		Header:         DefaultSoulHeader,
		SessionChars:   12000,
		MessageChars:   400,
	}

	sections := make([]SoulSection, 0)
	if list, ok := raw["sections"].([]any); ok {
		for _, entry := range list {
			mapping, isMap := entry.(map[string]any)
			if !isMap {
				continue
			}
			key := strings.TrimSpace(PyString(mapping["key"]))
			title := strings.TrimSpace(PyString(mapping["title"]))
			if key == "" || title == "" {
				continue
			}
			section := SoulSection{
				Key:      key,
				Title:    title,
				Limit:    pyCount(mapping["limit"], 400, 1, nil),
				Evidence: pyCount(mapping["evidence"], 1, 1, nil),
			}
			if age, isInt := AsInt(mapping["max_age_days"]); isInt && age >= 1 {
				value := age
				section.MaxAgeDays = &value
			}
			sections = append(sections, section)
		}
	}

	seen := map[string]struct{}{}
	duplicate := false
	for _, section := range sections {
		if _, exists := seen[section.Key]; exists {
			duplicate = true
		}
		seen[section.Key] = struct{}{}
	}

	config2 := &SoulConfig{
		QueenID:           queenID,
		Mode:              defaults.Mode,
		Hour:              pyCount(raw["hour"], defaults.Hour, 0, intPtr(23)),
		Model:             defaults.Model,
		MaxEdits:          pyCount(raw["max_edits"], defaults.MaxEdits, 1, nil),
		BootstrapEdits:    pyCount(raw["bootstrap_edits"], defaults.BootstrapEdits, 1, nil),
		IncludeTitles:     patterns(raw, "include_titles"),
		ExcludeTitles:     patterns(raw, "exclude_titles"),
		SourceKeys:        sourceKeys(raw),
		BootstrapMemories: patterns(raw, "bootstrap_memories"),
		Sections:          defaults.Sections,
		Header:            defaults.Header,
		SessionChars:      pyCount(raw["session_chars"], defaults.SessionChars, 1, nil),
		MessageChars:      pyCount(raw["message_chars"], defaults.MessageChars, 1, nil),
	}
	if mode, ok := raw["mode"].(string); ok && (mode == "review" || mode == "auto") {
		config2.Mode = mode
	}
	if model, ok := raw["model"].(string); ok && (model == "queen" || model == "worker") {
		config2.Model = model
	}
	if len(sections) > 0 && !duplicate {
		config2.Sections = sections
	}
	if sources, ok := raw["sources"].(map[string]any); ok {
		if file := OrString(sources["file"]); file != "" {
			config2.SourceFile = &file
		}
	}
	if header := OrString(raw["header"]); header != "" {
		config2.Header = header
	}
	return config2
}

func sourceKeys(raw map[string]any) []string {
	sources, _ := raw["sources"].(map[string]any)
	if sources == nil {
		return nil
	}
	list, _ := sources["keys"].([]any)
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if text, ok := entry.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func intPtr(value int) *int { return &value }

func SoulQueens(config map[string]any) []string {
	soul, _ := config["soul"].(map[string]any)
	queens, ok := soul["queens"].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(queens))
	for queenID := range queens {
		if GetSoulConfig(config, queenID) != nil {
			out = append(out, queenID)
		}
	}
	sort.Strings(out)
	return out
}
