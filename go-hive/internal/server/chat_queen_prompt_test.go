package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// The golden is the Python reference's captured first-turn system prompt for
// queen_growth (go_hive/docs/e2e-assets/prompt-catalog/system-prompt.python.txt,
// frozen into testdata because a Go test cannot import the reference package).
// go_hive/scripts/gen_queen_prompt_sections.py re-derives that prompt from the
// upstream modules, asserts it equals the live capture, and writes this file —
// so a drift on either side fails the generator, and a drift in Go's
// composition fails here.
func readQueenPromptGolden(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "prompt", "queen_growth.independent.python.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	return string(data)
}

func firstDifference(got, want string) string {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	index := 0
	for index < limit && got[index] == want[index] {
		index++
	}
	start := index - 60
	if start < 0 {
		start = 0
	}
	gotEnd, wantEnd := index+120, index+120
	if gotEnd > len(got) {
		gotEnd = len(got)
	}
	if wantEnd > len(want) {
		wantEnd = len(want)
	}
	return "\n  offset " + strconv.Itoa(index) +
		"\n  got:  " + strconv.Quote(got[start:gotEnd]) +
		"\n  want: " + strconv.Quote(want[start:wantEnd])
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func TestQueenStaticPromptMatchesPythonReference(t *testing.T) {
	home := t.TempDir()
	deps := ChatRuntimeDeps{
		HomeRoot: home,
		Phase:    func() string { return "independent" },
	}
	got := chatQueenStaticPrompt(deps, "queen_growth")
	want := readQueenPromptGolden(t)
	if got != want {
		t.Fatalf("queen static prompt differs from the Python capture: got %d chars, want %d%s",
			len(got), len(want), firstDifference(got, want))
	}
	// The capture is the whole first-turn prompt: identity, phase base prompt,
	// credentials block. Spot-check the seams so a silent truncation fails
	// loudly even if the golden were regenerated from the same mistake.
	for _, marker := range []string{
		"<core_identity>",
		"Before every response, internally calibrate",
		"# Tools",
		"# System Rules",
		"## Your Memory",
		"# Credentials",
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("first-turn prompt is missing %q", marker)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("first-turn prompt has a trailing newline; the reference does not")
	}
}

// The colony phase gets the colony base prompt (role, tool docs including the
// delegation loop, colony operating rules) ahead of the shared rules.
//
// Parity: framework/server/queen_orchestrator.py::create_queen
func TestQueenStaticPromptUsesThePhaseBase(t *testing.T) {
	home := t.TempDir()
	independent := chatQueenStaticPrompt(ChatRuntimeDeps{HomeRoot: home, Phase: func() string { return "independent" }}, "queen_growth")
	colony := chatQueenStaticPrompt(ChatRuntimeDeps{HomeRoot: home, Phase: func() string { return "colony" }}, "queen_growth")

	if independent == colony {
		t.Fatal("colony and independent rendered the same prompt")
	}
	if !strings.Contains(independent, "You are in INDEPENDENT mode.") {
		t.Error("independent prompt is missing its role section")
	}
	if !strings.Contains(colony, "You are in COLONY mode.") {
		t.Error("colony prompt is missing its role section")
	}
	// Both share the identity prefix and the credentials block.
	for name, prompt := range map[string]string{"independent": independent, "colony": colony} {
		if !strings.HasPrefix(prompt, "<core_identity>") {
			t.Errorf("%s prompt does not open with the identity", name)
		}
		if !strings.Contains(prompt, "# Credentials") {
			t.Errorf("%s prompt is missing the credentials block", name)
		}
		if strings.Contains(prompt, "--- Pre-Activated Skill:") {
			t.Errorf("%s prompt carries a pre-activated skill body", name)
		}
	}
}

// A missing phase provider (an embedded host, a test) must not leave the prompt
// without a base: it defaults to the independent phase.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt (else-branch)
func TestQueenStaticPromptDefaultsToIndependentPhase(t *testing.T) {
	home := t.TempDir()
	got := chatQueenStaticPrompt(ChatRuntimeDeps{HomeRoot: home}, "queen_growth")
	want := chatQueenStaticPrompt(ChatRuntimeDeps{HomeRoot: home, Phase: func() string { return "independent" }}, "queen_growth")
	if got != want {
		t.Fatal("absent phase provider did not default to independent")
	}
}

// The session the probe ran against: a queen chat session with browser_ and
// terminal_ tools available. Before the queen walked its own static-prompt
// path, the generic builder appended the whole hive.browser-automation and
// hive.terminal-tools-foundations bodies (~60 KB) to the system prefix; upstream
// never sends them, since the queen reads those skills from the catalog on
// demand.
//
// Parity: framework/skills/tool_gating.py::augment_catalog_for_tools (not run
// on the queen path upstream)
func TestChatRuntimeQueenPromptSkipsToolGatedSkillBodies(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	execute := func(_ context.Context, _ string, _ map[string]any) (string, bool, error) {
		return "", false, nil
	}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      root,
		SessionID: "session_prompt_golden",
		QueenName: "queen_growth",
		HomeRoot:  home,
		Tools: []llm.ToolDef{
			{Name: "browser_interact", Description: "drive the browser"},
			{Name: "terminal_exec", Description: "run a command"},
		},
		Execute: execute,
		Phase:   func() string { return "independent" },
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	prompt := runtime.conv.SystemPrompt()
	if strings.Contains(prompt, "--- Pre-Activated Skill:") {
		t.Error("queen system prompt carries tool-gated pre-activated skill blocks")
	}
	for _, body := range []string{"# GCU Browser Automation", "# Terminal Tools Foundations"} {
		if strings.Contains(prompt, body) {
			t.Errorf("queen system prompt carries the %q skill body", body)
		}
	}
	// The guidance that must survive: the tool sections and the pointer to the
	// browser skill protocol she is expected to read.
	for _, marker := range []string{"# Tools", "browser-automation skill protocol", "# System Rules", "# Credentials"} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("queen system prompt is missing %q", marker)
		}
	}
}

// A queen with connected accounts gets the compact provider summary, not the
// per-account dump.
//
// Parity: framework/orchestrator/prompting.py::build_credentials_summary
func TestQueenStaticPromptRendersConnectedCredentials(t *testing.T) {
	deps := ChatRuntimeDeps{
		HomeRoot: t.TempDir(),
		Phase:    func() string { return "independent" },
		Credentials: func() []map[string]any {
			return []map[string]any{
				{"provider": "google", "alias": "work"},
				{"provider": "google", "alias": "personal"},
				{"provider": "notion", "alias": "default"},
			}
		},
	}
	got := chatQueenStaticPrompt(deps, "queen_growth")
	if !strings.Contains(got, "# Credentials\nConnected: google (2), notion.\n") {
		t.Fatal("connected credentials block missing from the queen prompt")
	}
	if strings.Contains(got, "No credentials are connected yet") {
		t.Error("prompt renders both the connected and the empty credentials text")
	}
}
