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
// replace via temp file + rename.
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
	path, err := resolvePath(root, raw)
	if err != nil {
		return fail("write_file: %v", err), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fail("write_file: %s is a directory", raw), nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail("write_file: cannot stat %s: %v", raw, err), nil
	}

	if err := atomicfs.WriteFile(path, []byte(content), 0o644); err != nil {
		return fail("write_file: %v", err), nil
	}
	return Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(content), raw)}, nil
}
