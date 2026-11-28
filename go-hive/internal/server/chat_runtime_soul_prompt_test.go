package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/soul"
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

// Parity: core/tests/test_soul.py::test_her_prompt_follows_a_new_version_and_drops_a_disabled_soul
func TestChatRuntimePicksUpSoulConfigAtNextTurnBoundary(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	const queenID = "queen_technology"
	const conclusion = "I keep the loop budgets honest."
	writeServerTestFile(t, filepath.Join(home.QueensDir(), queenID, "profile.yaml"),
		"name: Fox\ntitle: The engineer\n")

	model := &queenChatLLM{}
	runtime, recorder, bus := newQueenChatRuntime(t, model, home.Root, queenID)
	session := NewSession(runtime.sessionID, bus)
	session.QueenName = queenID
	session.PhaseState = fakePhaseState{
		phase:   QueenPhaseIndependent,
		queenID: queenID,
		profile: map[string]any{"name": "Fox", "title": "The engineer"},
	}
	runtime.deps.Session = session

	if strings.Contains(runtime.agentCtx.IdentityPrompt, "<soul>") {
		t.Fatalf("identity prompt already carries a soul before the config entry: %q", runtime.agentCtx.IdentityPrompt)
	}
	mux := soulTestMux(SoulRouteDeps{Home: home})
	if before, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/"+queenID+"/soul", ""); before.Code != http.StatusOK || view["enabled"] != false {
		t.Fatalf("GET soul before the config entry = %d %#v, want 200 disabled", before.Code, view)
	}

	// The entry and the soul land while the runtime is live. The runtime
	// rebuilds its static prompt at the next turn boundary; the Settings panel
	// still fetches on mount (and around its own actions), so an already-open
	// page keeps the old view until it remounts/reloads.
	writeQueenSoulConfig(t, home, queenID)
	writeQueenSoul(t, home, queenID, conclusion)
	if after, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/"+queenID+"/soul", ""); after.Code != http.StatusOK || view["enabled"] != true {
		t.Fatalf("GET soul after the config entry = %d %#v, want 200 enabled", after.Code, view)
	}

	if err := runtime.Deliver("what changed?"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	system := chatSystemForUserMessage(t, model.requestSnapshot(), "what changed?")
	if !strings.Contains(system, "<soul>") || !strings.Contains(system, conclusion) {
		t.Fatalf("next-turn system prompt = %q, want the hot-reloaded soul block", system)
	}
}

func chatSystemForUserMessage(t *testing.T, requests []llm.CompletionRequest, needle string) string {
	t.Helper()
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, needle) {
				return request.System
			}
		}
	}
	t.Fatalf("no completion request carried user message %q; requests=%d", needle, len(requests))
	return ""
}
