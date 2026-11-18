package skills

import "testing"

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_default_is_enabled
func TestSkillsConfigDefaultEnabled(t *testing.T) {
	config := NewSkillsConfig()
	if !config.IsDefaultEnabled("hive.writing-hive-skills") {
		t.Fatal("unknown default skill should be enabled")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_explicit_disable
func TestSkillsConfigExplicitDisable(t *testing.T) {
	config := NewSkillsConfig()
	config.DefaultSkills["hive.writing-hive-skills"] = &DefaultSkillConfig{Enabled: false, Overrides: map[string]any{}}
	if config.IsDefaultEnabled("hive.writing-hive-skills") {
		t.Fatal("explicitly disabled skill should be disabled")
	}
	if !config.IsDefaultEnabled("hive.browser-automation") {
		t.Fatal("unconfigured skill should stay enabled")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_all_disabled_flag
func TestSkillsConfigAllDisabledFlag(t *testing.T) {
	config := NewSkillsConfig()
	config.AllDefaultsDisabled = true
	if config.IsDefaultEnabled("hive.writing-hive-skills") {
		t.Fatal("master switch should disable known skill")
	}
	if config.IsDefaultEnabled("anything") {
		t.Fatal("master switch should disable unknown skill")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_from_agent_vars_basic
func TestSkillsConfigFromAgentVarsBasic(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{
		"hive.writing-hive-skills": map[string]any{"enabled": true},
		"hive.browser-automation":  map[string]any{"enabled": false},
	})
	if !config.IsDefaultEnabled("hive.writing-hive-skills") {
		t.Fatal("writing-hive-skills should be enabled")
	}
	if config.IsDefaultEnabled("hive.browser-automation") {
		t.Fatal("browser-automation should be disabled")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_from_agent_vars_bool_shorthand
func TestSkillsConfigFromAgentVarsBoolShorthand(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{"hive.writing-hive-skills": false})
	if config.IsDefaultEnabled("hive.writing-hive-skills") {
		t.Fatal("bool shorthand false should disable")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_from_agent_vars_all_disabled
func TestSkillsConfigFromAgentVarsAllDisabled(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{"_all": map[string]any{"enabled": false}})
	if !config.AllDefaultsDisabled {
		t.Fatal("_all enabled=false should set the master switch")
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_get_default_overrides_returns_arbitrary_keys
func TestSkillsConfigGetDefaultOverridesArbitraryKeys(t *testing.T) {
	config := SkillsConfigFromAgentVars(map[string]any{
		"hive.browser-automation": map[string]any{"enabled": true, "custom_knob": float64(7)},
	})
	overrides := config.GetDefaultOverrides("hive.browser-automation")
	if len(overrides) != 1 || overrides["custom_knob"] != float64(7) {
		t.Fatalf("overrides = %+v", overrides)
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_get_default_overrides_empty
func TestSkillsConfigGetDefaultOverridesEmpty(t *testing.T) {
	config := NewSkillsConfig()
	if overrides := config.GetDefaultOverrides("hive.writing-hive-skills"); len(overrides) != 0 {
		t.Fatalf("overrides = %+v", overrides)
	}
}

// Parity: tests/test_default_skills.py::TestSkillsConfig::test_from_agent_vars_none_inputs
func TestSkillsConfigFromAgentVarsNoneInputs(t *testing.T) {
	config := SkillsConfigFromAgentVars(nil)
	if len(config.DefaultSkills) != 0 {
		t.Fatalf("default_skills = %+v", config.DefaultSkills)
	}
	if config.AllDefaultsDisabled {
		t.Fatal("all_defaults_disabled should be false")
	}
}
