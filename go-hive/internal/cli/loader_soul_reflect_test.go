package cli

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/e2e/soulgw"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
)

const soulReflectTestQueen = "queen_x"

// soulReflectTestHome writes a minimal hive home: an ollama/ model pointed at
// the local gateway (no API key needed), one soul queen, her profile, and no
// sessions until a test adds them.
func soulReflectTestHome(t *testing.T, gatewayURL, mode string) hivehome.Home {
	t.Helper()
	root := testTempDir(t)
	configuration := map[string]any{
		"llm": map[string]any{
			"provider": "custom",
			"model":    "ollama/soul-e2e",
			"api_base": gatewayURL + "/v1",
		},
		"soul": map[string]any{"queens": map[string]any{soulReflectTestQueen: map[string]any{
			"enabled":   true,
			"mode":      mode,
			"hour":      23,
			"max_edits": 2,
			"sections": []any{
				map[string]any{"key": "now", "title": "Now", "limit": 300},
			},
		}}},
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "configuration.json"), data, 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
	queenDir := filepath.Join(root, "queens", soulReflectTestQueen)
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatalf("create queen dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"), []byte("name: Fox\n"), 0o644); err != nil {
		t.Fatalf("write profile.yaml: %v", err)
	}
	return hivehome.Home{Root: root}
}

func soulReflectTestSession(t *testing.T, home hivehome.Home, sessionID, title, said, replied string) string {
	t.Helper()
	dir := filepath.Join(home.QueensDir(), soulReflectTestQueen, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	meta, err := json.Marshal(map[string]any{"title": title, "channel": false})
	if err != nil {
		t.Fatalf("encode meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	saidAt := time.Now().Add(-3 * time.Minute).Format("2006-01-02T15:04:05")
	repliedAt := time.Now().Add(-2 * time.Minute).Format("2006-01-02T15:04:05")
	events := []map[string]any{
		{"type": "client_input_received", "timestamp": saidAt, "data": map[string]any{"content": said}},
		{"type": "client_output_delta", "timestamp": repliedAt, "stream_id": "queen", "execution_id": "e1",
			"data": map[string]any{"snapshot": replied, "iteration": 0, "inner_turn": 0}},
	}
	var body strings.Builder
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode event: %v", err)
		}
		body.Write(encoded)
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	return dir
}

func soulReflectTestConfig(t *testing.T, home hivehome.Home) *soulstore.SoulConfig {
	t.Helper()
	cfg := soulstore.GetSoulConfig(hiveserver.HiveConfig(home), soulReflectTestQueen)
	if cfg == nil {
		t.Fatal("the test queen has no soul configuration")
	}
	return cfg
}

// Parity-exempt: no upstream test drives the serve adapter; the real HTTP transport must turn
// the queen's own session events into a pending review proposal.
func TestSoulReflectReviewWritesPendingProposal(t *testing.T) {
	gateway := soulgw.New(soulgw.Options{})
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	home := soulReflectTestHome(t, server.URL, "review")
	const said = "I stayed up painting again and it felt good."
	const replied = "I'm glad you made time for it."
	soulReflectTestSession(t, home, "s1", "Painting at night", said, replied)

	if err := newSoulReflectFor(home, "")(soulReflectTestQueen, soulReflectTestConfig(t, home), false); err != nil {
		t.Fatalf("reflect: %v", err)
	}

	proposal := soulstore.PendingProposal(home.QueensDir(), soulReflectTestQueen)
	if proposal == nil {
		t.Fatal("review reflect left no pending proposal")
	}
	if len(proposal.Edits) != 1 {
		t.Fatalf("edits = %d, want 1: %#v", len(proposal.Edits), proposal.Edits)
	}
	edit := proposal.Edits[0]
	if edit.Op != "add" || edit.Section != "now" {
		t.Fatalf("edit = %#v, want an add in now", edit)
	}
	if edit.Text != "I want to keep painting." {
		t.Fatalf("edit text = %q", edit.Text)
	}
	if len(edit.Evidence) == 0 {
		t.Fatalf("edit evidence = %#v", edit.Evidence)
	}
	quote, _ := edit.Evidence[0]["quote"].(string)
	if quote != said {
		t.Fatalf("evidence quote = %q, want the chat line %q", quote, said)
	}

	state := soulstore.LoadReflectionState(home.QueensDir(), soulReflectTestQueen)
	today := time.Now().Format("2006-01-02")
	if state["last_run"] != today || state["last_result"] != "1 changes" {
		t.Fatalf("state = %#v, want last_run %s and 1 changes", state, today)
	}
	cursors, _ := state["cursors"].(map[string]any)
	if cursors["s1"] == "" {
		t.Fatalf("cursors = %#v, want s1 read", cursors)
	}

	// The real HTTP transport must have played the whole tool loop: add_item,
	// then finish after the tool result.
	requests := gateway.ReflectionRequests()
	if len(requests) != 2 {
		t.Fatalf("reflection requests = %d, want 2", len(requests))
	}
}

// Parity-exempt: no upstream test drives the serve adapter; auto mode must apply the proposal
// to the soul immediately.
func TestSoulReflectAutoAppliesTheProposal(t *testing.T) {
	gateway := soulgw.New(soulgw.Options{})
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	home := soulReflectTestHome(t, server.URL, "auto")
	soulReflectTestSession(t, home, "s1", "Painting at night",
		"I stayed up painting again and it felt good.", "I'm glad you made time for it.")

	if err := newSoulReflectFor(home, "")(soulReflectTestQueen, soulReflectTestConfig(t, home), false); err != nil {
		t.Fatalf("reflect: %v", err)
	}

	items := soulstore.Load(home.QueensDir(), soulReflectTestQueen).Items("now")
	if len(items) != 1 || items[0].Text != "I want to keep painting." {
		t.Fatalf("soul items = %#v, want the reflected item applied", items)
	}
	entries, err := os.ReadDir(filepath.Join(soulstore.SoulDir(home.QueensDir(), soulReflectTestQueen), "proposals"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("proposal files = %v, err = %v", entries, err)
	}
	proposal := soulstore.LoadProposal(home.QueensDir(), soulReflectTestQueen, strings.TrimSuffix(entries[0].Name(), ".json"))
	if proposal == nil || proposal.Status != "applied" || len(proposal.Applied) != 1 {
		t.Fatalf("proposal = %#v, want applied with one change", proposal)
	}
}

// Parity-exempt: no upstream test covers the manual adapter's guard; a missing config or a queen
// with no readable sessions must be an explicit error, not a recorded run.
func TestSoulReflectReportsMissingInputsAsErrors(t *testing.T) {
	home := soulReflectTestHome(t, "http://127.0.0.1:1", "review")
	reflect := newSoulReflectFor(home, "")

	if err := reflect(soulReflectTestQueen, nil, false); err == nil || !strings.Contains(err.Error(), "no soul configuration") {
		t.Fatalf("nil config error = %v", err)
	}
	// The queen exists and has a soul config, but no chat sessions to read.
	if err := reflect(soulReflectTestQueen, soulReflectTestConfig(t, home), false); err == nil || !strings.Contains(err.Error(), "no chat sessions") {
		t.Fatalf("no-session error = %v", err)
	}
}

// Parity-exempt: no upstream test covers the manual adapter's failure path; a failing model must
// not leave a proposal or a last_run behind.
func TestSoulReflectReportsModelFailure(t *testing.T) {
	gateway := soulgw.New(soulgw.Options{FailureStatus: 500})
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	home := soulReflectTestHome(t, server.URL, "review")
	soulReflectTestSession(t, home, "s1", "Painting at night",
		"I stayed up painting again and it felt good.", "I'm glad you made time for it.")

	err := newSoulReflectFor(home, "")(soulReflectTestQueen, soulReflectTestConfig(t, home), false)
	if err == nil {
		t.Fatal("a failing model was reported as a successful reflection")
	}
	if proposal := soulstore.PendingProposal(home.QueensDir(), soulReflectTestQueen); proposal != nil {
		t.Fatalf("failed reflection left a proposal: %#v", proposal)
	}
	state := soulstore.LoadReflectionState(home.QueensDir(), soulReflectTestQueen)
	if _, present := state["last_run"]; present {
		t.Fatalf("failed reflection recorded last_run: %#v", state)
	}
}
