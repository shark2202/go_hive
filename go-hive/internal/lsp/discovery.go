package lsp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LookPathFunc is exec.LookPath, injectable for tests.
type LookPathFunc func(string) (string, error)

// localBinDirs are searched before PATH for bare commands, in this order. The
// list is project-local only; discovery never walks above cwd.
var localBinDirs = []string{
	filepath.Join(".hive", "bin"),
	filepath.Join("node_modules", ".bin"),
	filepath.Join(".venv", "bin"),
	filepath.Join("venv", "bin"),
	"bin",
}

// Discovery is the resolved state of one configured server in one cwd.
type Discovery struct {
	Name       string
	Definition *ServerDef
	// Root is the effective workspace root: the definition's rootDir when it
	// is set and usable, otherwise cwd. Commands, clients, documents and
	// workspace scans all use this root.
	Root        string
	RootMarker  string
	Rooted      bool
	CommandPath string
	Reason      string
}

// Available reports whether the server can be started: enabled, rooted in cwd
// and backed by a resolvable executable.
func (d Discovery) Available() bool {
	return d.Definition != nil && !d.Definition.Disabled && d.Rooted && d.CommandPath != ""
}

// State is a short status word: available, disabled, unrooted or missing.
func (d Discovery) State() string {
	switch {
	case d.Definition != nil && d.Definition.Disabled:
		return "disabled"
	case !d.Rooted:
		return "unrooted"
	case d.CommandPath == "":
		return "missing"
	default:
		return "available"
	}
}

// DiscoverServers evaluates every configured server against cwd. Root markers
// are cwd-only by default: a marker in a parent directory does not count. A
// definition that pins rootDir is rooted there instead, and its command-local
// bins are resolved from that directory.
func DiscoverServers(cwd string, cfg *Config, look LookPathFunc) []Discovery {
	if cfg == nil {
		return nil
	}
	if look == nil {
		look = exec.LookPath
	}
	names := cfg.ServerNames()
	out := make([]Discovery, 0, len(names))
	for _, name := range names {
		def := cfg.Servers[name]
		disc := Discovery{Name: name, Definition: def, Root: cwd}
		if def == nil {
			continue
		}
		root, rootErr := effectiveServerRoot(cwd, def)
		if rootErr == nil {
			disc.Root = root
		}
		if def.Disabled {
			disc.Reason = "disabled in config"
			disc.CommandPath, _ = resolveCommand(disc.Root, def.Command, look)
			out = append(out, disc)
			continue
		}
		if rootErr != nil {
			// The pin is a config-time intent, not a hard failure: report the
			// server as unrooted with the reason instead of hiding it.
			disc.Reason = rootErr.Error()
			disc.CommandPath, _ = resolveCommand(cwd, def.Command, look)
			out = append(out, disc)
			continue
		}
		disc.RootMarker, disc.Rooted = findRootMarker(disc.Root, def.RootMarkers)
		if def.RootDir != "" {
			// An explicit rootDir is the user's assertion that this is the
			// workspace; it does not need a marker to be rooted.
			disc.Rooted = true
		}
		path, err := resolveCommand(disc.Root, def.Command, look)
		if err != nil {
			disc.Reason = err.Error()
		} else {
			disc.CommandPath = path
		}
		if !disc.Rooted {
			disc.Reason = "no root marker in " + cwd + " (" + strings.Join(def.RootMarkers, ", ") + ")"
		}
		out = append(out, disc)
	}
	return out
}

// effectiveServerRoot resolves the workspace root for one definition: cwd
// unless rootDir pins an existing absolute directory.
func effectiveServerRoot(cwd string, def *ServerDef) (string, error) {
	if def == nil || def.RootDir == "" {
		return cwd, nil
	}
	root := filepath.Clean(def.RootDir)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("rootDir %s does not exist", root)
		}
		return "", fmt.Errorf("rootDir %s: %v", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("rootDir %s is not a directory", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("rootDir %s: %v", root, err)
	}
	return filepath.Clean(real), nil
}

func findRootMarker(cwd string, markers []string) (string, bool) {
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(cwd, marker)); err == nil {
			return marker, true
		}
	}
	return "", false
}

func resolveCommand(cwd, command string, look LookPathFunc) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is empty")
	}
	if filepath.IsAbs(command) {
		if err := checkExecutable(command); err != nil {
			return "", err
		}
		return command, nil
	}
	if strings.ContainsAny(command, `/\`) {
		return "", fmt.Errorf("command %q is a relative path; use a bare name or an absolute path", command)
	}
	var tried []string
	for _, dir := range localBinDirs {
		for _, name := range localCommandNames(command) {
			candidate := filepath.Join(cwd, dir, name)
			if err := checkExecutable(candidate); err == nil {
				return candidate, nil
			}
			tried = append(tried, filepath.ToSlash(filepath.Join(dir, name)))
		}
	}
	if resolved, err := look(command); err == nil && resolved != "" {
		return resolved, nil
	}
	return "", fmt.Errorf("command %q not found in %s or PATH", command, strings.Join(tried, ", "))
}

// localCommandNames adds the Windows shim extensions project-local bins use.
func localCommandNames(command string) []string {
	if runtime.GOOS != "windows" {
		return []string{command}
	}
	return []string{command + ".exe", command + ".cmd", command + ".bat", command}
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// SortDiscoveries sorts by name; DiscoverServers already does this, and the
// helper is exported for callers assembling mixed sets.
func SortDiscoveries(items []Discovery) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

// extension returns the lowercase extension of path.
func extension(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// languageIDFor picks the languageId for a document. A server definition may
// pin one; otherwise a small extension map covers the embedded catalog.
func languageIDFor(def *ServerDef, path string) string {
	if def != nil && def.LanguageID != "" {
		return def.LanguageID
	}
	switch extension(path) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hxx":
		return "cpp"
	case ".yaml", ".yml":
		return "yaml"
	case ".sh", ".bash", ".zsh":
		return "shellscript"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	default:
		return "plaintext"
	}
}
