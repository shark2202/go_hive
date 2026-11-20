package longmemeval

import (
	"strings"
	"testing"
)

// Parity-exempt: Go-side coverage for tests/e2e/longmemeval.py::reader_prompt
// (session selection and ordering for the oracle condition).
func TestReaderPromptConditions(t *testing.T) {
	items, err := ReadItems(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	oracle := ReaderPrompt(item, true)
	full := ReaderPrompt(item, false)
	if strings.Count(oracle, "### Session") != 1 {
		t.Fatalf("oracle prompt sessions = %d, want 1", strings.Count(oracle, "### Session"))
	}
	if strings.Count(full, "### Session") != len(item.HaystackSessionIDs) {
		t.Fatalf("full-history prompt sessions = %d", strings.Count(full, "### Session"))
	}
	if !strings.Contains(full, "Session Date: 2023/05/19 (Fri) 12:40") {
		t.Fatalf("full-history prompt missing the second session:\n%s", full)
	}
	if !strings.Contains(oracle, `{"role": "user"`) {
		t.Fatalf("oracle prompt does not embed Python-style JSON turns:\n%s", oracle)
	}
	if strings.Contains(oracle, "{}") || !strings.Contains(oracle, "Current Date: "+item.QuestionDate) {
		t.Fatalf("oracle prompt not fully rendered:\n%s", oracle)
	}
	// Sessions sort by their date string, as the official script does.
	first := strings.Index(full, "2023/05/18 (Thu) 08:12")
	second := strings.Index(full, "2023/05/19 (Fri) 12:40")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("session order wrong: %d > %d", first, second)
	}
}

// Parity-exempt: pins the ASCII escaping Python's json.dumps applies to the
// embedded history (needed for comparable reader prompts).
func TestPythonJSONEscaping(t *testing.T) {
	got := pythonJSONString("café 😀 \"x\"\n")
	want := `"caf\u00e9 \ud83d\ude00 \"x\"\n"`
	if got != want {
		t.Fatalf("pythonJSONString = %s, want %s", got, want)
	}
	turns := []Turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ok"}}
	encoded := pythonJSON(turns)
	if encoded != `[{"role": "user", "content": "hi"}, {"role": "assistant", "content": "ok"}]` {
		t.Fatalf("pythonJSON = %s", encoded)
	}
}
