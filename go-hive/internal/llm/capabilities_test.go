package llm

import "testing"

// Parity: core/tests/test_capabilities.py::TestSupportsImageToolResults::test_supported_models
func TestSupportedModels(t *testing.T) {
	models := []string{
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-5-20250929",
		"claude-opus-4-6",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gemini-3-flash-preview",
		"kimi-k2.5",
		"kimi-k2.6",
		"queen",
		"openrouter/openai/gpt-5.4",
		"openrouter/anthropic/claude-sonnet-4.6",
		"some-future-model",
		"azure/gpt-5",
	}
	for _, model := range models {
		if !SupportsImageToolResults(model) {
			t.Errorf("SupportsImageToolResults(%q) = false, want true", model)
		}
	}
}

// Parity: core/tests/test_capabilities.py::TestSupportsImageToolResults::test_unsupported_models
func TestUnsupportedModels(t *testing.T) {
	models := []string{
		"deepseek-reasoner",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"glm-5.1",
		"MiniMax-M2.7",
		"codestral-2508",
		"llama-3.3-70b-versatile",
		"deepseek/deepseek-reasoner",
		"hive/glm-5.1",
		"groq/llama-3.3-70b-versatile",
	}
	for _, model := range models {
		if SupportsImageToolResults(model) {
			t.Errorf("SupportsImageToolResults(%q) = true, want false", model)
		}
	}
}

// Parity: core/tests/test_capabilities.py::TestFilterToolsForModel::test_hides_image_tool_from_text_only_model
func TestHidesImageToolFromTextOnlyModel(t *testing.T) {
	tools := []ToolDef{
		{Name: "read_file", Description: "read a file"},
		{Name: "browser_screenshot", Description: "take a screenshot", ProducesImage: true},
		{Name: "browser_snapshot", Description: "get page content"},
	}
	filtered, hidden := FilterToolsForModel(tools, "glm-5.1")
	names := map[string]bool{}
	for _, tool := range filtered {
		names[tool.Name] = true
	}
	if names["browser_screenshot"] {
		t.Error("browser_screenshot should be hidden")
	}
	if !names["read_file"] || !names["browser_snapshot"] {
		t.Error("non-image tools should be kept")
	}
	if len(hidden) != 1 || hidden[0] != "browser_screenshot" {
		t.Errorf("hidden = %v, want [browser_screenshot]", hidden)
	}
}

// Parity: core/tests/test_capabilities.py::TestFilterToolsForModel::test_keeps_image_tool_for_vision_model
func TestKeepsImageToolForVisionModel(t *testing.T) {
	tools := []ToolDef{
		{Name: "read_file", Description: "read a file"},
		{Name: "browser_screenshot", Description: "take a screenshot", ProducesImage: true},
	}
	filtered, hidden := FilterToolsForModel(tools, "claude-sonnet-4-5-20250929")
	if len(filtered) != 2 {
		t.Fatalf("filtered len = %d, want 2", len(filtered))
	}
	if len(hidden) != 0 {
		t.Errorf("hidden = %v, want empty", hidden)
	}
}

// Parity: core/tests/test_capabilities.py::TestFilterToolsForModel::test_default_tools_are_not_filtered
func TestDefaultToolsAreNotFiltered(t *testing.T) {
	tools := []ToolDef{
		{Name: "read_file", Description: "read a file"},
		{Name: "web_search", Description: "search the web"},
	}
	textOnly, textHidden := FilterToolsForModel(tools, "glm-5.1")
	vision, visionHidden := FilterToolsForModel(tools, "claude-sonnet-4-5-20250929")
	if len(textOnly) != 2 || len(textHidden) != 0 {
		t.Errorf("text-only filter = %d tools, hidden %v", len(textOnly), textHidden)
	}
	if len(vision) != 2 || len(visionHidden) != 0 {
		t.Errorf("vision filter = %d tools, hidden %v", len(vision), visionHidden)
	}
}

// Parity: core/tests/test_capabilities.py::TestFilterToolsForModel::test_empty_model_string_returns_tools_unchanged
func TestEmptyModelStringReturnsToolsUnchanged(t *testing.T) {
	tools := []ToolDef{{Name: "browser_screenshot", Description: "", ProducesImage: true}}
	filtered, hidden := FilterToolsForModel(tools, "")
	if len(filtered) != 1 || len(hidden) != 0 {
		t.Errorf("filtered = %d, hidden = %v", len(filtered), hidden)
	}
}

// Parity: core/tests/test_capabilities.py::TestFilterToolsForModel::test_returned_list_is_a_copy
func TestReturnedListIsACopy(t *testing.T) {
	tools := []ToolDef{{Name: "read_file", Description: ""}}
	filtered, _ := FilterToolsForModel(tools, "gpt-4o")
	filtered = append(filtered, ToolDef{Name: "extra", Description: ""})
	if len(tools) != 1 {
		t.Errorf("input slice mutated: len = %d", len(tools))
	}
}

// Parity: core/tests/test_capabilities.py::TestSupportsImagesInToolResults::test_anthropic_routed_models_keep_images_in_tool_results
func TestAnthropicRoutedModelsKeepImagesInToolResults(t *testing.T) {
	models := []string{
		"anthropic/claude-opus-4-6",
		"claude-sonnet-4-5-20250929",
		"openrouter/anthropic/claude-sonnet-4.6",
		"hive/claude-opus-4-6",
		"kimi/kimi-k2.6",
		"Anthropic/Claude-Opus-4-6",
	}
	for _, model := range models {
		if !SupportsImagesInToolResults(model) {
			t.Errorf("SupportsImagesInToolResults(%q) = false, want true", model)
		}
	}
}

// Parity: core/tests/test_capabilities.py::TestSupportsImagesInToolResults::test_other_apis_need_images_hoisted
func TestOtherAPIsNeedImagesHoisted(t *testing.T) {
	models := []string{
		"openai/gpt-6-astra",
		"gpt-5.5",
		"openrouter/openai/gpt-5.4",
		"gemini/gemini-3-flash-preview",
		"azure/gpt-5",
	}
	for _, model := range models {
		if SupportsImagesInToolResults(model) {
			t.Errorf("SupportsImagesInToolResults(%q) = true, want false", model)
		}
	}
}

// Parity: core/tests/test_capabilities.py::TestSupportsImagesInToolResults::test_empty_model_leaves_messages_alone
func TestEmptyModelLeavesMessagesAlone(t *testing.T) {
	if !SupportsImagesInToolResults("") {
		t.Error("empty model should report capable")
	}
}
