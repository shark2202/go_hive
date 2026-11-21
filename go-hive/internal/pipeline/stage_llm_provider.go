package pipeline

import (
	"os"
	"strings"

	"github.com/aden-hive/hive-go/internal/hiveconf"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// defaultPreferredModel is get_preferred_model's fallback pair.
//
// Parity: framework/config.py::get_preferred_model
const defaultPreferredModel = "anthropic/claude-sonnet-5-5"

// LLMProviderStage resolves the LLM completer from global config — the
// pipeline's counterpart of upstream's "the ONLY place the LLM gets created
// for worker agents". A pre-injected completer wins and Initialize returns
// early, exactly as upstream skips construction when `llm` is not None.
//
// Config keys (config.*): model, mock_mode. The completer itself is a typed
// injection (struct field LLM / Resolver), not a JSON value.
//
// Parity: framework/pipeline/stages/llm_provider.py::LlmProviderStage
type LLMProviderStage struct {
	BaseStage
	// Model is the requested model id; empty resolves the preferred model.
	Model string
	// MockMode installs the deterministic mock completer instead of a
	// transport (upstream's mock_mode).
	MockMode bool
	// LLM is the pre-injected completer; if set, nothing is built.
	LLM llm.Completer
	// Resolver optionally builds the completer for a model id; it defaults
	// to DefaultLLMResolver over the stage's home.
	Resolver func(model string) (llm.Completer, error)
	// Home overrides the hive home the default resolver reads
	// configuration.json from; nil resolves the process home.
	Home *hivehome.Home
}

// NewLLMProviderStage builds the stage from its declarative config.
func NewLLMProviderStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
	}
	if raw, present := config["model"]; present {
		stage.Model = strings.TrimSpace(pyString(raw))
	}
	stage.MockMode = pyTruthy(config["mock_mode"])
	return stage, nil
}

// Initialize resolves the completer unless one was injected.
//
// Parity: framework/pipeline/stages/llm_provider.py::LlmProviderStage.initialize
func (s *LLMProviderStage) Initialize() error {
	if s.LLM != nil {
		return nil
	}
	home, err := s.resolveHome()
	if err != nil {
		return err
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = preferredModel(hiveconf.Section(home, "llm"))
	}
	if s.MockMode {
		s.LLM = llm.NewMockProvider(model)
		return nil
	}
	resolver := s.Resolver
	if resolver == nil {
		resolver = func(model string) (llm.Completer, error) {
			return DefaultLLMResolver(home, model)
		}
	}
	completer, err := resolver(model)
	if err != nil {
		return err
	}
	s.LLM = completer
	return nil
}

// Process is a pass-through: the work happened in Initialize.
func (s *LLMProviderStage) Process(_ *Context) (Result, error) { return ContinueResult(), nil }

func (s *LLMProviderStage) resolveHome() (hivehome.Home, error) {
	if s.Home != nil {
		return *s.Home, nil
	}
	return hivehome.Resolve()
}

// preferredModel rewrites the llm section into the joined "provider/model"
// pair get_preferred_model returns, tolerating a pasted openrouter/ prefix.
//
// Parity: framework/config.py::get_preferred_model
func preferredModel(section map[string]any) string {
	provider := strings.TrimSpace(pyString(section["provider"]))
	model := strings.TrimSpace(pyString(section["model"]))
	if provider != "" && model != "" {
		if strings.EqualFold(provider, "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
			model = model[len("openrouter/"):]
		}
		if model != "" {
			return provider + "/" + model
		}
	}
	return defaultPreferredModel
}

// DefaultLLMResolver builds an HTTP completer from the `llm` section: model
// (or the preferred join), api_base, and a key taken from the api_keys pool
// (first key, pool attached for rotation) or the literal api_key or the named
// api_key_env_var. Subscription-token and credential-store chains live in the
// server main-slot builder (MainSlotProvider) and are intentionally not
// duplicated here.
//
// Parity: framework/config.py::get_api_key, framework/config.py::get_api_keys
func DefaultLLMResolver(home hivehome.Home, model string) (llm.Completer, error) {
	section := hiveconf.Section(home, "llm")
	if strings.TrimSpace(model) == "" {
		model = preferredModel(section)
	}
	options := llm.ProviderOptions{Model: model}
	if base, ok := section["api_base"].(string); ok {
		options.APIBase = strings.TrimSpace(base)
	}

	keys := llmSectionAPIKeys(section)
	key := ""
	if len(keys) > 0 {
		key = keys[0]
	}
	if key == "" {
		if literal, ok := section["api_key"].(string); ok {
			key = literal
		}
	}
	if key == "" {
		if envName, ok := section["api_key_env_var"].(string); ok && strings.TrimSpace(envName) != "" {
			key = os.Getenv(strings.TrimSpace(envName))
		}
	}
	options.APIKey = key

	provider := llm.NewHTTPProvider(options)
	if len(keys) > 1 {
		if pool, err := llm.NewKeyPool(keys); err == nil {
			provider.KeyPool = pool
		}
	}
	return provider, nil
}

// llmSectionAPIKeys ports get_api_keys: the configured pool with empty
// entries dropped; nil when none is configured.
//
// Parity: framework/config.py::get_api_keys
func llmSectionAPIKeys(section map[string]any) []string {
	raw, ok := section["api_keys"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			keys = append(keys, text)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}
