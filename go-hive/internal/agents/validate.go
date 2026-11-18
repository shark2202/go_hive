package agents

// Parity: framework/server/app.py::validate_agent_path

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const invalidAgentPathMessage = "agent_path must be inside an allowed directory ($HIVE_HOME/colonies/, exports/, examples/, or $HIVE_HOME/agents/)"

type AllowedRoots struct {
	ColoniesDir string
	RepoRoot    string
	AgentsDir   string

	mu       sync.Mutex
	resolved bool
	cached   []string
	err      error
}

func NewAllowedRoots(coloniesDir, repoRoot, agentsDir string) *AllowedRoots {
	return &AllowedRoots{ColoniesDir: coloniesDir, RepoRoot: repoRoot, AgentsDir: agentsDir}
}

// Parity: framework/server/app.py::_get_allowed_agent_roots
func (a *AllowedRoots) Resolve() ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resolved {
		return a.cached, a.err
	}
	candidates := []string{
		a.ColoniesDir,
		filepath.Join(a.RepoRoot, "exports"),
		filepath.Join(a.RepoRoot, "examples"),
		a.AgentsDir,
	}
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		resolved, err := ResolvePath(candidate)
		if err != nil {
			a.resolved, a.err = true, err
			return nil, err
		}
		roots = append(roots, resolved)
	}
	a.cached, a.resolved = roots, true
	return roots, nil
}

func (a *AllowedRoots) Validate(agentPath string) (string, error) {
	roots, err := a.Resolve()
	if err != nil {
		return "", err
	}
	return ValidateAgentPath(agentPath, roots)
}

// Parity: framework/server/app.py::validate_agent_path
func ValidateAgentPath(agentPath string, roots []string) (string, error) {
	resolved, err := ResolvePath(agentPath)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		resolvedRoot, rootErr := ResolvePath(root)
		if rootErr != nil {
			continue
		}
		if IsRelativeTo(resolved, resolvedRoot) && !pathEqual(resolved, resolvedRoot) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%s", invalidAgentPathMessage)
}

func ResolvePath(path string) (string, error) {
	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("embedded null byte")
	}
	expanded, err := expandUser(path)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return resolveNonStrict(filepath.Clean(absolute)), nil
}

func expandUser(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func resolveNonStrict(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	remaining := ""
	current := path
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		base := filepath.Base(current)
		if remaining == "" {
			remaining = base
		} else {
			remaining = filepath.Join(base, remaining)
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolved, remaining)
		}
		current = parent
	}
}

func IsRelativeTo(path, root string) bool {
	if pathEqual(path, root) {
		return true
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathEqual(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
