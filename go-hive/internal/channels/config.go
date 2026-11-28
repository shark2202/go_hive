package channels

// Plugin discovery and configuration. Upstream finds channels through the
// ``hive.channels`` entry-point group and starts the ones whose
// ``configuration.json`` section carries a truthy ``enabled``. Go has no
// in-process Python loader, so a section declares one of two runtimes:
//
//	entry_point  run an installed Python ``hive.channels`` entry point through
//	             the embedded bridge (opt-in; see python.go and doc.go)
//	command      run a Go stdio plugin: argv (array, or the executable as a
//	             string), optional args, cwd and env
//
// The two are mutually exclusive. Python plumbing (interpreter, python_path)
// is only read when entry_point is set, so a plugin without Python keeps
// working with no interpreter configured.
//
// Parity: framework/server/channels.py::start_channels (the section shape and
// the enabled gate)
// Parity-exempt: command/args/cwd/env and the entry_point bridge are the Go
// extension; an enabled section with neither is reported failed by the host
// instead of being silently dropped.

import (
	"fmt"
	"sort"
	"strings"
)

// PluginConfig is one configured channel, resolved to what the host needs to
// spawn it. Section is the untouched “channels.<name>“ object handed to the
// plugin in the hello frame.
type PluginConfig struct {
	Name    string
	Enabled bool
	Command []string
	Args    []string
	Cwd     string
	Env     []string
	Section map[string]any

	// EntryPoint is the installed hive.channels entry point to run through
	// the Python bridge. Empty means a Go stdio command plugin.
	EntryPoint string
	// Interpreter is the Python argv for a bridge plugin. Empty resolves
	// HIVE_PYTHON, then python3, then python on PATH.
	Interpreter []string
	// PythonPath entries are prepended to the bridge's PYTHONPATH.
	PythonPath []string

	// ConfigError is set when Enabled is true but the Go extension fields
	// are malformed (for example command holds a non-string element). The
	// host reports this as a failed state; it never guesses a command.
	ConfigError string
}

// Argv is the executable followed by its configured arguments.
func (p PluginConfig) Argv() []string {
	if len(p.Command) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Command)+len(p.Args))
	out = append(out, p.Command...)
	out = append(out, p.Args...)
	return out
}

// Plugins reads the “channels“ map into sorted plugin configs. A missing or
// non-object section reads as none; a non-object entry is skipped, exactly
// like a channel that is not installed.
//
// Parity: framework/server/channels.py::start_channels
func Plugins(config map[string]any) []PluginConfig {
	raw, ok := config["channels"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]PluginConfig, 0, len(names))
	for _, name := range names {
		section, ok := raw[name].(map[string]any)
		if !ok {
			continue
		}
		plugin := PluginConfig{
			Name:    name,
			Enabled: truthy(section["enabled"]),
			Section: section,
		}
		plugin.Command, plugin.ConfigError = stringListField(section, "command", true)
		if plugin.ConfigError == "" {
			var argsErr string
			plugin.Args, argsErr = stringListField(section, "args", false)
			plugin.ConfigError = argsErr
		}
		if plugin.ConfigError == "" {
			var envErr string
			plugin.Env, envErr = stringListField(section, "env", false)
			plugin.ConfigError = envErr
		}
		if plugin.ConfigError == "" {
			plugin.EntryPoint, plugin.ConfigError = stringField(section, "entry_point")
		}
		if plugin.ConfigError == "" {
			var interpreterErr string
			plugin.Interpreter, interpreterErr = stringListField(section, "interpreter", false)
			plugin.ConfigError = interpreterErr
		}
		if plugin.ConfigError == "" {
			var pathErr string
			plugin.PythonPath, pathErr = stringListField(section, "python_path", false)
			plugin.ConfigError = pathErr
		}
		if cwd, isString := section["cwd"].(string); isString {
			plugin.Cwd = strings.TrimSpace(cwd)
		}
		if plugin.ConfigError == "" && plugin.EntryPoint != "" {
			// One section, one runtime. command/args belong to the stdio
			// plugin contract and are never silently ignored.
			switch {
			case len(plugin.Command) > 0:
				plugin.ConfigError = "command and entry_point are mutually exclusive"
			case len(plugin.Args) > 0:
				plugin.ConfigError = "args is only valid with command"
			}
		}
		if plugin.ConfigError == "" && plugin.EntryPoint == "" {
			switch {
			case len(plugin.Interpreter) > 0:
				plugin.ConfigError = "interpreter is only valid with entry_point"
			case len(plugin.PythonPath) > 0:
				plugin.ConfigError = "python_path is only valid with entry_point"
			}
		}
		if plugin.ConfigError == "" && plugin.Enabled && plugin.EntryPoint == "" && len(plugin.Command) == 0 && len(plugin.Args) > 0 {
			plugin.ConfigError = "args given without command"
		}
		if argv := plugin.Argv(); plugin.ConfigError == "" && plugin.Enabled && plugin.EntryPoint == "" && len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
			plugin.ConfigError = "command[0] is empty"
		}
		if plugin.ConfigError != "" {
			plugin.ConfigError = "channels." + name + ": " + plugin.ConfigError
		}
		out = append(out, plugin)
	}
	return out
}

// stringField reads an optional string field. Present-but-empty or a
// non-string value is an error rather than a silent default: a section that
// says entry_point but cannot name one must fail loudly.
func stringField(section map[string]any, key string) (string, string) {
	raw, present := section[key]
	if !present || raw == nil {
		return "", ""
	}
	text, isString := raw.(string)
	if !isString {
		return "", key + " must be a string"
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", key + " is empty"
	}
	return trimmed, ""
}

// stringListField reads a field that may be a string or an array of strings.
// required marks “command“, which may be a bare executable string.
func stringListField(section map[string]any, key string, required bool) ([]string, string) {
	raw, present := section[key]
	if !present || raw == nil {
		return nil, ""
	}
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			if required {
				return nil, "command is empty"
			}
			return nil, ""
		}
		return []string{trimmed}, ""
	case []string:
		return append([]string(nil), value...), ""
	case []any:
		out := make([]string, 0, len(value))
		for index, entry := range value {
			text, isString := entry.(string)
			if !isString {
				return nil, fmt.Sprintf("%s[%d] must be a string", key, index)
			}
			out = append(out, text)
		}
		return out, ""
	default:
		return nil, fmt.Sprintf("%s must be a string or an array of strings", key)
	}
}

// validChannelName rejects a configuration key that would step outside the
// state directory (a channel name is a directory name under $HIVE_HOME/channels).
func validChannelName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

// truthy is Python's bool(): JSON true, non-zero numbers, non-empty strings
// and non-empty collections count. Upstream's gate is
// “section.get("enabled")“.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
