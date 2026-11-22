package server

// Turn-context parity: the body a started package contributes to the reminder
// ahead of a queen's turn, driven through the real startup path:
//
//	configuration.json enables turn_context.probe -> StartTurnContexts ->
//	runtime.ForSession / Context -> the built-in probe package's text.
//
// The Go build links the probe package (turn_context_probe.go), the
// compile-time analogue of upstream's entry point; the frozen golden is the
// Python reference's context_for output for the same package body — see
// go_hive/scripts/prompt_catalog_parity.py.
//
// Regenerate:
//
//	HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1 go test ./internal/server -run TestTurnContextBodyParityGolden

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity-exempt: golden fixtures are captured from the Python runtime by
// scripts/prompt_catalog_parity.py; this test replays the body/wrapper against
// Go rendering rather than naming one upstream function.
func TestTurnContextBodyParityGolden(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	config := []byte("{\n  \"turn_context\": {\"probe\": {\"enabled\": true}}\n}\n")
	if err := os.WriteFile(filepath.Join(home.Root, "configuration.json"), config, 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}

	runtime := StartTurnContexts(home)
	defer StopTurnContexts(home.Root)
	if runtime == nil || runtime.Empty() {
		t.Fatal("turn-context runtime started nothing for an enabled package")
	}
	if names := runtime.Names(); len(names) != 1 || names[0] != TurnContextProbeName {
		t.Fatalf("started packages = %v, want [%s]", names, TurnContextProbeName)
	}

	body := runtime.Context("queen_growth", "PARITY_PROBE_MESSAGE")
	if body != TurnContextProbeBody {
		t.Fatalf("turn context body = %q, want %q", body, TurnContextProbeBody)
	}
	if block := wrapTurnContextBlockForTest(body); !strings.Contains(block, TurnContextProbeBody) {
		t.Fatalf("reminder block lost the package body: %q", block)
	}

	if captureDir := strings.TrimSpace(os.Getenv("HIVE_PROMPT_CATALOG_CAPTURE_DIR")); captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o755); err != nil {
			t.Fatalf("capture dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(captureDir, "turn-context.txt"), []byte(body), 0o644); err != nil {
			t.Fatalf("capture turn-context: %v", err)
		}
	}

	goldenPath := filepath.Join("testdata", "prompt-parity", "turn-context.body.txt")
	if os.Getenv("HIVE_PROMPT_CATALOG_WRITE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(body), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1)", err)
	}
	if body != string(golden) {
		t.Fatalf("turn-context body diverged from golden:\n  want: %s\n  got:  %s", strconv.Quote(string(golden)), strconv.Quote(body))
	}
}

// TestChatRuntimeBuiltinProbeInjectsBeforeUserMessage pins the injected block
// byte for byte and where it lands: the reminder rides the user message that
// answers the turn, ahead of the stamped text, exactly as the Python reference
// serializes it (sanitizeForAPI merges consecutive user messages with "\n").
// The opening turn carries no message, so the source's blank guard keeps it
// silent there — the live parity script observes the same shape.
// Parity: core/framework/server/turn_context.py::start_turn_context
func TestChatRuntimeBuiltinProbeInjectsBeforeUserMessage(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{
		"turn_context": map[string]any{TurnContextProbeName: map[string]any{"enabled": true}},
	})

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      t.TempDir(),
		SessionID: "turn-context-builtin-probe",
		QueenName: "queen_x",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	t.Cleanup(func() { StopTurnContexts(home.Root) })

	runtime.StartOpeningTurn()
	waitForAssistantParts(t, runtime, 1)
	if opening := chatAllText(model.request(0).Messages); strings.Contains(opening, TurnContextProbeBody) {
		t.Fatalf("opening turn injected the turn context despite the empty-message guard:\n%s", opening)
	}

	if err := runtime.Deliver("PARITY_PROBE_MESSAGE"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 2)

	last := model.request(model.callCount() - 1)
	if len(last.Messages) == 0 {
		t.Fatal("user-turn request carried no messages")
	}
	final := last.Messages[len(last.Messages)-1]
	if final.Role != "user" {
		t.Fatalf("last request message role = %q, want user", final.Role)
	}
	block := agentloop.WrapReminder([]string{TurnContextProbeBody})
	if !strings.HasPrefix(final.Content, block+"\n[") {
		t.Fatalf("user message does not start with the reminder block:\n%s", final.Content)
	}
	if !strings.HasSuffix(final.Content, "] PARITY_PROBE_MESSAGE") {
		t.Fatalf("reminder did not precede the stamped user text:\n%s", final.Content)
	}
	if count := strings.Count(final.Content, TurnContextProbeBody); count != 1 {
		t.Fatalf("turn-context body appears %d times in the user message, want 1:\n%s", count, final.Content)
	}
}

// TestChatRuntimeTurnContextUnconfiguredDoesNotInject keeps the default state
// honest: the probe package is linked into every binary, but without
// turn_context.probe.enabled it must not start and must not put anything in
// front of a user turn.
// Parity: core/framework/server/turn_context.py::start_turn_context
func TestChatRuntimeTurnContextUnconfiguredDoesNotInject(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"gcu_viewport_scale": 0.65})
	t.Cleanup(func() { StopTurnContexts(home.Root) })
	if names := StartTurnContexts(home).Names(); len(names) != 0 {
		t.Fatalf("unconfigured turn-context packages started: %v", names)
	}

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      t.TempDir(),
		SessionID: "turn-context-unconfigured",
		QueenName: "queen_x",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	runtime.StartOpeningTurn()
	waitForAssistantParts(t, runtime, 1)
	if err := runtime.Deliver("PARITY_PROBE_MESSAGE"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 2)

	for index := 0; index < model.callCount(); index++ {
		if sent := chatAllText(model.request(index).Messages); strings.Contains(sent, TurnContextProbeBody) {
			t.Fatalf("request %d injected the turn context while unconfigured:\n%s", index, sent)
		}
	}
}

// wrapTurnContextBlockForTest documents the wrapper the hub applies to the
// body (the byte-level wrapper parity lives in agentloop's reminder golden).
func wrapTurnContextBlockForTest(body string) string {
	return "<system-reminder>\n" + body + "\n</system-reminder>"
}
