package credentials

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.get_tool_provider_map

import "testing"

// TestToolProviderMapFollowsMergeOrder pins the generated reverse index:
// tools two specs list belong to the spec that appears later in
// CREDENTIAL_SPECS order, exactly as the adapter's “_tool_to_cred“ does.
// Parity: core/framework/agents/queen/queen_tools_defaults.py::CREDENTIAL_SPECS
func TestToolProviderMapFollowsMergeOrder(t *testing.T) {
	providers := ToolProviderMap()
	if len(providers) < 800 {
		t.Fatalf("tool provider map = %d entries, want the full registry (>800)", len(providers))
	}
	want := map[string]string{
		"airtable_list_bases":       "airtable_pat",
		"freshdesk_create_ticket":   "freshdesk_domain",
		"google_search":             "google_cse",
		"kafka_list_topics":         "kafka_cluster_id",
		"mongodb_find":              "mongodb_data_source",
		"quickbooks_create_invoice": "quickbooks_realm_id",
		"sap_list_products":         "sap_password",
		"tines_get_story":           "tines_api_key",
	}
	for tool, provider := range want {
		if got := providers[tool]; got != provider {
			t.Fatalf("providers[%q] = %q, want %q", tool, got, provider)
		}
	}

	// The returned map is a copy: a caller must not be able to corrupt the
	// generated table for the rest of the process.
	providers["airtable_list_bases"] = "mutated"
	if got := ToolProviderMap()["airtable_list_bases"]; got != "airtable_pat" {
		t.Fatalf("generated table mutated through the copy: %q", got)
	}
}
