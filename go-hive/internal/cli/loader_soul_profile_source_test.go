package cli

// The reflection's identity seam, pinned at the injection point.
// internal/soul reads a queen's profile through soul.QueenProfileSource (it
// must not import internal/queen), and this package injects the queen-backed
// adapter (loader_soul_profile_source.go). This test proves the adapter feeds
// the reflection exactly what the profile store renders: her name and the
// identity block, verbatim.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	queen "github.com/aden-hive/hive-go/internal/queen"
	"github.com/aden-hive/hive-go/internal/soul"
)

// soulProfileTestLLM records the reflection's requests and answers without any
// tool call, so the reflection builds its prompt and finishes with no edits.
type soulProfileTestLLM struct {
	requests []llm.CompletionRequest
}

func (m *soulProfileTestLLM) AComplete(_ context.Context, request llm.CompletionRequest) (llm.Completion, error) {
	m.requests = append(m.requests, request)
	return llm.Completion{Content: "nothing more"}, nil
}

// Parity-exempt: the seam is Go-specific — upstream's soul_reflection imports
// the profile store in the same package, the Go port injects it so
// internal/soul stays below internal/queen. This pins the injected
// implementation end to end.
func TestSoulProfileSourceFeedsTheReflectionIdentity(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(root)
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	queenID := "queen_profile"
	queenDir := filepath.Join(home.QueensDir(), queenID)
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatalf("create queen dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"),
		[]byte("name: Fox\ntitle: Sketch artist\ncore_traits: Fox draws.\n"), 0o644); err != nil {
		t.Fatalf("write profile.yaml: %v", err)
	}

	sessionDir := filepath.Join(queenDir, "sessions", "s1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	writeSoulProfileTestJSON(t, filepath.Join(sessionDir, "meta.json"), map[string]any{"title": "Night owls", "channel": false})
	writeSoulProfileTestText(t, filepath.Join(sessionDir, "events.jsonl"),
		`{"type":"client_input_received","timestamp":"2026-10-20T14:00:00","data":{"content":"your lantern sketch is lovely"}}`+"\n"+
			`{"type":"client_output_delta","timestamp":"2026-10-20T15:00:00","stream_id":"queen","execution_id":"e1","data":{"snapshot":"I'm glad","iteration":0,"inner_turn":0}}`+"\n")

	cfg := soul.GetSoulConfig(map[string]any{
		"soul": map[string]any{"queens": map[string]any{
			queenID: map[string]any{
				"enabled":   true,
				"mode":      "review",
				"hour":      0,
				"max_edits": 3,
				"sections": []any{
					map[string]any{"key": "now", "title": "Now", "limit": 300},
				},
			},
		}},
	}, queenID)
	if cfg == nil {
		t.Fatal("soul config did not parse")
	}

	model := &soulProfileTestLLM{}
	proposal, err := soul.Reflect(soul.ReflectionOptions{
		HomeRoot:      home.Root,
		QueensDir:     home.QueensDir(),
		QueenID:       queenID,
		Config:        cfg,
		Completer:     model,
		ProfileSource: soulProfileSource{},
		Now:           time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if proposal == nil {
		t.Fatal("the seeded chat should have been read")
	}
	if len(model.requests) == 0 {
		t.Fatal("the reflection never called the model")
	}
	system := model.requests[0].System
	if !strings.HasPrefix(system, "You are Fox.") {
		t.Fatalf("the system prompt does not carry the profile's name: %q", system)
	}
	profile, err := queen.LoadQueenProfile(home.QueensDir(), queenID)
	if err != nil {
		t.Fatalf("load queen profile: %v", err)
	}
	noExamples := 0
	identity := queen.FormatQueenIdentityPrompt(profile, &noExamples)
	if identity == "" || !strings.Contains(system, identity) {
		t.Fatalf("the system prompt does not embed the rendered identity verbatim:\nidentity %q\nsystem %q", identity, system)
	}
}

func writeSoulProfileTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	writeSoulProfileTestText(t, path, string(data))
}

func writeSoulProfileTestText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
