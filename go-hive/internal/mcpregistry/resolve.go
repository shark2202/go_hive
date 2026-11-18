package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools"
)

type ServerConfig struct {
	Name        string
	Transport   string
	Command     *string
	Args        []string
	Env         map[string]string
	Cwd         *string
	URL         *string
	Headers     map[string]string
	SocketPath  *string
	Description string
}

func (c *ServerConfig) ToDict() map[string]any {
	out := map[string]any{
		"name":        c.Name,
		"transport":   c.Transport,
		"command":     nullableString(c.Command),
		"args":        stringSliceToAny(c.Args),
		"env":         stringMapToAny(c.Env),
		"cwd":         nullableString(c.Cwd),
		"url":         nullableString(c.URL),
		"headers":     stringMapToAny(c.Headers),
		"socket_path": nullableString(c.SocketPath),
		"description": c.Description,
	}
	return out
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

type ResolveOptions struct {
	Include  []string
	Tags     []string
	Exclude  []string
	Profile  string
	MaxTools *int
	Versions map[string]string
}

func (r *Registry) ResolveForAgent(opts ResolveOptions) ([]*ServerConfig, error) {
	installed, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	servers := asMap(installed["servers"])
	index, err := r.readCachedIndex()
	if err != nil {
		return nil, err
	}
	excludeSet := map[string]struct{}{}
	for _, name := range opts.Exclude {
		excludeSet[name] = struct{}{}
	}

	var profileMatched []string
	if opts.Profile == "all" {
		var names []string
		for name := range tools.HarnessGroups {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, excluded := excludeSet[name]; excluded {
				continue
			}
			profileMatched = append(profileMatched, name)
		}
	}
	if opts.Profile != "" {
		for _, name := range sortedKeys(servers) {
			if _, excluded := excludeSet[name]; excluded {
				continue
			}
			if opts.Profile == "all" {
				profileMatched = append(profileMatched, name)
				continue
			}
			manifest := r.effectiveManifest(name, asMap(servers[name]), index)
			profiles := asStringSlice(asMap(manifest["hive"])["profiles"])
			if contains(profiles, opts.Profile) {
				profileMatched = append(profileMatched, name)
			}
		}
	}

	var tagMatched []string
	if len(opts.Tags) > 0 {
		want := map[string]struct{}{}
		for _, tag := range opts.Tags {
			want[tag] = struct{}{}
		}
		for _, name := range sortedKeys(servers) {
			if _, excluded := excludeSet[name]; excluded {
				continue
			}
			manifest := r.effectiveManifest(name, asMap(servers[name]), index)
			for _, tag := range asStringSlice(manifest["tags"]) {
				if _, ok := want[tag]; ok {
					tagMatched = append(tagMatched, name)
					break
				}
			}
		}
	}

	seen := map[string]struct{}{}
	var ordered []string
	appendName := func(name string) {
		if _, excluded := excludeSet[name]; excluded {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		ordered = append(ordered, name)
	}
	for _, name := range opts.Include {
		appendName(name)
	}
	for _, name := range profileMatched {
		appendName(name)
	}
	for _, name := range tagMatched {
		appendName(name)
	}

	configs := make([]*ServerConfig, 0, len(ordered))
	totalTools := 0
	for _, name := range ordered {
		if description, ok := tools.HarnessGroups[name]; ok {
			configs = append(configs, &ServerConfig{
				Name:        name,
				Transport:   "harness",
				Description: description,
			})
			continue
		}
		entry := asMap(servers[name])
		if entry == nil {
			continue
		}
		if !getTruthy(entry, "enabled", true) {
			continue
		}
		if asString(entry["mode"]) == "cli" {
			continue
		}
		if len(opts.Versions) > 0 {
			if want, ok := opts.Versions[name]; ok {
				have := stringOr(entry["manifest_version"], "0.0.0")
				if want != have {
					continue
				}
			}
		}
		manifest := r.effectiveManifest(name, entry, index)
		var count int
		if list, ok := manifest["tools"].([]any); ok {
			count = len(list)
		}
		if opts.MaxTools != nil {
			if count == 0 {
			} else if totalTools+count > *opts.MaxTools {
				continue
			}
		}
		config := manifestToServerConfig(name, manifest, asMap(entry["overrides"]), asString(entry["transport"]))
		if config == nil {
			continue
		}
		configs = append(configs, config)
		totalTools += count
	}
	return configs, nil
}

func manifestToServerConfig(name string, manifest map[string]any, overrides map[string]any, transportOverride string) *ServerConfig {
	if manifest == nil {
		manifest = map[string]any{}
	}
	transport := transportOverride
	if transport == "" {
		transport = stringOr(asMap(manifest["transport"])["default"], "stdio")
	}
	description := stringOr(manifest["description"], "")
	cfg := &ServerConfig{
		Name:        name,
		Transport:   transport,
		Description: description,
		Args:        []string{},
		Env:         map[string]string{},
		Headers:     map[string]string{},
	}
	overrideEnv := asStringMap(overrides["env"])
	overrideHeaders := asStringMap(overrides["headers"])
	switch transport {
	case "stdio":
		stdio := asMap(manifest["stdio"])
		if stdio == nil {
			stdio = map[string]any{}
		}
		cfg.Env = mergeStringMaps(asStringMap(stdio["env"]), overrideEnv)
		cfg.Command = strField(stdio, "command", nil)
		cfg.Args = asStringSlice(stdio["args"])
		if cfg.Args == nil {
			cfg.Args = []string{}
		}
		cfg.Cwd = strField(stdio, "cwd", nil)
	case "http":
		httpCfg := asMap(manifest["http"])
		if httpCfg == nil {
			httpCfg = map[string]any{}
		}
		cfg.URL = strField(httpCfg, "url", strPtr(""))
		cfg.Headers = mergeStringMaps(asStringMap(httpCfg["headers"]), overrideHeaders)
	case "unix":
		unixCfg := asMap(manifest["unix"])
		if unixCfg == nil {
			unixCfg = map[string]any{}
		}
		httpCfg := asMap(manifest["http"])
		if httpCfg == nil {
			httpCfg = map[string]any{}
		}
		cfg.SocketPath = strField(unixCfg, "socket_path", nil)
		if urlValue := stringOr(httpCfg["url"], ""); urlValue != "" {
			cfg.URL = strPtr(urlValue)
		} else {
			cfg.URL = strPtr("http://localhost")
		}
		cfg.Headers = mergeStringMaps(asStringMap(httpCfg["headers"]), overrideHeaders)
	case "sse":
		sse := asMap(manifest["sse"])
		if sse == nil {
			sse = map[string]any{}
		}
		cfg.URL = strField(sse, "url", strPtr(""))
		cfg.Headers = mergeStringMaps(asStringMap(sse["headers"]), overrideHeaders)
	default:
		return nil
	}
	return cfg
}

var expectedSelectionTypes = map[string]string{
	"include":   "list",
	"tags":      "list",
	"exclude":   "list",
	"profile":   "str",
	"max_tools": "int",
	"versions":  "dict",
}

func (r *Registry) LoadAgentSelection(agentPath string) ([]map[string]any, *int, error) {
	selectionPath := filepath.Join(agentPath, "mcp_registry.json")
	if !fileExists(selectionPath) {
		return []map[string]any{}, nil, nil
	}
	raw, err := os.ReadFile(selectionPath)
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var selection map[string]any
	if err := dec.Decode(&selection); err != nil {
		return nil, nil, err
	}

	opts := ResolveOptions{}
	for _, key := range sortedStringKeys(expectedSelectionTypes) {
		value, present := selection[key]
		if !present || value == nil {
			continue
		}
		switch key {
		case "include":
			if list, ok := value.([]any); ok {
				opts.Include = asStringSlice(list)
			}
		case "tags":
			if list, ok := value.([]any); ok {
				opts.Tags = asStringSlice(list)
			}
		case "exclude":
			if list, ok := value.([]any); ok {
				opts.Exclude = asStringSlice(list)
			}
		case "profile":
			if s, ok := value.(string); ok {
				opts.Profile = s
			}
		case "max_tools":
			if n, ok := intValue(value); ok {
				opts.MaxTools = &n
			}
		case "versions":
			if m, ok := value.(map[string]any); ok {
				versions := make(map[string]string, len(m))
				for name, v := range m {
					versions[name] = pyStr(v)
				}
				opts.Versions = versions
			}
		}
	}

	configs, err := r.ResolveForAgent(opts)
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, cfg.ToDict())
	}
	return out, opts.MaxTools, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
