package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Parity: core/tests/test_model_catalog.py::test_default_models_exist_in_each_provider_catalogue
// File-level anchor: the helpers below mirror the fixture shapes of that test module.

func mustDefaultModels(t *testing.T) map[string]string {
	t.Helper()
	models := GetDefaultModels()
	if len(models) == 0 {
		t.Fatal("GetDefaultModels returned no providers")
	}
	return models
}

func modelIDs(models []ModelEntry) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Parity: core/tests/test_model_catalog.py::test_default_models_exist_in_each_provider_catalogue
func TestDefaultModelsExistInEachProviderCatalogue(t *testing.T) {
	t.Setenv("HIVE_CLOUD_BASE", "")
	defaults := mustDefaultModels(t)
	catalogue := GetModelsCatalogue()

	for providerID, defaultModel := range defaults {
		models, surfaced := catalogue[providerID]
		if !surfaced {
			continue
		}
		found := false
		for _, model := range models {
			if model.ID == defaultModel {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s default model %q not present in catalogue", providerID, defaultModel)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_find_model_returns_curated_token_limits
func TestFindModelReturnsCuratedTokenLimits(t *testing.T) {
	model, ok := FindModel("openai", "gpt-5.5")
	if !ok {
		t.Fatal("openai/gpt-5.5 not found")
	}
	if model.Label != "GPT-5.5 - Frontier coding + reasoning" {
		t.Errorf("label = %q", model.Label)
	}
	if model.MaxTokens != 128000 {
		t.Errorf("max_tokens = %d, want 128000", model.MaxTokens)
	}
	if model.MaxContextTokens != 1050000 {
		t.Errorf("max_context_tokens = %d, want 1050000", model.MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_anthropic_curated_limits_track_documented_caps_with_safe_input_budget
func TestAnthropicCuratedLimitsTrackDocumentedCapsWithSafeInputBudget(t *testing.T) {
	haiku, ok := FindModel("anthropic", "claude-haiku-4-5-20251001")
	if !ok {
		t.Fatal("haiku not found")
	}
	sonnet45, ok := FindModel("anthropic", "claude-sonnet-4-5-20250929")
	if !ok {
		t.Fatal("sonnet 4.5 not found")
	}
	opus46, ok := FindModel("anthropic", "claude-opus-4-6")
	if !ok {
		t.Fatal("opus 4.6 not found")
	}

	checks := []struct {
		name   string
		model  ModelEntry
		maxOut int
		maxCtx int
	}{
		{"haiku", haiku, 64000, 136000},
		{"sonnet 4.5", sonnet45, 64000, 136000},
		{"opus 4.6", opus46, 128000, 872000},
	}
	for _, check := range checks {
		if check.model.MaxTokens != check.maxOut {
			t.Errorf("%s max_tokens = %d, want %d", check.name, check.model.MaxTokens, check.maxOut)
		}
		if check.model.MaxContextTokens != check.maxCtx {
			t.Errorf("%s max_context_tokens = %d, want %d", check.name, check.model.MaxContextTokens, check.maxCtx)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_groq_catalog_tracks_current_production_models
func TestGroqCatalogTracksCurrentProductionModels(t *testing.T) {
	defaultModel, ok := GetDefaultModel("groq")
	if !ok {
		t.Fatal("groq provider missing")
	}
	if defaultModel != "openai/gpt-oss-120b" {
		t.Errorf("groq default = %q", defaultModel)
	}
	models := GetProviderModels("groq")
	want := []string{
		"openai/gpt-oss-120b",
		"openai/gpt-oss-20b",
		"llama-3.3-70b-versatile",
		"llama-3.1-8b-instant",
	}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if models[0].MaxTokens != 65536 {
		t.Errorf("max_tokens = %d, want 65536", models[0].MaxTokens)
	}
	if models[0].MaxContextTokens != 131072 {
		t.Errorf("max_context_tokens = %d, want 131072", models[0].MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_cerebras_catalog_tracks_public_models_endpoint
func TestCerebrasCatalogTracksPublicModelsEndpoint(t *testing.T) {
	defaultModel, ok := GetDefaultModel("cerebras")
	if !ok {
		t.Fatal("cerebras provider missing")
	}
	if defaultModel != "gpt-oss-120b" {
		t.Errorf("cerebras default = %q", defaultModel)
	}
	models := GetProviderModels("cerebras")
	want := []string{
		"gpt-oss-120b",
		"zai-glm-4.7",
		"qwen-3-235b-a22b-instruct-2507",
	}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	for _, model := range models {
		if model.MaxTokens != 40960 {
			t.Errorf("%s max_tokens = %d, want 40960", model.ID, model.MaxTokens)
		}
		if model.MaxContextTokens != 131072 {
			t.Errorf("%s max_context_tokens = %d, want 131072", model.ID, model.MaxContextTokens)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_minimax_catalog_tracks_current_non_legacy_text_models
func TestMinimaxCatalogTracksCurrentNonLegacyTextModels(t *testing.T) {
	defaultModel, ok := GetDefaultModel("minimax")
	if !ok {
		t.Fatal("minimax provider missing")
	}
	if defaultModel != "MiniMax-M2.7" {
		t.Errorf("minimax default = %q", defaultModel)
	}
	models := GetProviderModels("minimax")
	want := []string{"MiniMax-M2.7", "MiniMax-M2.5"}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	for _, model := range models {
		if model.MaxContextTokens != 180000 {
			t.Errorf("%s max_context_tokens = %d, want 180000", model.ID, model.MaxContextTokens)
		}
		if model.MaxTokens != 40960 {
			t.Errorf("%s max_tokens = %d, want 40960", model.ID, model.MaxTokens)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_mistral_catalog_tracks_current_curated_models
func TestMistralCatalogTracksCurrentCuratedModels(t *testing.T) {
	defaultModel, ok := GetDefaultModel("mistral")
	if !ok {
		t.Fatal("mistral provider missing")
	}
	if defaultModel != "mistral-large-2512" {
		t.Errorf("mistral default = %q", defaultModel)
	}
	models := GetProviderModels("mistral")
	want := []string{
		"mistral-large-2512",
		"mistral-medium-2508",
		"mistral-small-2603",
		"codestral-2508",
	}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	wantContexts := []int{256000, 128000, 256000, 128000}
	for i, want := range wantContexts {
		if models[i].MaxContextTokens != want {
			t.Errorf("%s max_context_tokens = %d, want %d", models[i].ID, models[i].MaxContextTokens, want)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_together_catalog_tracks_current_serverless_recommendations
func TestTogetherCatalogTracksCurrentServerlessRecommendations(t *testing.T) {
	defaultModel, ok := GetDefaultModel("together")
	if !ok {
		t.Fatal("together provider missing")
	}
	if defaultModel != "deepseek-ai/DeepSeek-V3.1" {
		t.Errorf("together default = %q", defaultModel)
	}
	models := GetProviderModels("together")
	want := []string{
		"deepseek-ai/DeepSeek-V3.1",
		"Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8",
		"openai/gpt-oss-120b",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo",
	}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	wantContexts := []int{128000, 262144, 128000, 131072}
	for i, want := range wantContexts {
		if models[i].MaxContextTokens != want {
			t.Errorf("%s max_context_tokens = %d, want %d", models[i].ID, models[i].MaxContextTokens, want)
		}
	}
}

// Parity: core/tests/test_model_catalog.py::test_deepseek_catalog_tracks_current_api_models
func TestDeepseekCatalogTracksCurrentAPIModels(t *testing.T) {
	defaultModel, ok := GetDefaultModel("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if defaultModel != "deepseek-v4-pro" {
		t.Errorf("deepseek default = %q", defaultModel)
	}
	models := GetProviderModels("deepseek")
	want := []string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-reasoner"}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if models[0].MaxTokens != 384000 || models[0].MaxContextTokens != 1000000 {
		t.Errorf("v4-pro limits = %d/%d", models[0].MaxTokens, models[0].MaxContextTokens)
	}
	if models[0].PricingUSDPerMTok["input"] != 1.74 {
		t.Errorf("v4-pro input price = %v", models[0].PricingUSDPerMTok["input"])
	}
	if models[0].PricingUSDPerMTok["output"] != 3.48 {
		t.Errorf("v4-pro output price = %v", models[0].PricingUSDPerMTok["output"])
	}
	if models[1].PricingUSDPerMTok["input"] != 0.14 {
		t.Errorf("v4-flash input price = %v", models[1].PricingUSDPerMTok["input"])
	}
	if models[1].PricingUSDPerMTok["output"] != 0.28 {
		t.Errorf("v4-flash output price = %v", models[1].PricingUSDPerMTok["output"])
	}
	if models[2].MaxTokens != 64000 || models[2].MaxContextTokens != 128000 {
		t.Errorf("reasoner limits = %d/%d", models[2].MaxTokens, models[2].MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_openrouter_catalog_tracks_current_frontier_set
func TestOpenrouterCatalogTracksCurrentFrontierSet(t *testing.T) {
	defaultModel, ok := GetDefaultModel("openrouter")
	if !ok {
		t.Fatal("openrouter provider missing")
	}
	if defaultModel != "openai/gpt-5.4" {
		t.Errorf("openrouter default = %q", defaultModel)
	}
	models := GetProviderModels("openrouter")
	want := []string{
		"openai/gpt-5.4",
		"anthropic/claude-sonnet-4.6",
		"anthropic/claude-opus-4.6",
		"google/gemini-3.1-pro-preview-customtools",
		"qwen/qwen3.6-plus",
		"z-ai/glm-5v-turbo",
		"z-ai/glm-5.1",
		"minimax/minimax-m2.7",
		"xiaomi/mimo-v2-pro",
	}
	if got := modelIDs(models); !equalStrings(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if models[0].MaxTokens != 128000 || models[0].MaxContextTokens != 872000 {
		t.Errorf("gpt-5.4 limits = %d/%d", models[0].MaxTokens, models[0].MaxContextTokens)
	}
	for i := 1; i <= 3; i++ {
		if models[i].MaxContextTokens != 872000 {
			t.Errorf("%s max_context_tokens = %d, want 872000", models[i].ID, models[i].MaxContextTokens)
		}
	}
	if models[4].MaxContextTokens != 240000 {
		t.Errorf("%s max_context_tokens = %d, want 240000", models[4].ID, models[4].MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_find_model_any_provider_returns_provider_and_model
func TestFindModelAnyProviderReturnsProviderAndModel(t *testing.T) {
	providerID, model, ok := FindModelAnyProvider("google/gemini-3.1-pro-preview-customtools")
	if !ok {
		t.Fatal("model not found in any provider")
	}
	if providerID != "openrouter" {
		t.Errorf("provider = %q, want openrouter", providerID)
	}
	if model.MaxContextTokens != 872000 {
		t.Errorf("max_context_tokens = %d, want 872000", model.MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_get_preset_returns_subscription_specific_limits
func TestGetPresetReturnsSubscriptionSpecificLimits(t *testing.T) {
	preset, ok := GetPreset("kimi_code")
	if !ok {
		t.Fatal("kimi_code preset missing")
	}
	if preset.Provider != "kimi" {
		t.Errorf("provider = %q", preset.Provider)
	}
	if preset.Model != "kimi-k2.6" {
		t.Errorf("model = %q", preset.Model)
	}
	if preset.MaxTokens != 32768 {
		t.Errorf("max_tokens = %d", preset.MaxTokens)
	}
	if preset.MaxContextTokens != 262144 {
		t.Errorf("max_context_tokens = %d", preset.MaxContextTokens)
	}
	if preset.APIBase != "https://api.kimi.com/coding" {
		t.Errorf("api_base = %q", preset.APIBase)
	}
}

// Parity: core/tests/test_model_catalog.py::test_minimax_preset_uses_current_default_model
func TestMinimaxPresetUsesCurrentDefaultModel(t *testing.T) {
	preset, ok := GetPreset("minimax_code")
	if !ok {
		t.Fatal("minimax_code preset missing")
	}
	if preset.Model != "MiniMax-M2.7" {
		t.Errorf("model = %q", preset.Model)
	}
	if preset.MaxTokens != 40960 {
		t.Errorf("max_tokens = %d", preset.MaxTokens)
	}
	if preset.MaxContextTokens != 180800 {
		t.Errorf("max_context_tokens = %d", preset.MaxContextTokens)
	}
}

// Parity: core/tests/test_model_catalog.py::test_load_model_catalog_rejects_duplicate_model_ids
func TestLoadModelCatalogRejectsDuplicateModelIDs(t *testing.T) {
	ResetModelCatalogCache()
	defer ResetModelCatalogCache()

	model := func(label string, recommended bool) map[string]any {
		return map[string]any{
			"id":                 "dup-model",
			"label":              label,
			"recommended":        recommended,
			"max_tokens":         json.Number("1"),
			"max_context_tokens": json.Number("1"),
		}
	}
	bad := map[string]any{
		"schema_version": json.Number("1"),
		"providers": map[string]any{
			"anthropic": map[string]any{
				"default_model": "dup-model",
				"models":        []any{model("First", true), model("Second", false)},
			},
		},
	}
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal bad catalog: %v", err)
	}
	if _, err := parseModelCatalog(raw); err == nil {
		t.Fatal("expected duplicate model id error")
	} else if !strings.Contains(err.Error(), "Duplicate model id") {
		t.Fatalf("error = %v, want duplicate model id", err)
	}
}

// Parity-exempt: HIVE_CLOUD_BASE gating is covered by the Python catalogue
func TestManagedProvidersHiddenUnlessCloudBaseSet(t *testing.T) {
	t.Setenv("HIVE_CLOUD_BASE", "")
	if _, ok := GetModelsCatalogue()["hive"]; ok {
		t.Error("hive provider should be hidden without HIVE_CLOUD_BASE")
	}
	t.Setenv("HIVE_CLOUD_BASE", "https://cloud.example.test")
	if _, ok := GetModelsCatalogue()["hive"]; !ok {
		t.Error("hive provider should be surfaced with HIVE_CLOUD_BASE")
	}
}

// Parity-exempt: cache reset is a Go-only test seam mirroring
func TestResetModelCatalogCacheReparses(t *testing.T) {
	first, err := LoadModelCatalog()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	second, err := LoadModelCatalog()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first != second {
		t.Error("catalogue should be memoized between calls")
	}
	ResetModelCatalogCache()
	third, err := LoadModelCatalog()
	if err != nil {
		t.Fatalf("reload after reset: %v", err)
	}
	if first == third {
		t.Error("catalogue should be rebuilt after cache reset")
	}
}
