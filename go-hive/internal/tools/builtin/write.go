package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
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
		return fail("write_file: %v", err), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fail("write_file: %s is a directory", raw), nil
		}
		// Parity: tools/src/aden_tools/file_ops.py::write_file refuses to clobber an existing file
		// that was never read, or that changed since the recorded read. A
		// brand-new path has nothing to clobber and skips the guard.
		switch status, detail := t.readState().checkFresh(path); status {
		case freshnessUnread:
			return fail("Refusing to overwrite '%s': call read_file('%s') first so the harness "+
				"can track its state before you replace it. If you intend to discard the current "+
				"contents, read it first to acknowledge what you are overwriting.", raw, raw), nil
		case freshnessStale:
			return fail("Refusing to overwrite '%s': %s. Re-read the file with read_file before writing.", raw, detail), nil
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
	return Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(content), raw)}, nil
}
