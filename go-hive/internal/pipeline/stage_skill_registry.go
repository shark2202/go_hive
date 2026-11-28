package pipeline

import (
	"fmt"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// SkillRegistryStage discovers and loads skills at startup — upstream's only
// place skills get loaded for a worker agent.
//
// Config keys (config.*): project_root, interactive (default true),
// skills_config (`{skill: {"enabled": bool}}`, `_all` supported), and
// extra_scope_dirs (`[{"directory": ..., "label": ..., "priority": N}]`,
// layered between user and project scope). Initialize also starts the live
// SKILL.md watcher upstream runs: a change reloads the manager so the next
// turn's catalog prompt reflects it.
//
// Parity: framework/pipeline/stages/skill_registry.py::SkillRegistryStage
type SkillRegistryStage struct {
	BaseStage
	// ProjectRoot is the agent directory for community skill discovery.
	ProjectRoot string
	// Interactive allows trust-gating prompts during discovery.
	Interactive bool
	// SkillsConfig is the per-skill enable/override table.
	SkillsConfig *skills.SkillsConfig
	// ExtraScopeDirs are additional skill roots between user and project
	// scope.
	ExtraScopeDirs []skills.ExtraScope
	// SkillsManager is the loaded manager (output).
	SkillsManager *skills.SkillsManager
	// WatchInterval overrides the hot-reload polling cadence; zero uses
	// the package default.
	WatchInterval time.Duration
	// WatchDebounce overrides the quiet window before a reload; zero uses
	// the package default.
	WatchDebounce time.Duration
	// Watcher is the running hot-reload loop (output; nil when there is
	// nothing to watch).
	Watcher *SkillWatcher
}

// NewSkillRegistryStage builds the stage from its declarative config.
func NewSkillRegistryStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &SkillRegistryStage{
		BaseStage:    BaseStage{StageName: "skill_registry", StageOrder: 60},
		Interactive:  true,
		SkillsConfig: skills.NewSkillsConfig(),
	}
	if raw, present := config["project_root"]; present {
		stage.ProjectRoot = pyString(raw)
	}
	if raw, present := config["interactive"]; present {
		stage.Interactive = pyTruthy(raw)
	}
	if raw, present := config["skills_config"]; present {
		if table, ok := raw.(map[string]any); ok {
			stage.SkillsConfig = skills.SkillsConfigFromAgentVars(table)
		} else {
			return nil, fmt.Errorf("skill_registry skills_config must be an object, got %T", raw)
		}
	}
	if raw, present := config["extra_scope_dirs"]; present {
		scopes, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("skill_registry extra_scope_dirs must be a list, got %T", raw)
		}
		for _, entry := range scopes {
			scopeMap, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("skill_registry extra_scope_dirs entries must be objects, got %T", entry)
			}
			scope := skills.ExtraScope{
				Directory: pyString(scopeMap["directory"]),
				Label:     pyString(scopeMap["label"]),
			}
			if priority, ok := pyInt(scopeMap["priority"]); ok {
				scope.Priority = priority
			}
			stage.ExtraScopeDirs = append(stage.ExtraScopeDirs, scope)
		}
	}
	return stage, nil
}

// Initialize loads the skill catalog.
//
// Parity: framework/pipeline/stages/skill_registry.py::SkillRegistryStage.initialize
func (s *SkillRegistryStage) Initialize() error {
	config := skills.DefaultSkillsManagerConfig()
	config.SkillsConfig = s.SkillsConfig
	if config.SkillsConfig == nil {
		config.SkillsConfig = skills.NewSkillsConfig()
	}
	config.ProjectRoot = s.ProjectRoot
	config.Interactive = s.Interactive
	config.ExtraScopeDirs = s.ExtraScopeDirs

	manager := skills.NewSkillsManager(config)
	manager.Load()
	s.SkillsManager = manager
	s.Watcher = StartSkillWatcher(manager, s.WatchInterval, s.WatchDebounce)
	return nil
}

// StopWatching halts the hot-reload loop (if any). Tests and runtimes that
// tear a stage down without tearing down the process call it; the stage output
// stays readable afterwards.
func (s *SkillRegistryStage) StopWatching() {
	if s.Watcher != nil {
		s.Watcher.Stop()
		s.Watcher = nil
	}
}

// Process is a pass-through: the work happened in Initialize.
func (s *SkillRegistryStage) Process(_ *Context) (Result, error) { return ContinueResult(), nil }

// PopulateSkillsCatalogForPhase exposes the catalog prompt for tests and the
// runtime-output applier without reaching into the manager directly.
func (s *SkillRegistryStage) SkillsCatalogPrompt() string {
	if s.SkillsManager == nil {
		return ""
	}
	return s.SkillsManager.SkillsCatalogPrompt()
}
