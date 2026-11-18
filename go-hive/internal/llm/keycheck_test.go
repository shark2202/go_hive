package llm

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func stubCheckKeyHTTP(t *testing.T, status int, body string) *keyCheckRequest {
	t.Helper()
	recorded := &keyCheckRequest{}
	original := keyCheckDo
	keyCheckDo = func(request keyCheckRequest) (*keyCheckResponse, error) {
		*recorded = request
		return &keyCheckResponse{StatusCode: status, Body: []byte(body)}, nil
	}
	t.Cleanup(func() { keyCheckDo = original })
	return recorded
}

func openRouterModel(model string) *string { return &model }

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_200
func TestCheckOpenRouter200(t *testing.T) {
	recorded := stubCheckKeyHTTP(t, 200, "")
	result, err := CheckKey("openrouter", "test-key", nil, nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if want := map[string]any{"valid": true, "message": "OpenRouter API key valid"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
	if want := "https://openrouter.ai/api/v1/models"; recorded.URL != want {
		t.Errorf("url = %q, want %q", recorded.URL, want)
	}
	if want := map[string]string{"Authorization": "Bearer test-key"}; !reflect.DeepEqual(recorded.Headers, want) {
		t.Errorf("headers = %v, want %v", recorded.Headers, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_401
func TestCheckOpenRouter401(t *testing.T) {
	stubCheckKeyHTTP(t, 401, "")
	result, err := CheckKey("openrouter", "test-key", nil, nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if want := map[string]any{"valid": false, "message": "Invalid OpenRouter API key"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_403
func TestCheckOpenRouter403(t *testing.T) {
	stubCheckKeyHTTP(t, 403, "")
	result, err := CheckKey("openrouter", "test-key", nil, nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if want := map[string]any{"valid": false, "message": "OpenRouter API key lacks permissions"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_429
func TestCheckOpenRouter429(t *testing.T) {
	stubCheckKeyHTTP(t, 429, "")
	result, err := CheckKey("openrouter", "test-key", nil, nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if want := map[string]any{"valid": true, "message": "OpenRouter API key valid"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_anthropic_compatible_uses_requested_model
func TestCheckAnthropicCompatibleUsesRequestedModel(t *testing.T) {
	recorded := stubCheckKeyHTTP(t, 400, "")
	result, err := CheckKey("kimi", "test-key", nil, openRouterModel("kimi-k2.6"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if want := map[string]any{"valid": true, "message": "Kimi API key valid"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
	var body map[string]any
	if err := json.Unmarshal(recorded.Body, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if body["model"] != "kimi-k2.6" {
		t.Errorf("model = %v, want kimi-k2.6", body["model"])
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_200
func TestCheckOpenRouterModel200(t *testing.T) {
	recorded := stubCheckKeyHTTP(t, 200, `{"data":[{"id":"openai/gpt-4o-mini","canonical_slug":"openai/gpt-4o-mini"}]}`)
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   true,
		"message": "OpenRouter model is available: openai/gpt-4o-mini",
		"model":   "openai/gpt-4o-mini",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
	if wantURL := "https://openrouter.ai/api/v1/models/user"; recorded.URL != wantURL {
		t.Errorf("url = %q, want %q", recorded.URL, wantURL)
	}
	if wantHeaders := map[string]string{"Authorization": "Bearer test-key"}; !reflect.DeepEqual(recorded.Headers, wantHeaders) {
		t.Errorf("headers = %v, want %v", recorded.Headers, wantHeaders)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_200_matches_canonical_slug
func TestCheckOpenRouterModel200MatchesCanonicalSlug(t *testing.T) {
	stubCheckKeyHTTP(t, 200, `{"data":[{"id":"mistralai/mistral-small-4","canonical_slug":"mistralai/mistral-small-2603"}]}`)
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel("mistralai/mistral-small-2603"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   true,
		"message": "OpenRouter model is available: mistralai/mistral-small-2603",
		"model":   "mistralai/mistral-small-2603",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_200_sanitizes_pasted_unicode
func TestCheckOpenRouterModel200SanitizesPastedUnicode(t *testing.T) {
	stubCheckKeyHTTP(t, 200, `{"data":[{"id":"z-ai/glm-5-turbo","canonical_slug":"z-ai/glm-5-turbo"}]}`)
	model := "openrouter/z-ai\u200b/glm\u20115\u2011turbo"
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel(model))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   true,
		"message": "OpenRouter model is available: z-ai/glm-5-turbo",
		"model":   "z-ai/glm-5-turbo",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_200_not_found_with_suggestions
func TestCheckOpenRouterModel200NotFoundWithSuggestions(t *testing.T) {
	stubCheckKeyHTTP(t, 200, `{"data":[{"id":"z-ai/glm-5-turbo"},{"id":"z-ai/glm-4.6v"}]}`)
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel("z-ai/glm-5-turb"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   false,
		"message": "OpenRouter model is not available for this key/settings: z-ai/glm-5-turb. Closest matches: z-ai/glm-5-turbo",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_404_with_error_message
func TestCheckOpenRouterModel404WithErrorMessage(t *testing.T) {
	stubCheckKeyHTTP(t, 404, `{"error":{"message":"No endpoints available for this model"}}`)
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   false,
		"message": "OpenRouter model is not available for this key/settings: openai/gpt-4o-mini. No endpoints available for this model",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_check_llm_key_openrouter.py::test_check_openrouter_model_429
func TestCheckOpenRouterModel429(t *testing.T) {
	stubCheckKeyHTTP(t, 429, "")
	result, err := CheckKey("openrouter", "test-key", nil, openRouterModel("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{
		"valid":   true,
		"message": "OpenRouter model check rate-limited; assuming model is reachable",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity: core/tests/test_key_check_packaged.py::test_server_validates_a_key_without_the_repo_scripts_dir
func TestServerValidatesAKeyWithoutTheRepoScriptsDir(t *testing.T) {
	recorded := stubCheckKeyHTTP(t, 200, "{}")
	result := ValidateProviderKey("OpenAI", "sk-test", nil, nil)
	if want := map[string]any{"valid": true, "message": "OpenAI API key valid"}; !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
	if want := "https://api.openai.com/v1/models"; recorded.URL != want {
		t.Errorf("url = %q, want %q", recorded.URL, want)
	}
}

// Parity: core/tests/test_key_check_packaged.py::test_custom_api_base_is_checked_at_its_models_endpoint
func TestCustomAPIBaseIsCheckedAtItsModelsEndpoint(t *testing.T) {
	recorded := stubCheckKeyHTTP(t, 200, "{}")
	base := "https://api.z.ai/v1/"
	result := ValidateProviderKey("zai", "sk-test", &base, nil)
	if result["valid"] != true {
		t.Errorf("result = %v, want valid true", result)
	}
	if want := "https://api.z.ai/v1/models"; recorded.URL != want {
		t.Errorf("url = %q, want %q", recorded.URL, want)
	}
}

// Parity-exempt: core/tests has no test for the server's exception wrapper;
func TestValidateProviderKeyWrapsErrors(t *testing.T) {
	original := keyCheckDo
	keyCheckDo = func(keyCheckRequest) (*keyCheckResponse, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { keyCheckDo = original })

	result := ValidateProviderKey("OpenAI", "sk-test", nil, nil)
	if result["valid"] != nil {
		t.Errorf("valid = %v, want nil", result["valid"])
	}
	message, _ := result["message"].(string)
	if !strings.HasPrefix(message, "Validation error: ") {
		t.Errorf("message = %q", message)
	}
}

// Parity-exempt: no Python test module covers the provider dispatch table.
func TestUnknownProviderHasNoHealthCheck(t *testing.T) {
	result, err := CheckKey("mystery", "sk-test", nil, nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := map[string]any{"valid": true, "message": "No health check for mystery"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

// Parity-exempt: no Python test module covers the custom-base branches.
func TestCustomBaseProviderBranches(t *testing.T) {
	cases := []struct {
		provider string
		base     string
		model    *string
		url      string
		name     string
	}{
		{"minimax", "https://mini.test/v1", nil, "https://mini.test/v1/text/chatcompletion_v2", "MiniMax"},
		{"kimi", "https://kimi.test", nil, "https://kimi.test/v1/messages", "Kimi"},
		{"hive", "https://hive.test", nil, "https://hive.test/v1/messages", "Hive"},
		{"zai", "https://zai.test/v1", nil, "https://zai.test/v1/models", "ZAI"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			recorded := stubCheckKeyHTTP(t, 200, "{}")
			base, model := c.base, c.model
			result, err := CheckKey(c.provider, "sk-test", &base, model)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if result["valid"] != true {
				t.Errorf("result = %v, want valid", result)
			}
			if recorded.URL != c.url {
				t.Errorf("url = %q, want %q", recorded.URL, c.url)
			}
			if message := result["message"].(string); !strings.HasPrefix(message, c.name) {
				t.Errorf("message = %q, want prefix %q", message, c.name)
			}
		})
	}
}

// Parity-exempt: no Python test module covers these helpers directly.
func TestExtractErrorMessageVariants(t *testing.T) {
	cases := map[string]string{
		`{"error":{"message":" nested "}}`: "nested",
		`{"error":"flat"}`:                 "flat",
		`{"message":"top"}`:                "top",
		`{"error":{"message":7}}`:          "",
		`plain text`:                       "plain text",
	}
	for body, want := range cases {
		if got := extractErrorMessage([]byte(body)); got != want {
			t.Errorf("extractErrorMessage(%q) = %q, want %q", body, got, want)
		}
	}
	long := strings.Repeat("x", 300)
	if got := extractErrorMessage([]byte(long)); len(got) != 240 {
		t.Errorf("long text length = %d, want 240", len(got))
	}
}

// Parity-exempt: no Python test module covers sanitize directly.
func TestSanitizeOpenRouterModelID(t *testing.T) {
	cases := map[string]string{
		"openrouter/z-ai/glm-5": "z-ai/glm-5",
		"OpenRouter/z-ai/glm-5": "z-ai/glm-5",
		" z-ai / glm\u20145 ":   "z-ai/glm-5",
		"z-ai\uff0fglm-5":       "z-ai/glm-5",
		"plain":                 "plain",
	}
	for input, want := range cases {
		if got := sanitizeOpenRouterModelID(input); got != want {
			t.Errorf("sanitizeOpenRouterModelID(%q) = %q, want %q", input, got, want)
		}
	}
	if got := normalizeOpenRouterModelID("Z-AI/GLM-5"); got != "z-ai/glm-5" {
		t.Errorf("normalize = %q", got)
	}
}

// Parity-exempt: no Python test module covers closeMatches directly.
func TestCloseMatchesTieBreak(t *testing.T) {
	if got := closeMatches("abc", []string{"abd", "abe"}, 0.6); !reflect.DeepEqual(got, []string{"abe"}) {
		t.Errorf("tie-break = %v, want [abe]", got)
	}
	if got := closeMatches("zzz", []string{"abc"}, 0.6); len(got) != 0 {
		t.Errorf("no match = %v, want empty", got)
	}
}
