package agentloop

import (
	"encoding/json"

	"github.com/shark2202/go_hive/go-hive/internal/hiveclock"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// Parity: framework/agent_loop/prompting.py PromptSpec (frozen dataclass).
type PromptSpec struct {
	IdentityPrompt          string
	FocusPrompt             string
	Narrative               string
	AccountsPrompt          string
	SkillsCatalogPrompt     string
	ProtocolsPrompt         string
	MemoryPrompt            string
	BindingPrompt           string
	AgentType               string
	OutputKeys              []string
	ReportSchemaInstruction string
}

type PromptOverrides struct {
	FocusPrompt  *string
	Narrative    *string
	MemoryPrompt *string
}

// Parity: framework/agent_loop/prompting.py stamp_prompt_datetime. The Python version uses
func StampPromptDatetime(prompt string) string {
	local := hiveclock.Now()
	stamp := "Current date: " + local.Format("2006-01-02 MST (UTC-0700)")
	if prompt == "" {
		return stamp
	}
	return prompt + "\n\n" + stamp
}

// Parity: framework/agent_loop/prompting.py build_binding_prompt.
func BuildBindingPrompt(binding *ColonyBinding) string {
	if binding == nil || binding.TrackerDB == "" {
		return ""
	}
	return "Tracker DB: " + binding.TrackerDB + "\n" +
		"(Your tracker_* tools target this automatically — never pass the path " +
		"to them. Use it directly only when you script a batch operation on the " +
		"DB, e.g. bulk-loading rows from other files.)"
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_structured_output_instruction.
func BuildStructuredOutputInstruction(reportSchema map[string]any) string {
	if len(reportSchema) == 0 {
		return ""
	}
	pretty, err := json.MarshalIndent(reportSchema, "", "  ")
	if err != nil {
		return ""
	}
	return "<structured_output>\n" +
		"Before you finish, your final report_to_parent call MUST include a `data` " +
		"object conforming to this JSON Schema — required fields present, enum " +
		"values exact. This is the structured receipt your overseer relies on; a " +
		"free-form summary is not enough.\n\n" +
		string(pretty) + "\n" +
		"</structured_output>"
}

// Parity: framework/agent_loop/prompting.py build_prompt_spec.
func BuildPromptSpec(ctx *AgentContext, overrides PromptOverrides) PromptSpec {
	if ctx == nil {
		return PromptSpec{AgentType: "event_loop"}
	}

	resolvedMemory := ctx.MemoryPrompt
	if overrides.MemoryPrompt != nil {
		resolvedMemory = *overrides.MemoryPrompt
	} else if ctx.DynamicMemoryProvider != nil {
		resolvedMemory = callStringProvider(ctx.DynamicMemoryProvider, ctx.MemoryPrompt)
	}

	toolNames := []string{}
	if namer, ok := ctx.ToolTierState.(CurrentToolNamer); ok {
		toolNames = namer.CurrentToolNames()
	} else {
		toolNames = make([]string, 0, len(ctx.AvailableTools))
		for _, tool := range ctx.AvailableTools {
			toolNames = append(toolNames, tool.Name)
		}
	}

	rawCatalog := ctx.SkillsCatalogPrompt
	if ctx.DynamicSkillsCatalogProvider != nil {
		rawCatalog = callStringProvider(ctx.DynamicSkillsCatalogProvider, ctx.SkillsCatalogPrompt)
	}
	skillsCatalog := skills.AugmentCatalogForTools(rawCatalog, toolNames)

	bindingPrompt := ""
	if ctx.ColonyBindingProvider != nil {
		bindingPrompt = callBindingProvider(ctx.ColonyBindingProvider)
	}

	focus := ""
	if overrides.FocusPrompt != nil {
		focus = *overrides.FocusPrompt
	} else if ctx.AgentSpec.SystemPrompt != nil {
		focus = *ctx.AgentSpec.SystemPrompt
	}

	narrative := ctx.Narrative
	if overrides.Narrative != nil {
		narrative = *overrides.Narrative
	}

	agentType := ctx.AgentSpec.AgentType
	if agentType == "" {
		agentType = "event_loop"
	}

	outputKeys := append([]string(nil), ctx.AgentSpec.OutputKeys...)

	return PromptSpec{
		IdentityPrompt:          ctx.IdentityPrompt,
		FocusPrompt:             focus,
		Narrative:               narrative,
		AccountsPrompt:          ctx.AccountsPrompt,
		SkillsCatalogPrompt:     skillsCatalog,
		ProtocolsPrompt:         ctx.ProtocolsPrompt,
		MemoryPrompt:            resolvedMemory,
		BindingPrompt:           bindingPrompt,
		AgentType:               agentType,
		OutputKeys:              outputKeys,
		ReportSchemaInstruction: BuildStructuredOutputInstruction(ctx.AgentSpec.ReportSchema),
	}
}

// Parity: framework/agent_loop/prompting.py build_system_prompt_static (sections after the first
func BuildSystemPromptStatic(spec PromptSpec) string {
	parts := []string{}
	if spec.IdentityPrompt != "" {
		parts = append(parts, spec.IdentityPrompt)
	}
	if spec.BindingPrompt != "" {
		parts = append(parts, "\n"+spec.BindingPrompt)
	}
	if spec.AccountsPrompt != "" {
		parts = append(parts, "\n"+spec.AccountsPrompt)
	}
	if spec.SkillsCatalogPrompt != "" {
		parts = append(parts, "\n"+spec.SkillsCatalogPrompt)
	}
	if spec.ProtocolsPrompt != "" {
		parts = append(parts, "\n"+spec.ProtocolsPrompt)
	}
	if spec.MemoryPrompt != "" {
		parts = append(parts, "\n"+spec.MemoryPrompt)
	}
	if spec.FocusPrompt != "" {
		parts = append(parts, "\n"+spec.FocusPrompt)
	}
	if spec.ReportSchemaInstruction != "" {
		parts = append(parts, "\n"+spec.ReportSchemaInstruction)
	}
	return joinWithNewline(parts)
}

// Parity: framework/agent_loop/prompting.py build_system_prompt_dynamic_suffix.
func BuildSystemPromptDynamicSuffix(spec PromptSpec) string {
	parts := []string{}
	if spec.Narrative != "" {
		parts = append(parts, spec.Narrative)
	}
	return joinWithNewline(parts)
}

// JoinSystemPromptParts recombines the split prompt the way upstream's
// build_system_prompt does: the dynamic suffix rides after the static prefix,
// separated by a blank line. An empty leg contributes nothing (no stray
// separators), so a context with no narrative and no catalog renders exactly
// the static text.
func JoinSystemPromptParts(static, suffix string) string {
	if suffix == "" {
		return static
	}
	if static == "" {
		return suffix
	}
	return static + "\n\n" + suffix
}

// Parity: framework/agent_loop/prompting.py build_system_prompt.
func BuildSystemPrompt(spec PromptSpec) string {
	return JoinSystemPromptParts(BuildSystemPromptStatic(spec), BuildSystemPromptDynamicSuffix(spec))
}

func BuildSystemPromptForContext(ctx *AgentContext, overrides PromptOverrides) string {
	return BuildSystemPrompt(BuildPromptSpec(ctx, overrides))
}

func BuildSystemPromptPartsForContext(ctx *AgentContext, overrides PromptOverrides) (string, string) {
	spec := BuildPromptSpec(ctx, overrides)
	return BuildSystemPromptStatic(spec), BuildSystemPromptDynamicSuffix(spec)
}

func joinWithNewline(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, part := range parts[1:] {
		out += "\n" + part
	}
	return out
}

func callStringProvider(provider func() string, fallback string) (result string) {
	defer func() {
		if recover() != nil {
			result = fallback
		}
	}()
	return provider()
}

func callBindingProvider(provider func() *ColonyBinding) (result string) {
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	return BuildBindingPrompt(provider())
}
