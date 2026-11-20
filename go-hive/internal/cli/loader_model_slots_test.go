package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// slotTestHome builds a temp hive home whose configuration.json holds the given
// document.
func slotTestHome(t *testing.T, document map[string]any) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(filepath.Join(t.TempDir(), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal configuration: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), raw, 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return home
}

// TestQueenAwareChatProviderUsesQueenSection pins that a session opened with a
// queen who has her own queen_llm section runs on HER model, key and base, and
// that a queen without one keeps the main model.
//
// Parity-exempt: upstream has no test for get_queen_llm_section /
// queen_models.build; this mirrors framework/config.py::get_queen_llm_section
// and framework/server/queen_models.py::build, which the Go port reads instead.
func TestQueenAwareChatProviderUsesQueenSection(t *testing.T) {
	home := slotTestHome(t, map[string]any{
		"llm": map[string]any{
			"provider": "anthropic",
			"model":    "claude-main",
			"api_key":  "main-key",
		},
		"queen_llm": map[string]any{
			"archivist": map[string]any{
				"provider": "openai",
				"model":    "gpt-queen",
				"api_key":  "queen-key",
				"api_base": "https://queen.example/v1",
				"extra_body": map[string]any{
					"reasoning_effort": "high",
				},
			},
			// A section without a model is not a usable slot: she keeps the
			// main model, exactly as get_queen_llm_section reports None.
			"halfway": map[string]any{"provider": "openai"},
		},
	})

	provider, err := queenAwareChatProvider(home, "", "archivist")
	if err != nil {
		t.Fatalf("queenAwareChatProvider: %v", err)
	}
	queenProvider, ok := provider.(*llm.Provider)
	if !ok {
		t.Fatalf("provider type = %T", provider)
	}
	if got := queenProvider.ModelName(); got != "openai/gpt-queen" {
		t.Fatalf("queen model = %q", got)
	}
	if queenProvider.APIKey != "queen-key" {
		t.Fatalf("queen api key = %q", queenProvider.APIKey)
	}
	if queenProvider.APIBase != "https://queen.example/v1" {
		t.Fatalf("queen api base = %q", queenProvider.APIBase)
	}
	if extra, _ := queenProvider.ExtraKwargs["extra_body"].(map[string]any); extra["reasoning_effort"] != "high" {
		t.Fatalf("queen extra_body = %#v", queenProvider.ExtraKwargs)
	}

	// A queen with no usable section keeps the main model (resolved through the
	// injectable provider factory, so the test never builds an HTTP client).
	previous := chatProviderFactory
	t.Cleanup(func() { chatProviderFactory = previous })
	mainProvider := &llm.Provider{Model: "main-model"}
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return mainProvider, nil }

	for _, queenID := range []string{"", "halfway", "nosuchqueen"} {
		provider, err := queenAwareChatProvider(home, "", queenID)
		if err != nil {
			t.Fatalf("queen %q: %v", queenID, err)
		}
		if provider != llm.Completer(mainProvider) {
			t.Fatalf("queen %q did not fall back to the main provider", queenID)
		}
	}
}

// TestWorkerChatProviderUsesWorkerSection pins the worker_llm slot: nil when
// unset (workers share the queen's provider), a dedicated provider when the
// section carries provider+model.
//
// Parity: core/tests/test_session_unified_colony_runtime.py::test_unified_colony_uses_configured_worker_llm
func TestWorkerChatProviderUsesWorkerSection(t *testing.T) {
	empty := slotTestHome(t, map[string]any{"llm": map[string]any{"provider": "openai", "model": "main"}})
	if provider, err := workerChatProvider(empty); err != nil || provider != nil {
		t.Fatalf("workerChatProvider without worker_llm = (%v, %v), want (nil, nil)", provider, err)
	}

	home := slotTestHome(t, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-main", "api_key": "main-key"},
		"worker_llm": map[string]any{
			"provider": "groq",
			"model":    "llama-worker",
			"api_base": "https://worker.example/v1",
		},
	})
	provider, err := workerChatProvider(home)
	if err != nil {
		t.Fatalf("workerChatProvider: %v", err)
	}
	workerProvider, ok := provider.(*llm.Provider)
	if !ok {
		t.Fatalf("worker provider type = %T", provider)
	}
	if got := workerProvider.ModelName(); got != "groq/llama-worker" {
		t.Fatalf("worker model = %q", got)
	}
	if workerProvider.APIBase != "https://worker.example/v1" {
		t.Fatalf("worker api base = %q", workerProvider.APIBase)
	}
	// No key on the worker section: the provider env var is the fallback, and
	// the main slot's key after it. With GROQ unset the main key applies.
	if workerProvider.APIKey != "main-key" {
		t.Fatalf("worker api key = %q, want the main key", workerProvider.APIKey)
	}
}
