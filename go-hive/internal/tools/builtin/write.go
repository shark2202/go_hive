package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

// writeFile implements write_file: sandboxed, parent dirs created, atomic
// replace via temp file + rename. Existing files must have been read first and
// still match that read (file_ops.py::write_file stale-edit guard).
func (t *Toolbox) writeFile(ctx context.Context, input map[string]any) (Result, error) {
	raw, ok := stringArg(input, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		return fail("write_file: path is required"), nil
	}
	v, present := input["content"]
	if !present || v == nil {
		return fail("write_file: content is required"), nil
	}
	content, ok := v.(string)
	if !ok {
		return fail("write_file: content must be a string"), nil
	}

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	path, err := resolveWritePath(root, raw, t.writeSafeRoots())
	if err != nil {
		// Parity: tools/src/aden_tools/file_ops.py
		// Upstream returns its policy message as plain text ("Error: …"), not
		// as an error-marked result.
		return Result{Content: fmt.Sprintf("Error: %v", err)}, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	existed := false
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return Result{Content: fmt.Sprintf("Error writing file: %s is a directory", raw)}, nil
		}
		existed = info.Mode().IsRegular()
		// Parity: tools/src/aden_tools/file_ops.py::write_file refuses to clobber an existing file
		// that was never read, or that changed since the recorded read. A
		// brand-new path has nothing to clobber and skips the guard.
		// Parity: tools/src/aden_tools/file_ops.py::write_file returns both
		// refusals as plain text (no is_error), so the model sees a non-error
		// tool result.
		switch status, detail := t.readState().checkFresh(path); status {
		case freshnessUnread:
			return Result{Content: fmt.Sprintf("Refusing to overwrite '%s': call read_file('%s') first so the harness "+
				"can track its state before you replace it. If you intend to discard the current "+
				"contents, read it first to acknowledge what you are overwriting.", raw, raw)}, nil
		case freshnessStale:
			return Result{Content: fmt.Sprintf("Refusing to overwrite '%s': %s. Re-read the file with read_file before writing.", raw, detail)}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail("write_file: cannot stat %s: %v", raw, err), nil
	}

	if err := atomicfs.WriteFile(path, []byte(content), 0o644); err != nil {
		return fail("write_file: %v", err), nil
	}
	// Record the post-write state so a later write/edit in the same turn does
	// not trip the guard against the file this call just created or replaced.
	t.readState().recordRead(path, []byte(content))

	// Parity: tools/src/aden_tools/file_ops.py::write_file success text —
	// "<Created|Updated> <path> (<bytes with thousands separators>, <lines>)".
	lineCount := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		lineCount++
	}
	action := "Created"
	if existed {
		action = "Updated"
	}
	return Result{Content: fmt.Sprintf("%s %s (%s bytes, %d lines)", action, raw, formatThousands(len(content)), lineCount)}, nil
}

// formatThousands renders an integer with Python's `{:,}` separators.
func formatThousands(value int) string {
	digits := strconv.Itoa(value)
	if len(digits) <= 3 {
		return digits
	}
	var builder strings.Builder
	lead := len(digits) % 3
	if lead > 0 {
		builder.WriteString(digits[:lead])
	}
	for index := lead; index < len(digits); index += 3 {
		if builder.Len() > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(digits[index : index+3])
	}
	return builder.String()
}
