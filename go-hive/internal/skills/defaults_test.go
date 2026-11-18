package skills

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func defaultSkillsTestDir(t *testing.T) string {
	t.Helper()
	root, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	return filepath.Join(root, "_default_skills")
}

func firstRunes(text string, count int) string {
	runes := []rune(text)
	if len(runes) > count {
		runes = runes[:count]
	}
	return string(runes)
}

// Parity: tests/test_default_skills.py::TestDefaultSkillFiles::test_skill_parses
func TestDefaultSkillFilesParse(t *testing.T) {
	dir := defaultSkillsTestDir(t)
	for _, entry := range DefaultSkillRegistry {
		t.Run(entry.Dir, func(t *testing.T) {
			path := filepath.Join(dir, entry.Dir, "SKILL.md")
			parsed := ParseSkillMD(path, "framework")
			if parsed == nil {
				t.Fatalf("failed to parse %s", path)
			}
			if parsed.Name != entry.Name {
				t.Fatalf("name = %q, want %q", parsed.Name, entry.Name)
			}
			if parsed.Description == "" {
				t.Fatal("description is empty")
			}
			if parsed.Body == "" {
				t.Fatal("body is empty")
			}
			if parsed.SourceScope != "framework" {
				t.Fatalf("source_scope = %q", parsed.SourceScope)
			}
		})
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillFiles::test_combined_token_budget
func TestDefaultCombinedTokenBudget(t *testing.T) {
	dir := defaultSkillsTestDir(t)
	totalChars := 0
	for _, entry := range DefaultSkillRegistry {
		path := filepath.Join(dir, entry.Dir, "SKILL.md")
		parsed := ParseSkillMD(path, "framework")
		if parsed == nil {
			t.Fatalf("failed to parse %s", path)
		}
		totalChars += utf8.RuneCountInString(parsed.Body)
	}
	approxTokens := totalChars / 4
	if approxTokens >= 20000 {
		t.Fatalf("combined default skill bodies are ~%d tokens (%d chars), exceeding the 20000 token budget",
			approxTokens, totalChars)
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillFiles::test_data_buffer_keys_all_prefixed
func TestDataBufferKeysAllPrefixed(t *testing.T) {
	for _, key := range DataBufferKeys {
		if !strings.HasPrefix(key, "_") {
			t.Fatalf("data buffer key missing _ prefix: %s", key)
		}
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_load_all_defaults
func TestDefaultManagerLoadAll(t *testing.T) {
	manager := NewDefaultSkillManager(nil)
	manager.Load()

	if len(manager.ActiveSkillNames()) != len(DefaultSkillRegistry) {
		t.Fatalf("active skills = %v", manager.ActiveSkillNames())
	}
	active := map[string]bool{}
	for _, name := range manager.ActiveSkillNames() {
		active[name] = true
	}
	for _, entry := range DefaultSkillRegistry {
		if !active[entry.Name] {
			t.Fatalf("%s not active", entry.Name)
		}
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_load_idempotent
func TestDefaultManagerLoadIdempotent(t *testing.T) {
	manager := NewDefaultSkillManager(nil)
	manager.Load()
	first := manager.ActiveSkills()
	manager.Load()
	second := manager.ActiveSkills()
	if len(first) != len(second) {
		t.Fatalf("first = %d, second = %d", len(first), len(second))
	}
	for name, skill := range first {
		if second[name] != skill {
			t.Fatalf("%s changed on second load", name)
		}
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_build_protocols_prompt
func TestDefaultManagerBuildProtocolsPrompt(t *testing.T) {
	manager := NewDefaultSkillManager(nil)
	manager.Load()
	prompt := manager.BuildProtocolsPrompt()

	if !strings.HasPrefix(prompt, "## Operational Protocols") {
		t.Fatalf("prompt does not start with header: %.40q", prompt)
	}
	skills := manager.ActiveSkills()
	for _, entry := range DefaultSkillRegistry {
		skill := skills[entry.Name]
		if skill == nil {
			t.Fatalf("%s not active", entry.Name)
		}
		if !strings.Contains(prompt, firstRunes(skill.Body, 20)) {
			t.Fatalf("prompt missing body prefix for %s", entry.Name)
		}
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_protocols_prompt_empty_when_all_disabled
func TestDefaultManagerProtocolsPromptEmptyWhenAllDisabled(t *testing.T) {
	config := NewSkillsConfig()
	config.AllDefaultsDisabled = true
	manager := NewDefaultSkillManager(config)
	manager.Load()

	if prompt := manager.BuildProtocolsPrompt(); prompt != "" {
		t.Fatalf("prompt = %q", prompt)
	}
	if names := manager.ActiveSkillNames(); len(names) != 0 {
		t.Fatalf("active skills = %v", names)
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_disable_single_skill
func TestDefaultManagerDisableSingle(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{
		"hive.writing-hive-skills": map[string]any{"enabled": false},
	})
	manager := NewDefaultSkillManager(config)
	manager.Load()

	for _, name := range manager.ActiveSkillNames() {
		if name == "hive.writing-hive-skills" {
			t.Fatal("disabled skill should not be active")
		}
	}
	if len(manager.ActiveSkillNames()) != len(DefaultSkillRegistry)-1 {
		t.Fatalf("active skills = %v", manager.ActiveSkillNames())
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_disable_all_via_convention
func TestDefaultManagerDisableAllViaConvention(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{"_all": map[string]any{"enabled": false}})
	manager := NewDefaultSkillManager(config)
	manager.Load()

	if names := manager.ActiveSkillNames(); len(names) != 0 {
		t.Fatalf("active skills = %v", names)
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_log_active_skills
func TestDefaultManagerLogActiveSkills(t *testing.T) {
	output := captureLog(t, func() {
		manager := NewDefaultSkillManager(nil)
		manager.Load()
		manager.LogActiveSkills()
	})
	if !strings.Contains(output, "Default skills active:") {
		t.Fatalf("log output = %q", output)
	}
}

// Parity: tests/test_default_skills.py::TestDefaultSkillManager::test_log_all_disabled
func TestDefaultManagerLogAllDisabled(t *testing.T) {
	config := NewSkillsConfig()
	config.AllDefaultsDisabled = true
	output := captureLog(t, func() {
		manager := NewDefaultSkillManager(config)
		manager.Load()
		manager.LogActiveSkills()
	})
	if !strings.Contains(output, "all disabled") {
		t.Fatalf("log output = %q", output)
	}
}

// Parity-exempt: the summary counter log line has no Python pytest counterpart.
func TestDefaultManagerLogSummary(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{
		"hive.writing-hive-skills": map[string]any{"enabled": false},
		"hive.browser-automation":  map[string]any{"enabled": true, "custom_knob": float64(7)},
	})
	output := captureLog(t, func() {
		manager := NewDefaultSkillManager(config)
		manager.Load()
		manager.LogActiveSkills()
	})
	wantSummary := fmt.Sprintf("Skills: %d default (2 active, 1 disabled, 0 error)", len(DefaultSkillRegistry))
	if !strings.Contains(output, wantSummary) {
		t.Fatalf("log output = %q, want %q", output, wantSummary)
	}
	if !strings.Contains(output, "skill_startup name=hive.browser-automation scope=framework status=loaded overrides={'custom_knob': 7}") {
		t.Fatalf("overrides status missing: %q", output)
	}
}
