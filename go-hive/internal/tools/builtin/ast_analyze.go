package builtin

// Native repository AST analysis adapter. The core lives in internal/astanalyze;
// this file owns the model-facing schema, input coercion and report rendering.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/astanalyze"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	// ToolAstAnalyze is the model-facing tool name.
	ToolAstAnalyze = "ast_analyze"
	// AstAnalyzeDescription is frozen by the design's tool contract.
	AstAnalyzeDescription = "Analyze a repository with AST facts: units, import graph, waves and cycles, " +
		"risk hotspots, readiness/priority and start_here. Read-only: no code execution and no network. " +
		"path is absolute and must resolve inside the session workspace root."
)

// AstAnalyzeToolbox executes ast_analyze for one workspace root.
type AstAnalyzeToolbox struct {
	root string
	home string
	cfg  astanalyze.Config
}

// AstAnalyzeEnabled reports whether the family is enabled by
// <HIVE_HOME>/ast_analyze.json (default enabled).
func AstAnalyzeEnabled(home string) bool {
	return astanalyze.LoadConfig(home).Enabled
}

// NewAstAnalyzeToolbox builds a toolbox rooted at the session sandbox.
func NewAstAnalyzeToolbox(root, home string) *AstAnalyzeToolbox {
	return &AstAnalyzeToolbox{root: root, home: home, cfg: astanalyze.LoadConfig(home)}
}

// WithRoot re-roots the toolbox for a colony worker.
func (t *AstAnalyzeToolbox) WithRoot(root string) *AstAnalyzeToolbox {
	if t == nil {
		return nil
	}
	return &AstAnalyzeToolbox{root: root, home: t.home, cfg: t.cfg}
}

// Close is a no-op; the analyzer holds no long-lived resources.
func (t *AstAnalyzeToolbox) Close() error { return nil }

// Def returns the model-visible definition.
func (t *AstAnalyzeToolbox) Def() llm.ToolDef { return AstAnalyzeToolDef() }

// AstAnalyzeToolDef is the frozen tool definition.
func AstAnalyzeToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        ToolAstAnalyze,
		Description: AstAnalyzeDescription,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to a file, directory or glob inside the session workspace root. Omitted means the workspace root; relative paths are rejected.",
				},
				"lang": map[string]any{
					"type":        "string",
					"description": "Optional canonical language override (go, python, typescript, javascript, tsx).",
				},
				"include_tests": map[string]any{"type": "boolean", "default": false},
				"include_ui":    map[string]any{"type": "boolean", "default": true},
				"ignore": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Extra directory names or globs to skip.",
				},
				"focus": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional unit id prefixes to report (e.g. internal/app/); not filesystem paths. Dependencies are still analyzed.",
				},
				"detail": map[string]any{
					"type":        "string",
					"enum":        []string{"summary", "units", "full"},
					"default":     "summary",
					"description": "summary: counts + start_here + top risks; units: + unit table; full: + packages/dependencies/risk guide.",
				},
				"format": map[string]any{
					"type":    "string",
					"enum":    []string{"json", "markdown"},
					"default": "json",
				},
				"write_to": map[string]any{
					"type":        "string",
					"description": "Absolute output path inside the session workspace root. Parent directories are created and an existing file is replaced atomically; relative paths are rejected. Omitted means auto-spill under <sessionRoot>/data/tool-output when the report exceeds max_inline_bytes.",
				},
				"max_files":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50000, "default": 5000},
				"max_file_bytes": map[string]any{"type": "integer", "minimum": 1024, "default": 1048576},
				"max_bytes":      map[string]any{"type": "integer", "minimum": 1024, "default": 8388608},
			},
			"required": []string{},
		},
		ConcurrencySafe: false,
	}
}

// Execute runs one ast_analyze call.
func (t *AstAnalyzeToolbox) Execute(ctx context.Context, input map[string]any) (Result, error) {
	if t == nil || !t.cfg.Enabled {
		return Result{Content: "Error: ast_analyze is disabled by configuration", IsError: true}, nil
	}
	req, err := parseAstAnalyzeRequest(input)
	if err != nil {
		return Result{Content: "Error: " + err.Error(), IsError: true}, nil
	}
	report, err := astanalyze.Analyze(ctx, t.cfg, t.root, req)
	if err != nil {
		var typed *astanalyze.Error
		if errors.As(err, &typed) {
			return Result{Content: "Error: " + typed.Message, IsError: true}, nil
		}
		return Result{}, err
	}
	applyDetail(&report, req.Detail)
	text := renderAstAnalyze(report, req.Format)
	if len(text) > 0 && text[len(text)-1] != '\n' {
		text += "\n"
	}
	if req.WriteTo != "" || int64(len(text)) > t.cfg.MaxInlineBytes {
		if int64(len(text)) > t.cfg.MaxReportBytes {
			return Result{Content: "Error: report exceeds max_report_bytes", IsError: true}, nil
		}
		var path string
		if req.WriteTo != "" {
			path, err = astanalyze.ResolvePath(t.root, req.WriteTo, true)
		} else {
			path, err = astanalyze.SpillReport(t.root, req.Format, []byte(text))
		}
		if err != nil {
			return Result{Content: "Error: " + err.Error(), IsError: true}, nil
		}
		if err := astanalyze.WriteReport(path, []byte(text)); err != nil {
			return Result{Content: "Error: write report: " + err.Error(), IsError: true}, nil
		}
		report.SavedTo = &path
		report.Bytes = int64(len(text))
		return Result{Content: renderAstAnalyzeMetadata(report), IsError: false}, nil
	}
	return Result{Content: text, IsError: false}, nil
}

func parseAstAnalyzeRequest(input map[string]any) (astanalyze.Request, error) {
	req := astanalyze.Request{}
	var err error
	if input == nil {
		input = map[string]any{}
	}
	req.Path, _ = stringArg(input, "path")
	req.Lang, _ = stringArg(input, "lang")
	req.Detail = "summary"
	if detail, ok := stringArg(input, "detail"); ok && detail != "" {
		req.Detail = detail
	}
	req.Format = "json"
	if format, ok := stringArg(input, "format"); ok && format != "" {
		req.Format = format
	}
	req.WriteTo, _ = stringArg(input, "write_to")
	if req.IncludeTests, err = boolPtrArg(input, "include_tests"); err != nil {
		return req, err
	}
	if req.IncludeUI, err = boolPtrArg(input, "include_ui"); err != nil {
		return req, err
	}
	if req.Ignore, err = stringSliceArg(input, "ignore"); err != nil {
		return req, err
	}
	if req.Focus, err = stringSliceArg(input, "focus"); err != nil {
		return req, err
	}
	if value, ok := intArg(input, "max_files"); ok && value > 0 {
		req.MaxFiles = value
	}
	if value, ok := finiteNumberArg(input, "max_file_bytes"); ok && value >= 1024 {
		req.MaxFileBytes = int64(value)
	}
	if value, ok := finiteNumberArg(input, "max_bytes"); ok && value >= 1024 {
		req.MaxBytes = int64(value)
	}
	return req, nil
}

func boolPtrArg(input map[string]any, key string) (*bool, error) {
	value, present := input[key]
	if !present || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
	return &flag, nil
}

func stringSliceArg(input map[string]any, key string) ([]string, error) {
	value, present := input[key]
	if !present || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of strings", key)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}

func applyDetail(report *astanalyze.Report, detail string) {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "summary":
		report.Units = nil
		report.Packages = nil
		report.Dependencies = nil
	case "units":
		report.Packages = nil
		report.Dependencies = nil
	default:
	}
}

func renderAstAnalyze(report astanalyze.Report, format string) string {
	if strings.EqualFold(strings.TrimSpace(format), "markdown") {
		return renderAstAnalyzeMarkdown(report)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":%q,"error_kind":"invalid_input"}`, err.Error())
	}
	return string(data)
}

func renderAstAnalyzeMetadata(report astanalyze.Report) string {
	payload := map[string]any{
		"schema":     report.Schema,
		"language":   report.Language,
		"summary":    report.Summary,
		"start_here": report.StartHere,
		"warnings":   report.Warnings,
		"saved_to":   report.SavedTo,
		"bytes":      report.Bytes,
		"truncated":  report.Truncated,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"saved_to":%q}`, valueOrEmpty(report.SavedTo))
	}
	return string(data)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func renderAstAnalyzeMarkdown(report astanalyze.Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# AST analysis (%s)\n\n", report.Language)
	fmt.Fprintf(&out, "%d packages, %d files (%d tests, %d UI), %d LOC, %d units.\n\n",
		report.Summary.Packages, report.Summary.Files, report.Summary.TestFiles,
		report.Summary.UIFiles, report.Summary.LOC, report.Summary.Units)
	fmt.Fprintf(&out, "| Ready | Caution | Blocked | Keep |\n| ---: | ---: | ---: | ---: |\n| %d | %d | %d | %d |\n\n",
		report.Summary.Ready, report.Summary.Caution, report.Summary.Blocked, report.Summary.Keep)
	for _, warning := range report.Warnings {
		fmt.Fprintf(&out, "> **Warning:** %s\n\n", warning)
	}
	if len(report.StartHere) > 0 {
		out.WriteString("## Start here\n\n")
		for _, id := range report.StartHere {
			fmt.Fprintf(&out, "- `%s`\n", id)
		}
		out.WriteString("\n")
	}
	if len(report.Units) > 0 {
		out.WriteString("## Units\n\n| Unit | Status | Wave | Readiness | Priority | LOC | Bridge |\n")
		out.WriteString("| --- | --- | ---: | ---: | ---: | ---: | --- |\n")
		for _, unit := range report.Units {
			fmt.Fprintf(&out, "| `%s` | %s | %d | %d | %d | %d | %s |\n",
				unit.ID, unit.Status, unit.Wave, unit.Readiness, unit.Priority, unit.LOC, unit.Bridge)
		}
		out.WriteString("\n")
	}
	if len(report.Packages) > 0 {
		out.WriteString("## Packages\n\n")
		for _, pkg := range report.Packages {
			fmt.Fprintf(&out, "- **%s** (`%s`, order %d): %s\n", pkg.Name, pkg.Dir, pkg.Order, strings.Join(pkg.Units, ", "))
		}
		out.WriteString("\n")
	}
	if len(report.Dependencies) > 0 {
		out.WriteString("## Dependencies\n\n")
		for _, dep := range report.Dependencies {
			fmt.Fprintf(&out, "- `%s` (used by %d, fit %s)\n", dep.Name, dep.UsedBy, dep.Fit)
		}
	}
	return out.String()
}
