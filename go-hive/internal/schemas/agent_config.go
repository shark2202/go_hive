package schemas

import (
	"encoding/json"
	"errors"
)

const ErrPolicyAll = "tool policy 'all' is not allowed — list every tool explicitly in 'allowed' instead. This ensures agents only see the tools they need."

type ToolAccessConfig struct {
	Policy  string   `json:"policy"`
	Allowed []string `json:"allowed"`
	Denied  []string `json:"denied"`
}

func (t *ToolAccessConfig) Validate() error {
	if t.Policy == "all" {
		return errors.New(ErrPolicyAll)
	}
	return nil
}

func (t ToolAccessConfig) MarshalJSON() ([]byte, error) {
	type plain ToolAccessConfig
	v := plain(t)
	if v.Policy == "" {
		v.Policy = "explicit"
	}
	if v.Allowed == nil {
		v.Allowed = []string{}
	}
	if v.Denied == nil {
		v.Denied = []string{}
	}
	return json.Marshal(v)
}

func (t *ToolAccessConfig) UnmarshalJSON(b []byte) error {
	type plain ToolAccessConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if tools, ok := raw["tools"]; ok {
		var allowed []string
		if err := json.Unmarshal(tools, &allowed); err != nil {
			return err
		}
		v.Allowed = allowed
	}
	*t = ToolAccessConfig(v)
	if t.Policy == "" {
		t.Policy = "explicit"
	}
	if t.Allowed == nil {
		t.Allowed = []string{}
	}
	if t.Denied == nil {
		t.Denied = []string{}
	}
	return t.Validate()
}

type NodeConfig struct {
	ID                 string           `json:"id"`
	Name               *string          `json:"name"`
	Description        *string          `json:"description"`
	NodeType           string           `json:"node_type"`
	SystemPrompt       *string          `json:"system_prompt"`
	Tools              ToolAccessConfig `json:"tools"`
	Model              *string          `json:"model"`
	InputKeys          []string         `json:"input_keys"`
	OutputKeys         []string         `json:"output_keys"`
	NullableOutputKeys []string         `json:"nullable_output_keys"`
	MaxIterations      int              `json:"max_iterations"`
	MaxNodeVisits      int              `json:"max_node_visits"`
	ClientFacing       bool             `json:"client_facing"`
	SuccessCriteria    *string          `json:"success_criteria"`
	FailureCriteria    *string          `json:"failure_criteria"`
	SkipJudge          bool             `json:"skip_judge"`
	MaxRetries         *int             `json:"max_retries"`
}

func (n NodeConfig) MarshalJSON() ([]byte, error) {
	type plain NodeConfig
	v := plain(n)
	if v.NodeType == "" {
		v.NodeType = "event_loop"
	}
	if v.Tools.Policy == "" {
		v.Tools.Policy = "explicit"
	}
	if v.Tools.Allowed == nil {
		v.Tools.Allowed = []string{}
	}
	if v.Tools.Denied == nil {
		v.Tools.Denied = []string{}
	}
	if v.InputKeys == nil {
		v.InputKeys = []string{}
	}
	if v.OutputKeys == nil {
		v.OutputKeys = []string{}
	}
	if v.NullableOutputKeys == nil {
		v.NullableOutputKeys = []string{}
	}
	if v.MaxIterations == 0 {
		v.MaxIterations = 30
	}
	if v.MaxNodeVisits == 0 {
		v.MaxNodeVisits = 1
	}
	return json.Marshal(v)
}

func (n *NodeConfig) UnmarshalJSON(b []byte) error {
	type plain NodeConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["node_type"]; !ok {
		v.NodeType = "event_loop"
	}
	if _, ok := raw["max_iterations"]; !ok {
		v.MaxIterations = 30
	}
	if _, ok := raw["max_node_visits"]; !ok {
		v.MaxNodeVisits = 1
	}
	if v.InputKeys == nil {
		v.InputKeys = []string{}
	}
	if v.OutputKeys == nil {
		v.OutputKeys = []string{}
	}
	if v.NullableOutputKeys == nil {
		v.NullableOutputKeys = []string{}
	}
	*n = NodeConfig(v)
	return nil
}

func (n *NodeConfig) Validate() error { return n.Tools.Validate() }

type EdgeConfig struct {
	FromNode      string            `json:"from_node"`
	ToNode        string            `json:"to_node"`
	Condition     string            `json:"condition"`
	ConditionExpr *string           `json:"condition_expr"`
	InputMapping  map[string]string `json:"input_mapping"`
	Priority      int               `json:"priority"`
}

func (e EdgeConfig) MarshalJSON() ([]byte, error) {
	type plain EdgeConfig
	v := plain(e)
	if v.Condition == "" {
		v.Condition = "on_success"
	}
	if v.InputMapping == nil {
		v.InputMapping = map[string]string{}
	}
	if v.Priority == 0 {
		v.Priority = 1
	}
	return json.Marshal(v)
}

func (e *EdgeConfig) UnmarshalJSON(b []byte) error {
	type plain EdgeConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["condition"]; !ok {
		v.Condition = "on_success"
	}
	if v.InputMapping == nil {
		v.InputMapping = map[string]string{}
	}
	if _, ok := raw["priority"]; !ok {
		v.Priority = 1
	}
	*e = EdgeConfig(v)
	return nil
}

type GoalConfig struct {
	Description     string   `json:"description"`
	SuccessCriteria []string `json:"success_criteria"`
	Constraints     []string `json:"constraints"`
}

func (g GoalConfig) MarshalJSON() ([]byte, error) {
	type plain GoalConfig
	v := plain(g)
	if v.SuccessCriteria == nil {
		v.SuccessCriteria = []string{}
	}
	if v.Constraints == nil {
		v.Constraints = []string{}
	}
	return json.Marshal(v)
}

func (g *GoalConfig) UnmarshalJSON(b []byte) error {
	type plain GoalConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.SuccessCriteria == nil {
		v.SuccessCriteria = []string{}
	}
	if v.Constraints == nil {
		v.Constraints = []string{}
	}
	*g = GoalConfig(v)
	return nil
}

type EntryPointConfig struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	EntryNode      *string        `json:"entry_node"`
	TriggerType    string         `json:"trigger_type"`
	TriggerConfig  map[string]any `json:"trigger_config"`
	IsolationLevel string         `json:"isolation_level"`
	MaxConcurrent  *int           `json:"max_concurrent"`
}

func (e EntryPointConfig) MarshalJSON() ([]byte, error) {
	type plain EntryPointConfig
	v := plain(e)
	if v.ID == "" {
		v.ID = "default"
	}
	if v.Name == "" {
		v.Name = "Default"
	}
	if v.TriggerType == "" {
		v.TriggerType = "manual"
	}
	if v.TriggerConfig == nil {
		v.TriggerConfig = map[string]any{}
	}
	if v.IsolationLevel == "" {
		v.IsolationLevel = "shared"
	}
	return json.Marshal(v)
}

func (e *EntryPointConfig) UnmarshalJSON(b []byte) error {
	type plain EntryPointConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["id"]; !ok {
		v.ID = "default"
	}
	if _, ok := raw["name"]; !ok {
		v.Name = "Default"
	}
	if _, ok := raw["trigger_type"]; !ok {
		v.TriggerType = "manual"
	}
	if _, ok := raw["isolation_level"]; !ok {
		v.IsolationLevel = "shared"
	}
	if v.TriggerConfig == nil {
		v.TriggerConfig = map[string]any{}
	}
	*e = EntryPointConfig(v)
	return nil
}

type MCPServerRef struct {
	Name   string         `json:"name"`
	Config map[string]any `json:"config"`
}

type MetadataConfig struct {
	IntroMessage string `json:"intro_message"`
}

type AgentConfig struct {
	Name             string             `json:"name"`
	Version          string             `json:"version"`
	Description      *string            `json:"description"`
	Metadata         MetadataConfig     `json:"metadata"`
	Variables        map[string]string  `json:"variables"`
	Goal             GoalConfig         `json:"goal"`
	Nodes            []NodeConfig       `json:"nodes"`
	Edges            []EdgeConfig       `json:"edges"`
	EntryNode        string             `json:"entry_node"`
	TerminalNodes    []string           `json:"terminal_nodes"`
	PauseNodes       []string           `json:"pause_nodes"`
	EntryPoints      []EntryPointConfig `json:"entry_points"`
	Tools            ToolAccessConfig   `json:"tools"`
	MCPServers       []MCPServerRef     `json:"mcp_servers"`
	Model            *string            `json:"model"`
	MaxTokens        int                `json:"max_tokens"`
	ConversationMode string             `json:"conversation_mode"`
	IdentityPrompt   string             `json:"identity_prompt"`
	LoopConfig       map[string]any     `json:"loop_config"`
	Pipeline         map[string]any     `json:"pipeline"`
	MaxCostPerRun    *float64           `json:"max_cost_per_run"`
}

func DefaultLoopConfig() map[string]any {
	return map[string]any{
		"max_iterations":     100,
		"tool_call_budget":   30,
		"max_context_tokens": 180000,
	}
}

func (a *AgentConfig) Validate() error {
	if err := a.Tools.Validate(); err != nil {
		return err
	}
	for i := range a.Nodes {
		if err := a.Nodes[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

func ParseAgentConfig(data []byte) (*AgentConfig, error) {
	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (a AgentConfig) MarshalJSON() ([]byte, error) {
	type plain AgentConfig
	v := plain(a)
	if v.Version == "" {
		v.Version = "1.0.0"
	}
	if v.Variables == nil {
		v.Variables = map[string]string{}
	}
	if v.Nodes == nil {
		v.Nodes = []NodeConfig{}
	}
	if v.Edges == nil {
		v.Edges = []EdgeConfig{}
	}
	if v.TerminalNodes == nil {
		v.TerminalNodes = []string{}
	}
	if v.PauseNodes == nil {
		v.PauseNodes = []string{}
	}
	if v.EntryPoints == nil {
		v.EntryPoints = []EntryPointConfig{}
	}
	if v.Tools.Policy == "" {
		v.Tools.Policy = "explicit"
	}
	if v.Tools.Allowed == nil {
		v.Tools.Allowed = []string{}
	}
	if v.Tools.Denied == nil {
		v.Tools.Denied = []string{}
	}
	if v.MCPServers == nil {
		v.MCPServers = []MCPServerRef{}
	}
	if v.MaxTokens == 0 {
		v.MaxTokens = 4096
	}
	if v.ConversationMode == "" {
		v.ConversationMode = "continuous"
	}
	if v.LoopConfig == nil {
		v.LoopConfig = DefaultLoopConfig()
	}
	if v.Pipeline == nil {
		v.Pipeline = map[string]any{}
	}
	return json.Marshal(v)
}

func (a *AgentConfig) UnmarshalJSON(b []byte) error {
	type plain AgentConfig
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["version"]; !ok {
		v.Version = "1.0.0"
	}
	if v.Variables == nil {
		v.Variables = map[string]string{}
	}
	if v.Nodes == nil {
		v.Nodes = []NodeConfig{}
	}
	if v.Edges == nil {
		v.Edges = []EdgeConfig{}
	}
	if v.TerminalNodes == nil {
		v.TerminalNodes = []string{}
	}
	if v.PauseNodes == nil {
		v.PauseNodes = []string{}
	}
	if v.EntryPoints == nil {
		v.EntryPoints = []EntryPointConfig{}
	}
	if _, ok := raw["max_tokens"]; !ok {
		v.MaxTokens = 4096
	}
	if _, ok := raw["conversation_mode"]; !ok {
		v.ConversationMode = "continuous"
	}
	if _, ok := raw["loop_config"]; !ok {
		v.LoopConfig = DefaultLoopConfig()
	}
	if v.Pipeline == nil {
		v.Pipeline = map[string]any{}
	}
	if v.MCPServers == nil {
		v.MCPServers = []MCPServerRef{}
	}
	*a = AgentConfig(v)
	return a.Validate()
}
