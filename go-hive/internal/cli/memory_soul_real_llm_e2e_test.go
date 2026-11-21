package cli

// Real-LLM checks for the memory timeline and the daily soul reflection.
// They are opt-in: HIVE_REAL_LLM_E2E=1 and a HIVE_HOME whose configuration.json
// resolves to a reachable OpenAI-compatible model (the same main-slot
// resolution every live session uses). The deterministic e2e suite covers the
// wire shape; these tests prove the production code path survives a real
// model's output.
//
// Parity-exempt: integration tests against a live model; the Python suite
// scripts its completer, so there is no upstream test to mirror.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/soul"
)

func realLLMHome(t *testing.T) hivehome.Home {
	t.Helper()
	if os.Getenv("HIVE_REAL_LLM_E2E") != "1" {
		t.Skip("real LLM e2e is opt-in: set HIVE_REAL_LLM_E2E=1")
	}
	home, err := hivehome.Resolve()
	if err != nil {
		t.Fatalf("resolve HIVE_HOME: %v", err)
	}
	return home
}

func writeRealLLMFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRealLLMTimelineExtraction drives UpdateSessionTimeline with the real
// configured model over a session whose user message carries a relative date.
// Parity-exempt: opt-in 真实模型 E2E（HIVE_REAL_LLM_E2E=1），上游无同名用例
func TestRealLLMTimelineExtraction(t *testing.T) {
	home := realLLMHome(t)
	provider, err := resolveChatProvider(home, "")
	if err != nil {
		t.Fatalf("resolve real LLM provider: %v", err)
	}
	sessionID := "session_20260102_090000_realtl01"
	sessionDir := filepath.Join(t.TempDir(), sessionID)
	event := `{"type":"client_input_received","timestamp":"2026-01-02T09:00:01","data":{"content":"昨天我去参加了同事的婚礼，随礼 800 元。"}}`
	writeRealLLMFile(t, filepath.Join(sessionDir, "events.jsonl"), event+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	count, err := memory.UpdateSessionTimeline(ctx, provider, sessionDir)
	if err != nil {
		t.Fatalf("UpdateSessionTimeline: %v", err)
	}
	meta := memory.ReadTimelineMeta(sessionDir)
	if meta["version"] != float64(memory.TimelineSchemaVersion) && meta["version"] != memory.TimelineSchemaVersion {
		t.Fatalf("timeline_meta version = %v, want %d (%v)", meta["version"], memory.TimelineSchemaVersion, meta)
	}
	if model, _ := meta["model"].(string); model == "" {
		t.Fatalf("timeline_meta model is empty: %v", meta)
	}
	if got := meta["user_turns"]; got != float64(1) && got != 1 {
		t.Fatalf("timeline_meta user_turns = %v, want 1", got)
	}
	if count == 0 {
		t.Fatalf("real model extracted no timeline item from %q; meta=%v", "婚礼", meta)
	}
	raw, err := os.ReadFile(filepath.Join(sessionDir, memory.TimelineFile))
	if err != nil {
		t.Fatalf("read timeline.jsonl: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item memory.TimelineItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("timeline line is not a TimelineItem: %v (%s)", err, line)
		}
		if item.Text == "" || item.Kind == "" || item.Start == "" || item.End == "" || item.Turn < 1 {
			t.Fatalf("timeline item is incomplete: %+v", item)
		}
		t.Logf("real timeline item: %+v", item)
	}
}

// TestRealLLMSoulReflection runs one review-mode reflection with the real
// configured model over a seeded chat and an aged-out soul item, then approves
// the proposal. It proves the tool loop, the evidence check and soul.json /
// state.json / history persistence against a real model.
// Parity-exempt: opt-in 真实模型 E2E（HIVE_REAL_LLM_E2E=1），上游无同名用例
func TestRealLLMSoulReflection(t *testing.T) {
	home := realLLMHome(t)
	provider, err := resolveChatProvider(home, "")
	if err != nil {
		t.Fatalf("resolve real LLM provider: %v", err)
	}
	queensDir := filepath.Join(t.TempDir(), "queens")
	queenID := "queen_real"
	said := "I stayed up painting the harbour lanterns again last night and Ann asked me for a colour version."
	replied := "I'm glad the lanterns found someone else to love them too."
	writeRealLLMFile(t, filepath.Join(queensDir, queenID, "profile.yaml"), "name: Fox\n")
	events := "{\"type\":\"client_input_received\",\"timestamp\":\"" + time.Now().Add(-3*time.Minute).Format("2006-01-02T15:04:05") + "\",\"data\":{\"content\":" + pyQuote(said) + "}}\n" +
		"{\"type\":\"client_output_delta\",\"timestamp\":\"" + time.Now().Add(-2*time.Minute).Format("2006-01-02T15:04:05") + "\",\"stream_id\":\"queen\",\"execution_id\":\"e1\",\"data\":{\"snapshot\":" + pyQuote(replied) + ",\"iteration\":0,\"inner_turn\":0}}\n"
	writeRealLLMFile(t, filepath.Join(queensDir, queenID, "sessions", "s1", "events.jsonl"), events)
	writeRealLLMFile(t, filepath.Join(queensDir, queenID, "sessions", "s1", "meta.json"), `{"title":"Night painting"}`)
	aged := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	writeRealLLMFile(t, filepath.Join(queensDir, queenID, "soul", "soul.json"),
		`{"version":0,"updated_at":"","sections":{"now":[{"text":"I used to keep a notebook of harbour sketches.","date":"`+aged+`"}]}}`)

	cfg := soul.GetSoulConfig(map[string]any{
		"soul": map[string]any{"queens": map[string]any{queenID: map[string]any{
			"enabled":   true,
			"mode":      "review",
			"hour":      0,
			"max_edits": 3,
			"sections": []any{
				map[string]any{"key": "now", "title": "Now", "limit": 300, "max_age_days": 14},
				map[string]any{"key": "people", "title": "Me and them", "limit": 800, "evidence": 2},
			},
		}}},
	}, queenID)
	if cfg == nil {
		t.Fatal("soul config did not parse")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	proposal, err := soul.Reflect(soul.ReflectionOptions{
		HomeRoot:      home.Root,
		QueensDir:     queensDir,
		QueenID:       queenID,
		Config:        cfg,
		Completer:     provider,
		ProfileSource: soulProfileSource{},
		Context:       ctx,
		MaxTokens:     8192,
	})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if proposal == nil {
		t.Fatal("Reflect returned no proposal; the seeded chat should have been read")
	}
	if len(proposal.Edits) == 0 {
		t.Fatalf("proposal has no edits (state=%+v)", soul.LoadReflectionState(queensDir, queenID))
	}
	for _, edit := range proposal.Edits {
		for _, entry := range edit.Evidence {
			quote, _ := entry["quote"].(string)
			if quote == "" || entry["where"] == nil {
				t.Fatalf("evidence entry is incomplete: %#v", entry)
			}
			if !strings.Contains(stripAllSpace(said), stripAllSpace(quote)) &&
				!strings.Contains(stripAllSpace(replied), stripAllSpace(quote)) {
				t.Fatalf("evidence quote is not verbatim in the chat: %q", quote)
			}
		}
		t.Logf("real soul edit: %+v", edit)
	}
	state := soul.LoadReflectionState(queensDir, queenID)
	if state["last_run"] != time.Now().Format("2006-01-02") {
		t.Fatalf("state.json last_run = %v", state["last_run"])
	}
	pending := soul.PendingProposal(queensDir, queenID)
	if pending == nil || pending.ID != proposal.ID {
		t.Fatalf("review proposal is not pending: %+v (pending=%+v)", proposal, pending)
	}
	applied, err := soul.Decide(queensDir, cfg, queenID, pending, nil, "owner")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if applied == nil || applied.Version < 1 {
		t.Fatalf("approved soul = %+v, want a new version", applied)
	}
	if _, err := os.Stat(filepath.Join(queensDir, queenID, "soul", "history", "v1.json")); err != nil {
		t.Fatalf("history/v1.json missing after approval: %v", err)
	}
}

func stripAllSpace(text string) string {
	return strings.Join(strings.Fields(text), "")
}

func pyQuote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}
