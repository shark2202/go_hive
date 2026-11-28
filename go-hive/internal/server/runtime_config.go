package server

// RuntimeConfig is the aggregated agent-runtime configuration.
//
// Upstream's framework/config.py::RuntimeConfig is a dataclass with field
// default factories: the preferred model, a 0.7 temperature, the main slot's
// max_tokens and max_context_tokens, the resolved api_key/api_base and the
// provider extra_kwargs. The Go port had the individual readers (MainSlot*,
// LLMMaxTokens, LLMMaxContextTokens) but no single aggregate, so no caller could
// see "the runtime configuration" the way build_llm does. LoadRuntimeConfig is
// that aggregate.
//
// Temperature: the dataclass default is 0.7, but upstream's build_llm constructs
// LiteLLMProvider without passing it, so the server request path carries no
// temperature (LiteLLM's own provider default wins). The Go aggregate keeps the
// field at 0.7 for data-shape parity and deliberately does not inject it into
// ExtraKwargs; a caller that wants the dataclass value sends it explicitly.
//
// Parity: framework/config.py::RuntimeConfig

import (
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// DefaultRuntimeTemperature is RuntimeConfig.temperature's upstream default.
const DefaultRuntimeTemperature = 0.7

// RuntimeConfig mirrors framework/config.py::RuntimeConfig.
type RuntimeConfig struct {
	Model            string
	Temperature      float64
	MaxTokens        int
	MaxContextTokens int
	APIKey           string
	APIBase          string
	ExtraKwargs      map[string]any
}

// LoadRuntimeConfig resolves the main slot into the aggregate. An explicit
// requested model wins as written, exactly as RuntimeConfig(model=...) does.
//
// Parity: framework/config.py::RuntimeConfig, get_preferred_model,
// get_max_tokens, get_max_context_tokens, get_api_key, get_api_base,
// get_llm_extra_kwargs
func LoadRuntimeConfig(home hivehome.Home, requestedModel string) RuntimeConfig {
	apiKey := MainSlotKey(home)
	return RuntimeConfig{
		Model:            MainSlotModel(home, requestedModel),
		Temperature:      DefaultRuntimeTemperature,
		MaxTokens:        LLMMaxTokens(home),
		MaxContextTokens: LLMMaxContextTokens(home, DefaultRuntimeMaxContextTokens),
		APIKey:           apiKey,
		APIBase:          MainSlotAPIBase(home),
		ExtraKwargs:      MainSlotExtraKwargs(home, apiKey),
	}
}
