package longmemeval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "fixture_cleaned.json")
}

// stageFixture copies the small cleaned-release fixture under a data
// directory with the official file name.
func stageFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rawFileName(VariantS)), data, 0o644); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	return dir
}

// Parity-exempt: Go-side coverage for tests/e2e/longmemeval.py::ensure_split
// (the Python module has no unit test for the splitter).
func TestPrepareSplitsFixtureAndReloads(t *testing.T) {
	dataDir := stageFixture(t)
	splitDir, err := Prepare(context.Background(), SourceConfig{
		Variant:  VariantS,
		DataPath: dataDir,
		Download: false,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if splitDir != filepath.Join(dataDir, "s_cleaned") {
		t.Fatalf("split dir = %q", splitDir)
	}
	index, err := LoadIndex(splitDir)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(index) != 3 {
		t.Fatalf("index = %d entries, want 3", len(index))
	}
	if index[0].Sessions != 2 {
		t.Fatalf("first entry sessions = %d, want 2", index[0].Sessions)
	}
	item, err := LoadItem(splitDir, "fixture_commute_01")
	if err != nil {
		t.Fatalf("LoadItem: %v", err)
	}
	if item.QuestionType != "single-session-user" || !strings.Contains(item.Answer.Text(), "45 minutes") {
		t.Fatalf("item = %+v", item)
	}
	// A second Prepare trusts the existing index instead of re-splitting.
	again, err := Prepare(context.Background(), SourceConfig{Variant: VariantS, DataPath: dataDir, Download: false})
	if err != nil || again != splitDir {
		t.Fatalf("second Prepare = %q, %v", again, err)
	}
}

// Parity-exempt: verifies the Go splitter rejects a truncated release instead
// of silently loading a smaller dataset (upstream raises the same error).
func TestPrepareRejectsTruncatedArray(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, rawFileName(VariantS))
	if err := os.WriteFile(raw, []byte(`[{"question_id": "half"`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(context.Background(), SourceConfig{Variant: VariantS, DataPath: raw, CacheDir: t.TempDir(), Download: false})
	if err == nil || !strings.Contains(err.Error(), "truncated JSON array") {
		t.Fatalf("err = %v, want truncated-array error", err)
	}
}

// Parity-exempt: path-traversal guard for question ids used as file names.
func TestValidateItemRejectsUnsafeQuestionID(t *testing.T) {
	item := Item{
		QuestionID:   "../escape",
		QuestionType: "single-session-user",
		Question:     "q",
		QuestionDate: "2023/05/20 (Sat) 10:00",
	}
	if err := ValidateItem(&item); err == nil {
		t.Fatal("ValidateItem accepted a traversing question_id")
	}
}

// Parity-exempt: haystack arrays must line up before seeding (upstream's zip
// would silently truncate).
func TestValidateItemRejectsMismatchedHaystack(t *testing.T) {
	item := Item{
		QuestionID:         "fixture_x",
		QuestionType:       "single-session-user",
		Question:           "q",
		QuestionDate:       "2023/05/20 (Sat) 10:00",
		HaystackSessionIDs: []string{"s1", "s2"},
		HaystackDates:      []string{"2023/05/18 (Thu) 08:12"},
		HaystackSessions:   [][]Turn{{}, {}},
	}
	if err := ValidateItem(&item); err == nil || !strings.Contains(err.Error(), "haystack arrays disagree") {
		t.Fatalf("err = %v", err)
	}
}

// Parity-exempt: Go unit coverage for the weekday-drop in
// tests/e2e/longmemeval.py::parse_date.
func TestParseDateDropsWeekday(t *testing.T) {
	when, err := ParseDate("2023/05/30 (Tue) 23:40")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if when.Year() != 2023 || when.Month() != 5 || when.Day() != 30 || when.Hour() != 23 || when.Minute() != 40 {
		t.Fatalf("when = %s", when)
	}
	if _, err := ParseDate("not a date"); err == nil {
		t.Fatal("ParseDate accepted garbage")
	}
}

// Parity-exempt: the official question ids are slugs; the fixture must round
// trip through JSON unchanged.
func TestFixtureRoundTripsThroughJSON(t *testing.T) {
	items, err := ReadItems(fixturePath(t))
	if err != nil {
		t.Fatalf("ReadItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded Item
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.QuestionID != items[0].QuestionID || len(decoded.HaystackSessions) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// Parity-exempt: the 2025 cleaned release stores 32 of S's 500 answers as bare
// JSON numbers (e.g. "3"). Python's json.loads keeps the int and only formats
// it; the Go Item must accept that shape instead of aborting the whole split.
func TestAnswerAcceptsNumericJSONAndKeepsType(t *testing.T) {
	raw := `[{"question_id":"numeric_answer","question_type":"knowledge-update",
		"question":"How many?","answer":3,"question_date":"2023/05/30 (Tue) 23:40",
		"haystack_session_ids":["s1"],"haystack_dates":["2023/05/29 (Mon) 10:00"],
		"haystack_sessions":[[{"role":"user","content":"three"}]],"answer_session_ids":["s1"]}]`
	dir := t.TempDir()
	path := filepath.Join(dir, rawFileName(VariantS))
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	splitDir, err := Prepare(context.Background(), SourceConfig{Variant: VariantS, DataPath: dir, Download: false})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	item, err := LoadItem(splitDir, "numeric_answer")
	if err != nil {
		t.Fatalf("LoadItem: %v", err)
	}
	if item.Answer.Text() != "3" {
		t.Fatalf("Answer.Text() = %q, want %q", item.Answer.Text(), "3")
	}
	if got, ok := item.Answer.Value().(float64); !ok || got != 3 {
		t.Fatalf("Answer.Value() = %#v, want the numeric 3", item.Answer.Value())
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"answer":3`) {
		t.Fatalf("split file lost the numeric type: %s", encoded)
	}
}

// Parity-exempt: the abstention marker is the official "_abs" suffix rule.
func TestIsAbstention(t *testing.T) {
	if !IsAbstention("fixture_abs_01_abs") {
		t.Fatal("abstention id not recognized")
	}
	if IsAbstention("fixture_commute_01") {
		t.Fatal("plain id misread as abstention")
	}
}
