package builtin

// V4A structured patch (edit_file mode=patch): a lenient parser plus the
// two-phase apply that validates every operation in memory before the first
// byte is written.
//
// Grammar:
//
//	*** Begin Patch          (optional)
//	*** Update File: path
//	@@ optional context @@   (optional, per hunk)
//	 context line
//	-removed line
//	+added line
//	*** Add File: path
//	*** Delete File: path
//	*** Move File: src -> dst
//	*** End Patch            (optional)

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

// ── V4A structured patch ─────────────────────────────────────────────────

var (
	patchBeginRe = regexp.MustCompile(`^\*\*\*\s*Begin\s+Patch\s*$`)
	patchEndRe   = regexp.MustCompile(`^\*\*\*\s*End\s+Patch\s*$`)
	patchOpRe    = regexp.MustCompile(`^\*\*\*\s+(Update|Add|Delete|Move)\s+File:\s*(.+)$`)
	patchHintRe  = regexp.MustCompile(`^@@(?:\s*(.*?)\s*@@)?\s*$`)
	patchMoveRe  = regexp.MustCompile(`^(.+?)\s*->\s*(.+)$`)
)

const patchHunkExample = "Use '@@' or '@@ hint @@' followed by context/removal/addition lines. Example:\n@@\n-old text\n+new text"

type patchLine struct {
	prefix  byte // ' ', '-', '+'
	content string
}

type patchHunk struct {
	contextHint string
	lines       []patchLine
}

type patchOp struct {
	kind       string // add | update | delete | move
	path       string
	dest       string
	hunks      []patchHunk
	addContent string
}

// editPatch implements mode=patch.
func (t *Toolbox) editPatch(ctx context.Context, input map[string]any) (Result, error) {
	patchText, _ := stringArg(input, "patch_text")
	if patchText == "" {
		return plain("Error: patch mode requires a non-empty 'patch_text'"), nil
	}
	ops, parseErr := parseV4A(patchText)
	if parseErr != "" {
		return plain("Error: %s", parseErr), nil
	}
	if len(ops) == 0 {
		return plain("Error: patch text contained no operations"), nil
	}
	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	summary, applyErr := t.applyV4A(ops, root)
	if applyErr != "" {
		return plain("Error: %s", applyErr), nil
	}
	if summary == "" {
		summary = "Patch applied"
	}
	return Result{Content: summary}, nil
}

// parseV4A parses the lenient structured patch grammar. On failure it returns
// no operations and a message describing why parsing stopped.
func parseV4A(text string) ([]patchOp, string) {
	lines := splitLines(text)
	i := 0
	for i < len(lines) {
		if patchBeginRe.MatchString(strings.TrimSpace(lines[i])) {
			i++
			break
		}
		if patchOpRe.MatchString(lines[i]) {
			break
		}
		i++
	}

	var ops []patchOp
	for i < len(lines) {
		raw := lines[i]
		if patchEndRe.MatchString(strings.TrimSpace(raw)) {
			break
		}
		m := patchOpRe.FindStringSubmatch(raw)
		if m == nil {
			i++
			continue
		}
		kindWord, rest := m[1], strings.TrimSpace(m[2])
		i++
		switch kindWord {
		case "Update":
			var hunks []patchHunk
			for i < len(lines) {
				if patchOpRe.MatchString(lines[i]) || patchEndRe.MatchString(strings.TrimSpace(lines[i])) {
					break
				}
				if strings.HasPrefix(lines[i], "@@") && !patchHintRe.MatchString(lines[i]) {
					return nil, fmt.Sprintf("Update %s, line %d: invalid hunk header %q. %s", rest, i+1, lines[i], patchHunkExample)
				}
				hunkStart := i
				hunk, next := parseHunk(lines, i)
				i = next
				if hunk == nil {
					return nil, fmt.Sprintf("Update %s, line %d: expected hunk content. %s", rest, hunkStart+1, patchHunkExample)
				}
				hunks = append(hunks, *hunk)
			}
			if len(hunks) == 0 {
				return nil, fmt.Sprintf("Update %s: missing hunk. %s", rest, patchHunkExample)
			}
			ops = append(ops, patchOp{kind: "update", path: rest, hunks: hunks})
		case "Add":
			var contentLines []string
			for i < len(lines) {
				if patchOpRe.MatchString(lines[i]) || patchEndRe.MatchString(strings.TrimSpace(lines[i])) {
					break
				}
				if strings.HasPrefix(lines[i], "+") {
					contentLines = append(contentLines, lines[i][1:])
				}
				i++
			}
			ops = append(ops, patchOp{kind: "add", path: rest, addContent: strings.Join(contentLines, "\n")})
		case "Delete":
			ops = append(ops, patchOp{kind: "delete", path: rest})
		case "Move":
			mv := patchMoveRe.FindStringSubmatch(rest)
			if mv == nil {
				return nil, fmt.Sprintf("Move requires 'src -> dst', got: %s", rest)
			}
			ops = append(ops, patchOp{kind: "move", path: strings.TrimSpace(mv[1]), dest: strings.TrimSpace(mv[2])})
		}
	}

	if len(ops) == 0 {
		return nil, "patch text contained no operations"
	}
	var errors []string
	for _, op := range ops {
		if op.path == "" {
			errors = append(errors, op.kind+": empty path")
		}
		if op.kind == "move" && op.dest == "" {
			errors = append(errors, "move "+op.path+": missing destination")
		}
	}
	if len(errors) > 0 {
		return nil, strings.Join(errors, "; ")
	}
	return ops, ""
}

// parseHunk parses one hunk starting at startIdx and returns the next index.
func parseHunk(lines []string, startIdx int) (*patchHunk, int) {
	i := startIdx
	if i >= len(lines) {
		return nil, i
	}
	hint := ""
	if m := patchHintRe.FindStringSubmatch(lines[i]); m != nil {
		hint = strings.TrimSpace(m[1])
		i++
	}
	var hunkLines []patchLine
	started := false
	for i < len(lines) {
		line := lines[i]
		if patchOpRe.MatchString(line) || patchEndRe.MatchString(strings.TrimSpace(line)) || strings.HasPrefix(line, "@@") {
			break
		}
		if strings.HasPrefix(line, `\`) {
			// Git-diff artifact like "\ No newline at end of file".
			i++
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") {
			hunkLines = append(hunkLines, patchLine{prefix: line[0], content: line[1:]})
			started = true
			i++
			continue
		}
		if started {
			// Implicit context line: the model forgot the leading space.
			hunkLines = append(hunkLines, patchLine{prefix: ' ', content: line})
			i++
			continue
		}
		break
	}
	if len(hunkLines) == 0 {
		return nil, i
	}
	return &patchHunk{contextHint: hint, lines: hunkLines}, i
}

// applyHunk applies one hunk to content, returning the new content or an
// error message.
func applyHunk(content string, hunk patchHunk) (string, string) {
	var searchLines, replaceLines []string
	for _, line := range hunk.lines {
		switch line.prefix {
		case ' ', '-':
			searchLines = append(searchLines, line.content)
		}
		switch line.prefix {
		case ' ', '+':
			replaceLines = append(replaceLines, line.content)
		}
	}
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}

	// Addition-only hunk: insert after the hint, or append to EOF.
	if len(searchLines) == 0 && len(replaceLines) > 0 {
		addition := strings.Join(replaceLines, newline)
		if hunk.contextHint != "" {
			count := strings.Count(content, hunk.contextHint)
			if count > 1 {
				return content, fmt.Sprintf("addition-only hunk: context hint '%s' is ambiguous (%d occurrences)", hunk.contextHint, count)
			}
			if count == 1 {
				idx := strings.Index(content, hunk.contextHint)
				lineEnd := strings.Index(content[idx:], "\n")
				if lineEnd < 0 {
					return content + newline + addition, ""
				}
				lineEnd += idx
				return content[:lineEnd+1] + addition + newline + content[lineEnd+1:], ""
			}
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += newline
		}
		return content + addition + newline, ""
	}

	search := strings.Join(searchLines, newline)
	replace := strings.Join(replaceLines, newline)
	if search == "" {
		return content, "hunk has neither context nor removed lines"
	}

	matched := ""
	matchedOK := false
	forEachFuzzyCandidate(content, search, func(candidate string, _ int) bool {
		first := strings.Index(content, candidate)
		if first < 0 {
			return true
		}
		if strings.LastIndex(content, candidate) == first {
			matched = candidate
			matchedOK = true
			return false
		}
		return true
	})

	if !matchedOK && hunk.contextHint != "" {
		hintPos := strings.Index(content, hunk.contextHint)
		if hintPos >= 0 {
			windowStart := max(0, hintPos-500)
			windowEnd := min(len(content), hintPos+2000)
			window := content[windowStart:windowEnd]
			windowMatched := ""
			windowOK := false
			forEachFuzzyCandidate(window, search, func(candidate string, _ int) bool {
				first := strings.Index(window, candidate)
				if first < 0 {
					return true
				}
				if strings.LastIndex(window, candidate) == first {
					windowMatched = candidate
					windowOK = true
					return false
				}
				return true
			})
			if windowOK {
				newWindow := strings.Replace(window, windowMatched, replace, 1)
				return content[:windowStart] + newWindow + content[windowEnd:], ""
			}
		}
	}

	if !matchedOK {
		return content, "could not find a unique match for hunk"
	}
	return strings.Replace(content, matched, replace, 1), ""
}

// applyV4A validates every operation against an in-memory copy of the touched
// files, then commits the simulated state. Validation failure writes nothing.
func (t *Toolbox) applyV4A(ops []patchOp, root string) (string, string) {
	fsState := map[string]string{}
	fsExists := map[string]bool{}
	originalExisted := map[string]bool{}
	var order []string

	touch := func(path string) {
		for _, existing := range order {
			if existing == path {
				return
			}
		}
		order = append(order, path)
	}

	ensureLoaded := func(resolved string) string {
		if _, ok := fsState[resolved]; ok {
			return ""
		}
		content, _, err := readEditableFile(resolved, t.maxReadSize)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "file not found: " + resolved
			}
			return "failed to read: " + err.Error()
		}
		fsState[resolved] = content
		fsExists[resolved] = true
		originalExisted[resolved] = true
		touch(resolved)
		return ""
	}

	existsForOp := func(resolved string) bool {
		if state, tracked := fsExists[resolved]; tracked {
			return state
		}
		_, err := os.Stat(resolved)
		return err == nil
	}

	var errors []string
	for idx, op := range ops {
		resolved, err := resolveWritePath(root, op.path, t.writeSafeRoots())
		if err != nil {
			errors = append(errors, fmt.Sprintf("Op #%d %s %s: %v", idx+1, op.kind, op.path, err))
			continue
		}

		switch op.kind {
		case "add":
			if existsForOp(resolved) {
				errors = append(errors, fmt.Sprintf("Op #%d add %s: file already exists", idx+1, op.path))
				continue
			}
			content := op.addContent
			if content != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			if _, tracked := originalExisted[resolved]; !tracked {
				_, statErr := os.Stat(resolved)
				originalExisted[resolved] = statErr == nil
			}
			fsState[resolved] = content
			fsExists[resolved] = true
			touch(resolved)

		case "delete":
			if msg := ensureLoaded(resolved); msg != "" {
				errors = append(errors, fmt.Sprintf("Op #%d delete %s: %s", idx+1, op.path, msg))
				continue
			}
			fsExists[resolved] = false

		case "update":
			if msg := ensureLoaded(resolved); msg != "" {
				errors = append(errors, fmt.Sprintf("Op #%d update %s: %s", idx+1, op.path, msg))
				continue
			}
			content := fsState[resolved]
			for hunkIdx, hunk := range op.hunks {
				newContent, hunkErr := applyHunk(content, hunk)
				if hunkErr != "" {
					errors = append(errors, fmt.Sprintf("Op #%d update %s hunk #%d: %s", idx+1, op.path, hunkIdx+1, hunkErr))
					break
				}
				content = newContent
			}
			fsState[resolved] = content

		case "move":
			dst, dstErr := resolveWritePath(root, op.dest, t.writeSafeRoots())
			if dstErr != nil {
				errors = append(errors, fmt.Sprintf("Op #%d move %s: %v", idx+1, op.path, dstErr))
				continue
			}
			if msg := ensureLoaded(resolved); msg != "" {
				errors = append(errors, fmt.Sprintf("Op #%d move %s: %s", idx+1, op.path, msg))
				continue
			}
			if existsForOp(dst) {
				errors = append(errors, fmt.Sprintf("Op #%d move %s: destination already exists", idx+1, op.path))
				continue
			}
			fsState[dst] = fsState[resolved]
			fsExists[dst] = true
			fsExists[resolved] = false
			if _, tracked := originalExisted[dst]; !tracked {
				_, statErr := os.Stat(dst)
				originalExisted[dst] = statErr == nil
			}
			touch(dst)
		}
	}

	if len(errors) > 0 {
		return "", "Patch validation failed (no files were modified):\n  " + strings.Join(errors, "\n  ")
	}

	// Phase 2: commit the simulated state.
	var filesModified, filesCreated, filesDeleted, diffs, applyErrors []string
	for _, resolved := range order {
		willExist := fsExists[resolved]
		existed, tracked := originalExisted[resolved]
		if !tracked {
			_, statErr := os.Stat(resolved)
			existed = statErr == nil
		}
		if willExist {
			newContent := fsState[resolved]
			oldContent := ""
			if existed {
				if previous, _, err := readEditableFile(resolved, t.maxReadSize); err == nil {
					oldContent = previous
				}
			}
			if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
				applyErrors = append(applyErrors, fmt.Sprintf("%s: %v", resolved, err))
				continue
			}
			perm := os.FileMode(0o644)
			if info, err := os.Stat(resolved); err == nil {
				perm = info.Mode().Perm()
			}
			if err := atomicfs.WriteFile(resolved, []byte(newContent), perm); err != nil {
				applyErrors = append(applyErrors, fmt.Sprintf("%s: %v", resolved, err))
				continue
			}
			// Parity: tools/src/aden_tools/file_ops.py::_patch_apply records the post-write state so
			// the next edit in the same turn starts from a fresh record.
			t.readState().recordRead(resolved, []byte(newContent))
			if existed {
				filesModified = append(filesModified, resolved)
				if diff := unifiedDiff(oldContent, newContent, resolved); diff != "" {
					diffs = append(diffs, diff)
				}
			} else {
				filesCreated = append(filesCreated, resolved)
			}
			continue
		}
		if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
			if err := os.Remove(resolved); err != nil {
				applyErrors = append(applyErrors, fmt.Sprintf("%s: %v", resolved, err))
				continue
			}
		}
		filesDeleted = append(filesDeleted, resolved)
	}

	if len(applyErrors) > 0 {
		return "", "Apply phase failed (state may be inconsistent — run `git diff` to assess):\n  " + strings.Join(applyErrors, "\n  ")
	}

	var parts []string
	if len(filesModified) > 0 {
		parts = append(parts, fmt.Sprintf("Modified %d file(s): %s", len(filesModified), strings.Join(filesModified, ", ")))
	}
	if len(filesCreated) > 0 {
		parts = append(parts, fmt.Sprintf("Created %d file(s): %s", len(filesCreated), strings.Join(filesCreated, ", ")))
	}
	if len(filesDeleted) > 0 {
		parts = append(parts, fmt.Sprintf("Deleted %d file(s): %s", len(filesDeleted), strings.Join(filesDeleted, ", ")))
	}
	summary := strings.Join(parts, "\n")
	if summary == "" {
		summary = "Patch applied (no files changed)"
	}
	if len(diffs) > 0 {
		summary += "\n\n" + strings.Join(diffs, "\n\n")
	}
	return summary, ""
}
