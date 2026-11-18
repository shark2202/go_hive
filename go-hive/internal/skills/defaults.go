package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var DefaultSkillRegistry = []struct {
	Name string
	Dir  string
}{
	{"hive.writing-hive-skills", "writing-hive-skills"},
	{"hive.browser-automation", "browser-automation"},
	{"hive.pdf", "pdf"},
}

func DefaultSkillDirNames() []string {
	out := make([]string, 0, len(DefaultSkillRegistry))
	for _, entry := range DefaultSkillRegistry {
		out = append(out, entry.Dir)
	}
	return out
}

var DataBufferKeys = []string{}

var skillDefaults = map[string]map[string]any{}

type DefaultSkillManager struct {
	config *SkillsConfig
	skills map[string]*ParsedSkill
	order  []string
	loaded bool

	errorCount int
	SkillsDir  string
}

func NewDefaultSkillManager(config *SkillsConfig) *DefaultSkillManager {
	if config == nil {
		config = NewSkillsConfig()
	}
	return &DefaultSkillManager{config: config, skills: map[string]*ParsedSkill{}}
}

func (m *DefaultSkillManager) defaultSkillsDir() string {
	if m.SkillsDir != "" {
		return m.SkillsDir
	}
	return DefaultSkillsDir()
}

func DefaultSkillsDir() string {
	root, err := bundledSkillsRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "_default_skills")
}

func (m *DefaultSkillManager) Load() {
	if m.loaded {
		return
	}

	errorCount := 0
	dir := m.defaultSkillsDir()
	for _, entry := range DefaultSkillRegistry {
		if !m.config.IsDefaultEnabled(entry.Name) {
			log.Printf("Default skill '%s' disabled by config", entry.Name)
			continue
		}

		skillPath := filepath.Join(dir, entry.Dir, "SKILL.md")
		info, statErr := os.Stat(skillPath)
		if statErr != nil || info.IsDir() {
			LogSkillError("error", SkillNotFound,
				fmt.Sprintf("Default skill SKILL.md not found: '%s'", skillPath),
				fmt.Sprintf("The framework skill '%s' is missing its SKILL.md file.", entry.Name),
				"Reinstall the hive framework — this file is part of the package.")
			errorCount++
			continue
		}

		parsed := ParseSkillMD(skillPath, "framework")
		if parsed == nil {
			LogSkillError("error", SkillParseError,
				fmt.Sprintf("Failed to parse default skill '%s'", entry.Name),
				fmt.Sprintf("parse_skill_md returned None for '%s'.", skillPath),
				"Reinstall the hive framework — this file may be corrupted.")
			errorCount++
			continue
		}

		m.skills[entry.Name] = parsed
		m.order = append(m.order, entry.Name)
	}

	m.loaded = true
	m.errorCount = errorCount
}

func applyPlaceholderOverrides(skillName, body string, overrides map[string]any) string {
	values := map[string]any{}
	for key, value := range skillDefaults[skillName] {
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		body = strings.ReplaceAll(body, "{{"+key+"}}", pyStrValue(values[key]))
	}
	return body
}

func pyStrValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return typed
	case float64:
		text := strconv.FormatFloat(typed, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text
	case float32:
		text := strconv.FormatFloat(float64(typed), 'g', -1, 32)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return fmt.Sprintf("%v", value)
}

func pythonDictRepr(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		var rendered string
		switch typed := values[key].(type) {
		case string:
			rendered = "'" + typed + "'"
		case float64:
			rendered = strconv.FormatFloat(typed, 'f', -1, 64)
		default:
			rendered = pyStrValue(values[key])
		}
		parts = append(parts, "'"+key+"': "+rendered)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (m *DefaultSkillManager) BuildProtocolsPrompt() string {
	if len(m.skills) == 0 {
		return ""
	}

	parts := []string{"## Operational Protocols\n"}
	for _, entry := range DefaultSkillRegistry {
		skill := m.skills[entry.Name]
		if skill == nil {
			continue
		}
		overrides := m.config.GetDefaultOverrides(entry.Name)
		parts = append(parts, applyPlaceholderOverrides(entry.Name, skill.Body, overrides))
	}

	if len(parts) <= 1 {
		return ""
	}

	combined := strings.Join(parts, "\n\n")

	approxTokens := utf8.RuneCountInString(combined) / 4
	if approxTokens > 2000 {
		log.Printf("Default skill protocols exceed 2000 token budget (~%d tokens, %d chars). Consider trimming.",
			approxTokens, utf8.RuneCountInString(combined))
	}

	return combined
}

func (m *DefaultSkillManager) LogActiveSkills() {
	if len(m.skills) == 0 {
		log.Printf("Default skills: all disabled")
	}

	for _, entry := range DefaultSkillRegistry {
		if _, ok := m.skills[entry.Name]; ok {
			overrides := m.config.GetDefaultOverrides(entry.Name)
			status := "loaded"
			if len(overrides) > 0 {
				status = "loaded overrides=" + pythonDictRepr(overrides)
			}
			log.Printf("skill_startup name=%s scope=framework status=%s", entry.Name, status)
		} else if !m.config.IsDefaultEnabled(entry.Name) {
			log.Printf("skill_startup name=%s scope=framework status=disabled", entry.Name)
		} else {
			log.Printf("skill_startup name=%s scope=framework status=error", entry.Name)
		}
	}

	active := make([]string, 0, len(m.order))
	for _, entry := range DefaultSkillRegistry {
		if _, ok := m.skills[entry.Name]; !ok {
			continue
		}
		overrides := m.config.GetDefaultOverrides(entry.Name)
		if len(overrides) > 0 {
			active = append(active, fmt.Sprintf("%s (%s)", entry.Name, pythonDictRepr(overrides)))
		} else {
			active = append(active, entry.Name)
		}
	}

	if len(active) > 0 {
		log.Printf("Default skills active: %s", strings.Join(active, ", "))
	}

	total := len(DefaultSkillRegistry)
	activeCount := len(m.skills)
	disabledCount := total - activeCount - m.errorCount
	log.Printf("Skills: %d default (%d active, %d disabled, %d error)",
		total, activeCount, disabledCount, m.errorCount)
}

func (m *DefaultSkillManager) ActiveSkillNames() []string {
	out := make([]string, 0, len(m.order))
	out = append(out, m.order...)
	return out
}

func (m *DefaultSkillManager) ActiveSkills() map[string]*ParsedSkill {
	out := make(map[string]*ParsedSkill, len(m.skills))
	for name, skill := range m.skills {
		out[name] = skill
	}
	return out
}

func (m *DefaultSkillManager) ErrorCount() int { return m.errorCount }
