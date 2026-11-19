package soul

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sedimentTestConfig(limit int) *SoulConfig {
	return &SoulConfig{
		QueenID: "queen_a",
		Mode:    "auto",
		Sections: []SoulSection{
			{Key: "now", Title: "Now", Limit: limit, Evidence: 1},
			{Key: "people", Title: "Me and them", Limit: 800, Evidence: 2},
		},
		Header: DefaultSoulHeader,
	}
}

func sedimentTestQueens(t *testing.T, queens ...string) string {
	t.Helper()
	queensDir := t.TempDir()
	for _, queenID := range queens {
		if err := os.MkdirAll(filepath.Join(queensDir, queenID), 0o755); err != nil {
			t.Fatalf("create queen dir: %v", err)
		}
	}
	return queensDir
}

func sedimentTestNow(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)
}

func readSoulFile(t *testing.T, queensDir, queenID string) *Soul {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(SoulDir(queensDir, queenID), SoulFile))
	if err != nil {
		t.Fatalf("read soul: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode soul: %v", err)
	}
	return SoulFromJSON(value)
}

func soulItemTexts(soul *Soul, section string) []string {
	texts := make([]string, 0)
	for _, item := range soul.Items(section) {
		texts = append(texts, item.Text)
	}
	return texts
}

// Parity-exempt: the sediment has no upstream counterpart — upstream writes a
// soul from its own daily chat-reading reflection, not from memory reflection.
func TestSedimentWritesConclusionsAsNewVersion(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	now := sedimentTestNow(t)

	result, err := Sediment(SedimentOptions{
		QueensDir: queensDir,
		QueenID:   "queen_a",
		Config:    sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{
			{Text: "prefers implementation-first answers", Source: "turn-learnings.md"},
			{Text: "keeps   her   drafts  short", Source: "style.md"},
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if !result.Written || result.Version != 1 || result.Section != "now" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Added) != 2 {
		t.Fatalf("added = %#v", result.Added)
	}

	stored := readSoulFile(t, queensDir, "queen_a")
	if stored.Version != 1 || stored.UpdatedAt == "" {
		t.Fatalf("soul = %#v", stored)
	}
	if _, err := time.Parse("2006-01-02T15:04:05-07:00", stored.UpdatedAt); err != nil {
		t.Fatalf("updated_at %q is not a timestamp: %v", stored.UpdatedAt, err)
	}
	items := stored.Items("now")
	if len(items) != 2 || items[0].Text != "prefers implementation-first answers" ||
		items[1].Text != "keeps her drafts short" || items[0].Date != "2026-11-19" {
		t.Fatalf("items = %#v", items)
	}

	// The version is archived and logged exactly like a hand edit.
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), "history", "v1.json")); err != nil {
		t.Fatalf("history/v1.json missing: %v", err)
	}
	logData, err := os.ReadFile(filepath.Join(SoulDir(queensDir, "queen_a"), "log.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logLine := strings.TrimSpace(string(logData))
	if !strings.Contains(logLine, `"author": "reflection"`) || !strings.Contains(logLine, "2 from memory reflection") {
		t.Fatalf("log = %q", logLine)
	}

	state := LoadSedimentState(queensDir, "queen_a")
	if state == nil || state.LastRun != "2026-11-19" || state.LastAdded != 2 || len(state.Written) != 2 {
		t.Fatalf("sediment state = %#v", state)
	}
}

// Parity-exempt: no upstream counterpart; upstream never re-adds an item it
// already holds, which is the invariant checked here.
func TestSedimentSkipsUnchangedConclusions(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	opts := SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{{Text: "prefers implementation-first answers"}},
		Now:         sedimentTestNow(t),
	}
	if _, err := Sediment(opts); err != nil {
		t.Fatalf("first Sediment: %v", err)
	}

	again, err := Sediment(opts)
	if err != nil {
		t.Fatalf("second Sediment: %v", err)
	}
	if again.Written || again.Duplicates != 1 || again.Version != 1 || len(again.Added) != 0 {
		t.Fatalf("second result = %#v", again)
	}
	stored := readSoulFile(t, queensDir, "queen_a")
	if stored.Version != 1 || len(stored.Items("now")) != 1 {
		t.Fatalf("unchanged conclusion rewrote the soul: %#v", stored)
	}
	history, err := os.ReadDir(filepath.Join(SoulDir(queensDir, "queen_a"), "history"))
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %v (err %v), want one version", history, err)
	}

	// A batch repeats itself: only the first copy is added.
	queensB := sedimentTestQueens(t, "queen_b")
	batch, err := Sediment(SedimentOptions{
		QueensDir: queensB,
		QueenID:   "queen_b",
		Config:    sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{
			{Text: "same thought"},
			{Text: "same   thought"},
		},
		Now: sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("batch Sediment: %v", err)
	}
	if len(batch.Added) != 1 || batch.Duplicates != 1 {
		t.Fatalf("batch result = %#v", batch)
	}
}

// Parity-exempt: no upstream counterpart; upstream has one soul dir per queen
// and no automatic writer that could cross over.
func TestSedimentKeepsQueensIsolated(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a", "queen_b")
	if _, err := Sediment(SedimentOptions{
		QueensDir: queensDir,
		QueenID:   "queen_a",
		Config:    sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{
			{Text: "queen a's conclusion"},
		},
		Now: sedimentTestNow(t),
	}); err != nil {
		t.Fatalf("Sediment queen_a: %v", err)
	}
	if _, err := os.Stat(SoulDir(queensDir, "queen_b")); !os.IsNotExist(err) {
		t.Fatalf("queen_b soul dir exists after queen_a's write: %v", err)
	}

	bCfg := sedimentTestConfig(300)
	bCfg.QueenID = "queen_b"
	if _, err := Sediment(SedimentOptions{
		QueensDir: queensDir,
		QueenID:   "queen_b",
		Config:    bCfg,
		Conclusions: []SedimentConclusion{
			{Text: "queen b's conclusion"},
		},
		Now: sedimentTestNow(t),
	}); err != nil {
		t.Fatalf("Sediment queen_b: %v", err)
	}
	if texts := soulItemTexts(readSoulFile(t, queensDir, "queen_a"), "now"); len(texts) != 1 || texts[0] != "queen a's conclusion" {
		t.Fatalf("queen_a items = %v", texts)
	}
	if texts := soulItemTexts(readSoulFile(t, queensDir, "queen_b"), "now"); len(texts) != 1 || texts[0] != "queen b's conclusion" {
		t.Fatalf("queen_b items = %v", texts)
	}

	entries, err := os.ReadDir(queensDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("queens dir = %v (err %v), want exactly the two queens", entries, err)
	}
}

// Parity-exempt: no upstream counterpart; the guard keeps an automatic writer
// inside queens/<id>/soul, which upstream's fixed soul_dir also never leaves.
func TestSedimentRejectsInvalidQueenIDs(t *testing.T) {
	parent := t.TempDir()
	queensDir := filepath.Join(parent, "queens")
	if err := os.MkdirAll(filepath.Join(queensDir, "queen_a"), 0o755); err != nil {
		t.Fatalf("create queen dir: %v", err)
	}
	valid := sedimentTestConfig(300)
	for _, queenID := range []string{"", ".", "..", "../queen_b", "queen_b/../queen_a", `..\queen_b`, "/tmp/queen_b", ".hidden"} {
		_, err := Sediment(SedimentOptions{
			QueensDir:   queensDir,
			QueenID:     queenID,
			Config:      valid,
			Conclusions: []SedimentConclusion{{Text: "escape"}},
			Now:         sedimentTestNow(t),
		})
		if !errors.Is(err, ErrInvalidQueenID) {
			t.Fatalf("queen id %q: err = %v, want ErrInvalidQueenID", queenID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "queen_b")); !os.IsNotExist(err) {
		t.Fatalf("a rejected id created queens/../queen_b: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queensDir, "queen_a", "soul")); !os.IsNotExist(err) {
		t.Fatalf("a rejected id wrote into queen_a: %v", err)
	}

	// An id that is well formed but names no queen must not invent one.
	if _, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_missing",
		Config:      valid,
		Conclusions: []SedimentConclusion{{Text: "invent a queen"}},
		Now:         sedimentTestNow(t),
	}); !errors.Is(err, ErrUnknownQueen) {
		t.Fatalf("unknown queen err = %v, want ErrUnknownQueen", err)
	}
	if _, err := os.Stat(filepath.Join(queensDir, "queen_missing")); !os.IsNotExist(err) {
		t.Fatalf("unknown queen directory was created: %v", err)
	}
}

// Parity-exempt: no upstream counterpart; upstream refuses an over-limit edit
// (add_item returns an ERROR), while an automatic writer must bound itself.
func TestSedimentDropsOnlyItemsItWroteWhenSectionFills(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	cfg := sedimentTestConfig(40)
	if _, err := Save(queensDir, cfg, "queen_a",
		map[string][]Item{"now": {{Text: "human note stays", Date: "2026-11-01"}}},
		"owner", "edited by hand", nil); err != nil {
		t.Fatalf("seed human soul: %v", err)
	}

	sediment := func(text string) SedimentResult {
		t.Helper()
		result, err := Sediment(SedimentOptions{
			QueensDir:   queensDir,
			QueenID:     "queen_a",
			Config:      cfg,
			Conclusions: []SedimentConclusion{{Text: text}},
			Now:         sedimentTestNow(t),
		})
		if err != nil {
			t.Fatalf("Sediment(%q): %v", text, err)
		}
		return result
	}

	for _, text := range []string{"bot one", "bot two", "bot three"} {
		if result := sediment(text); !result.Written {
			t.Fatalf("Sediment(%q) did not write: %#v", text, result)
		}
	}
	result := sediment("bot four")
	if !result.Written || len(result.Evicted) != 1 || result.Evicted[0].Text != "bot one" {
		t.Fatalf("filling result = %#v", result)
	}
	texts := soulItemTexts(readSoulFile(t, queensDir, "queen_a"), "now")
	if len(texts) == 0 || texts[0] != "human note stays" {
		t.Fatalf("human item was touched: %v", texts)
	}
	for _, text := range texts {
		if text == "bot one" {
			t.Fatalf("oldest sedimented item was not dropped: %v", texts)
		}
	}
	if texts[len(texts)-1] != "bot four" {
		t.Fatalf("newest conclusion missing: %v", texts)
	}

	// Nothing sedimented is left to evict, so a conclusion that cannot fit is
	// skipped instead of overwriting the human item.
	fullDir := sedimentTestQueens(t, "queen_b")
	fullCfg := sedimentTestConfig(20)
	if _, err := Save(fullDir, fullCfg, "queen_b",
		map[string][]Item{"now": {{Text: "human note stays"}}},
		"owner", "edited by hand", nil); err != nil {
		t.Fatalf("seed full soul: %v", err)
	}
	skipped, err := Sediment(SedimentOptions{
		QueensDir:   fullDir,
		QueenID:     "queen_b",
		Config:      fullCfg,
		Conclusions: []SedimentConclusion{{Text: "does not fit"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("Sediment into a full section: %v", err)
	}
	if skipped.Written || skipped.Dropped != 1 {
		t.Fatalf("full-section result = %#v", skipped)
	}
	if stored := readSoulFile(t, fullDir, "queen_b"); stored.Version != 1 || len(stored.Items("now")) != 1 {
		t.Fatalf("full section was rewritten: %#v", stored)
	}
}

// Parity-exempt: no upstream counterpart; the soul is bounded, so an
// automatic writer clips what it contributes.
func TestSedimentCapsConclusionLength(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	long := strings.Repeat("长", 600)
	result, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{{Text: long}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if !result.Written || len(result.Added) != 1 {
		t.Fatalf("result = %#v", result)
	}
	items := readSoulFile(t, queensDir, "queen_a").Items("now")
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	runes := []rune(items[0].Text)
	if len(runes) != MaxSedimentItemRunes || runes[len(runes)-1] != '…' {
		t.Fatalf("clipped length = %d, tail = %q", len(runes), string(runes[len(runes)-1]))
	}
}
