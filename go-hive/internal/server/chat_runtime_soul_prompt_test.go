package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/soul"
)

func writeQueenSoulConfig(t *testing.T, home hivehome.Home, queenID string) {
	t.Helper()
	config := map[string]any{
		"soul": map[string]any{
			"queens": map[string]any{
				queenID: map[string]any{"enabled": true},
			},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal configuration: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), data, 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
}

func writeQueenSoul(t *testing.T, home hivehome.Home, queenID, text string) {
	t.Helper()
	dir := soul.SoulDir(home.QueensDir(), queenID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir soul: %v", err)
	}
	payload := map[string]any{
		"version": 1,
		"sections": map[string]any{
			"views": []any{map[string]any{"text": text, "date": "2026-11-01"}},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal soul: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, soul.SoulFile), data, 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}
}

// Parity: core/tests/test_soul.py::test_the_soul_rides_her_prompt_right_after_her_identity
func TestChatRuntimeSoulRidesSystemPrompt(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeQueenSoulConfig(t, home, "queen_technology")
	const conclusion = "I keep promises to the team."
	writeQueenSoul(t, home, "queen_technology", conclusion)

	model := &queenChatLLM{}
	runtime, recorder, _ := newQueenChatRuntime(t, model, home.Root, "queen_technology")
	if !strings.Contains(runtime.agentCtx.IdentityPrompt, "<soul>") {
		t.Fatalf("identity prompt = %q, want the soul block", runtime.agentCtx.IdentityPrompt)
	}
	if !strings.Contains(runtime.agentCtx.IdentityPrompt, conclusion) {
		t.Fatalf("identity prompt = %q, want the soul conclusion", runtime.agentCtx.IdentityPrompt)
	}

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	requests := model.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("model never received a completion request")
	}
	system := requests[0].System
	if !strings.Contains(system, "<soul>") || !strings.Contains(system, conclusion) {
		t.Fatalf("completion system prompt = %q, want the soul block", system)
	}
	if strings.Index(system, "<soul>") < strings.Index(system, "<core_identity>") {
		t.Fatalf("system prompt = %q, want the soul after the identity", system)
	}
}

// Parity: core/tests/test_soul.py::test_an_empty_soul_adds_nothing_to_her_prompt
func TestChatRuntimeEmptySoulAddsNothing(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeQueenSoulConfig(t, home, "queen_technology")
	// No soul.json: the queen has a configured soul entry but no notes yet.

	runtime, _, _ := newQueenChatRuntime(t, &queenChatLLM{}, home.Root, "queen_technology")
	if strings.Contains(runtime.agentCtx.IdentityPrompt, "<soul>") {
		t.Fatalf("identity prompt = %q, want no empty soul block", runtime.agentCtx.IdentityPrompt)
	}
}
