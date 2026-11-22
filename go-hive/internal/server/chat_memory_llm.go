package server

// The memory-work model slot: the model a session's memory work (reflection,
// the dated timeline) calls. Upstream looks it up at each call on
// ``SessionManager.memory_llm`` and builds the worker provider again only when
// the ``worker_llm`` section changes.
//
// Parity: framework/server/session_manager.py::_MemoryLLM (and the
// ``_memory_llm`` the queen orchestrator hands to subscribe_reflection_triggers
// and subscribe_timeline_triggers).

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

// chatMemoryLLM resolves the memory-work model per call. `memory_reflection
// .model` picks it: "worker" (the default) uses the `worker_llm` slot, or
// the session's own model when that slot is unset; "main" always uses the
// session's model. The worker provider is rebuilt only when the section
// changed, so a Settings edit reaches a running session without rebuilding a
// provider on every reflection.
type chatMemoryLLM struct {
	session llm.Completer
	// config reads memory_reflection at each call (chatMemoryReflectionConfig),
	// so a Settings change reaches sessions already running.
	config func() memory.ReflectionConfig
	// buildWorker builds the worker-slot provider; nil when no worker_llm
	// section is configured (upstream's SessionManager.build_worker_llm
	// returns None and the session's own model stands in).
	buildWorker func() llm.Completer
	// workerSection identifies the worker_llm section a cached provider was
	// built from; upstream compares json.dumps(section, sort_keys=True), so a
	// changed slot rebuilds and an unchanged one is reused.
	workerSection func() string

	mu          sync.Mutex
	workerBuilt bool
	workerFor   string
	worker      llm.Completer
}

// newChatMemoryLLM builds the resolver over one session's provider. home is the
// runtime's HIVE_HOME; without a resolvable worker_llm section the resolver
// returns the session provider, exactly as upstream falls back to session.llm.
func newChatMemoryLLM(session llm.Completer, home hivehome.Home, config func() memory.ReflectionConfig) *chatMemoryLLM {
	resolver := &chatMemoryLLM{session: session, config: config}
	resolver.workerSection = func() string {
		section := WorkerLLMSection(home)
		if section == nil {
			// Python: json.dumps(None) == "null".
			return "null"
		}
		data, err := json.Marshal(section)
		if err != nil {
			return ""
		}
		return string(data)
	}
	resolver.buildWorker = func() llm.Completer {
		section := WorkerLLMSection(home)
		if section == nil {
			return nil
		}
		return SectionProvider(home, section)
	}
	return resolver
}

// current is upstream _MemoryLLM.current: the model this call runs on.
func (m *chatMemoryLLM) current() llm.Completer {
	if m.config().Model != "worker" {
		return m.session
	}
	section := m.workerSection()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.workerBuilt || m.workerFor != section {
		m.workerBuilt = true
		m.workerFor = section
		m.worker = m.buildWorker()
	}
	if m.worker != nil {
		return m.worker
	}
	return m.session
}

// AComplete sends the request to the model this call resolves to.
func (m *chatMemoryLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	return m.current().AComplete(ctx, req)
}

// ModelName is upstream _MemoryLLM.model: the name of the resolved model, for
// the timeline log's model field.
func (m *chatMemoryLLM) ModelName() string {
	if namer, ok := m.current().(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// memoryWorkLLM is the completer the runtime's memory work uses: the resolver
// when the session has a home to read worker_llm from, else the session's own
// provider (the pre-memory behaviour of a home-less test runtime).
func (r *ChatRuntime) memoryWorkLLM() llm.Completer {
	if r == nil {
		return nil
	}
	if r.memoryLLM != nil {
		return r.memoryLLM
	}
	return r.deps.LLM
}

// chatAppProviderFromChannel is upstream's `chat_app_provider=lambda:
// session.channel`: the channel name when the session belongs to a channel,
// nil on the dashboard. A nil reader yields nil, so a session without a
// channel source leaves AgentContext.ChatAppProvider unset and the tasks
// reminder source's chat-app gate stays off.
//
// Parity: framework/server/queen_orchestrator.py (chat_app_provider)
func chatAppProviderFromChannel(channel func() string) any {
	if channel == nil {
		return nil
	}
	return func() any {
		if name := strings.TrimSpace(channel()); name != "" {
			return name
		}
		return nil
	}
}
