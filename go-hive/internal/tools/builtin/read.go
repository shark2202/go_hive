package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// readFile implements read_file: UTF-8 text, numbered lines, bounded output.
func (t *Toolbox) readFile(ctx context.Context, input map[string]any) (Result, error) {
	raw, ok := stringArg(input, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		return fail("read_file: path is required"), nil
	}
	offset := 1
	if v, present := input["offset"]; present && v != nil {
		parsed, ok := intArg(input, "offset")
		if !ok {
			return fail("read_file: offset must be an integer"), nil
		}
		offset = parsed
	}
	if offset < 1 {
		offset = 1
	}
	limit := 0
	if v, present := input["limit"]; present && v != nil {
		parsed, ok := intArg(input, "limit")
		if !ok {
			return fail("read_file: limit must be an integer"), nil
		}
		limit = parsed
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	hashline := boolArgOr(input, "hashline", false)

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	path, err := resolvePath(root, raw)
	if err != nil {
		// Parity: tools/src/aden_tools/file_ops.py::read_file returns the
		// policy message as plain text ("Error: …"), not as a tool error — the
		// model sees the same non-error result the Python file server produces.
		return Result{Content: fmt.Sprintf("Error: %v", err)}, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	info, statErr := os.Stat(path)
	if statErr == nil && info.IsDir() {
		return Result{Content: formatDirectoryListing(raw, path)}, nil
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Result{Content: fmt.Sprintf("Error: File not found: %s", raw)}, nil
	}
	if err != nil {
		return fail("read_file: cannot open %s: %v", raw, err), nil
	}
	defer f.Close()

	info, err = f.Stat()
	if err != nil {
		return fail("read_file: cannot stat %s: %v", raw, err), nil
	}
	if !info.Mode().IsRegular() {
		return fail("read_file: %s is not a regular file", raw), nil
	}
	limitBytes := t.maxReadSize
	if limitBytes <= 0 {
		limitBytes = MaxReadFileBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limitBytes)+1))
	if err != nil {
		return fail("read_file: cannot read %s: %v", raw, err), nil
	}
	if len(data) > limitBytes {
		return fail("read_file: file is too large (%s exceeds the %d byte read limit)",
			raw, limitBytes), nil
	}
	if isBinary(data) {
		return fail("read_file: %s is a binary file (%d bytes); cannot display binary content",
			raw, len(data)), nil
	}

	content := strings.ToValidUTF8(string(data), "\uFFFD")
	// Parity: tools/src/aden_tools/file_ops.py::read_file records the raw bytes (partial reads
	// included) so a later edit can detect external writes.
	t.readState().recordRead(path, data)
	return Result{Content: formatReadOutput(content, offset, limit, hashline)}, nil
}

// formatDirectoryListing ports the branch of file_ops.py::read_file that
// answers a directory with its entries instead of an error: the raw path (not
// the resolved one), the entry count before truncation, and up to 200 entries
// with a trailing "/" on subdirectories.
func formatDirectoryListing(raw, resolved string) string {
	children, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Sprintf("Error: cannot list directory %s: %v", raw, err)
	}
	entries := make([]string, 0, len(children))
	for _, child := range children {
		suffix := ""
		if child.IsDir() {
			suffix = "/"
		}
		entries = append(entries, "  "+child.Name()+suffix)
	}
	if len(entries) > maxDirectoryListingEntries {
		entries = entries[:maxDirectoryListingEntries]
	}
	return fmt.Sprintf("Directory: %s (%d entries)\n", raw, len(children)) + strings.Join(entries, "\n")
}

// isBinary reports whether data looks binary: a NUL byte in the leading
// window, or bytes there that are not valid UTF-8 (a rune cut at the window
// edge does not count).
func isBinary(data []byte) bool {
	window := data
	if len(window) > 8192 {
		window = window[:8192]
	}
	for _, b := range window {
		if b == 0 {
			return true
		}
	}
	if utf8.Valid(window) {
		return false
	}
	for trim := 1; trim <= 3 && trim < len(window); trim++ {
		if utf8.Valid(window[:len(window)-trim]) {
			return false
		}
	}
	return true
}

// splitLines splits content the way Python's str.splitlines does: on \n, \r,
// \r\n, \v, \f, \x1c-\x1e, \x85, \u2028 and \u2029, dropping the empty element
// a trailing boundary would otherwise produce. file_ops.py::read_file uses
// splitlines() before hashing, so the hashline numbering depends on it.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := make([]string, 0, strings.Count(content, "\n")+1)
	start := 0
	for i := 0; i < len(content); {
		r, size := utf8.DecodeRuneInString(content[i:])
		if !isPythonLineBoundary(r) {
			i += size
			continue
		}
		lines = append(lines, content[start:i])
		if r == '\r' && i+size < len(content) && content[i+size] == '\n' {
			size++ // \r\n is one boundary
		}
		i += size
		start = i
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

// isPythonLineBoundary reports whether r ends a line for str.splitlines.
func isPythonLineBoundary(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// formatReadOutput renders lines offset..offset+limit-1 with 1-based line
// numbers and a continuation hint when output was cut short. In hashline mode
// each line is prefixed with "N:hhhh|" anchors and no line is truncated, since
// truncation would corrupt the hash (file_ops.py::read_file).
func formatReadOutput(content string, offset, limit int, hashline bool) string {
	lines := splitLines(content)
	total := len(lines)
	if total == 0 {
		return ""
	}
	start := offset - 1
	if start >= total {
		return fmt.Sprintf("(offset %d is past the end of the file: %d lines total)", offset, total)
	}
	end := start + limit
	if end > total {
		end = total
	}

	var b strings.Builder
	shown := 0
	byteCount := 0
	truncatedByBytes := false
	for i := start; i < end; i++ {
		line := lines[i]
		var formatted string
		if hashline {
			formatted = fmt.Sprintf("%d:%s|%s", i+1, computeLineHash(line), line)
		} else {
			if utf8.RuneCountInString(line) > MaxReadLineLength {
				runes := []rune(line)
				line = string(runes[:MaxReadLineLength]) + "..."
			}
			formatted = fmt.Sprintf("%6d	%s", i+1, line)
		}
		lineBytes := len(formatted) + 1
		if byteCount+lineBytes > MaxReadOutputBytes {
			truncatedByBytes = true
			break
		}
		if shown > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatted)
		byteCount += lineBytes
		shown++
	}

	result := b.String()
	actualEnd := start + shown
	if actualEnd < total || truncatedByBytes {
		result += fmt.Sprintf("\n\n(Showing lines %d-%d of %d.", start+1, actualEnd, total)
		if truncatedByBytes {
			result += " Truncated by byte budget."
		}
		result += fmt.Sprintf(" Use offset=%d to continue reading.)", actualEnd+1)
	}
	return result
}
