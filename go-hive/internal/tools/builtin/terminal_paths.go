package builtin

import (
	"path/filepath"
	"strings"
)

// terminalBaseDir is the directory relative paths resolve against: the
// framework-injected session_cwd when present, otherwise the toolbox root.
func (t *Toolbox) terminalBaseDir(input map[string]any) (string, error) {
	if sessionCwd, ok := stringArg(input, "session_cwd"); ok && strings.TrimSpace(sessionCwd) != "" {
		if filepath.IsAbs(sessionCwd) {
			return filepath.Clean(sessionCwd), nil
		}
	}
	return t.rootPath()
}

// terminalWorkdir resolves the optional cwd argument. Relative paths anchor to
// the session workdir; absolute paths are honored as given (loose default, not
// a sandbox — the terminal surface may point anywhere).
func (t *Toolbox) terminalWorkdir(input map[string]any) (string, error) {
	cwd, ok := stringArg(input, "cwd")
	if !ok || strings.TrimSpace(cwd) == "" {
		return t.terminalBaseDir(input)
	}
	if filepath.IsAbs(cwd) {
		return filepath.Clean(cwd), nil
	}
	base, err := t.terminalBaseDir(input)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, cwd), nil
}

// terminalTargetPath resolves a search/list path argument with the same loose
// rule: relative paths join the session workdir, absolute paths pass through,
// and the "." default means the workdir itself.
func (t *Toolbox) terminalTargetPath(input map[string]any) (string, error) {
	raw, ok := stringArg(input, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		raw = "."
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	base, err := t.terminalBaseDir(input)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, raw), nil
}
