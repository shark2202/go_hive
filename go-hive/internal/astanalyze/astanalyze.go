// Package astanalyze is the read-only repository AST analysis core behind the
// ast_analyze tool. It reuses internal/astgrep's gotreesitter language registry
// and parses Go/Python/TypeScript/JavaScript into per-file facts, groups files
// into units, resolves imports, builds the dependency graph and waves, scores
// readiness/priority, and renders a bounded report.
//
// The analysis root is the session sandbox root (c.root), never the process
// cwd. User path inputs are absolute-only and must stay inside that root.
package astanalyze

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Schema is the report contract version. Bump on a breaking shape change.
const Schema = "hive-go/ast-analyze@1"

// Error kinds. The set is closed; the adapter maps them into the result.
const (
	ErrInvalidInput        = "invalid_input"
	ErrPathNotAbsolute     = "path_not_absolute"
	ErrPathEscape          = "path_escape"
	ErrUnsupportedLanguage = "unsupported_language"
	ErrAmbiguousLanguage   = "ambiguous_language"
	ErrNoSourceFiles       = "no_source_files"
	ErrTooManyFiles        = "too_many_files"
	ErrFileTooLarge        = "file_too_large"
	ErrParseFailed         = "parse_failed"
	ErrWriteFailed         = "write_failed"
	ErrReportTooLarge      = "report_too_large"
	ErrCancelled           = "cancelled"
	ErrConfigDisabled      = "config_disabled"
)

// Error is a typed analysis failure. Infrastructure errors are reserved for
// cancellation; model-facing failures carry a closed kind.
type Error struct {
	Kind    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// IsKind reports whether err is an Error of kind.
func IsKind(err error, kind string) bool {
	var target *Error
	return errors.As(err, &target) && target.Kind == kind
}

// Config is the operator-level ast_analyze.json policy. It is read from
// $HIVE_HOME only; the session directory is agent-writable and is never a
// config source.
type Config struct {
	Enabled         bool
	Languages       []string
	IncludeTests    bool
	IncludeUI       bool
	Ignore          []string
	MaxFiles        int
	MaxFileBytes    int64
	MaxTotalBytes   int64
	MaxUnits        int
	MaxInlineBytes  int64
	MaxReportBytes  int64
	ParseTimeout    time.Duration
	RiskSampleLines int
	StartHereLimit  int
	Workers         int
	LoadError       error
}

// DefaultConfig returns the built-in policy.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		Languages:       []string{"go", "python", "typescript", "tsx", "javascript", "rust", "json", "yaml", "markdown", "c", "cpp", "java", "csharp"},
		IncludeTests:    false,
		IncludeUI:       true,
		Ignore:          []string{".git", "node_modules", "vendor", "dist", "build", ".venv", "__pycache__", ".tox", ".mypy_cache", ".ruff_cache", ".pytest_cache", ".cache"},
		MaxFiles:        5000,
		MaxFileBytes:    1 << 20,
		MaxTotalBytes:   64 << 20,
		MaxUnits:        2000,
		MaxInlineBytes:  64 << 10,
		MaxReportBytes:  8 << 20,
		ParseTimeout:    2 * time.Second,
		RiskSampleLines: 3,
		StartHereLimit:  10,
		Workers:         4,
	}
}

type rawConfig struct {
	Enabled         *bool    `json:"enabled"`
	Languages       []string `json:"languages"`
	IncludeTests    *bool    `json:"include_tests"`
	IncludeUI       *bool    `json:"include_ui"`
	Ignore          []string `json:"ignore"`
	MaxFiles        *int     `json:"max_files"`
	MaxFileBytes    *int64   `json:"max_file_bytes"`
	MaxTotalBytes   *int64   `json:"max_total_bytes"`
	MaxUnits        *int     `json:"max_units"`
	MaxInlineBytes  *int64   `json:"max_inline_bytes"`
	MaxReportBytes  *int64   `json:"max_report_bytes"`
	ParseTimeoutMS  *int     `json:"parse_timeout_ms"`
	RiskSampleLines *int     `json:"risk_sample_lines"`
	StartHereLimit  *int     `json:"start_here_limit"`
	Workers         *int     `json:"workers"`
}

// ConfigPath returns the only on-disk config location.
func ConfigPath(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		home = os.Getenv("HIVE_HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "ast_analyze.json")
}

// LoadConfig reads $HIVE_HOME/ast_analyze.json. Missing keeps defaults; a
// malformed or unreadable file disables the family (fail closed).
// HIVE_AST_ANALYZE=0 is a hard kill switch.
func LoadConfig(home string) Config {
	cfg := DefaultConfig()
	path := ConfigPath(home)
	if path == "" {
		return applyEnv(cfg)
	}
	// #nosec G304 -- path is $HIVE_HOME/ast_analyze.json (fixed basename, operator-owned); no session/agent input.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return applyEnv(cfg)
		}
		cfg.Enabled = false
		cfg.LoadError = err
		return applyEnv(cfg)
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		cfg.Enabled = false
		cfg.LoadError = err
		return applyEnv(cfg)
	}
	applyRaw(&cfg, raw)
	return applyEnv(cfg)
}

func applyEnv(cfg Config) Config {
	if strings.TrimSpace(os.Getenv("HIVE_AST_ANALYZE")) == "0" {
		cfg.Enabled = false
	}
	return cfg
}

func applyRaw(cfg *Config, raw rawConfig) {
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	if raw.Languages != nil {
		cfg.Languages = append([]string(nil), raw.Languages...)
	}
	if raw.IncludeTests != nil {
		cfg.IncludeTests = *raw.IncludeTests
	}
	if raw.IncludeUI != nil {
		cfg.IncludeUI = *raw.IncludeUI
	}
	if raw.Ignore != nil {
		cfg.Ignore = append([]string(nil), raw.Ignore...)
	}
	if raw.MaxFiles != nil {
		cfg.MaxFiles = *raw.MaxFiles
	}
	if raw.MaxFileBytes != nil {
		cfg.MaxFileBytes = *raw.MaxFileBytes
	}
	if raw.MaxTotalBytes != nil {
		cfg.MaxTotalBytes = *raw.MaxTotalBytes
	}
	if raw.MaxUnits != nil {
		cfg.MaxUnits = *raw.MaxUnits
	}
	if raw.MaxInlineBytes != nil {
		cfg.MaxInlineBytes = *raw.MaxInlineBytes
	}
	if raw.MaxReportBytes != nil {
		cfg.MaxReportBytes = *raw.MaxReportBytes
	}
	if raw.ParseTimeoutMS != nil {
		cfg.ParseTimeout = time.Duration(*raw.ParseTimeoutMS) * time.Millisecond
	}
	if raw.RiskSampleLines != nil {
		cfg.RiskSampleLines = *raw.RiskSampleLines
	}
	if raw.StartHereLimit != nil {
		cfg.StartHereLimit = *raw.StartHereLimit
	}
	if raw.Workers != nil {
		cfg.Workers = *raw.Workers
	}
	clamp(cfg)
}

func clamp(cfg *Config) {
	if cfg.MaxFiles < 1 {
		cfg.MaxFiles = 1
	}
	if cfg.MaxFileBytes < 1024 {
		cfg.MaxFileBytes = 1024
	}
	if cfg.MaxTotalBytes < cfg.MaxFileBytes {
		cfg.MaxTotalBytes = cfg.MaxFileBytes
	}
	if cfg.MaxUnits < 1 {
		cfg.MaxUnits = 1
	}
	if cfg.MaxInlineBytes < 0 {
		cfg.MaxInlineBytes = 0
	}
	if cfg.MaxReportBytes < 1024 {
		cfg.MaxReportBytes = 1024
	}
	if cfg.ParseTimeout <= 0 {
		cfg.ParseTimeout = 2 * time.Second
	}
	if cfg.RiskSampleLines < 1 {
		cfg.RiskSampleLines = 1
	}
	if cfg.StartHereLimit < 1 {
		cfg.StartHereLimit = 1
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
}

// Request is one ast_analyze call. Path is absolute and must resolve inside
// the session root; omitted means the root itself.
type Request struct {
	Path         string
	Lang         string
	IncludeTests *bool
	IncludeUI    *bool
	Ignore       []string
	Focus        []string
	Detail       string
	Format       string
	WriteTo      string
	MaxFiles     int
	MaxFileBytes int64
	MaxBytes     int64
}

// Severity weights and labels.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

func (s Severity) Weight() float64 {
	switch s {
	case SeverityHigh:
		return 6
	case SeverityMedium:
		return 3
	default:
		return 1
	}
}

// Risk is the closed risk taxonomy. Detectors are heuristic.
type Risk string

const (
	RiskDynamicCode        Risk = "dynamic-code"
	RiskReflection         Risk = "reflection"
	RiskStringOffsets      Risk = "string-offsets"
	RiskRegexSyntax        Risk = "regex-syntax"
	RiskDynamicRegex       Risk = "dynamic-regex"
	RiskConcurrency        Risk = "concurrency"
	RiskUnsafe             Risk = "unsafe"
	RiskPlatformSpecific   Risk = "platform-specific"
	RiskDynamicTyping      Risk = "dynamic-typing"
	RiskIntegerSemantics   Risk = "integer-semantics"
	RiskOrderedCollections Risk = "ordered-collections"
	RiskNullReferences     Risk = "null-references"
	RiskExceptions         Risk = "exceptions"
	RiskAsync              Risk = "async"
	RiskGenerators         Risk = "generators"
	RiskMonkeypatching     Risk = "monkeypatching"
	RiskAlreadyCompiled    Risk = "already-compiled"
	RiskClockRandom        Risk = "clock-random"
	RiskWebAPIs            Risk = "web-apis"
	RiskIntl               Risk = "intl"
	RiskDateParsing        Risk = "date-parsing"
	RiskUICode             Risk = "ui-code"
)

// Severity maps the taxonomy to its weight.
func (r Risk) Severity() Severity {
	switch r {
	case RiskDynamicCode, RiskReflection, RiskStringOffsets, RiskRegexSyntax,
		RiskUnsafe, RiskPlatformSpecific, RiskMonkeypatching, RiskIntl, RiskDateParsing:
		return SeverityHigh
	case RiskConcurrency, RiskDynamicTyping, RiskIntegerSemantics,
		RiskOrderedCollections, RiskNullReferences, RiskAsync, RiskGenerators,
		RiskAlreadyCompiled, RiskWebAPIs, RiskDynamicRegex:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// Status is the port/analysis verdict.
type Status string

const (
	StatusReady   Status = "ready"
	StatusCaution Status = "caution"
	StatusBlocked Status = "blocked"
	StatusKeep    Status = "keep"
)

// Bridge is the suggested integration boundary.
type Bridge string

const (
	BridgeDirect     Bridge = "direct"
	BridgeCgo        Bridge = "cgo"
	BridgeSubprocess Bridge = "subprocess"
	BridgeHTTP       Bridge = "http"
	BridgeKeep       Bridge = "keep"
)

// Platform is a runtime/UI marker.
type Platform string

const (
	PlatformNode   Platform = "node"
	PlatformUI     Platform = "ui"
	PlatformEditor Platform = "editor"
)

// FileKind classifies one source file.
type FileKind string

const (
	FileSource FileKind = "source"
	FileTest   FileKind = "test"
	FileUI     FileKind = "ui"
)

// Import is one import specifier.
type Import struct {
	Specifier string
	TypeOnly  bool
	Dynamic   bool
}

// FileFacts is the per-file fact set.
type FileFacts struct {
	Path        string
	Unit        string
	Kind        FileKind
	LOC         int
	Loops       int
	Imports     []Import
	Risks       map[Risk][]int
	Platforms   []Platform
	ParseErrors int
}

// RiskReport is the aggregated risk for a unit.
type RiskReport struct {
	Risk     Risk     `json:"risk"`
	Severity Severity `json:"severity"`
	Count    int      `json:"count"`
	Samples  []string `json:"samples,omitempty"`
}

// UnitReport is one unit in the report.
type UnitReport struct {
	ID            string       `json:"id"`
	Package       string       `json:"package"`
	Files         []string     `json:"files"`
	LOC           int          `json:"loc"`
	Wave          int          `json:"wave"`
	Readiness     int          `json:"readiness"`
	Status        Status       `json:"status"`
	Bridge        Bridge       `json:"bridge"`
	Platforms     []Platform   `json:"platforms,omitempty"`
	Risks         []RiskReport `json:"risks,omitempty"`
	InternalDeps  []string     `json:"internal_deps,omitempty"`
	WorkspaceDeps []string     `json:"workspace_deps,omitempty"`
	ExternalDeps  []string     `json:"external_deps,omitempty"`
	DirectTests   int          `json:"direct_tests"`
	FanIn         int          `json:"fan_in"`
	Density       float64      `json:"density"`
	Priority      int          `json:"priority"`
	Exports       int          `json:"exports"`
	Bridgeable    int          `json:"bridgeable"`
	Notes         []string     `json:"notes,omitempty"`
}

// PackageReport groups units by top-level directory.
type PackageReport struct {
	Name          string   `json:"name"`
	Dir           string   `json:"dir"`
	Order         int      `json:"order"`
	Runtime       string   `json:"runtime"`
	WorkspaceDeps []string `json:"workspace_deps,omitempty"`
	TestFiles     int      `json:"test_files"`
	Units         []string `json:"units"`
}

// DependencyReport is one external dependency.
type DependencyReport struct {
	Name   string  `json:"name"`
	UsedBy int     `json:"used_by"`
	Crates *string `json:"crates"`
	Fit    string  `json:"fit"`
	Note   string  `json:"note"`
}

// Summary is the top-level counts.
type Summary struct {
	Packages    int `json:"packages"`
	Files       int `json:"files"`
	TestFiles   int `json:"test_files"`
	UIFiles     int `json:"ui_files"`
	LOC         int `json:"loc"`
	Units       int `json:"units"`
	Ready       int `json:"ready"`
	Caution     int `json:"caution"`
	Blocked     int `json:"blocked"`
	Keep        int `json:"keep"`
	ParseErrors int `json:"parse_errors"`
}

// Report is the ast_analyze result.
type Report struct {
	Schema       string             `json:"schema"`
	Language     string             `json:"language"`
	Summary      Summary            `json:"summary"`
	Packages     []PackageReport    `json:"packages"`
	Units        []UnitReport       `json:"units"`
	StartHere    []string           `json:"start_here"`
	Dependencies []DependencyReport `json:"dependencies,omitempty"`
	Warnings     []string           `json:"warnings,omitempty"`
	SavedTo      *string            `json:"saved_to,omitempty"`
	Bytes        int64              `json:"bytes,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
	Error        string             `json:"error,omitempty"`
	ErrorKind    string             `json:"error_kind,omitempty"`
}
