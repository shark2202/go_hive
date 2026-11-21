package queen

import (
	"strings"
	"testing"
)

// Parity: core/framework/agents/queen/queen_profiles.py::format_queen_identity_prompt
func TestMinimalProfileRendersEmptyCoreTraits(t *testing.T) {
	prompt := FormatQueenIdentityPrompt(map[string]any{"name": "Queen Review"}, nil)
	want := "<core_identity>\nYou are a queen agent, your name is Queen Review, Senior Advisor.\n\n</core_identity>"
	if !strings.Contains(prompt, want) {
		t.Fatalf("minimal profile core_identity block is wrong:\n%s", prompt)
	}
	if strings.Contains(prompt, "Senior Advisor.\nNone") {
		t.Fatalf("a missing core_traits rendered as %q; upstream's profile.get(\"core_traits\", \"\") is an empty line:\n%s", "None", prompt)
	}
}

// Parity-exempt: pins the upstream semantics of a present-but-nil value
// (f"{None}" renders "None"), which is why the fix only changes the absent
// case.
func TestPresentCoreTraitsNoneRendersNone(t *testing.T) {
	prompt := FormatQueenIdentityPrompt(map[string]any{"name": "Queen X", "core_traits": nil}, nil)
	if !strings.Contains(prompt, "Senior Advisor.\nNone\n</core_identity>") {
		t.Fatalf("present-but-nil core_traits should render None like Python:\n%s", prompt)
	}
}
