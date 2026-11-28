package lsp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func stubLookPath(paths map[string]string) LookPathFunc {
	return func(name string) (string, error) {
		if path, ok := paths[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

// Parity-exempt: the embedded LSP catalog and its representative-subset rule
// are defined by docs/lsp-design.json catalog/risks; no upstream catalog test.
func TestEmbeddedCatalogRepresentatives(t *testing.T) {
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	want := []string{
		"gopls", "pyright", "rust-analyzer", "typescript-language-server",
		"clangd", "yamlls", "bashls",
	}
	if len(cfg.Servers) != len(want) {
		t.Fatalf("catalog size = %d, want %d (update status text if this changes)", len(cfg.Servers), len(want))
	}
	for _, name := range want {
		def, ok := cfg.Servers[name]
		if !ok {
			t.Fatalf("missing representative %q", name)
		}
		if err := def.Validate(); err != nil {
			t.Fatalf("%s invalid: %v", name, err)
		}
		if strings.TrimSpace(def.Command) == "" || len(def.FileTypes) == 0 || len(def.RootMarkers) == 0 {
			t.Fatalf("%s missing required fields: %+v", name, def)
		}
	}
	if EmbeddedCatalogSize() != len(want) {
		t.Fatalf("EmbeddedCatalogSize = %d, want %d", EmbeddedCatalogSize(), len(want))
	}
	if CatalogTarget != 55 {
		t.Fatalf("CatalogTarget = %d; design target is 55", CatalogTarget)
	}
}

// Parity-exempt: the user-over-built-in precedence, shallow merge and
// object-replacement contract is new LSP config work from
// docs/lsp-design.json R3/A5. There is no session-level config level.
func TestConfigPrecedenceShallowMergeAndObjectReplacement(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"requestTimeout": 7, "lazy": false},
		"servers": map[string]any{
			"gopls": map[string]any{
				"args":        []string{"user-serve"},
				"initOptions": map[string]any{"a": 1, "b": 2},
			},
			"custom": map[string]any{
				"command":     "/usr/bin/custom-ls",
				"fileTypes":   []string{".xyz"},
				"rootMarkers": []string{"x.marker"},
			},
		},
	})

	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Settings.RequestTimeout != 7 {
		t.Fatalf("requestTimeout = %d, want 7", cfg.Settings.RequestTimeout)
	}
	if cfg.Settings.Lazy {
		t.Fatal("lazy should be false from the user config")
	}
	gopls := cfg.Servers["gopls"]
	if gopls == nil {
		t.Fatal("gopls missing")
	}
	if len(gopls.Args) != 1 || gopls.Args[0] != "user-serve" {
		t.Fatalf("args = %v; user override did not apply", gopls.Args)
	}
	if len(gopls.InitOptions) != 2 || gopls.InitOptions["a"] != float64(1) || gopls.InitOptions["b"] != float64(2) {
		t.Fatalf("initOptions = %v, want a=1 b=2", gopls.InitOptions)
	}
	if len(gopls.FileTypes) != 4 {
		t.Fatalf("fileTypes = %v; built-in values should survive an override that omits them", gopls.FileTypes)
	}
	if cfg.Origin("gopls") != "user" {
		t.Fatalf("origin = %q, want user", cfg.Origin("gopls"))
	}
	custom := cfg.Servers["custom"]
	if custom == nil || cfg.Origin("custom") != "user" {
		t.Fatalf("custom server/origin = %+v / %q", custom, cfg.Origin("custom"))
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("sources = %v, want built-in + user", cfg.Sources)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", cfg.Warnings)
	}
}

// Parity-exempt: invalid-new vs invalid-override retention is the frozen
// config rule in docs/lsp-design.json D3/R3; no upstream test.
func TestConfigInvalidOverrideRetainsPrevious(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"servers": map[string]any{
			"gopls": map[string]any{"fileTypes": []string{}},
			"bad":   map[string]any{"command": "bad", "fileTypes": []string{}, "rootMarkers": []string{"go.mod"}},
		},
	})
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	gopls := cfg.Servers["gopls"]
	if gopls == nil || len(gopls.FileTypes) != 4 {
		t.Fatalf("gopls definition lost after invalid override: %+v", gopls)
	}
	if _, ok := cfg.Servers["bad"]; ok {
		t.Fatal("invalid new server was accepted")
	}
	joined := strings.Join(cfg.Warnings, "\n")
	if !strings.Contains(joined, "keeping previous definition") {
		t.Fatalf("missing override-retention warning: %v", cfg.Warnings)
	}
	if !strings.Contains(joined, `ignoring server "bad"`) {
		t.Fatalf("missing invalid-new warning: %v", cfg.Warnings)
	}
}

// Parity-exempt: command/config validation is the LSP trust-boundary rule
// from docs/lsp-design.json R10/A11.
func TestConfigRejectsForbiddenCommands(t *testing.T) {
	cases := []string{
		"gopls; rm -rf /",
		"gopls && evil",
		"./gopls",
		"../bin/gopls",
		"gopls\n--evil",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			home := t.TempDir()
			writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
				"servers": map[string]any{
					"evil": map[string]any{
						"command":     command,
						"fileTypes":   []string{".evil"},
						"rootMarkers": []string{"go.mod"},
					},
				},
			})
			cfg, err := LoadConfig(home)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if _, ok := cfg.Servers["evil"]; ok {
				t.Fatalf("command %q was accepted", command)
			}
			if len(cfg.Warnings) == 0 {
				t.Fatalf("command %q rejected without a warning", command)
			}
		})
	}
}

// Parity-exempt: cwd-only root-marker discovery is frozen by
// docs/lsp-design.json R3/D3; there is no Python discovery test.
func TestDiscoveryRootMarkerIsCwdOnly(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		t.Fatal(err)
	}
	look := stubLookPath(map[string]string{"gopls": "/fake/gopls"})
	if disc := findDiscovery(DiscoverServers(child, cfg, look), "gopls"); disc.Rooted {
		t.Fatal("child cwd inherited a parent go.mod; discovery must be cwd-only")
	}
	if disc := findDiscovery(DiscoverServers(parent, cfg, look), "gopls"); !disc.Rooted {
		t.Fatal("parent cwd should match go.mod")
	}
}

func findDiscovery(discs []Discovery, name string) Discovery {
	for _, disc := range discs {
		if disc.Name == name {
			return disc
		}
	}
	return Discovery{}
}

// Parity-exempt: disabled/unresolved discovery states are config-matrix
// acceptance cases (A5) with no Python counterpart.
func TestDiscoveryDisabledAndMissingBinary(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "pyproject.toml"), []byte("[project]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"enabled": false},
		"servers":  map[string]any{"gopls": map[string]any{"disabled": true}},
	})
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Enabled {
		t.Fatal("enabled=false did not apply")
	}
	discs := DiscoverServers(cwd, cfg, stubLookPath(nil))
	gopls := findDiscovery(discs, "gopls")
	if !gopls.Definition.Disabled || gopls.State() != "disabled" {
		t.Fatalf("gopls state = %s, want disabled", gopls.State())
	}
	pyright := findDiscovery(discs, "pyright")
	if pyright.Available() {
		t.Fatal("pyright available without a binary")
	}
	if pyright.State() != "missing" {
		t.Fatalf("pyright state = %s, want missing", pyright.State())
	}
	if !strings.Contains(pyright.Reason, "not found") {
		t.Fatalf("reason = %q", pyright.Reason)
	}
}

// Parity-exempt: local-bin-before-PATH resolution is the frozen discovery rule
// in docs/lsp-design.json R3/D3.
func TestDiscoveryLocalBinBeforePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit semantics differ on Windows")
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localBin := filepath.Join(cwd, ".hive", "bin", "gopls")
	writeExecutable(t, localBin)
	pathBin := filepath.Join(t.TempDir(), "gopls")
	writeExecutable(t, pathBin)
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		t.Fatal(err)
	}
	disc := findDiscovery(DiscoverServers(cwd, cfg, stubLookPath(map[string]string{"gopls": pathBin})), "gopls")
	if disc.CommandPath != localBin {
		t.Fatalf("command = %s, want local bin %s", disc.CommandPath, localBin)
	}
}

// Parity-exempt: config caching and reload are frozen by
// docs/lsp-design.json config_contract.cache/reload; no Python test.
func TestLoaderCacheAndReload(t *testing.T) {
	home := t.TempDir()
	loader := NewLoader(home)
	first, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Settings.Enabled {
		t.Fatal("default should be enabled")
	}
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"enabled": false, "requestTimeout": 3},
	})
	cached, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Settings.Enabled {
		t.Fatal("cache was bypassed before Reload")
	}
	if cached.Settings.RequestTimeout != 10 {
		t.Fatalf("cached requestTimeout = %d", cached.Settings.RequestTimeout)
	}
	reloaded, err := loader.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Settings.Enabled {
		t.Fatal("Reload did not pick up enabled=false")
	}
	if reloaded.Settings.RequestTimeout != 3 {
		t.Fatalf("reloaded requestTimeout = %d, want 3", reloaded.Settings.RequestTimeout)
	}
}

// Parity-exempt: malformed config files must be ignored with a warning, per
// docs/lsp-design.json D3; no upstream test.
func TestConfigMalformedFileIgnored(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "lsp.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Servers) != EmbeddedCatalogSize() {
		t.Fatalf("catalog lost after malformed file: %d", len(cfg.Servers))
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(strings.Join(cfg.Warnings, "\n"), "invalid JSON") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"servers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), "unsupported schema_version") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}
}

// Parity-exempt: the user tier may be selected through HIVE_HOME when no home
// is passed explicitly (frozen config chain); no Python test.
func TestConfigHomeFromEnv(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"requestTimeout": 8},
	})
	t.Setenv("HIVE_HOME", home)
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.RequestTimeout != 8 {
		t.Fatalf("requestTimeout = %d, want 8 from $HIVE_HOME/lsp.json", cfg.Settings.RequestTimeout)
	}
}

// TestConfigIgnoresSessionDirectory proves the session directory is not a
// config source. It is the file tools' sandbox root and is agent-writable, and
// lsp.json names the server command, so <sessionDir>/.hive/lsp.json and
// .hive/lsp.local.json must not change the operator-level config.
//
// Parity-exempt: Go-only security regression test; no Python counterpart.
func TestConfigIgnoresSessionDirectory(t *testing.T) {
	home := t.TempDir()
	session := t.TempDir()
	writeJSON(t, filepath.Join(session, ".hive", "lsp.json"), map[string]any{
		"settings": map[string]any{"enabled": false, "requestTimeout": 3},
		"servers": map[string]any{
			"evil": map[string]any{
				"command":     "evil-ls",
				"fileTypes":   []string{".evil"},
				"rootMarkers": []string{"x"},
			},
		},
	})
	writeJSON(t, filepath.Join(session, ".hive", "lsp.local.json"), map[string]any{
		"settings": map[string]any{"enabled": false},
	})

	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Settings.Enabled {
		t.Fatal("session-dir config disabled lsp")
	}
	if cfg.Settings.RequestTimeout != 10 {
		t.Fatalf("requestTimeout = %d; session-dir override applied", cfg.Settings.RequestTimeout)
	}
	if _, ok := cfg.Servers["evil"]; ok {
		t.Fatal("session-dir server definition was loaded")
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0] != "built-in" {
		t.Fatalf("sources = %v, want only built-in", cfg.Sources)
	}
}

// Parity-exempt: requestTimeout bounds are part of the frozen settings
// contract (config_contract.settings); no Python test.
func TestConfigRequestTimeoutBounds(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"requestTimeout": 999},
	})
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.RequestTimeout != 10 {
		t.Fatalf("out-of-range timeout = %d, want default 10", cfg.Settings.RequestTimeout)
	}
	if cfg.Settings.RequestTimeoutDuration() != 10*time.Second {
		t.Fatalf("duration = %s", cfg.Settings.RequestTimeoutDuration())
	}
}

// Parity-exempt: optional per-server rootDir parsing, precedence and clearing
// are new LSP config work for docs/lsp-design.json R3/D3; no Python source.
func TestConfigRootDirParsingAndClearing(t *testing.T) {
	module := t.TempDir()
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"servers": map[string]any{"gopls": map[string]any{"rootDir": module}},
	})

	// The user level can pin a server workspace.
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Servers["gopls"].RootDir; got != module {
		t.Fatalf("user rootDir = %q, want %q", got, module)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}

	// An explicit empty string clears the pin instead of inheriting it.
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"servers": map[string]any{"gopls": map[string]any{"rootDir": ""}},
	})
	cfg, err = LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Servers["gopls"].RootDir; got != "" {
		t.Fatalf("cleared rootDir = %q, want empty", got)
	}
	if err := cfg.Servers["gopls"].Validate(); err != nil {
		t.Fatalf("gopls invalid after clearing rootDir: %v", err)
	}
}

// Parity-exempt: rootDir is a config trust boundary (absolute, no traversal,
// no control characters) introduced for docs/lsp-design.json R3/D3.
func TestServerDefRootDirValidation(t *testing.T) {
	root := t.TempDir()
	base := func() *ServerDef {
		return &ServerDef{
			Command:     "gopls",
			FileTypes:   []string{".go"},
			RootMarkers: []string{"go.mod"},
		}
	}
	valid := base()
	valid.RootDir = root
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rootDir rejected: %v", err)
	}

	cases := map[string]string{
		"relative":       "relative/path",
		"parent":         "relative/../path",
		"traversal":      root + string(filepath.Separator) + ".." + string(filepath.Separator) + "elsewhere",
		"control":        root + "\x00",
		"newline":        root + "\n",
		"surrounding ws": " " + root,
	}
	for name, rootDir := range cases {
		t.Run(name, func(t *testing.T) {
			def := base()
			def.RootDir = rootDir
			err := def.Validate()
			if err == nil {
				t.Fatalf("rootDir %q accepted", rootDir)
			}
			if !strings.Contains(err.Error(), "rootDir") {
				t.Fatalf("error %q does not name rootDir", err)
			}
		})
	}

	// An invalid override is a warning and keeps the previous definition.
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"servers": map[string]any{"gopls": map[string]any{"rootDir": "relative/path"}},
	})
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(strings.Join(cfg.Warnings, "\n"), "rootDir") {
		t.Fatalf("warnings = %v, want a rootDir warning", cfg.Warnings)
	}
	if got := cfg.Servers["gopls"].RootDir; got != "" {
		t.Fatalf("invalid override changed rootDir to %q", got)
	}
}

// Parity-exempt: rootDir discovery from a session-like cwd is new Go-only
// behavior for docs/lsp-design.json R3/D3; there is no Python discovery test.
func TestDiscoveryPinnedRootDirFromSessionCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit semantics differ on Windows")
	}
	session := t.TempDir()
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localBin := filepath.Join(module, ".hive", "bin", "gopls")
	writeExecutable(t, localBin)
	realModule, err := filepath.EvalSymlinks(module)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		t.Fatal(err)
	}
	gopls := cfg.Servers["gopls"]
	gopls.RootDir = module

	// The session cwd has no go.mod, yet the pinned server is rooted and its
	// command resolves from rootDir-local bins.
	disc := findDiscovery(DiscoverServers(session, cfg, stubLookPath(nil)), "gopls")
	if !disc.Rooted || disc.State() != "available" {
		t.Fatalf("pinned gopls state = %s, reason = %q", disc.State(), disc.Reason)
	}
	if disc.Root != realModule {
		t.Fatalf("root = %q, want %q", disc.Root, realModule)
	}
	if disc.RootMarker != "go.mod" {
		t.Fatalf("root marker = %q, want go.mod", disc.RootMarker)
	}
	if disc.CommandPath != filepath.Join(realModule, ".hive", "bin", "gopls") {
		t.Fatalf("command = %q, want the rootDir-local bin", disc.CommandPath)
	}

	// Without a pin discovery stays cwd-only and explains the unrooted state.
	gopls.RootDir = ""
	look := stubLookPath(map[string]string{"gopls": "/fake/gopls"})
	disc = findDiscovery(DiscoverServers(session, cfg, look), "gopls")
	if disc.Rooted || disc.State() != "unrooted" {
		t.Fatalf("unpinned gopls state = %s", disc.State())
	}
	if disc.Root != session {
		t.Fatalf("unpinned root = %q, want cwd %q", disc.Root, session)
	}
	if !strings.Contains(disc.Reason, "no root marker") {
		t.Fatalf("unpinned reason = %q", disc.Reason)
	}

	// A stale pin is reported, not silently dropped.
	gopls.RootDir = filepath.Join(module, "missing")
	disc = findDiscovery(DiscoverServers(session, cfg, look), "gopls")
	if disc.Rooted || disc.State() != "unrooted" {
		t.Fatalf("stale-pin gopls state = %s", disc.State())
	}
	if !strings.Contains(disc.Reason, "rootDir") || !strings.Contains(disc.Reason, "does not exist") {
		t.Fatalf("stale-pin reason = %q", disc.Reason)
	}
}
