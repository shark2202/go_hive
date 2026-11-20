package longmemeval

import (
	"sort"
	"strings"
	"testing"
)

func sampleIndex() []IndexEntry {
	shares := map[string]int{
		"single-session-user":       70,
		"single-session-assistant":  56,
		"single-session-preference": 30,
		"multi-session":             133,
		"temporal-reasoning":        133,
		"knowledge-update":          78,
	}
	types := make([]string, 0, len(shares))
	for t := range shares {
		types = append(types, t)
	}
	sort.Strings(types)
	var index []IndexEntry
	for _, t := range types {
		for i := 0; i < shares[t]; i++ {
			index = append(index, IndexEntry{
				QuestionID:   t + "_" + itoa(i),
				QuestionType: t,
				QuestionDate: "2023/05/20 (Sat) 10:00",
				Sessions:     1,
			})
		}
	}
	return index
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Parity-exempt: Go-side unit coverage for tests/e2e/longmemeval.py::select
// (no Python unit test exists for the sampler).
func TestSelectAllAndIDs(t *testing.T) {
	index := sampleIndex()
	all, err := Select(index, "all")
	if err != nil || len(all) != 500 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	ids, err := Select(index, "multi-session_1, knowledge-update_2")
	if err != nil {
		t.Fatalf("ids: %v", err)
	}
	if strings.Join(ids, ",") != "multi-session_1,knowledge-update_2" {
		t.Fatalf("ids = %v", ids)
	}
	if _, err := Select(index, "does-not-exist"); err == nil || !strings.Contains(err.Error(), "unknown LongMemEval question ids") {
		t.Fatalf("unknown-id err = %v", err)
	}
}

// Parity-exempt: stratified sample proportions (largest remainder) and
// determinism.
func TestSelectStratifiedSample(t *testing.T) {
	index := sampleIndex()
	first, err := Select(index, "12")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	second, err := Select(index, "12")
	if err != nil {
		t.Fatalf("Select again: %v", err)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("selection is not deterministic")
	}
	if len(first) != 12 {
		t.Fatalf("sample = %d ids, want 12", len(first))
	}
	counts := map[string]int{}
	known := map[string]string{}
	for _, entry := range index {
		known[entry.QuestionID] = entry.QuestionType
	}
	for _, id := range first {
		counts[known[id]]++
	}
	want := map[string]int{
		"single-session-user":       2,
		"single-session-assistant":  1,
		"single-session-preference": 1,
		"multi-session":             3,
		"temporal-reasoning":        3,
		"knowledge-update":          2,
	}
	for qtype, n := range want {
		if counts[qtype] != n {
			t.Fatalf("%s = %d ids, want %d (all: %v)", qtype, counts[qtype], n, counts)
		}
	}
}

// Parity-exempt: fewer questions than types keeps one slot per selected type,
// and a count above the index size clamps to everything.
func TestSelectSmallAndOversizedCounts(t *testing.T) {
	index := sampleIndex()
	small, err := Select(index, "4")
	if err != nil {
		t.Fatalf("small: %v", err)
	}
	if len(small) != 4 {
		t.Fatalf("small sample = %d", len(small))
	}
	huge, err := Select(index, "9999")
	if err != nil || len(huge) != len(index) {
		t.Fatalf("oversized sample = %d, %v", len(huge), err)
	}
}

// Parity-exempt: a count is only a count when it is all digits, mirroring
// Python str.isdigit (so "-3" is an id list, not a negative count).
func TestSelectNegativeIsNotACount(t *testing.T) {
	if _, err := Select(sampleIndex(), "-3"); err == nil {
		t.Fatal("negative spec was treated as a count")
	}
}
