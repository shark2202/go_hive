package queen

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The goldens under testdata/prompt are rendered by the Python reference
// (go_hive/scripts/gen_queen_prompt_sections.py imports the upstream node
// module and writes them). Comparing Go's composition against them is what
// makes the port text-exact rather than "looks the same": a re-typed paragraph
// or a dropped blank line fails here.
func readPromptGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", "prompt", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	return string(data)
}

// firstPromptDifference reports the offset and neighbourhood of the first
// differing character, so a golden failure is a diff instead of two 20 KB
// blobs.
func firstPromptDifference(got, want string) string {
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
		"\n  got:  " + quotePrompt(got[start:gotEnd]) +
		"\n  want: " + quotePrompt(want[start:wantEnd])
}

func quotePrompt(text string) string {
	return strconv.Quote(text)
}

// Parity: framework/server/queen_orchestrator.py::create_queen (phase composition)
func TestPhasePromptMatchesUpstreamGolden(t *testing.T) {
	cases := []struct {
		name  string
		phase string
		file  string
	}{
		{"independent", PhaseIndependent, "independent.txt"},
		{"colony", PhaseColony, "colony.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := readPromptGolden(t, tc.file)
			if got := PhasePrompt(tc.phase); got != want {
				t.Fatalf("PhasePrompt(%q) differs from the upstream-rendered golden: got %d chars, want %d\nfirst difference: %s",
					tc.phase, len(got), len(want), firstPromptDifference(got, want))
			}
		})
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func TestPhasePromptFallsBackToIndependent(t *testing.T) {
	independent := IndependentPhasePrompt()
	for _, phase := range []string{"", "independent", "working", "reviewing", "incubating", "COLONY "} {
		if got := PhasePrompt(phase); got != independent {
			t.Errorf("PhasePrompt(%q) did not fall back to the independent base", phase)
		}
	}
	if PhasePrompt(PhaseColony) == independent {
		t.Fatal("colony phase rendered the independent base")
	}
}

// The segment order is the model-visible structure: character core first, then
// the phase role, then tool docs, then the shared rules. Reordering sections
// changes what the model attends to, so pin the order (not just the bytes: the
// golden above already covers bytes, this says why they are in that order).
//
// Parity: framework/server/queen_orchestrator.py::create_queen (segment order of prompt_independent)
func TestIndependentPhasePromptSectionOrder(t *testing.T) {
	prompt := IndependentPhasePrompt()
	markers := []string{
		"Before every response, internally calibrate",
		"You are in INDEPENDENT mode.",
		"# Tools",
		"## Browser Automation (hive-browser CLI)",
		"# System Rules",
		"<ask_user_tool>",
		"<task_plan_tool>",
		"<pivot>",
		"<file_creation>",
		"<sharing_files>",
		"<draft_outbound_message>",
		"## Communicating with the user",
		"## Your Memory",
		"## Independent execution",
		"<ask_user_for_colony_scope>",
	}
	last := 0
	for _, marker := range markers {
		offset := strings.Index(prompt[last:], marker)
		if offset < 0 {
			t.Fatalf("independent prompt is missing %q (after offset %d)", marker, last)
		}
		last += offset
	}
}

// Parity: framework/server/queen_orchestrator.py::create_queen (segment order of prompt_colony)
func TestColonyPhasePromptSectionOrder(t *testing.T) {
	prompt := ColonyPhasePrompt()
	markers := []string{
		"Before every response, internally calibrate",
		"You are in COLONY mode.",
		"# Tools (COLONY mode)",
		"## Credentials (hive-cred CLI)",
		"## Delegation loop",
		"## Colony operating rules",
		"# System Rules",
		"## Your Memory",
	}
	last := 0
	for _, marker := range markers {
		offset := strings.Index(prompt[last:], marker)
		if offset < 0 {
			t.Fatalf("colony prompt is missing %q (after offset %d)", marker, last)
		}
		last += offset
	}
}

// The queen path must not carry whole tool-gated skill bodies: upstream reads
// those skills from the catalog on demand, and the bodies are ~60 KB of cached
// prefix the reference never sends.
//
// Parity: framework/skills/tool_gating.py::augment_catalog_for_tools (never run on the queen path)
func TestPhasePromptCarriesNoPreActivatedSkillBodies(t *testing.T) {
	for name, prompt := range map[string]string{
		"independent": IndependentPhasePrompt(),
		"colony":      ColonyPhasePrompt(),
	} {
		if strings.Contains(prompt, "--- Pre-Activated Skill:") {
			t.Errorf("%s prompt contains a pre-activated skill block", name)
		}
		for _, body := range []string{"# GCU Browser Automation", "# Terminal Tools Foundations"} {
			if strings.Contains(prompt, body) {
				t.Errorf("%s prompt contains the %q skill body", name, body)
			}
		}
	}
	// The pointer to the skill protocol must stay: that is how upstream keeps
	// browser guidance in the prompt without copying the body.
	if !strings.Contains(IndependentPhasePrompt(), "browser-automation skill protocol") {
		t.Error("independent prompt lost the browser-automation protocol pointer")
	}
}

// Parity: framework/orchestrator/prompting.py::build_credentials_summary
func TestCredentialsSummaryBlockMatchesUpstreamGolden(t *testing.T) {
	sample := []map[string]any{
		{"provider": "google", "alias": "work", "credential_id": "google"},
		{"provider": "google", "alias": "personal", "credential_id": "google"},
		{"provider": "notion", "alias": "default", "credential_id": "notion"},
		{"alias": "no-provider", "credential_id": "slack"},
	}
	cases := []struct {
		name     string
		accounts []map[string]any
		file     string
	}{
		{"none", nil, "credentials.none.txt"},
		{"some", sample, "credentials.some.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := readPromptGolden(t, tc.file)
			if got := CredentialsSummaryBlock(tc.accounts); got != want {
				t.Fatalf("credentials block differs from the upstream-rendered golden: got %q, want %q", got, want)
			}
		})
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func TestStaticPromptComposition(t *testing.T) {
	base := IndependentPhasePrompt()

	// Identity, base and credentials join with a blank line; empty optional
	// segments disappear instead of leaving a heading with no content.
	got := StaticPrompt(StaticPromptOptions{Identity: "ID", Base: base, Credentials: "CREDS"})
	if want := "ID\n\n" + base + "\n\nCREDS"; got != want {
		t.Fatalf("identity + base + credentials composition differs%s", firstPromptDifference(got, want))
	}

	// Soul rides directly under the identity, both before the base.
	got = StaticPrompt(StaticPromptOptions{Identity: "ID", Soul: "SOUL", Base: "BASE"})
	if want := "ID\n\nSOUL\n\nBASE"; got != want {
		t.Fatalf("soul placement = %q, want %q", got, want)
	}

	// mcp_cli and protocols come after credentials, in that order.
	got = StaticPrompt(StaticPromptOptions{Identity: "ID", Base: "BASE", Credentials: "CREDS", MCPCLI: "MCP", Protocols: "PROTO"})
	if want := "ID\n\nBASE\n\nCREDS\n\nMCP\n\nPROTO"; got != want {
		t.Fatalf("full composition = %q, want %q", got, want)
	}

	// The phase base is never skipped: a prompt with no identity still opens
	// with the character core, exactly as upstream's unconditional append.
	if got := StaticPrompt(StaticPromptOptions{Base: "BASE"}); got != "BASE" {
		t.Fatalf("base-only composition = %q", got)
	}
}
