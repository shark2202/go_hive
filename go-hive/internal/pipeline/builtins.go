package pipeline

// Built-in stage types, registered at package init so a configuration.json
// entry always resolves and an unknown type fails loudly (upstream
// _ensure_builtins_registered imports these modules for the same reason).
//
// Parity: framework/pipeline/registry.py::_ensure_builtins_registered
func init() {
	Register("llm_provider", NewLLMProviderStage)
	Register("credential_resolver", NewCredentialResolverStage)
	Register("mcp_registry", NewMCPRegistryStage)
	Register("skill_registry", NewSkillRegistryStage)
	Register("input_validation", NewInputValidationStage)
	Register("rate_limit", NewRateLimitStage)
	Register("cost_guard", NewCostGuardStage)
}
