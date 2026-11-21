package builtin

// edit_file: the unified file editor. It carries the two upstream modes:
//
//   - replace: fuzzy find-and-replace in one file (unique match unless
//     replace_all), returning a unified diff and the strategy that matched.
//   - patch: a V4A/structured multi-file patch with two-phase apply. Every
//     operation is simulated against an in-memory copy of the touched files
//     first; nothing is written unless every operation validates.
//
// The fuzzy matcher is a straight port of tools/src/aden_tools/file_ops.py's
// _fuzzy_find_candidates: exact, line-trimmed, whitespace-normalized,
// indentation-flexible, escape-normalized, trimmed-boundary,
// unicode-normalized, block-anchor and context-aware, in that safety order.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

var (
	errEditDirectory = errors.New("path is a directory")
	errEditNotFile   = errors.New("path is not a regular file")
)

// fuzzyStrategyNames labels the candidate order returned by
// forEachFuzzyCandidate. The index is what replace mode reports back as
// "(matched via ...)".
var fuzzyStrategyNames = []string{
	"exact",
	"line-trimmed",
	"whitespace-normalized",
	"indentation-flexible",
	"escape-normalized",
	"trimmed-boundary",
	"unicode-normalized",
	"block-anchor",
	"context-aware",
}

// editFile dispatches the replace and patch modes.
func (t *Toolbox) editFile(ctx context.Context, input map[string]any) (Result, error) {
	mode, _ := stringArg(input, "mode")
	mode = strings.TrimSpace(mode)
	switch mode {
	case "":
		mode = "replace"
	case "replace", "patch":
	default:
		return plain("Error: unknown mode '%s'. Use mode='replace' or mode='patch'.", mode), nil
	}
	if mode == "patch" {
		return t.editPatch(ctx, input)
	}
	return t.editReplace(ctx, input)
}

// editReplace implements mode=replace.
func (t *Toolbox) editReplace(ctx context.Context, input map[string]any) (Result, error) {
	raw, _ := stringArg(input, "path")
	if strings.TrimSpace(raw) == "" {
		return plain("Error: replace mode requires a non-empty 'path'"), nil
	}
	oldText, _ := stringArg(input, "old_string")
	if oldText == "" {
		return plain("Error: 'old_string' must not be empty"), nil
	}
	newText, _ := stringArg(input, "new_string")
	if oldText == newText {
		return plain("Error: 'old_string' and 'new_string' are identical — nothing to do"), nil
	}
	replaceAll := boolArgOr(input, "replace_all", false)

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	path, err := resolveWritePath(root, raw, t.writeSafeRoots())
	if err != nil {
		return plain("Error: %v", err), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	content, info, err := readEditableFile(path, t.maxReadSize)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errEditDirectory) || errors.Is(err, errEditNotFile) {
		// Parity: tools/src/aden_tools/file_ops.py::_patch_replace returns this
		// as plain text, not a tool error — the model sees a non-error result.
		return plain("Error: File not found: %s", raw), nil
	}
	// Parity: tools/src/aden_tools/file_ops.py::_patch_replace refuses to edit a file that was never
	// read, or that changed on disk since the recorded read. Both refusals are
	// plain-text results upstream (no is_error marker).
	switch status, detail := t.readState().checkFresh(path); status {
	case freshnessUnread:
		return plain("Refusing to edit '%s': call read_file('%s') first so the harness can "+
			"track its state before you edit it.", raw, raw), nil
	case freshnessStale:
		return plain("Refusing to edit '%s': %s. Re-read the file with read_file before editing.", raw, detail), nil
	}
	if err != nil {
		return plain("Error editing file: %v", err), nil
	}

	// CRLF files: match in LF space but keep the file's line endings.
	crlf := strings.Contains(content, "\r\n")
	if crlf {
		oldText = toCRLF(oldText)
	}
	if !replaceAll && strings.Count(content, oldText) > 1 {
		return plain("Error: Could not find a unique match for old_string in %s. Include more context or explicitly use replace_all.", raw), nil
	}

	matched, strategy, ok := firstFuzzyCandidate(content, oldText, replaceAll)
	if !ok {
		msg := fmt.Sprintf(
			"Error: Could not find a unique match for old_string in %s. "+
				"Use read_file to verify the current content, or search_files "+
				"to locate the text.", raw)
		if suggestions := closeLineMatches(oldText, content); len(suggestions) > 0 {
			msg += "\n\nDid you mean one of these lines?\n" + strings.Join(suggestions, "\n")
		}
		return plain("%s", msg), nil
	}

	if crlf {
		newText = toCRLF(newText)
	}
	count := 1
	newContent := strings.Replace(content, matched, newText, 1)
	if replaceAll {
		count = strings.Count(content, matched)
		newContent = strings.ReplaceAll(content, matched, newText)
	}

	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := atomicfs.WriteFile(path, []byte(newContent), perm); err != nil {
		return plain("Error editing file: %v", err), nil
	}
	// Record the post-write state so chained edits in the same turn do not
	// self-invalidate (file_ops.py::_patch_replace records after writing).
	t.readState().recordRead(path, []byte(newContent))

	diff := unifiedDiff(content, newContent, raw)
	matchInfo := ""
	if strategy != "exact" {
		matchInfo = fmt.Sprintf(" (matched via %s)", strategy)
	}
	result := fmt.Sprintf("Replaced %d occurrence(s) in %s%s", count, raw, matchInfo)
	if diff != "" {
		result += "\n\n" + diff
	}
	return Result{Content: result}, nil
}

// readEditableFile reads a regular file, bounding the result by the toolbox's
// read limit (the same 10 MiB ceiling read_file uses).
func readEditableFile(path string, maxBytes int) (string, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", info, errEditDirectory
	}
	if !info.Mode().IsRegular() {
		return "", info, errEditNotFile
	}
	if maxBytes <= 0 {
		maxBytes = MaxReadFileBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return "", info, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return "", info, err
	}
	if len(data) > maxBytes {
		return "", info, fmt.Errorf("file is too large (%d bytes exceeds the %d byte edit limit)", len(data), maxBytes)
	}
	return string(data), info, nil
}

// firstFuzzyCandidate returns the first candidate the caller can act on: for
// replace_all every candidate is usable; otherwise the candidate must occur
// exactly once in content.
func firstFuzzyCandidate(content, oldText string, replaceAll bool) (string, string, bool) {
	found := ""
	strategy := ""
	ok := false
	forEachFuzzyCandidate(content, oldText, func(candidate string, index int) bool {
		first := strings.Index(content, candidate)
		if first < 0 {
			return true
		}
		if !replaceAll && strings.LastIndex(content, candidate) != first {
			return true
		}
		found = candidate
		strategy = fuzzyStrategyNames[min(index, len(fuzzyStrategyNames)-1)]
		ok = true
		return false
	})
	return found, strategy, ok
}

// forEachFuzzyCandidate yields match candidates in the upstream safety order.
// Returning false from yield stops the walk.
func forEachFuzzyCandidate(content, oldText string, yield func(candidate string, index int) bool) {
	// 1. Exact match.
	if strings.Contains(content, oldText) {
		if !yield(oldText, 0) {
			return
		}
	}

	contentLines := strings.Split(content, "\n")
	searchLines := strings.Split(oldText, "\n")
	// Strip trailing empty lines: a common copy-paste artifact.
	for len(searchLines) > 0 && strings.TrimSpace(searchLines[len(searchLines)-1]) == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}
	if len(searchLines) == 0 {
		return
	}
	n := len(searchLines)

	// 2. Line-trimmed match.
	for i := 0; i+n <= len(contentLines); i++ {
		block := contentLines[i : i+n]
		if linesTrimEqual(block, searchLines) {
			if !yield(strings.Join(block, "\n"), 1) {
				return
			}
		}
	}

	// 3. Whitespace-normalized match: collapse runs of whitespace.
	normalizedSearch := strings.TrimSpace(whitespaceRunRe.ReplaceAllString(oldText, " "))
	for i := 0; i+n <= len(contentLines); i++ {
		block := strings.Join(contentLines[i:i+n], "\n")
		if strings.TrimSpace(whitespaceRunRe.ReplaceAllString(block, " ")) == normalizedSearch {
			if !yield(block, 2) {
				return
			}
		}
	}

	// 4. Indentation-flexible match: strip the common leading indent.
	strippedSearch := stripCommonIndent(searchLines)
	for i := 0; i+n <= len(contentLines); i++ {
		block := contentLines[i : i+n]
		if stripCommonIndent(block) == strippedSearch {
			if !yield(strings.Join(block, "\n"), 3) {
				return
			}
		}
	}

	// 5. Escape-normalized match: agents sometimes paste literal \n/\t/\r.
	if strings.Contains(oldText, `\n`) || strings.Contains(oldText, `\t`) || strings.Contains(oldText, `\r`) {
		unescaped := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(oldText)
		if strings.Contains(content, "\r\n") {
			unescaped = toCRLF(unescaped)
		}
		if unescaped != oldText && strings.Contains(content, unescaped) {
			if !yield(unescaped, 4) {
				return
			}
		}
	}

	// 6. Trimmed-boundary match: only outer whitespace differs.
	trimmed := strings.TrimSpace(oldText)
	if trimmed != oldText && trimmed != "" && strings.Contains(content, trimmed) {
		if !yield(trimmed, 5) {
			return
		}
	}

	// 7. Unicode-normalized match: smart quotes, em/en-dashes, ellipsis,
	// NBSP. Match in normalized space, replace in original space so length
	// deltas never corrupt the file.
	normalizedNeedle := normalizeUnicode(oldText)
	if normalizedNeedle != oldText {
		normalizedHaystack, origRunes, posMap := normalizeWithPositionMap(content)
		if at := strings.Index(normalizedHaystack, normalizedNeedle); at >= 0 {
			startRune, okStart := runeIndexAtByte(normalizedHaystack, at)
			endRune, okEnd := runeIndexAtByte(normalizedHaystack, at+len(normalizedNeedle))
			if okStart && okEnd && endRune <= len(posMap) && startRune <= endRune {
				origStart, origEnd := posMap[startRune], posMap[endRune]
				if origStart <= origEnd && origEnd <= len(origRunes) {
					if !yield(string(origRunes[origStart:origEnd]), 6) {
						return
					}
				}
			}
		}
	}

	// 8. Block-anchor match: first and last lines match exactly (trimmed)
	// and the middle is similar enough. Thresholds are deliberately tight.
	if n >= 3 {
		firstTrimmed := strings.TrimSpace(searchLines[0])
		lastTrimmed := strings.TrimSpace(searchLines[n-1])
		type anchorCandidate struct {
			similarity float64
			text       string
		}
		var candidates []anchorCandidate
		for i, line := range contentLines {
			if strings.TrimSpace(line) != firstTrimmed {
				continue
			}
			end := i + n
			if end > len(contentLines) {
				continue
			}
			if strings.TrimSpace(contentLines[end-1]) != lastTrimmed {
				continue
			}
			block := contentLines[i:end]
			middleContent := strings.Join(block[1:len(block)-1], "\n")
			middleSearch := strings.Join(searchLines[1:n-1], "\n")
			candidates = append(candidates, anchorCandidate{
				similarity: levenshteinSimilarity(middleContent, middleSearch),
				text:       strings.Join(block, "\n"),
			})
		}
		if len(candidates) > 0 {
			sort.SliceStable(candidates, func(i, j int) bool {
				return candidates[i].similarity > candidates[j].similarity
			})
			threshold := 0.70
			if len(candidates) == 1 {
				threshold = 0.50
			}
			if candidates[0].similarity >= threshold {
				if !yield(candidates[0].text, 7) {
					return
				}
			}
		}
	}

	// 9. Context-aware match: last resort, per-line similarity 50% with a
	// 65% average across the block.
	if n >= 2 {
		for i := 0; i+n <= len(contentLines); i++ {
			block := contentLines[i : i+n]
			minSim, sum := 1.0, 0.0
			for j, line := range block {
				sim := levenshteinSimilarity(strings.TrimSpace(line), strings.TrimSpace(searchLines[j]))
				if sim < minSim {
					minSim = sim
				}
				sum += sim
			}
			if minSim >= 0.50 && sum/float64(n) >= 0.65 {
				if !yield(strings.Join(block, "\n"), 8) {
					return
				}
				break
			}
		}
	}
}

var whitespaceRunRe = regexp.MustCompile(`[\t\n\v\f\r ]+`)

// linesTrimEqual compares two equal-length line windows after trimming each
// line's outer whitespace.
func linesTrimEqual(a, b []string) bool {
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

// stripCommonIndent removes the smallest leading-whitespace run across the
// non-empty lines (Python's _strip_indent).
func stripCommonIndent(lines []string) string {
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for _, r := range line {
			if !unicode.IsSpace(r) {
				break
			}
			indent++
		}
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent < 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		runes := []rune(line)
		drop := minIndent
		if drop > len(runes) {
			drop = len(runes)
		}
		out[i] = string(runes[drop:])
	}
	return strings.Join(out, "\n")
}

// toCRLF converts every line ending in s to CRLF without doubling \r.
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// unicodeNormalizationMap mirrors file_ops.py's typographic substitutions.
var unicodeNormalizationMap = map[rune]string{
	'‘':      "'",   // left single quote
	'’':      "'",   // right single quote
	'“':      "\"",  // left double quote
	'”':      "\"",  // right double quote
	'—':      "--",  // em-dash
	'–':      "-",   // en-dash
	'…':      "...", // ellipsis
	'\u00a0': " ",   // NBSP
}

// normalizeUnicode applies the typographic substitutions to s.
func normalizeUnicode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if replacement, ok := unicodeNormalizationMap[r]; ok {
			b.WriteString(replacement)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeWithPositionMap normalizes content while recording, for every
// normalized rune, the index of the original rune that produced it. posMap
// gets a sentinel entry for the end of the string.
func normalizeWithPositionMap(content string) (normalized string, origRunes []rune, posMap []int) {
	origRunes = []rune(content)
	var b strings.Builder
	b.Grow(len(content))
	for i, r := range origRunes {
		replacement, ok := unicodeNormalizationMap[r]
		if !ok {
			replacement = string(r)
		}
		b.WriteString(replacement)
		for range []rune(replacement) {
			posMap = append(posMap, i)
		}
	}
	posMap = append(posMap, len(origRunes))
	return b.String(), origRunes, posMap
}

// runeIndexAtByte converts a byte offset in s to a rune index.
func runeIndexAtByte(s string, byteOffset int) (int, bool) {
	if byteOffset < 0 || byteOffset > len(s) {
		return 0, false
	}
	return len([]rune(s[:byteOffset])), true
}

// levenshteinSimilarity is 1 - distance/maxlen over runes, matching
// file_ops.py's _similarity.
func levenshteinSimilarity(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(levenshteinDistance(ar, br))/float64(maxLen)
}

func levenshteinDistance(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// closeLineMatches returns up to three "did you mean" lines, using the first
// 200 characters of oldText like upstream's difflib.get_close_matches call.
func closeLineMatches(oldText, content string) []string {
	needle := oldText
	if runes := []rune(needle); len(runes) > 200 {
		needle = string(runes[:200])
	}
	lines := strings.Split(content, "\n")
	type scored struct {
		score float64
		line  string
	}
	var hits []scored
	for _, line := range lines {
		score := levenshteinSimilarity(line, needle)
		if score < 0.4 {
			continue
		}
		hits = append(hits, scored{score: score, line: line})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]string, 0, 3)
	for _, hit := range hits {
		if len(out) == 3 {
			break
		}
		if strings.TrimSpace(hit.line) == "" {
			continue
		}
		out = append(out, "  "+hit.line)
	}
	return out
}
