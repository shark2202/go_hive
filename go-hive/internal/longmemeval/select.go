package longmemeval

// Question selection.
//
// Parity: tests/e2e/longmemeval.py::select

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// Select returns the question ids for spec: "all", comma-separated ids, or a
// count. A count is split across question types in proportion to the full
// benchmark (largest remainder, at least one per type), so a sample's overall
// accuracy estimates the full run's. Deterministic for a given index.
func Select(index []IndexEntry, spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, "all") {
		ids := make([]string, 0, len(index))
		for _, entry := range index {
			ids = append(ids, entry.QuestionID)
		}
		return ids, nil
	}
	if !isAllDigits(spec) {
		wanted := splitIDs(spec)
		known := make(map[string]struct{}, len(index))
		for _, entry := range index {
			known[entry.QuestionID] = struct{}{}
		}
		var unknown []string
		for _, id := range wanted {
			if _, ok := known[id]; !ok {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("unknown LongMemEval question ids: %v", unknown)
		}
		return wanted, nil
	}

	count, err := parseCount(spec)
	if err != nil {
		return nil, err
	}
	if count > len(index) {
		count = len(index)
	}

	byType := map[string][]string{}
	for _, entry := range index {
		byType[entry.QuestionType] = append(byType[entry.QuestionType], entry.QuestionID)
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	if count < len(types) {
		types = types[:count]
	}

	share := make(map[string]float64, len(types))
	alloc := make(map[string]int, len(types))
	for _, t := range types {
		share[t] = float64(count) * float64(len(byType[t])) / float64(len(index))
		alloc[t] = int(share[t])
		if alloc[t] < 1 {
			alloc[t] = 1
		}
	}
	// Leftover slots go to whoever is furthest below their share; a type
	// lifted to its one-slot minimum is already at or above it.
	ordered := append([]string(nil), types...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return share[ordered[i]]-float64(alloc[ordered[i]]) > share[ordered[j]]-float64(alloc[ordered[j]])
	})
	for _, t := range ordered {
		if totalAlloc(alloc, types) >= count {
			break
		}
		alloc[t]++
	}
	for totalAlloc(alloc, types) > count {
		biggest := types[0]
		for _, t := range types {
			if alloc[t] > alloc[biggest] {
				biggest = t
			}
		}
		alloc[biggest]--
	}

	rng := rand.New(rand.NewSource(0))
	chosen := make([]string, 0, count)
	for _, t := range types {
		ids := append([]string(nil), byType[t]...)
		sort.Strings(ids)
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		for i := 0; i < alloc[t] && i < len(ids); i++ {
			chosen = append(chosen, ids[i])
		}
	}
	return chosen, nil
}

func totalAlloc(alloc map[string]int, types []string) int {
	total := 0
	for _, t := range types {
		total += alloc[t]
	}
	return total
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseCount(s string) (int, error) {
	count := 0
	for _, r := range s {
		count = count*10 + int(r-'0')
		if count > 1_000_000_000 {
			return 0, fmt.Errorf("question count %q is too large", s)
		}
	}
	return count, nil
}

func splitIDs(spec string) []string {
	var out []string
	for _, part := range strings.Split(spec, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
