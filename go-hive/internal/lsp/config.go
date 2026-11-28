package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultIdleTimeout is how long an unused client/document may stay alive.
const DefaultIdleTimeout = 5 * time.Minute

// MaxConfigFileBytes caps one lsp.json file.
const MaxConfigFileBytes = 1 << 20

// MaxServers caps the merged server map.
const MaxServers = 128

// Settings are the frozen top-level lsp settings.
type Settings struct {
	Enabled            bool `json:"enabled"`
	Lazy               bool `json:"lazy"`
	RequestTimeout     int  `json:"requestTimeout"` // seconds
	FormatOnWrite      bool `json:"formatOnWrite"`
	DiagnosticsOnWrite bool `json:"diagnosticsOnWrite"`
	DiagnosticsOnEdit  bool `json:"diagnosticsOnEdit"`
	Shared             bool `json:"shared"`
}

// DefaultSettings mirrors the frozen config contract.
func DefaultSettings() Settings {
	return Settings{
		Enabled:            true,
		Lazy:               true,
		RequestTimeout:     10,
		FormatOnWrite:      false,
		DiagnosticsOnWrite: true,
		DiagnosticsOnEdit:  false,
		Shared:             false,
	}
}

// RequestTimeoutDuration returns the per-request bound, defaulting to 10s and
// refusing values outside 1..300s.
func (s Settings) RequestTimeoutDuration() time.Duration {
	seconds := s.RequestTimeout
	if seconds < 1 {
		seconds = 10
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// ServerDef is one language-server definition. Required fields are command,
// fileTypes and rootMarkers; everything else is optional.
type ServerDef struct {
	Command         string         `json:"command"`
	Args            []string       `json:"args,omitempty"`
	FileTypes       []string       `json:"fileTypes"`
	RootMarkers     []string       `json:"rootMarkers"`
	LanguageID      string         `json:"languageId,omitempty"`
	InitOptions     map[string]any `json:"initOptions,omitempty"`
	Settings        map[string]any `json:"settings,omitempty"`
	Disabled        bool           `json:"disabled,omitempty"`
	WarmupTimeoutMS int            `json:"warmupTimeoutMs,omitempty"`
	IsLinter        bool           `json:"isLinter,omitempty"`
	WatchFiles      []string       `json:"watchFiles,omitempty"`
	// RootDir optionally pins the server's workspace root to an absolute
	// directory. It exists so a session cwd (for example an isolated queen
	// session directory) can use a server rooted at the real project. When
	// empty, discovery stays cwd-only.
	RootDir string `json:"rootDir,omitempty"`

	// Source records which config level supplied the current definition.
	Source string `json:"-"`
}

func (d *ServerDef) clone() *ServerDef {
	if d == nil {
		return nil
	}
	out := *d
	out.Args = append([]string(nil), d.Args...)
	out.FileTypes = append([]string(nil), d.FileTypes...)
	out.RootMarkers = append([]string(nil), d.RootMarkers...)
	out.WatchFiles = append([]string(nil), d.WatchFiles...)
	if d.InitOptions != nil {
		out.InitOptions = deepCopyMap(d.InitOptions)
	}
	if d.Settings != nil {
		out.Settings = deepCopyMap(d.Settings)
	}
	return &out
}

func deepCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}

// Warmup returns the initialize bound for this server.
func (d *ServerDef) Warmup() time.Duration {
	if d != nil && d.WarmupTimeoutMS > 0 {
		return time.Duration(d.WarmupTimeoutMS) * time.Millisecond
	}
	return 5 * time.Second
}

// SupportsExtension reports whether the definition claims the extension.
func (d *ServerDef) SupportsExtension(ext string) bool {
	ext = strings.ToLower(ext)
	for _, candidate := range d.FileTypes {
		if strings.ToLower(candidate) == ext {
			return true
		}
	}
	return false
}

var shellMetaChars = ";|&$`<>()"

// Validate checks a merged server definition. It is deliberately strict: a
// command is a trust boundary, so shell metacharacters and relative command
// paths are rejected instead of interpreted.
func (d *ServerDef) Validate() error {
	if d == nil {
		return errors.New("server definition is nil")
	}
	command := strings.TrimSpace(d.Command)
	if command == "" {
		return errors.New("command is required")
	}
	if strings.ContainsRune(command, 0) || strings.ContainsAny(command, shellMetaChars+"\r\n\t\"'") {
		return errors.New("command contains shell metacharacters")
	}
	if strings.Contains(command, "/") || strings.Contains(command, `\`) {
		if !filepath.IsAbs(command) {
			return fmt.Errorf("command %q must be a bare name or an absolute path", command)
		}
		for _, part := range strings.Split(filepath.ToSlash(command), "/") {
			if part == ".." {
				return errors.New("command path contains ..")
			}
		}
	}
	if len(d.Args) > 64 {
		return errors.New("args exceeds 64 entries")
	}
	for _, arg := range d.Args {
		if strings.ContainsRune(arg, 0) || strings.ContainsAny(arg, "\r\n") {
			return errors.New("arg contains a control character")
		}
		if len(arg) > 2048 {
			return errors.New("arg exceeds 2048 bytes")
		}
	}
	if len(d.FileTypes) == 0 {
		return errors.New("fileTypes is required")
	}
	for _, ext := range d.FileTypes {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 || len(ext) > 32 {
			return fmt.Errorf("invalid fileType %q", ext)
		}
		if strings.ContainsAny(ext, `/\*?,"' `) {
			return fmt.Errorf("invalid fileType %q", ext)
		}
	}
	if len(d.RootMarkers) == 0 {
		return errors.New("rootMarkers is required")
	}
	for _, marker := range d.RootMarkers {
		if strings.TrimSpace(marker) == "" {
			return errors.New("rootMarkers contains an empty entry")
		}
		if filepath.IsAbs(marker) {
			return fmt.Errorf("root marker %q must be relative", marker)
		}
		for _, part := range strings.Split(filepath.ToSlash(marker), "/") {
			if part == ".." {
				return fmt.Errorf("root marker %q contains ..", marker)
			}
		}
		if strings.ContainsAny(marker, "\r\n\x00") {
			return fmt.Errorf("root marker %q contains a control character", marker)
		}
	}
	if d.LanguageID != "" && strings.ContainsAny(d.LanguageID, " 	\r\n/\\") {
		return errors.New("languageId contains whitespace or a path separator")
	}
	if err := validateRootDirShape(d.RootDir); err != nil {
		return err
	}
	if d.WarmupTimeoutMS < 0 || d.WarmupTimeoutMS > 60000 {
		return errors.New("warmupTimeoutMs must be within 0..60000")
	}
	return nil
}

// MaxRootDirBytes caps a pinned workspace root.
const MaxRootDirBytes = 4096

// validateRootDirShape checks the parts of rootDir that are a trust boundary:
// it must be an absolute path without traversal segments or control characters.
// Directory existence is checked at discovery time so a stale pin is reported
// as an unrooted server instead of silently dropping the definition.
func validateRootDirShape(rootDir string) error {
	if rootDir == "" {
		return nil
	}
	if len(rootDir) > MaxRootDirBytes {
		return fmt.Errorf("rootDir exceeds %d bytes", MaxRootDirBytes)
	}
	if strings.TrimSpace(rootDir) != rootDir {
		return errors.New("rootDir must not have leading or trailing whitespace")
	}
	for _, r := range rootDir {
		if r < 0x20 || r == 0x7f {
			return errors.New("rootDir contains a control character")
		}
	}
	if !filepath.IsAbs(rootDir) {
		return fmt.Errorf("rootDir %q must be absolute", rootDir)
	}
	for _, part := range strings.Split(strings.ReplaceAll(filepath.ToSlash(rootDir), `\`, "/"), "/") {
		if part == ".." {
			return fmt.Errorf("rootDir %q contains ..", rootDir)
		}
	}
	return nil
}

// Config is the merged, read-only configuration for one home.
type Config struct {
	SchemaVersion int
	Settings      Settings
	IdleTimeout   time.Duration
	Servers       map[string]*ServerDef
	Sources       []string
	Warnings      []string

	origins map[string]string
}

// ServerNames returns the configured server names in sorted order.
func (c *Config) ServerNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Origin returns the config level that supplied a server definition.
func (c *Config) Origin(name string) string {
	if c == nil {
		return ""
	}
	return c.origins[name]
}

// ConfigSource is one level of the frozen configuration chain.
type ConfigSource struct {
	Label string
	Path  string
}

// ConfigSources returns the config levels for home. The built-in level has no
// path. Only $HIVE_HOME/lsp.json is read: the session directory is the file
// tools' sandbox root and is agent-writable, and lsp.json names the server
// command, so a session-scoped file must not gate process startup. See
// go_hive/docs/config-roots.html.
func ConfigSources(home string) []ConfigSource {
	sources := []ConfigSource{{Label: "built-in"}}
	if home != "" {
		sources = append(sources, ConfigSource{Label: "user", Path: filepath.Join(home, "lsp.json")})
	}
	return sources
}

type fileConfig struct {
	SchemaVersion *int                       `json:"schema_version"`
	Settings      map[string]json.RawMessage `json:"settings"`
	IdleTimeoutMS *int                       `json:"idleTimeoutMs"`
	Servers       map[string]json.RawMessage `json:"servers"`
}

type serverPatch struct {
	Command         *string
	Args            *[]string
	FileTypes       *[]string
	RootMarkers     *[]string
	LanguageID      *string
	InitOptions     *map[string]any
	Settings        *map[string]any
	Disabled        *bool
	WarmupTimeoutMS *int
	IsLinter        *bool
	WatchFiles      *[]string
	RootDir         *string
}

func parseServerPatch(raw json.RawMessage) (serverPatch, []string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return serverPatch{}, nil, err
	}
	var patch serverPatch
	var unknown []string
	for key, value := range fields {
		switch key {
		case "command":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("command: %w", err)
			}
			patch.Command = &v
		case "args":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("args: %w", err)
			}
			patch.Args = &v
		case "fileTypes":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("fileTypes: %w", err)
			}
			patch.FileTypes = &v
		case "rootMarkers":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("rootMarkers: %w", err)
			}
			patch.RootMarkers = &v
		case "languageId":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("languageId: %w", err)
			}
			patch.LanguageID = &v
		case "initOptions":
			var v map[string]any
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("initOptions: %w", err)
			}
			patch.InitOptions = &v
		case "settings":
			var v map[string]any
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("settings: %w", err)
			}
			patch.Settings = &v
		case "disabled":
			var v bool
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("disabled: %w", err)
			}
			patch.Disabled = &v
		case "warmupTimeoutMs":
			var v int
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("warmupTimeoutMs: %w", err)
			}
			patch.WarmupTimeoutMS = &v
		case "isLinter":
			var v bool
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("isLinter: %w", err)
			}
			patch.IsLinter = &v
		case "watchFiles":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("watchFiles: %w", err)
			}
			patch.WatchFiles = &v
		case "rootDir":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return patch, unknown, fmt.Errorf("rootDir: %w", err)
			}
			patch.RootDir = &v
		default:
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return patch, unknown, nil
}

func applyPatch(base *ServerDef, patch serverPatch) *ServerDef {
	out := base.clone()
	if out == nil {
		out = &ServerDef{}
	}
	if patch.Command != nil {
		out.Command = *patch.Command
	}
	if patch.Args != nil {
		out.Args = append([]string(nil), (*patch.Args)...)
	}
	if patch.FileTypes != nil {
		out.FileTypes = append([]string(nil), (*patch.FileTypes)...)
	}
	if patch.RootMarkers != nil {
		out.RootMarkers = append([]string(nil), (*patch.RootMarkers)...)
	}
	if patch.LanguageID != nil {
		out.LanguageID = *patch.LanguageID
	}
	if patch.InitOptions != nil {
		// Object-valued fields replace whole; they are never deep-merged.
		out.InitOptions = deepCopyMap(*patch.InitOptions)
	}
	if patch.Settings != nil {
		out.Settings = deepCopyMap(*patch.Settings)
	}
	if patch.Disabled != nil {
		out.Disabled = *patch.Disabled
	}
	if patch.WarmupTimeoutMS != nil {
		out.WarmupTimeoutMS = *patch.WarmupTimeoutMS
	}
	if patch.IsLinter != nil {
		out.IsLinter = *patch.IsLinter
	}
	if patch.WatchFiles != nil {
		out.WatchFiles = append([]string(nil), (*patch.WatchFiles)...)
	}
	if patch.RootDir != nil {
		out.RootDir = *patch.RootDir
	}
	return out
}

func applySettings(dst *Settings, raw map[string]json.RawMessage, source string, warnings *[]string) {
	if raw == nil {
		return
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := raw[key]
		switch key {
		case "enabled", "lazy", "formatOnWrite", "diagnosticsOnWrite", "diagnosticsOnEdit", "shared":
			var b bool
			if err := json.Unmarshal(value, &b); err != nil {
				*warnings = append(*warnings, fmt.Sprintf("%s: settings.%s must be a boolean; ignored", source, key))
				continue
			}
			switch key {
			case "enabled":
				dst.Enabled = b
			case "lazy":
				dst.Lazy = b
			case "formatOnWrite":
				dst.FormatOnWrite = b
			case "diagnosticsOnWrite":
				dst.DiagnosticsOnWrite = b
			case "diagnosticsOnEdit":
				dst.DiagnosticsOnEdit = b
			case "shared":
				dst.Shared = b
			}
		case "requestTimeout":
			seconds, ok := parseJSONInt(value)
			if !ok || seconds < 1 || seconds > 300 {
				*warnings = append(*warnings, fmt.Sprintf("%s: settings.requestTimeout must be an integer within 1..300 seconds; ignored", source))
				continue
			}
			dst.RequestTimeout = seconds
		default:
			*warnings = append(*warnings, fmt.Sprintf("%s: unknown setting %q ignored", source, key))
		}
	}
}

func parseJSONInt(raw json.RawMessage) (int, bool) {
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// LoadConfig loads and merges the built-in defaults and the user level for
// home. It is the non-caching entry point; Service uses Loader for the
// per-home cache. There is no project/session level (see ConfigSources).
func LoadConfig(home string) (*Config, error) {
	if home == "" {
		home = os.Getenv("HIVE_HOME")
	}
	return loadConfig(home, nil)
}

// loadConfig merges embedded defaults and the user level. The embedded bytes
// parameter is injectable for tests. An empty home leaves only the built-in
// catalog.
func loadConfig(home string, builtin []byte) (*Config, error) {
	cfg := &Config{
		Settings:    DefaultSettings(),
		IdleTimeout: DefaultIdleTimeout,
		Servers:     map[string]*ServerDef{},
		origins:     map[string]string{},
	}
	if builtin == nil {
		builtin = embeddedDefaults
	}
	if err := mergeFile(cfg, builtin, "built-in", "built-in"); err != nil {
		return nil, fmt.Errorf("lsp: embedded catalog: %w", err)
	}
	cfg.Sources = []string{"built-in"}

	for _, source := range ConfigSources(home) {
		if source.Label == "built-in" {
			continue
		}
		merged, err := mergeConfigFile(cfg, source)
		if err != nil {
			return nil, err
		}
		if merged {
			cfg.Sources = append(cfg.Sources, source.Path)
		}
	}
	return cfg, nil
}

func mergeConfigFile(cfg *Config, source ConfigSource) (bool, error) {
	data, err := readConfigFile(source.Path)
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: %v; ignored", source.Label, err))
		return false, nil
	}
	if data == nil {
		return false, nil
	}
	if err := mergeFile(cfg, data, source.Label, source.Path); err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: %v; ignored", source.Label, err))
		return false, nil
	}
	return true, nil
}

func readConfigFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxConfigFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, MaxConfigFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), nil
}

func mergeFile(cfg *Config, data []byte, label, path string) error {
	if len(data) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var file fileConfig
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if file.SchemaVersion != nil && *file.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", *file.SchemaVersion)
	}
	if file.SchemaVersion != nil {
		cfg.SchemaVersion = *file.SchemaVersion
	}
	if file.IdleTimeoutMS != nil {
		if *file.IdleTimeoutMS < 1000 || *file.IdleTimeoutMS > 24*60*60*1000 {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: idleTimeoutMs outside 1000..86400000; ignored", label))
		} else {
			cfg.IdleTimeout = time.Duration(*file.IdleTimeoutMS) * time.Millisecond
		}
	}
	applySettings(&cfg.Settings, file.Settings, label, &cfg.Warnings)

	names := make([]string, 0, len(file.Servers))
	for name := range file.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := file.Servers[name]
		if err := validateServerName(name); err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: ignoring server %q: %v", label, name, err))
			continue
		}
		patch, unknown, err := parseServerPatch(raw)
		if err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: server %q invalid (%v); ignored", label, name, err))
			continue
		}
		for _, field := range unknown {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: server %q has unknown field %q; ignored", label, name, field))
		}
		previous := cfg.Servers[name]
		merged := applyPatch(previous, patch)
		if err := merged.Validate(); err != nil {
			if previous != nil {
				cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: server %q override invalid (%v); keeping previous definition", label, name, err))
				continue
			}
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: ignoring server %q: %v", label, name, err))
			continue
		}
		if previous == nil && len(cfg.Servers) >= MaxServers {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s: ignoring server %q: server cap %d reached", label, name, MaxServers))
			continue
		}
		merged.Source = label
		cfg.Servers[name] = merged
		cfg.origins[name] = label
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing content: %w", err)
	}
	return nil
}

func validateServerName(name string) error {
	if name == "" || len(name) > 64 {
		return errors.New("name must be 1..64 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return fmt.Errorf("invalid character %q", r)
		}
	}
	return nil
}

// Loader caches the merged configuration for one home. The config no longer
// depends on the session directory, so a single cached value is enough.
// Reload clears it and re-reads $HIVE_HOME/lsp.json.
type Loader struct {
	home string

	mu      sync.Mutex
	cached  *Config
	builtin []byte
}

// NewLoader returns a loader for home. An empty home reads HIVE_HOME.
func NewLoader(home string) *Loader {
	if home == "" {
		home = os.Getenv("HIVE_HOME")
	}
	return &Loader{home: home, builtin: embeddedDefaults}
}

// Home returns the user config root.
func (l *Loader) Home() string { return l.home }

// Load returns the cached merged config for the loader's home.
func (l *Loader) Load() (*Config, error) {
	l.mu.Lock()
	if l.cached != nil {
		cfg := l.cached
		l.mu.Unlock()
		return cfg, nil
	}
	l.mu.Unlock()

	cfg, err := loadConfig(l.home, l.builtin)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cached = cfg
	l.mu.Unlock()
	return cfg, nil
}

// Reload clears the cache and re-reads the user config.
func (l *Loader) Reload() (*Config, error) {
	l.mu.Lock()
	l.cached = nil
	l.mu.Unlock()
	return l.Load()
}

// ReloadAll clears the cache.
func (l *Loader) ReloadAll() {
	l.mu.Lock()
	l.cached = nil
	l.mu.Unlock()
}
