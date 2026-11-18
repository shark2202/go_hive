package skills

type DefaultSkillConfig struct {
	Enabled   bool
	Overrides map[string]any
}

func DefaultSkillConfigFromDict(data map[string]any) *DefaultSkillConfig {
	enabled := true
	if raw, ok := data["enabled"]; ok {
		enabled = pyBool(raw)
	}
	overrides := map[string]any{}
	for key, value := range data {
		if key != "enabled" {
			overrides[key] = value
		}
	}
	return &DefaultSkillConfig{Enabled: enabled, Overrides: overrides}
}

type SkillsConfig struct {
	DefaultSkills       map[string]*DefaultSkillConfig
	AllDefaultsDisabled bool
}

func NewSkillsConfig() *SkillsConfig {
	return &SkillsConfig{DefaultSkills: map[string]*DefaultSkillConfig{}}
}

func (c *SkillsConfig) IsDefaultEnabled(skillName string) bool {
	if c.AllDefaultsDisabled {
		return false
	}
	config := c.DefaultSkills[skillName]
	if config == nil {
		return true
	}
	return config.Enabled
}

func (c *SkillsConfig) GetDefaultOverrides(skillName string) map[string]any {
	config := c.DefaultSkills[skillName]
	if config == nil {
		return map[string]any{}
	}
	return config.Overrides
}

func SkillsConfigFromAgentVars(defaultSkills map[string]any) *SkillsConfig {
	config := NewSkillsConfig()
	if len(defaultSkills) == 0 {
		return config
	}
	for name, raw := range defaultSkills {
		if name == "_all" {
			if dict, ok := raw.(map[string]any); ok {
				if enabled, ok := dict["enabled"]; ok && !pyBool(enabled) {
					config.AllDefaultsDisabled = true
				}
			}
			continue
		}
		switch typed := raw.(type) {
		case map[string]any:
			config.DefaultSkills[name] = DefaultSkillConfigFromDict(typed)
		case bool:
			config.DefaultSkills[name] = &DefaultSkillConfig{
				Enabled:   typed,
				Overrides: map[string]any{},
			}
		}
	}
	return config
}
