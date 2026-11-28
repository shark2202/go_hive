package channels

// Python entry-point channels. Upstream imports the factory named by the
// plugin's ``hive.channels`` entry point and calls it with a ChannelHost in
// the server process (framework/server/channels.py::start_channels). Go keeps
// the process boundary and reuses the stdio plugin protocol: the embedded
// bridge.py is written to a private temp directory, started with the
// configured interpreter, discovers the entry point through
// importlib.metadata, imports the factory, calls factory(host), and translates
// every host call and QueenOutput into the hello/start/stop/output frames the
// existing host already understands.
//
// The bridge is opt-in: only a section with ``entry_point`` takes this path.
// ``command`` sections keep spawning their own executable and never need
// Python.
//
//	channels.<name>.entry_point  entry point name in the hive.channels group
//	channels.<name>.interpreter  optional Python argv (default: HIVE_PYTHON,
//	                             then python3, then python on PATH)
//	channels.<name>.python_path  optional entries prepended to PYTHONPATH
//
// Configuration and env reach the plugin unchanged: the hello frame carries
// the section as ``host.config``, and the bridge process inherits the serve
// environment plus the section's ``env`` overrides (``python_path`` is
// prepended to any inherited/configured PYTHONPATH).
//
// Parity: framework/server/channels.py::_installed
// Parity: framework/server/channels.py::start_channels (entry point load,
// factory(host), enabled gate and start budget)
// Parity-exempt: the external interpreter, temp bridge file, stdio transport
// and PYTHONPATH/interpreter plumbing are the Go process-boundary adapter.
// Python entry points never run in the Go process.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed bridge.py
var bridgeSource string

// PythonEntryPointGroup is the entry-point group upstream discovers channels
// in (framework/server/channels.py::ENTRY_POINT_GROUP).
const PythonEntryPointGroup = "hive.channels"

// EntryPointInfo is one installed hive.channels entry point, as the bridge's
// interpreter sees it.
type EntryPointInfo struct {
	Name         string `json:"name"`
	Value        string `json:"value"`
	Distribution string `json:"distribution,omitempty"`
}

// launch resolves the argv and environment overrides for one plugin: the
// configured stdio command, or the bridge running the Python entry point.
func (h *Host) launch(plugin PluginConfig) ([]string, []string, error) {
	if plugin.EntryPoint == "" {
		return plugin.Argv(), plugin.Env, nil
	}
	script, err := h.bridgeScript()
	if err != nil {
		return nil, nil, err
	}
	interpreter, err := resolveInterpreter(plugin.Interpreter)
	if err != nil {
		return nil, nil, err
	}
	argv := make([]string, 0, len(interpreter)+4)
	argv = append(argv, interpreter...)
	argv = append(argv, "-u", script, plugin.EntryPoint)
	return argv, bridgeEnv(plugin.Env, plugin.PythonPath), nil
}

// bridgeScript materializes bridge.py once per host. The temp directory is
// removed by Stop; a script path (rather than `python -c`) keeps tracebacks
// meaningful and keeps sys.path[0] a directory this package owns instead of
// the serve process's cwd.
func (h *Host) bridgeScript() (string, error) {
	h.bridgeMu.Lock()
	defer h.bridgeMu.Unlock()
	if h.bridgePath != "" {
		return h.bridgePath, nil
	}
	path, dir, err := writeBridgeScript()
	if err != nil {
		return "", err
	}
	h.bridgePath, h.bridgeDir = path, dir
	return path, nil
}

func (h *Host) cleanupBridge() {
	h.bridgeMu.Lock()
	dir := h.bridgeDir
	h.bridgeDir, h.bridgePath = "", ""
	h.bridgeMu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// writeBridgeScript writes the embedded bridge to a fresh private directory
// and returns its path and the directory to remove.
func writeBridgeScript() (string, string, error) {
	dir, err := os.MkdirTemp("", "hive-channel-bridge-")
	if err != nil {
		return "", "", fmt.Errorf("create bridge directory: %w", err)
	}
	path := filepath.Join(dir, "hive_channel_bridge.py")
	if err := os.WriteFile(path, []byte(bridgeSource), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("write bridge script: %w", err)
	}
	return path, dir, nil
}

// resolveInterpreter returns the argv that runs Python: the configured
// interpreter, else HIVE_PYTHON (the same deployment override the playbook
// runner honors), else python3, then python on PATH. A missing interpreter is
// a failed channel, never a fake success.
func resolveInterpreter(configured []string) ([]string, error) {
	if len(configured) > 0 {
		argv := append([]string(nil), configured...)
		for index, part := range argv {
			if strings.TrimSpace(part) == "" {
				return nil, fmt.Errorf("interpreter[%d] is empty", index)
			}
		}
		return argv, nil
	}
	if override := strings.TrimSpace(os.Getenv("HIVE_PYTHON")); override != "" {
		return []string{override}, nil
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return []string{path}, nil
		}
	}
	return nil, errors.New("no Python interpreter found: looked for python3, then python on PATH; set channels.<name>.interpreter or HIVE_PYTHON")
}

// bridgeEnv builds the environment overrides for the bridge: the section's
// env, with python_path prepended to PYTHONPATH and unbuffered UTF-8 stdio.
// The configured PYTHONPATH (section env) is the base; otherwise the inherited
// one is. Other inherited variables pass through untouched, exactly like the
// stdio plugin path, because a channel needs its own credentials.
func bridgeEnv(overrides, pythonPath []string) []string {
	env := append([]string(nil), overrides...)
	base := envValue(env, "PYTHONPATH")
	if base == "" {
		base = os.Getenv("PYTHONPATH")
	}
	entries := make([]string, 0, len(pythonPath)+1)
	for _, entry := range pythonPath {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}
	if base != "" {
		entries = append(entries, base)
	}
	if len(entries) > 0 {
		env = setEnvEntry(env, "PYTHONPATH", strings.Join(entries, string(os.PathListSeparator)))
	}
	env = setEnvEntry(env, "PYTHONUNBUFFERED", "1")
	env = setEnvEntry(env, "PYTHONIOENCODING", "utf-8")
	return env
}

// envValue returns the last value for key in an override list.
func envValue(env []string, key string) string {
	for index := len(env) - 1; index >= 0; index-- {
		name, value, found := strings.Cut(env[index], "=")
		if found && name == key {
			return value
		}
	}
	return ""
}

// setEnvEntry replaces (or appends) one KEY=value entry.
func setEnvEntry(env []string, key, value string) []string {
	out := env[:0]
	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if found && name == key {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}

// DiscoverEntryPoints lists the hive.channels entry points installed for one
// interpreter. It runs the same bridge in --list mode, so discovery observes
// exactly the interpreter, site-packages and PYTHONPATH a start would.
//
// Upstream discovers installed entry points even when no section enables them
// (they appear as "off"). This Go host keeps its documented configured-only
// state map and exposes discovery to callers that want the installed set
// without starting a channel.
//
// Parity: framework/server/channels.py::_installed
func DiscoverEntryPoints(ctx context.Context, interpreter []string, pythonPath []string) ([]EntryPointInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolveInterpreter(interpreter)
	if err != nil {
		return nil, err
	}
	script, dir, err := writeBridgeScript()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	args := make([]string, 0, len(resolved)+3)
	args = append(args, resolved[1:]...)
	args = append(args, "-u", script, "--list")
	command := exec.CommandContext(ctx, resolved[0], args...)
	command.Env = mergeEnv(os.Environ(), bridgeEnv(nil, pythonPath))
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
			return nil, fmt.Errorf("discover %s entry points: %s", PythonEntryPointGroup,
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("discover %s entry points: %w", PythonEntryPointGroup, err)
	}
	var entries []EntryPointInfo
	if err := json.Unmarshal(bytes.TrimSpace(output), &entries); err != nil {
		return nil, fmt.Errorf("discover %s entry points: bridge printed %q: %w",
			PythonEntryPointGroup, truncateForError(output), err)
	}
	return entries, nil
}

// truncateForError keeps a bad bridge line short in a failed-state message.
func truncateForError(data []byte) string {
	text := strings.TrimSpace(string(data))
	const limit = 200
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}
