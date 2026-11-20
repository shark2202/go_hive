package llm

import (
	"context"
	"sync"
	"testing"
)

type slotProbeCompleter struct {
	model string
	mu    sync.Mutex
	calls int
}

func (p *slotProbeCompleter) AComplete(context.Context, CompletionRequest) (Completion, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return Completion{Content: p.model, Model: p.model}, nil
}

func (p *slotProbeCompleter) ModelName() string { return p.model }

func probeFactory(fullModel string, apiKey, apiBase *string, extraBody any) Completer {
	return &slotProbeCompleter{model: fullModel}
}

// Parity-exempt: 上游 LiteLLMProvider.reconfigure 无单测；本文件锁定 Go 的
// ProviderSlot 替换层（重建经 NewHTTPProvider，不复制 provider 逻辑）。
func TestProviderSlotDelegatesToCurrent(t *testing.T) {
	initial := &slotProbeCompleter{model: "old-model"}
	slot := NewProviderSlot(initial, probeFactory)

	if got := slot.ModelName(); got != "old-model" {
		t.Fatalf("ModelName = %q, want old-model", got)
	}
	completion, err := slot.AComplete(context.Background(), CompletionRequest{})
	if err != nil || completion.Content != "old-model" {
		t.Fatalf("AComplete = (%q, %v), want old-model", completion.Content, err)
	}
}

// Parity-exempt: 上游 reconfigure 直接改共享 provider 字段；Go 用工厂按同一
// 构造器重建，本测试锁定重建参数（model/key/base/extra_body）的传递。
func TestProviderSlotReconfigureRebuildsThroughTheFactory(t *testing.T) {
	var calls []string
	factory := func(fullModel string, apiKey, apiBase *string, extraBody any) Completer {
		key, base := "", ""
		if apiKey != nil {
			key = *apiKey
		}
		if apiBase != nil {
			base = *apiBase
		}
		calls = append(calls, fullModel+"|"+key+"|"+base)
		provider := NewProvider(ProviderOptions{Model: fullModel, APIKey: key, APIBase: base})
		if extraBody != nil {
			provider.ExtraKwargs = map[string]any{"extra_body": extraBody}
		}
		return provider
	}
	slot := NewProviderSlot(&slotProbeCompleter{model: "old-model"}, factory)

	key := "new-key"
	base := "http://127.0.0.1:9/v1"
	extra := map[string]any{"reasoning_effort": "high"}
	slot.Reconfigure("openai/new-model", &key, &base, extra)

	if len(calls) != 1 || calls[0] != "openai/new-model|new-key|http://127.0.0.1:9/v1" {
		t.Fatalf("factory calls = %#v", calls)
	}
	rebuilt, ok := slot.Current().(*Provider)
	if !ok || rebuilt.Model != "openai/new-model" || rebuilt.APIKey != "new-key" || rebuilt.APIBase != base {
		t.Fatalf("rebuilt provider = %#v", slot.Current())
	}
	if rebuilt.ExtraKwargs["extra_body"] == nil {
		t.Fatalf("extra_body not carried onto the rebuilt provider: %#v", rebuilt.ExtraKwargs)
	}
	if got := slot.ModelName(); got != "openai/new-model" {
		t.Fatalf("ModelName after reconfigure = %q", got)
	}
}

// Parity-exempt: 上游原地 mutate，没有「pin 旧 provider 完成当前回合」的语义；
// 本测试锁定 Go 的替换语义（旧 completer 仍可完成，新调用拿新 provider）。
func TestProviderSlotPinnedCompleterSurvivesAReconfigure(t *testing.T) {
	slot := NewProviderSlot(&slotProbeCompleter{model: "old-model"}, probeFactory)
	pinned := ResolveCompleter(slot)
	namer, ok := pinned.(ModelNamer)
	if !ok || namer.ModelName() != "old-model" {
		t.Fatalf("pinned = %#v", pinned)
	}

	slot.Reconfigure("openai/new-model", nil, nil, nil)

	completion, err := pinned.AComplete(context.Background(), CompletionRequest{})
	if err != nil || completion.Content != "old-model" {
		t.Fatalf("pinned completer after swap = (%q, %v), want old-model", completion.Content, err)
	}
	fresh, err := ResolveCompleter(slot).AComplete(context.Background(), CompletionRequest{})
	if err != nil || fresh.Content != "openai/new-model" {
		t.Fatalf("fresh completer = (%q, %v), want openai/new-model", fresh.Content, err)
	}
}

// Parity-exempt: 上游 reconfigure 走 litellm 的字段判定；本测试只锁定 Go 出厂
// 工厂把 model/key/base/extra_body 交给 NewHTTPProvider 并保留 HTTP 传输。
func TestProviderSlotHTTPFactoryCarriesModelKeyBaseAndExtraBody(t *testing.T) {
	slot := NewProviderSlot(&slotProbeCompleter{model: "old-model"}, HTTPProviderFactory)
	key := "sk-test"
	base := "http://127.0.0.1:9/v1"
	slot.Reconfigure("openai/rebuilt-model", &key, &base, map[string]any{"reasoning_effort": "low"})

	provider, ok := slot.Current().(*Provider)
	if !ok {
		t.Fatalf("current = %T, want *Provider", slot.Current())
	}
	if provider.Model != "openai/rebuilt-model" || provider.APIKey != key || provider.APIBase != base {
		t.Fatalf("rebuilt provider = %#v", provider)
	}
	body, ok := provider.ExtraKwargs["extra_body"].(map[string]any)
	if !ok || body["reasoning_effort"] != "low" {
		t.Fatalf("extra_body = %#v", provider.ExtraKwargs["extra_body"])
	}
	if provider.Call == nil || provider.Gate == nil {
		t.Fatalf("rebuilt provider lacks the HTTP transport: %#v", provider)
	}
}

// Parity-exempt: Go 侧并发安全要求（上游 GIL 下无此问题）；本测试在 -race 下
// 并发重建与调用，确保 slot 的替换不会让在读调用撕裂。
func TestProviderSlotConcurrentReconfigureAndCalls(t *testing.T) {
	slot := NewProviderSlot(&slotProbeCompleter{model: "model-0"}, probeFactory)

	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				slot.Reconfigure("openai/model-swap", nil, nil, nil)
			}
		}()
	}
	for reader := 0; reader < 8; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := slot.AComplete(context.Background(), CompletionRequest{}); err != nil {
					t.Errorf("AComplete: %v", err)
					return
				}
				_ = slot.ModelName()
			}
		}()
	}
	wg.Wait()

	if got := slot.ModelName(); got != "openai/model-swap" {
		t.Fatalf("final model = %q", got)
	}
}

// Parity-exempt: 上游没有无工厂 slot；本测试锁定「没有重建工厂时保持原传输」
// 的保守分支。
func TestProviderSlotWithoutFactoryKeepsItsTransport(t *testing.T) {
	initial := &slotProbeCompleter{model: "kept"}
	slot := NewProviderSlot(initial, nil)
	slot.Reconfigure("openai/ignored", nil, nil, nil)
	if slot.Current() != initial {
		t.Fatalf("slot without factory replaced its transport")
	}
}

// Parity-exempt: 上游 provider 就是共享对象、无需解包；本测试锁定 Go 中
// session.LLM 可能嵌套 slot 时的解包行为。
func TestResolveCompleterUnwrapsNestedSlots(t *testing.T) {
	inner := &slotProbeCompleter{model: "inner"}
	outer := NewProviderSlot(NewProviderSlot(inner, nil), nil)
	if resolved := ResolveCompleter(outer); resolved != inner {
		t.Fatalf("ResolveCompleter = %#v, want the inner completer", resolved)
	}
	if resolved := ResolveCompleter(inner); resolved != inner {
		t.Fatalf("ResolveCompleter(plain) = %#v", resolved)
	}
}
