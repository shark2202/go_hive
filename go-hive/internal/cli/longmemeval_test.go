package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/longmemeval"
)

// lmeScriptedLLM plays the queen: the first tool-enabled call searches past
// sessions, the second answers from what the search returned. Auxiliary
// calls (memory recall, reflection) get a neutral reply.
type lmeScriptedLLM struct {
	mu         sync.Mutex
	queenCalls int
}

func (m *lmeScriptedLLM) ModelName() string { return "scripted-queen" }

func (m *lmeScriptedLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if !hasToolName(req.Tools, searchMessagesTool) {
		return llm.Completion{Content: "", StopReason: "stop", Model: "scripted-queen"}, nil
	}
	m.mu.Lock()
	m.queenCalls++
	call := m.queenCalls
	m.mu.Unlock()
	if call == 1 {
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: searchMessagesTool, Arguments: `{"pattern": "45 minutes"}`}},
			StopReason: "tool_calls",
			Model:      "scripted-queen",
		}, nil
	}
	// The second call already carries the tool result; keep it for the test.
	return llm.Completion{
		Content:    "Your commute is 45 minutes each way.",
		StopReason: "end_turn",
		Model:      "scripted-queen",
	}, nil
}

// Parity-exempt: Go-side coverage for the pytest queen driver
// (tests/e2e/conftest.py::run_queen): one real chat session over seeded
// history, the frozen clock, and the fetched search result.
func TestLMERunnerAsksSeededQueenWithFrozenClock(t *testing.T) {
	items, err := longmemeval.ReadItems(filepath.Join("..", "longmemeval", "testdata", "fixture_cleaned.json"))
	if err != nil {
		t.Fatalf("ReadItems: %v", err)
	}
	item := items[0]
	homeRoot := t.TempDir()
	seeded, err := longmemeval.SeedHaystack(homeRoot, "queen_technology", item)
	if err != nil {
		t.Fatalf("SeedHaystack: %v", err)
	}

	provider := &lmeScriptedLLM{}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	t.Setenv(EnvFakeNow, "")
	runner := NewLMERunner(LMERunnerOptions{Queen: "queen_technology"})
	when, err := longmemeval.ParseDate(item.QuestionDate)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := runner.Ask(context.Background(), longmemeval.AskRequest{
		HomeRoot: homeRoot,
		Question: item.Question,
		When:     when,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(answer.Text, "45 minutes") {
		t.Fatalf("answer text = %q", answer.Text)
	}
	if len(answer.Searches) != 1 || answer.Searches[0].Tool != searchMessagesTool {
		t.Fatalf("searches = %+v", answer.Searches)
	}
	if answer.Searches[0].Input["pattern"] != "45 minutes" {
		t.Fatalf("search input = %+v", answer.Searches[0].Input)
	}
	hiveID := seeded.Mapping["fixture_s1"]
	if !strings.Contains(strings.Join(answer.SearchResults, " "), hiveID) {
		t.Fatalf("search results lack the seeded session %s: %q", hiveID, answer.SearchResults)
	}
	if !strings.Contains(strings.Join(answer.Tools, ","), searchMessagesTool) {
		t.Fatalf("tools = %v", answer.Tools)
	}

	// The queen's question turn is stamped with the question's date, not today.
	var questionTurn string
	for _, part := range answer.Turns {
		role, _ := part["role"].(string)
		content, _ := part["content"].(string)
		if role == "user" && strings.Contains(content, item.Question) {
			questionTurn = content
			break
		}
	}
	if !strings.HasPrefix(questionTurn, "[2023-05-20 10:00") {
		t.Fatalf("question turn stamp = %q, want the frozen question date", questionTurn)
	}
	if got := os.Getenv(EnvFakeNow); got != "" {
		t.Fatalf("clock env leaked after Ask: %q", got)
	}
	if provider.queenCalls < 2 {
		t.Fatalf("queen calls = %d, want the search and the answer", provider.queenCalls)
	}
}
