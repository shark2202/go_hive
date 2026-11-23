package channels

// Plugin discovery and configuration. Upstream finds channels through the
// ``hive.channels`` entry-point group and starts the ones whose
// ``configuration.json`` section carries a truthy ``enabled``. Go has no
// entry points, so a plugin is declared by a Go stdio extension in the same
// section: ``command`` (argv array, or the executable as a string), optional
// ``args``, ``cwd`` and ``env``.
//
// Parity: framework/server/channels.py::start_channels (the section shape and
// the enabled gate)
// Parity-exempt: command/args/cwd/env are the Go stdio-plugin extension; an
// enabled section without one is reported failed by the host instead of being
// silently dropped.

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
		if cwd, isString := section["cwd"].(string); isString {
			plugin.Cwd = strings.TrimSpace(cwd)
		}
		if plugin.ConfigError == "" && plugin.Enabled && len(plugin.Command) == 0 && len(plugin.Args) > 0 {
			plugin.ConfigError = "args given without command"
		}
		if argv := plugin.Argv(); plugin.ConfigError == "" && plugin.Enabled && len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
			plugin.ConfigError = "command[0] is empty"
		}
		if plugin.ConfigError != "" {
			plugin.ConfigError = "channels." + name + ": " + plugin.ConfigError
		}
		out = append(out, plugin)
	}
	return out
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
