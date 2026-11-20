package llm

// Hot-swappable provider slots: the model-slot PUT routes rebuild a running
// session's transport in place (upstream's ``provider.reconfigure``), without
// restarting ``serve`` and without duplicating the provider's request logic.
//
// Upstream mutates the shared ``LiteLLMProvider`` object and relies on the
// object being handed around by reference. Go providers are read by in-flight
// requests, so the port splits the concern instead:
//
//   - *Provider stays immutable after construction (its fields are read while
//     a request is in flight; mutating them under a live reader would be a
//     data race).
//   - ProviderSlot is the replaceable surface: it holds the live Completer
//     behind an RWMutex and rebuilds it through the same factory
//     (HTTPProviderFactory -> NewHTTPProvider) the session started on.
//
// A session pins the slot's Current() completer for the duration of one turn,
// so a PUT that races a running turn cannot tear it: the turn finishes on the
// provider it started with and the next turn resolves the replacement.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.reconfigure

import (
	"context"
	"sync"
)

// Reconfigurable is the interface a hot-swappable transport exposes to the
// model-slot routes: the model, key, base and extra body are replaced in
// place. It mirrors upstream's “provider.reconfigure“ signature.
type Reconfigurable interface {
	Reconfigure(fullModel string, apiKey, apiBase *string, extraBody any)
}

// ProviderFactory rebuilds a transport from the values a slot PUT carries.
// HTTPProviderFactory is the production implementation.
type ProviderFactory func(fullModel string, apiKey, apiBase *string, extraBody any) Completer

// HTTPProviderFactory rebuilds an HTTP transport through NewHTTPProvider, the
// same constructor production sessions are built with, so a hot swap cannot
// diverge from the provider logic the session was created on.
func HTTPProviderFactory(fullModel string, apiKey, apiBase *string, extraBody any) Completer {
	options := ProviderOptions{Model: fullModel}
	if apiKey != nil {
		options.APIKey = *apiKey
	}
	if apiBase != nil {
		options.APIBase = *apiBase
	}
	provider := NewHTTPProvider(options)
	if extraBody != nil {
		provider.ExtraKwargs = map[string]any{"extra_body": extraBody}
	}
	return provider
}

// ProviderSlot is a replaceable completion transport. Sessions (and colony
// worker slots) hold the slot; a model-slot PUT calls Reconfigure and the next
// turn/new worker resolves the rebuilt provider.
type ProviderSlot struct {
	mu      sync.RWMutex
	current Completer
	build   ProviderFactory
}

// NewProviderSlot wraps an initial transport. A nil build factory makes
// Reconfigure a no-op that keeps the current transport.
func NewProviderSlot(initial Completer, build ProviderFactory) *ProviderSlot {
	return &ProviderSlot{current: initial, build: build}
}

// Current returns the live transport. Callers that must not observe a swap
// mid-flight (a turn, a worker run) pin it once and reuse the value.
func (s *ProviderSlot) Current() Completer {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Replace installs a transport without rebuilding it (tests, direct
// injection).
func (s *ProviderSlot) Replace(completer Completer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.current = completer
	s.mu.Unlock()
}

// Reconfigure rebuilds the transport from the new model/key/base/extra_body.
// A slot without a factory keeps its current transport.
func (s *ProviderSlot) Reconfigure(fullModel string, apiKey, apiBase *string, extraBody any) {
	if s == nil || s.build == nil {
		return
	}
	rebuilt := s.build(fullModel, apiKey, apiBase, extraBody)
	s.mu.Lock()
	s.current = rebuilt
	s.mu.Unlock()
}

// AComplete delegates to the current transport. Auxiliary consumers (recall,
// reflection, timeline, judge) call through the slot so they follow a
// reconfigure on their next call.
func (s *ProviderSlot) AComplete(ctx context.Context, req CompletionRequest) (Completion, error) {
	current := s.Current()
	if current == nil {
		return Completion{}, errSlotUninitialised
	}
	return current.AComplete(ctx, req)
}

// ACompleteStream delegates streaming to the current transport. The slot
// always satisfies StreamCompleter so a session built while its provider
// streamed keeps streaming after a swap; a non-streaming replacement reports
// ErrStreamingUnsupported, which the chat streaming adapter already handles by
// falling back to the non-streaming path.
func (s *ProviderSlot) ACompleteStream(ctx context.Context, req CompletionRequest, emit func(StreamEvent)) (Completion, error) {
	current := s.Current()
	streamer, ok := current.(StreamCompleter)
	if !ok {
		return Completion{}, ErrStreamingUnsupported
	}
	return streamer.ACompleteStream(ctx, req, emit)
}

// ModelName reports the current transport's model, so session snapshots and
// turn events name the model a swap put in place.
func (s *ProviderSlot) ModelName() string {
	current := s.Current()
	if namer, ok := current.(ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// ResolveCompleter unwraps nested slots and returns the live transport a
// caller should pin. A plain (non-slot) completer is returned unchanged.
func ResolveCompleter(completer Completer) Completer {
	for i := 0; i < 8; i++ {
		resolver, ok := completer.(interface{ Current() Completer })
		if !ok {
			return completer
		}
		next := resolver.Current()
		if next == nil || next == completer {
			return completer
		}
		completer = next
	}
	return completer
}

var errSlotUninitialised = errSlot("llm: provider slot is not initialised")

type errSlot string

func (e errSlot) Error() string { return string(e) }
