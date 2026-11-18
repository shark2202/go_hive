package agents

// Parity: framework/loader/agent_loader.py::AgentLoader.load
//
// AgentLoader.load is how upstream turns a colony directory into the worker it
// runs: it discovers the directory's worker config files, skips the provenance
// sidecars (metadata.json and friends), reads the first config in sorted order,
// and builds the worker's goal and single-node graph from it. The Python loader
// finishes by constructing an AgentLoader/GraphSpec; this port stops one layer
// earlier and returns the worker spec itself, because the Go colony host takes
// an agentloop.AgentSpec plus a schemas.Goal rather than a graph object.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/schemas"
	"gopkg.in/yaml.v3"
)

// ErrNoWorkerConfig reports an agent directory that carries no worker
// definition. Upstream raises FileNotFoundError; callers test with errors.Is.
var ErrNoWorkerConfig = errors.New("No worker config found")

// workerSpecExtensions are the config formats a worker definition may use.
// Upstream reads JSON only. YAML is a Go-side extension (yaml.v3 is already a
// module dependency) so hand-authored agent definitions need no JSON step.
var workerSpecExtensions = map[string]struct{}{
	".json": {},
	".yaml": {},
	".yml":  {},
}

const (
	workerSpecDefaultMaxTokens   = 4096
	workerSpecDefaultAgentType   = "event_loop"
	workerSpecDefaultToolPolicy  = "explicit"
	workerSpecDefaultMaxRetries  = 3
	workerSpecConversationMode   = "continuous"
	workerSpecGoalSuffix         = "-goal"
	workerSpecScIDPrefix         = "sc-"
	workerSpecConstraintIDPrefix = "c-"
	workerSpecSuccessMetric      = "llm_judge"
	workerSpecConstraintType     = "hard"
	workerSpecConstraintCategory = "general"
)

// WorkerSpec is one colony worker parsed from its on-disk definition. It is the
// Go counterpart of the worker AgentLoader.load builds: AgentSpec is the agent
// template the colony clones per worker, Goal is the declarative goal built from
// the config's goal block, and the remaining fields carry the config values the
// colony host applies around them (loop overrides, max tokens, identity).
type WorkerSpec struct {
	// ColonyID is the directory name of the colony the worker belongs to.
	ColonyID string
	// Dir is the resolved colony directory.
	Dir string
	// ConfigPath is the config file the spec was read from.
	ConfigPath string

	// AgentSpec is the worker's agent-loop spec (id, name, prompt, tools, model).
	AgentSpec agentloop.AgentSpec
	// Goal is the worker's declarative goal (description, criteria, constraints).
	Goal *schemas.Goal

	// LoopConfig is the raw loop_config block from the config file. Values are
	// passed through untouched so colony.BuildWorkerLoopConfig applies its own
	// whitelist and bounds exactly as it does for runtime-supplied overrides.
	LoopConfig map[string]any
	// IdentityPrompt is the config's identity_prompt ("" when unset).
	IdentityPrompt string
	// MaxTokens is the config's token ceiling: loop_config.max_tokens, then a
	// top-level max_tokens, then upstream's 4096 default.
	MaxTokens int
	// ConversationMode is the config's conversation_mode, defaulting to the
	// "continuous" mode upstream gives a colony worker graph.
	ConversationMode string
}

// HostFields are the host.Config fields a colony directory defines. Callers
// building a host.Config fill these three straight from the parsed spec;
// storage, LLM, and the tool surface stay caller-owned.
type HostFields struct {
	ColonyID  string
	AgentSpec agentloop.AgentSpec
	Goal      *schemas.Goal
}

// HostFields returns the host.Config spec fields for this worker.
func (s *WorkerSpec) HostFields() HostFields {
	if s == nil {
		return HostFields{}
	}
	return HostFields{ColonyID: s.ColonyID, AgentSpec: s.AgentSpec, Goal: s.Goal}
}

// LoadAgentSpec resolves agentDir and returns the worker spec defined there.
// The first worker config in sorted filename order wins, matching upstream's
// sorted()[0] selection; reserved sidecar stems are never treated as workers.
func LoadAgentSpec(agentDir string) (*WorkerSpec, error) {
	resolved, err := ResolvePath(agentDir)
	if err != nil {
		return nil, err
	}
	return loadAgentSpecAt(resolved)
}

// LoadAgentSpecInRoots is LoadAgentSpec with the agent path constrained to
// roots (the allowed agent roots ValidateAgentPath enforces). An empty roots
// slice skips the check, which is what `hive serve --colony` does for an
// explicit user-supplied path.
func LoadAgentSpecInRoots(agentDir string, roots []string) (*WorkerSpec, error) {
	resolved, err := ResolvePath(agentDir)
	if err != nil {
		return nil, err
	}
	if len(roots) > 0 {
		validated, err := ValidateAgentPath(resolved, roots)
		if err != nil {
			return nil, err
		}
		resolved = validated
	}
	return loadAgentSpecAt(resolved)
}

// LoadAgentSpecWithin is LoadAgentSpec constrained to the shared AllowedRoots
// set. A nil set skips the check.
func LoadAgentSpecWithin(agentDir string, allowed *AllowedRoots) (*WorkerSpec, error) {
	if allowed == nil {
		return LoadAgentSpec(agentDir)
	}
	resolved, err := allowed.Validate(agentDir)
	if err != nil {
		return nil, err
	}
	return loadAgentSpecAt(resolved)
}

// DiscoverAgentDirs lists the agent directories directly under root that carry
// at least one worker config, resolved and sorted by name. This is the "which
// directories can a colony host actually load" probe behind --colony preload.
func DiscoverAgentDirs(root string) []string {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	dirs := []string{}
	for _, name := range sortedEntryNames(root) {
		path := filepath.Join(root, name)
		entryInfo, err := os.Stat(path)
		if err != nil || !entryInfo.IsDir() {
			continue
		}
		if len(FindWorkerSpecFiles(path)) == 0 {
			continue
		}
		if resolved, resolveErr := ResolvePath(path); resolveErr == nil {
			path = resolved
		}
		dirs = append(dirs, path)
	}
	return dirs
}

// FindWorkerSpecFiles returns the worker definition files in agentDir, sorted
// by filename. It extends FindWorkerConfigs (JSON-only, upstream parity) with
// the YAML formats LoadAgentSpec accepts; the reserved sidecar stems stay shared
// so metadata.json can never be read as a worker spec.
func FindWorkerSpecFiles(agentDir string) []string {
	info, err := os.Stat(agentDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	configs := []string{}
	for _, name := range sortedEntryNames(agentDir) {
		path := filepath.Join(agentDir, name)
		entryInfo, err := os.Stat(path)
		if err != nil || !entryInfo.Mode().IsRegular() {
			continue
		}
		stem, suffix := pyStemSuffix(name)
		if _, isConfig := workerSpecExtensions[suffix]; !isConfig {
			continue
		}
		if _, excluded := excludedJSONStems[stem]; excluded {
			continue
		}
		configs = append(configs, path)
	}
	return configs
}

func loadAgentSpecAt(dir string) (*WorkerSpec, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("agent dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent dir %s is not a directory", dir)
	}
	configs := FindWorkerSpecFiles(dir)
	if len(configs) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoWorkerConfig, dir)
	}
	configPath := configs[0]
	config, err := readWorkerSpecConfig(configPath)
	if err != nil {
		return nil, err
	}
	return buildWorkerSpec(dir, configPath, config), nil
}

// workerSpecConfig is the subset of a worker definition the colony host needs.
// Both JSON and YAML configs decode into it; unknown keys are ignored, and the
// fields upstream reads with .get() keep their Python defaults when absent.
type workerSpecConfig struct {
	Name               string          `json:"name" yaml:"name"`
	Description        *string         `json:"description" yaml:"description"`
	SystemPrompt       *string         `json:"system_prompt" yaml:"system_prompt"`
	IdentityPrompt     string          `json:"identity_prompt" yaml:"identity_prompt"`
	AgentType          string          `json:"agent_type" yaml:"agent_type"`
	NodeType           string          `json:"node_type" yaml:"node_type"`
	Model              *string         `json:"model" yaml:"model"`
	InputKeys          []string        `json:"input_keys" yaml:"input_keys"`
	OutputKeys         []string        `json:"output_keys" yaml:"output_keys"`
	NullableOutputKeys []string        `json:"nullable_output_keys" yaml:"nullable_output_keys"`
	ReportSchema       map[string]any  `json:"report_schema" yaml:"report_schema"`
	Tools              workerSpecTools `json:"tools" yaml:"tools"`
	Goal               workerSpecGoal  `json:"goal" yaml:"goal"`
	LoopConfig         map[string]any  `json:"loop_config" yaml:"loop_config"`
	MaxTokens          *int            `json:"max_tokens" yaml:"max_tokens"`
	ConversationMode   string          `json:"conversation_mode" yaml:"conversation_mode"`
	MaxRetries         *int            `json:"max_retries" yaml:"max_retries"`
	RetryOn            []string        `json:"retry_on" yaml:"retry_on"`
	MaxNodeVisits      *int            `json:"max_node_visits" yaml:"max_node_visits"`
	ClientFacing       bool            `json:"client_facing" yaml:"client_facing"`
	SuccessCriteria    *string         `json:"success_criteria" yaml:"success_criteria"`
	SkipJudge          bool            `json:"skip_judge" yaml:"skip_judge"`
}

// workerSpecGoal mirrors the goal block upstream reads as a dict: the
// description is optional (its presence decides the goal name fallback), and
// criteria/constraints are plain strings.
type workerSpecGoal struct {
	Description     *string  `json:"description" yaml:"description"`
	SuccessCriteria []string `json:"success_criteria" yaml:"success_criteria"`
	Constraints     []string `json:"constraints" yaml:"constraints"`
}

// workerSpecTools accepts both tool shapes found on disk: the worker-config
// form (a bare list of tool names) and the exported-agent form
// ({"policy": ..., "allowed": [...]}). Upstream's colony loader assumes the
// list form; the object form keeps exported agent directories loadable.
type workerSpecTools struct {
	Policy  string
	Allowed []string
}

func (t *workerSpecTools) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var names []string
		if err := json.Unmarshal(trimmed, &names); err != nil {
			return err
		}
		t.Allowed = names
		return nil
	}
	var object struct {
		Policy  string   `json:"policy"`
		Allowed []string `json:"allowed"`
	}
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	t.Policy = object.Policy
	t.Allowed = object.Allowed
	return nil
}

func (t *workerSpecTools) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var names []string
		if err := node.Decode(&names); err != nil {
			return err
		}
		t.Allowed = names
		return nil
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	var object struct {
		Policy  string   `yaml:"policy"`
		Allowed []string `yaml:"allowed"`
	}
	if err := node.Decode(&object); err != nil {
		return err
	}
	t.Policy = object.Policy
	t.Allowed = object.Allowed
	return nil
}

func readWorkerSpecConfig(path string) (*workerSpecConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read worker config %s: %w", path, err)
	}
	config := &workerSpecConfig{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(config); err != nil {
			return nil, fmt.Errorf("parse worker config %s: %w", path, err)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse worker config %s: trailing data after config object", path)
		}
	default:
		if err := yaml.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("parse worker config %s: %w", path, err)
		}
	}
	return config, nil
}

func buildWorkerSpec(dir, configPath string, config *workerSpecConfig) *WorkerSpec {
	colonyID := filepath.Base(dir)
	stem, _ := pyStemSuffix(filepath.Base(configPath))
	workerName := config.Name
	if workerName == "" {
		workerName = stem
	}

	agentType := config.AgentType
	if agentType == "" {
		agentType = config.NodeType
	}
	if agentType == "" {
		agentType = workerSpecDefaultAgentType
	}
	toolPolicy := config.Tools.Policy
	if toolPolicy == "" {
		toolPolicy = workerSpecDefaultToolPolicy
	}
	maxRetries := workerSpecDefaultMaxRetries
	if config.MaxRetries != nil {
		maxRetries = *config.MaxRetries
	}

	// Upstream passes first_worker.get("system_prompt", "") straight through, so
	// an absent prompt stays an explicit empty focus prompt rather than falling
	// back to a generated "You are <name>." line.
	systemPrompt := ""
	if config.SystemPrompt != nil {
		systemPrompt = *config.SystemPrompt
	}
	identityPrompt := config.IdentityPrompt

	spec := agentloop.AgentSpec{
		ID:                 workerName,
		Name:               pythonTitle(strings.ReplaceAll(workerName, "_", " ")),
		Description:        workerSpecString(config.Description),
		AgentType:          agentType,
		InputKeys:          workerSpecStrings(config.InputKeys),
		OutputKeys:         workerSpecStrings(config.OutputKeys),
		NullableOutputKeys: workerSpecStrings(config.NullableOutputKeys),
		ReportSchema:       workerSpecMap(config.ReportSchema),
		SystemPrompt:       &systemPrompt,
		Tools:              workerSpecStrings(config.Tools.Allowed),
		ToolAccessPolicy:   toolPolicy,
		Model:              config.Model,
		RetryOn:            workerSpecStrings(config.RetryOn),
		MaxRetries:         maxRetries,
		ClientFacing:       config.ClientFacing,
		SuccessCriteria:    config.SuccessCriteria,
		SkipJudge:          config.SkipJudge,
	}
	if config.MaxNodeVisits != nil {
		spec.MaxVisits = *config.MaxNodeVisits
	}

	// Parity: framework/loader/agent_loader.py::AgentLoader.load goal block --
	// name falls back to the worker name only when the key is absent; an
	// explicit empty description stays empty.
	goalName := workerName
	goalDescription := ""
	if config.Goal.Description != nil {
		goalDescription = *config.Goal.Description
		goalName = goalDescription
	}
	goal := &schemas.Goal{
		ID:              colonyID + workerSpecGoalSuffix,
		Name:            goalName,
		Description:     goalDescription,
		Status:          schemas.GoalDraft,
		SuccessCriteria: []schemas.SuccessCriterion{},
		Constraints:     []schemas.Constraint{},
	}
	for i, criterion := range config.Goal.SuccessCriteria {
		goal.SuccessCriteria = append(goal.SuccessCriteria, schemas.SuccessCriterion{
			ID:          workerSpecScIDPrefix + strconv.Itoa(i),
			Description: criterion,
			Metric:      workerSpecSuccessMetric,
			Target:      "",
			Weight:      1,
		})
	}
	for i, constraint := range config.Goal.Constraints {
		goal.Constraints = append(goal.Constraints, schemas.Constraint{
			ID:             workerSpecConstraintIDPrefix + strconv.Itoa(i),
			Description:    constraint,
			ConstraintType: workerSpecConstraintType,
			Category:       workerSpecConstraintCategory,
		})
	}

	loopConfig := workerSpecMap(config.LoopConfig)

	maxTokens := workerSpecDefaultMaxTokens
	if value, ok := workerSpecInt(loopConfig["max_tokens"]); ok {
		maxTokens = value
	} else if config.MaxTokens != nil {
		maxTokens = *config.MaxTokens
	}

	conversationMode := config.ConversationMode
	if conversationMode == "" {
		conversationMode = workerSpecConversationMode
	}

	return &WorkerSpec{
		ColonyID:         colonyID,
		Dir:              dir,
		ConfigPath:       configPath,
		AgentSpec:        spec,
		Goal:             goal,
		LoopConfig:       loopConfig,
		IdentityPrompt:   identityPrompt,
		MaxTokens:        maxTokens,
		ConversationMode: conversationMode,
	}
}

func workerSpecString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func workerSpecStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

// workerSpecMap copies a config map with numeric values normalized. Nil maps
// stay nil so an absent loop_config block is distinguishable from an empty one.
func workerSpecMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = normalizeWorkerSpecValue(value)
	}
	return out
}

// workerSpecInt reads an integer config value across the encodings the two
// readers produce: json.Number for JSON before normalization, int/float64 after.
func workerSpecInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

// normalizeWorkerSpecValue flattens json.Number into int/float64 so a config
// decodes to the same Go values whichever reader saw it. Python's json.load
// yields ints for integer literals, and colony.BuildWorkerLoopConfig expects
// that shape from the spawn path.
func normalizeWorkerSpecValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			if parsed <= math.MaxInt && parsed >= math.MinInt {
				return int(parsed)
			}
			return float64(parsed)
		}
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
		return typed.String()
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeWorkerSpecValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeWorkerSpecValue(item)
		}
		return out
	}
	return value
}
