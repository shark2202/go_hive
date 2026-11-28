package pipeline

import (
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
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
	// KeySources supplies the credential-backed API-key steps; nil reads the
	// real encrypted credential store and named endpoints.
	KeySources *LLMKeySources
	// Home overrides the hive home the default resolver reads
	// configuration.json from; nil resolves the process home.
	Home *hivehome.Home
}

// LLMKeySources is the credential-backed half of the API-key chain: a named
// endpoint's key and a provider's stored BYOK key. Both hooks read the real
// encrypted store by default; tests and callers with their own store inject
// fakes.
type LLMKeySources struct {
	// EndpointKey is the key to send a named endpoint: the endpoint's stored
	// key, else its placeholder, never an ambient key. Nil reads llm.KeyFor.
	EndpointKey func(name string) string
	// StoredProviderKey is a provider's key from the credential store. Nil
	// reads credentials.StoredProviderKey.
	StoredProviderKey func(provider string) (string, bool)
}

func (s *LLMKeySources) endpointKey(name string) string {
	if s != nil && s.EndpointKey != nil {
		return s.EndpointKey(name)
	}
	return llm.KeyFor(name)
}

func (s *LLMKeySources) storedProviderKey(provider string) string {
	if s != nil && s.StoredProviderKey != nil {
		key, _ := s.StoredProviderKey(provider)
		return key
	}
	key, _ := credentials.StoredProviderKey(provider)
	return key
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
			return DefaultLLMResolverWith(home, model, s.KeySources)
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

// DefaultLLMResolver builds an HTTP completer from the `llm` section, reading
// the real credential store and named endpoints for its credential-backed key
// steps.
//
// Parity: framework/pipeline/stages/llm_provider.py::LlmProviderStage.initialize,
// framework/config.py::get_api_key, framework/config.py::get_api_keys
func DefaultLLMResolver(home hivehome.Home, model string) (llm.Completer, error) {
	return DefaultLLMResolverWith(home, model, nil)
}

// DefaultLLMResolverWith is DefaultLLMResolver with the credential-backed key
// steps supplied by sources (nil reads the real store).
//
// The key chain is get_api_key's order: a named endpoint's own key, the
// api_keys pool (first key, pool attached for rotation), the literal api_key,
// an enabled subscription's token, the provider's credential-store key, then
// the named api_key_env_var. An Antigravity subscription swap happens first,
// exactly as upstream's LlmProviderStage.
//
// Parity: framework/pipeline/stages/llm_provider.py::LlmProviderStage.initialize,
// framework/config.py::get_api_key, framework/config.py::get_api_keys
func DefaultLLMResolverWith(home hivehome.Home, model string, sources *LLMKeySources) (llm.Completer, error) {
	section := hiveconf.Section(home, "llm")
	if strings.TrimSpace(model) == "" {
		model = preferredModel(section)
	}
	// Antigravity is a whole-session transport swap: when the flag is set
	// and a login exists, the Gemini-style provider wins over every HTTP
	// path. Without credentials upstream falls through to LiteLLM.
	if pyTruthy(section["use_antigravity_subscription"]) {
		provider := llm.NewAntigravityProvider(llm.AntigravityOptions{Model: model})
		if provider.HasCredentials() {
			return provider, nil
		}
	}
	options := llm.ProviderOptions{Model: model}
	if base, ok := section["api_base"].(string); ok {
		options.APIBase = strings.TrimSpace(base)
	}

	keys := llmSectionAPIKeys(section)
	// A model served from a named endpoint uses that endpoint's key first,
	// and never an ambient one: a keyless endpoint gets its placeholder,
	// exactly as _endpoint_key/key_for resolve it.
	key := ""
	if name, ok := section["endpoint"].(string); ok && strings.TrimSpace(name) != "" {
		key = sources.endpointKey(strings.TrimSpace(name))
	}
	if key == "" && len(keys) > 0 {
		key = keys[0]
	}
	if key == "" {
		if literal, ok := section["api_key"].(string); ok {
			key = literal
		}
	}
	if key == "" {
		// Claude Code / Codex / Kimi Code / Antigravity plan tokens, in
		// upstream get_api_key's order.
		key = hiveconf.SubscriptionToken(section, home)
	}
	if key == "" {
		// Credential store, before the env var so a desktop-pushed key wins
		// over the frozen spawn-time environment.
		key = sources.storedProviderKey(pyString(section["provider"]))
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
