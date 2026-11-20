package server

// Turn context: installed packages put text in front of a queen before each of
// her turns.
//
// Parity: framework/server/tests/test_turn_context.py

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// fakeTurnContext mirrors the upstream test double: it records who asked what,
// and can delay, fail per call or fail to start.
type fakeTurnContext struct {
	host      TurnContextHost
	reply     string
	delay     time.Duration
	failure   error
	failStart bool

	mu      sync.Mutex
	asked   [][2]string
	stopped bool
}

func (f *fakeTurnContext) Start() error {
	if f.failStart {
		return errors.New("model missing")
	}
	return nil
}

func (f *fakeTurnContext) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeTurnContext) Context(queenID, message string) (string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, [2]string{queenID, message})
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.failure != nil {
		return "", f.failure
	}
	return f.reply, nil
}

func (f *fakeTurnContext) askedPairs() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.asked...)
}

// installTurnContexts writes the section, registers the factories and starts
// them, returning the built providers by name. Cleanup stops the runtime and
// drops the factories so tests cannot leak into each other.
func installTurnContexts(t *testing.T, settings map[string]any, factories map[string]func(TurnContextHost) *fakeTurnContext) (hivehome.Home, map[string]*fakeTurnContext) {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	config := map[string]any{}
	if settings != nil {
		config["turn_context"] = settings
	}
	writeFeatureConfig(t, home, config)

	built := map[string]*fakeTurnContext{}
	for name, factory := range factories {
		built[name] = nil
		RegisterTurnContextProvider(name, func(host TurnContextHost) (TurnContext, error) {
			provider := factory(host)
			built[name] = provider
			return provider, nil
		})
	}
	t.Cleanup(func() {
		StopTurnContexts(home.Root)
		for name := range factories {
			UnregisterTurnContextProvider(name)
		}
	})
	StartTurnContexts(home)
	return home, built
}

// TestTurnContextEnabledPackageGetsSettingsAndTextReachesQueen ports the
// upstream happy path: only the enabled package starts, it sees its own
// configuration section, the queen's name and the message, and its text lands
// in the reminder block.
//
// Parity: framework/server/tests/test_turn_context.py::test_an_enabled_package_gets_its_settings_and_its_text_reaches_the_queen
func TestTurnContextEnabledPackageGetsSettingsAndTextReachesQueen(t *testing.T) {
	_, built := installTurnContexts(t,
		map[string]any{
			"voice": map[string]any{"enabled": true, "k": float64(3)},
			"other": map[string]any{"enabled": false},
		},
		map[string]func(TurnContextHost) *fakeTurnContext{
			"voice": func(host TurnContextHost) *fakeTurnContext {
				return &fakeTurnContext{host: host, reply: "<voice_examples>…</voice_examples>"}
			},
			"other":        func(host TurnContextHost) *fakeTurnContext { return &fakeTurnContext{host: host, reply: "never"} },
			"unconfigured": func(host TurnContextHost) *fakeTurnContext { return &fakeTurnContext{host: host, reply: "never"} },
		})

	if built["other"] != nil || built["unconfigured"] != nil {
		t.Fatalf("disabled/unconfigured packages started: other=%v unconfigured=%v", built["other"], built["unconfigured"])
	}
	if built["voice"] == nil {
		t.Fatal("enabled package did not start")
	}
	if got := built["voice"].host.Name; got != "voice" {
		t.Fatalf("host name = %q, want voice", got)
	}
	if got := pyStr(built["voice"].host.Config["k"]); got != "3" {
		t.Fatalf("host config = %#v, want k=3", built["voice"].host.Config)
	}

	// What the queen orchestrator wires: the session's queen, not its directory.
	runtime := StartTurnContexts(hivehome.Home{Root: built["voice"].host.home.Root})
	hub := agentloop.NewReminderHub()
	hub.Register(agentloop.NewTurnContextReminderSource())
	block := hub.Fire(
		agentloop.USER_PROMPT_SUBMIT,
		&agentloop.ReminderAgentContext{
			IsQueenStream:       true,
			TurnContextProvider: runtime.ForSession(func() string { return "queen_x" }),
		},
		nil,
		"今天有点累",
	)

	if got := built["voice"].askedPairs(); len(got) != 1 || got[0] != [2]string{"queen_x", "今天有点累"} {
		t.Fatalf("asked = %#v, want [(queen_x, 今天有点累)]", got)
	}
	if !strings.Contains(block, "<voice_examples>…</voice_examples>") {
		t.Fatalf("reminder block missing the package text: %q", block)
	}

	StopTurnContexts(built["voice"].host.home.Root)
	if !built["voice"].stopped {
		t.Fatal("enabled package was not stopped")
	}
}

// TestTurnContextSlowOrFailingPackageIsSkippedForTheTurn ports upstream: a slow
// or raising package is skipped for that turn, the healthy one still answers.
//
// Parity: framework/server/tests/test_turn_context.py::test_a_slow_or_failing_package_is_skipped_for_the_turn
func TestTurnContextSlowOrFailingPackageIsSkippedForTheTurn(t *testing.T) {
	previous := TurnContextTimeout
	TurnContextTimeout = 50 * time.Millisecond
	t.Cleanup(func() { TurnContextTimeout = previous })

	home, _ := installTurnContexts(t,
		map[string]any{
			"slow":   map[string]any{"enabled": true},
			"broken": map[string]any{"enabled": true},
			"good":   map[string]any{"enabled": true},
			"quiet":  map[string]any{"enabled": true},
		},
		map[string]func(TurnContextHost) *fakeTurnContext{
			"slow": func(host TurnContextHost) *fakeTurnContext {
				return &fakeTurnContext{host: host, reply: "late", delay: time.Second}
			},
			"broken": func(host TurnContextHost) *fakeTurnContext {
				return &fakeTurnContext{host: host, failure: errors.New("index corrupt")}
			},
			"good":  func(host TurnContextHost) *fakeTurnContext { return &fakeTurnContext{host: host, reply: "on time"} },
			"quiet": func(host TurnContextHost) *fakeTurnContext { return &fakeTurnContext{host: host} },
		})

	runtime := StartTurnContexts(home)
	if got := runtime.Context("queen_x", "hi"); got != "on time" {
		t.Fatalf("context = %q, want %q", got, "on time")
	}
}

// TestTurnContextPackageThatFailsToStartIsLeftOut ports upstream: a package
// whose Start fails is logged, left out, and contributes nothing.
//
// Parity: framework/server/tests/test_turn_context.py::test_a_package_that_fails_to_start_is_left_out
func TestTurnContextPackageThatFailsToStartIsLeftOut(t *testing.T) {
	home, _ := installTurnContexts(t,
		map[string]any{"voice": map[string]any{"enabled": true}},
		map[string]func(TurnContextHost) *fakeTurnContext{
			"voice": func(host TurnContextHost) *fakeTurnContext {
				return &fakeTurnContext{host: host, reply: "never", failStart: true}
			},
		})

	runtime := StartTurnContexts(home)
	if names := runtime.Names(); len(names) != 0 {
		t.Fatalf("started packages = %v, want none", names)
	}
	if got := runtime.Context("queen_x", "hi"); got != "" {
		t.Fatalf("context = %q, want empty", got)
	}
}

// TestTurnContextOnlyQueensWithProviderWiredAreAsked ports upstream: the
// reminder source applies to a queen stream with a provider, and nowhere else.
//
// Parity: framework/server/tests/test_turn_context.py::test_only_queens_with_the_provider_wired_are_asked
func TestTurnContextOnlyQueensWithProviderWiredAreAsked(t *testing.T) {
	source := agentloop.NewTurnContextReminderSource()
	provider := func(string) string { return "context" }

	if !source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: true, TurnContextProvider: provider}) {
		t.Fatal("queen with provider must apply")
	}
	if source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: false, TurnContextProvider: provider}) {
		t.Fatal("non-queen stream must not apply")
	}
	if source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("queen without provider must not apply")
	}
}

// TestTurnContextConfigGateReadsEnabledSections pins the configuration gate:
// `turn_context.<name>.enabled` must be a truthy value inside an object section,
// and unrelated sections/keys leave it alone.
//
// Parity: framework/server/tests/test_turn_context.py::test_an_enabled_package_gets_its_settings_and_its_text_reaches_the_queen
func TestTurnContextConfigGateReadsEnabledSections(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{
		"gcu_viewport_scale": float64(0.65),
		"turn_context": map[string]any{
			"dict_on":     map[string]any{"enabled": true},
			"dict_off":    map[string]any{"enabled": false},
			"dict_truthy": map[string]any{"enabled": "yes"},
			"dict_empty":  map[string]any{"enabled": ""},
			"dict_zero":   map[string]any{"enabled": 0},
			"not_a_dict":  true,
			"no_enabled":  map[string]any{"k": 1},
			"null":        nil,
		},
	})

	settings := TurnContextSettings(HiveConfig(home))
	section, ok := settings["dict_on"].(map[string]any)
	if !ok || !TurnContextSectionEnabled(section) {
		t.Fatalf("dict_on should be enabled: %#v", settings["dict_on"])
	}
	// Python truthiness, not a strict bool: a non-empty string enables, an empty
	// one does not.
	truthy, _ := settings["dict_truthy"].(map[string]any)
	if !TurnContextSectionEnabled(truthy) {
		t.Fatalf("dict_truthy should be enabled: %#v", settings["dict_truthy"])
	}
	for _, name := range []string{"dict_off", "dict_empty", "dict_zero", "not_a_dict", "no_enabled", "null", "missing"} {
		candidate, _ := settings[name].(map[string]any)
		if TurnContextSectionEnabled(candidate) {
			t.Fatalf("%s must not be enabled", name)
		}
	}
}

// TestTurnContextSettingsToleratesNonObjectSection pins upstream's
// `get_hive_config().get("turn_context") or {}`: a non-object section reads as
// empty instead of panicking.
//
// Parity: framework/server/turn_context.py::start_turn_context
func TestTurnContextSettingsToleratesNonObjectSection(t *testing.T) {
	for _, config := range []map[string]any{
		nil,
		{"turn_context": nil},
		{"turn_context": []any{"voice"}},
		{"turn_context": "voice"},
	} {
		if settings := TurnContextSettings(config); len(settings) != 0 {
			t.Fatalf("settings for %#v = %#v, want empty", config, settings)
		}
	}
}

// TestTurnContextScopeFollowsQueenStream pins the agent-context adaptation: the
// provider rides the agent context, but the reminder source only consults it on
// a queen stream — a worker stream's provider is never asked.
//
// Parity-exempt: 上游 test_turn_context.py 只测 reminder source 的 applies_to；
// AgentContext→ReminderAgentContext 的 queen/worker 判定是 Go 侧接线，无对应上游测试。
func TestTurnContextScopeFollowsQueenStream(t *testing.T) {
	queen := &agentloop.AgentContext{
		StreamID:            "queen",
		TurnContextProvider: func(string) string { return "queen context" },
	}
	worker := &agentloop.AgentContext{
		StreamID:            "worker",
		TurnContextProvider: func(string) string { return "worker context" },
	}

	source := agentloop.NewTurnContextReminderSource()
	queenView := agentloop.NewReminderAgentContext(queen)
	if !queenView.IsQueenStream || queenView.TurnContextProvider == nil {
		t.Fatalf("queen view = %#v, want a wired provider", queenView)
	}
	if !source.AppliesTo(queenView) {
		t.Fatal("queen stream must consult the turn context")
	}
	if got := queenView.TurnContextProvider("hi"); got != "queen context" {
		t.Fatalf("queen provider = %q", got)
	}
	workerView := agentloop.NewReminderAgentContext(worker)
	if workerView.IsQueenStream {
		t.Fatalf("worker view is a queen stream: %#v", workerView)
	}
	if source.AppliesTo(workerView) {
		t.Fatal("worker stream must not consult the turn context")
	}
}

// TestChatRuntimeTurnContextReachesTheQueenPrompt is the real call chain: the
// session reads configuration.json, starts the enabled package, and its text
// reaches the model as a system reminder ahead of the user message.
//
// Parity: framework/server/tests/test_turn_context.py::test_an_enabled_package_gets_its_settings_and_its_text_reaches_the_queen
func TestChatRuntimeTurnContextReachesTheQueenPrompt(t *testing.T) {
	home, built := installTurnContexts(t,
		map[string]any{"brief": map[string]any{"enabled": true}},
		map[string]func(TurnContextHost) *fakeTurnContext{
			"brief": func(host TurnContextHost) *fakeTurnContext {
				return &fakeTurnContext{host: host, reply: "TURN-CONTEXT-BRIEF"}
			},
		})

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     nil,
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      t.TempDir(),
		SessionID: "turn-context-chain",
		QueenName: "queen_x",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("今天有点累"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)

	sent := chatAllText(model.request(0).Messages)
	contextAt := strings.Index(sent, "TURN-CONTEXT-BRIEF")
	userAt := strings.Index(sent, "今天有点累")
	if contextAt < 0 || userAt < 0 {
		t.Fatalf("model request missing turn context or user message:\n%s", sent)
	}
	if contextAt > userAt {
		t.Fatalf("turn context should precede the user message:\n%s", sent)
	}
	if !strings.Contains(sent, "<system-reminder>") {
		t.Fatalf("turn context should ride a system reminder:\n%s", sent)
	}
	// The first message fires both SESSION_START and USER_PROMPT_SUBMIT (the
	// upstream source's two points), so the provider may be asked twice; what
	// matters is the queen's name and the message as she receives it.
	asked := built["brief"].askedPairs()
	if len(asked) == 0 {
		t.Fatal("turn-context provider was never asked")
	}
	for _, pair := range asked {
		if pair != [2]string{"queen_x", "今天有点累"} {
			t.Fatalf("provider asked = %#v, want (queen_x, 今天有点累)", asked)
		}
	}
}
