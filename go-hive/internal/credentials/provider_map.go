package credentials

// ToolProviderMap mirrors CredentialStoreAdapter.get_tool_provider_map: every
// credential-backed tool name mapped to the credential id that gates it. The
// table is generated (tool_provider_map_gen.go) because the winner for a tool
// two specs list is decided by the CREDENTIAL_SPECS merge order, which only the
// Python source encodes. The MCP admission gate and the live-tools diagnostic
// read this map instead of an empty one.
func ToolProviderMap() map[string]string {
	out := make(map[string]string, len(GeneratedToolProviderMap))
	for name, provider := range GeneratedToolProviderMap {
		out[name] = provider
	}
	return out
}
