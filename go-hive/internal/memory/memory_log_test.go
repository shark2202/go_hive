package memory

// Parity: core/tests/test_memory_log.py — every change to a memory file says
// who wrote it, why, how fresh it is, and keeps its text before and after.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const logNote = "---\nname: Tea\ndescription: What Ava drinks\ntype: preference\n---\n\nAva drinks oolong.\n"

func fixedDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.Local)
}

func frontmatterFields(t *testing.T, text string) map[string]string {
	t.Helper()
	return ParseFrontmatter(text)
}

// Parity: core/tests/test_memory_log.py::test_a_written_file_says_who_wrote_it_and_when
func TestAWrittenFileSaysWhoWroteItAndWhen(t *testing.T) {
	dir := t.TempDir()
	text, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tea.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != text {
		t.Fatalf("file = %q, want %q", data, text)
	}
	got := frontmatterFields(t, text)
	want := map[string]string{
		"name":        "Tea",
		"description": "What Ava drinks",
		"type":        "preference",
		"author":      "reflection",
		"learned":     "2026-03-02",
		"confirmed":   "2026-03-02",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("frontmatter[%s] = %q, want %q (all %#v)", key, got[key], value, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("frontmatter = %#v, want %d keys", got, len(want))
	}
	if !strings.HasSuffix(text, "\n\nAva drinks oolong.\n") {
		t.Fatalf("text = %q", text)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_rewrite_keeps_the_day_it_was_learned_and_moves_the_day_it_was_confirmed
func TestARewriteKeepsLearnedAndMovesConfirmed(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)}); err != nil {
		t.Fatal(err)
	}
	text, err := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "pu-erh", 1), WriteOptions{Author: "queen", Now: fixedDay(2026, 4, 5)})
	if err != nil {
		t.Fatal(err)
	}
	fields := frontmatterFields(t, text)
	if fields["author"] != "queen" || fields["learned"] != "2026-03-02" || fields["confirmed"] != "2026-04-05" {
		t.Fatalf("author/learned/confirmed = %q/%q/%q", fields["author"], fields["learned"], fields["confirmed"])
	}
}

// Parity: core/tests/test_memory_log.py::test_tidying_a_file_does_not_make_it_look_newly_confirmed
func TestTidyingAFileDoesNotConfirmItAgain(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)}); err != nil {
		t.Fatal(err)
	}
	text, err := Write(dir, "tea.md", strings.Replace(logNote, "drinks", "likes", 1), WriteOptions{Author: "reflection", Reorganised: true, Now: fixedDay(2026, 6, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if day, ok := ConfirmedOn(text); !ok || day != "2026-03-02" {
		t.Fatalf("confirmed = %q (%v), want 2026-03-02", day, ok)
	}
}

// Parity: core/tests/test_memory_log.py::test_she_cannot_overwrite_or_retire_what_the_owner_wrote
func TestSheCannotOverwriteOrRetireWhatTheOwnerWrote(t *testing.T) {
	dir := t.TempDir()
	mine, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "owner", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, author := range []string{"queen", "reflection"} {
		_, writeErr := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "coffee", 1), WriteOptions{Author: author})
		var ownerWritten *OwnerWritten
		if !errors.As(writeErr, &ownerWritten) {
			t.Fatalf("%s write error = %v, want OwnerWritten", author, writeErr)
		}
		if !strings.Contains(ownerWritten.Message, "tea.md was written by the owner") {
			t.Fatalf("message = %q", ownerWritten.Message)
		}
		if retireErr := Retire(dir, "tea.md", author, "", time.Time{}); !errors.As(retireErr, &ownerWritten) {
			t.Fatalf("%s retire error = %v, want OwnerWritten", author, retireErr)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "tea.md"))
	if string(data) != mine {
		t.Fatalf("owner file changed: %q", data)
	}
	name := "tea.md"
	if entries := History(dir, &name); len(entries) != 1 {
		t.Fatalf("history = %d entries, want 1", len(entries))
	}
}

// Parity: core/tests/test_memory_log.py::test_a_file_from_before_authors_were_recorded_is_hers_to_update_until_the_owner_edits_it
func TestAFileFromBeforeAuthorsIsHersUntilTheOwnerEditsIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tea.md"), []byte(logNote), 0o644); err != nil {
		t.Fatal(err)
	}
	if author := AuthorOf(logNote); author != "" {
		t.Fatalf("author = %q, want nobody", author)
	}
	if _, err := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "pu-erh", 1), WriteOptions{Author: "reflection"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "sencha", 1), WriteOptions{Author: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection"}); !IsOwnerWritten(err) {
		t.Fatalf("reflection write = %v, want OwnerWritten", err)
	}
	name := "tea.md"
	entries := History(dir, &name)
	if len(entries) != 2 || entries[0].Before == nil || *entries[0].Before != logNote {
		t.Fatalf("first change = %#v", entries)
	}
}

// Parity: core/tests/test_memory_log.py::test_the_owner_can_change_what_she_wrote
func TestTheOwnerCanChangeWhatSheWrote(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "queen"}); err != nil {
		t.Fatal(err)
	}
	text, err := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "sencha", 1), WriteOptions{Author: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if author := AuthorOf(text); author != "owner" {
		t.Fatalf("author = %q", author)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_change_that_does_not_claim_the_file_leaves_its_author
func TestAChangeThatDoesNotClaimTheFileLeavesItsAuthor(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "user-profile.md", logNote, WriteOptions{Author: "reflection"}); err != nil {
		t.Fatal(err)
	}
	text, err := Write(dir, "user-profile.md", strings.Replace(logNote, "oolong", "sencha", 1), WriteOptions{Author: "owner", KeepAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if author := AuthorOf(text); author != "reflection" {
		t.Fatalf("author = %q, want reflection", author)
	}
	entries := History(dir, nil)
	if len(entries) == 0 || entries[len(entries)-1].Author != "owner" {
		t.Fatalf("last log author = %#v", entries[len(entries)-1])
	}
}

// Parity: core/tests/test_memory_log.py::test_the_author_and_dates_a_file_claims_for_itself_are_overruled
func TestForgedProvenanceIsOverruled(t *testing.T) {
	dir := t.TempDir()
	forged := strings.Replace(logNote, "type: preference", "type: preference\nauthor: owner\nlearned: 2031-01-01\nconfirmed: 2031-01-01", 1)
	text, err := Write(dir, "tea.md", forged, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}
	fields := frontmatterFields(t, text)
	if fields["author"] != "reflection" || fields["learned"] != "2026-03-02" || fields["confirmed"] != "2026-03-02" {
		t.Fatalf("author/learned/confirmed = %q/%q/%q", fields["author"], fields["learned"], fields["confirmed"])
	}
}

// Parity: core/tests/test_memory_log.py::test_a_file_can_say_it_was_learned_earlier_than_it_was_written
func TestAFileCanSayItWasLearnedEarlier(t *testing.T) {
	dir := t.TempDir()
	earlier := strings.Replace(logNote, "type: preference", "type: preference\nlearned: 2025-12-24", 1)
	text, err := Write(dir, "tea.md", earlier, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if learned := frontmatterFields(t, text)["learned"]; learned != "2025-12-24" {
		t.Fatalf("learned = %q", learned)
	}
}

// Parity: core/tests/test_memory_log.py::test_text_without_frontmatter_gets_one
func TestTextWithoutFrontmatterGetsOne(t *testing.T) {
	dir := t.TempDir()
	text, err := Write(dir, "tea.md", "Ava drinks oolong.", WriteOptions{Author: "queen", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nauthor: queen\nlearned: 2026-03-02\nconfirmed: 2026-03-02\n---\n\nAva drinks oolong.\n"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_file_too_large_once_stamped_is_not_written
func TestAFileTooLargeOnceStampedIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", MaxFileSizeBytes-len(logNote))
	_, err := Write(dir, "tea.md", logNote+body, WriteOptions{Author: "reflection"})
	var tooLarge *TooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v, want TooLarge", err)
	}
	if !strings.Contains(tooLarge.Message, fmt.Sprintf("exceeds %d byte limit", MaxFileSizeBytes)) {
		t.Fatalf("message = %q", tooLarge.Message)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tea.md")); !os.IsNotExist(statErr) {
		t.Fatalf("file exists: %v", statErr)
	}
	if entries := History(dir, nil); len(entries) != 0 {
		t.Fatalf("history = %#v", entries)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_retired_file_leaves_the_folder_and_stays_in_the_log
func TestARetiredFileLeavesTheFolderAndStaysInTheLog(t *testing.T) {
	dir := t.TempDir()
	written, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Retire(dir, "tea.md", "reflection", "merged into drinks.md", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tea.md")); !os.IsNotExist(statErr) {
		t.Fatalf("file still exists: %v", statErr)
	}
	name := "tea.md"
	entries := History(dir, &name)
	last := entries[len(entries)-1]
	if last.Op != "retire" || last.Reason != "merged into drinks.md" || last.Before == nil || *last.Before != written || last.After != nil {
		t.Fatalf("last = %#v", last)
	}
}

// Parity: core/tests/test_memory_log.py::test_retiring_a_file_that_is_not_there_fails
func TestRetiringAMissingFileFails(t *testing.T) {
	if err := Retire(t.TempDir(), "tea.md", "reflection", "", time.Time{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want not-exist", err)
	}
}

// Parity: core/tests/test_memory_log.py::test_the_log_records_each_change_with_its_evidence
func TestTheLogRecordsEachChangeWithItsEvidence(t *testing.T) {
	dir := t.TempDir()
	sources := []map[string]string{{"quote": "I only drink oolong", "where": "session_a"}}
	now := time.Date(2026, 3, 2, 9, 30, 0, 0, time.Local)
	written, err := Write(dir, "tea.md", logNote, WriteOptions{
		Author: "queen", Reason: "she said so", Sources: sources, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "pets.md", logNote, WriteOptions{Author: "queen", Now: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	name := "tea.md"
	entries := History(dir, &name)
	if len(entries) != 1 {
		t.Fatalf("history = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.N != 0 || entry.Op != "write" || entry.Author != "queen" || entry.Reason != "she said so" || entry.Before != nil {
		t.Fatalf("entry = %#v", entry)
	}
	if !strings.HasPrefix(entry.At, "2026-03-02T09:30:00") {
		t.Fatalf("at = %q", entry.At)
	}
	if !strings.Contains(entry.At, "+") && !strings.Contains(entry.At, "-") {
		t.Fatalf("at has no numeric offset: %q", entry.At)
	}
	files := []string{}
	for _, item := range History(dir, nil) {
		files = append(files, item.File)
	}
	if strings.Join(files, ",") != "tea.md,pets.md" {
		t.Fatalf("files = %v", files)
	}

	// The line itself is byte-fixed: key order and Python's json.dumps
	// separators, so the reference's reader and ours see the same format.
	raw, err := os.ReadFile(filepath.Join(dir, LogFile))
	if err != nil {
		t.Fatal(err)
	}
	wantLine := `{"at": ` + pyJSONString(entry.At) +
		`, "op": "write", "file": "tea.md", "author": "queen", "reason": "she said so", ` +
		`"sources": [{"quote": "I only drink oolong", "where": "session_a"}], "before": null, "after": ` +
		pyJSONString(written) + "}"
	if gotLine := strings.Split(string(raw), "\n")[0]; gotLine != wantLine {
		t.Fatalf("log line =\n%q\nwant\n%q", gotLine, wantLine)
	}
}

// Parity-exempt: upstream's history() counts every line, including ones that
// fail to parse; this pins the n arithmetic that rollback depends on.
func TestHistoryCountsMalformedLinesInN(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"at": "2026-03-02T09:30:00+08:00", "op": "write", "file": "tea.md", "author": "queen", "reason": "", "sources": [], "before": null, "after": "x"}`,
		`not json`,
		`{"at": "2026-03-02T09:31:00+08:00", "op": "write", "file": "tea.md", "author": "reflection", "reason": "", "sources": [], "before": "x", "after": "y"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, LogFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "tea.md"
	entries := History(dir, &name)
	if len(entries) != 2 || entries[0].N != 0 || entries[1].N != 2 {
		t.Fatalf("entries = %#v", entries)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_retired_file_can_be_restored_as_it_was
func TestARetiredFileCanBeRestoredAsItWas(t *testing.T) {
	dir := t.TempDir()
	written, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection", Now: fixedDay(2026, 3, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if err := Retire(dir, "tea.md", "reflection", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	name := "tea.md"
	retirement := History(dir, &name)[0]
	restored, err := Restore(dir, "tea.md", retirement.N, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if restored != written {
		t.Fatalf("restored = %q, want %q", restored, written)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "tea.md"))
	if string(data) != written {
		t.Fatalf("file = %q", data)
	}
	entries := History(dir, &name)
	if entries[len(entries)-1].Op != "restore" {
		t.Fatalf("last op = %q", entries[len(entries)-1].Op)
	}
}

// Parity: core/tests/test_memory_log.py::test_a_file_can_be_put_back_to_an_earlier_version
func TestAFileCanBePutBackToAnEarlierVersion(t *testing.T) {
	dir := t.TempDir()
	first, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "tea.md", strings.Replace(logNote, "oolong", "coffee", 1), WriteOptions{Author: "reflection"}); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(dir, "tea.md", 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if restored != first {
		t.Fatalf("restored = %q, want %q", restored, first)
	}
}

// Parity: core/tests/test_memory_log.py::test_restoring_a_change_that_does_not_exist_fails
func TestRestoringAChangeThatDoesNotExistFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "tea.md", logNote, WriteOptions{Author: "reflection"}); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(dir, "tea.md", 7, time.Time{})
	var noChange *NoChange
	if !errors.As(err, &noChange) || noChange.Message != "tea.md has no change 7 to go back to" {
		t.Fatalf("error = %v", err)
	}
}

// Parity-exempt: upstream returns retired() newest-first; this pins the order
// and that restoring drops a file out of the list.
func TestRetiredListsNewestFirstAndRestoreDropsIt(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if _, err := Write(dir, name, logNote, WriteOptions{Author: "reflection"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Retire(dir, "a.md", "reflection", "gone a", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := Retire(dir, "b.md", "reflection", "gone b", time.Time{}); err != nil {
		t.Fatal(err)
	}
	gone := Retired(dir)
	if len(gone) != 2 || gone[0].File != "b.md" || gone[1].File != "a.md" {
		t.Fatalf("retired = %#v", gone)
	}
	if _, err := Restore(dir, "a.md", gone[1].N, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if remaining := Retired(dir); len(remaining) != 1 || remaining[0].File != "b.md" {
		t.Fatalf("retired after restore = %#v", remaining)
	}
}

// Parity-exempt: a seeded memory claims nobody, so her reflection can build on
// it (upstream core/tests/test_routes_memories.py::test_a_seeded_memory_is_nobodys_so_her_reflection_can_build_on_it).
func TestASeededMemoryIsNobodys(t *testing.T) {
	dir := t.TempDir()
	text, err := Write(dir, "company.md", logNote, WriteOptions{Author: "owner", Reason: "seeded", KeepAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := ParseFrontmatter(text)["author"]; present {
		t.Fatalf("seeded frontmatter has an author: %#v", ParseFrontmatter(text))
	}
	if _, err := Write(dir, "company.md", strings.Replace(logNote, "oolong", "coffee", 1), WriteOptions{Author: "reflection"}); err != nil {
		t.Fatalf("reflection could not build on a seed: %v", err)
	}
}
