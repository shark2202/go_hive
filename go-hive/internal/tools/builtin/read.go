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

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	path, err := resolvePath(root, raw)
	if err != nil {
		return fail("read_file: %v", err), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fail("read_file: file not found: %s", raw), nil
	}
	if err != nil {
		return fail("read_file: cannot open %s: %v", raw, err), nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fail("read_file: cannot stat %s: %v", raw, err), nil
	}
	if info.IsDir() {
		return fail("read_file: %s is a directory, not a file", raw), nil
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
	return Result{Content: formatReadOutput(content, offset, limit)}, nil
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

// splitLines splits content the way Python's str.splitlines does for the
// newline forms agents produce, dropping a single trailing newline.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// formatReadOutput renders lines offset..offset+limit-1 with 1-based line
// numbers and a continuation hint when output was cut short.
func formatReadOutput(content string, offset, limit int) string {
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
		if utf8.RuneCountInString(line) > MaxReadLineLength {
			runes := []rune(line)
			line = string(runes[:MaxReadLineLength]) + "..."
		}
		formatted := fmt.Sprintf("%6d\t%s", i+1, line)
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
