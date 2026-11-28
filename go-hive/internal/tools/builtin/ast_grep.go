package builtin

// Parity: A1 tool definition golden. This adapter owns only the model-facing
// schema and input coercion; the matcher, path safety and renderer live in
// internal/astgrep. Registration is deliberately left to the coordinator so
// this lane does not edit shared registry.go.

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/astgrep"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	// ToolAstGrep is the model-facing tool name.
	ToolAstGrep = "ast_grep"
	// AstGrepDescription is frozen by the design's tool contract.
	AstGrepDescription = "Search code by AST structure instead of text. Patterns use $NAME to capture one node, " +
		"$_ to match one node without capturing, and $$$NAME or $$$ to capture zero or more nodes. " +
		"A pattern must parse as exactly one AST node in the target language; a reused metavariable " +
		"requires identical code at each occurrence. Language is inferred from the file extension " +
		"unless lang is supplied."
)

// AstGrepTool executes ast_grep against one workspace root. The zero value is
// usable and follows the process working directory, mirroring Toolbox.
type AstGrepTool struct {
	root string
}

// NewAstGrepTool returns a tool rooted at root. An empty root is resolved by
// the core to the process working directory at call time.
func NewAstGrepTool(root string) *AstGrepTool {
	return &AstGrepTool{root: strings.TrimSpace(root)}
}

// AstGrepToolDef returns the standalone tool definition. The coordinator can
// append it to Defs() without this lane touching the shared registry.
func AstGrepToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        ToolAstGrep,
		Description: AstGrepDescription,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pat": map[string]any{
					"type":        "string",
					"description": "Single structural pattern using $NAME, $_, $$$NAME or $$$.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute file, directory, glob or semicolon-separated list inside the workspace root; defaults to the workspace root. Relative paths are rejected.",
				},
				"lang": map[string]any{
					"type":        "string",
					"description": "Optional canonical language override.",
				},
				"skip": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Match offset; defaults to 0.",
				},
			},
			"required": []string{"pat"},
		},
		ConcurrencySafe: true,
	}
}

// Def returns the same definition regardless of root.
func (t *AstGrepTool) Def() llm.ToolDef { return AstGrepToolDef() }

// AstGrepResult is the adapter result: text for the model and the frozen
// details map for hosts that render tool metadata.
type AstGrepResult struct {
	Content  string
	Details  map[string]any
	IsError  bool
	ToolName string
}

// Run validates the model input, calls astgrep.Search and renders the result.
// Invalid input and unsupported-language failures are returned as model-visible
// errors (IsError) rather than infrastructure errors, matching the other file
// tools' recovery-friendly contract.
func (t *AstGrepTool) Run(ctx context.Context, input map[string]any) (AstGrepResult, error) {
	invalid := func(format string, args ...any) (AstGrepResult, error) {
		return AstGrepResult{
			Content:  "Error: " + fmt.Sprintf(format, args...),
			Details:  map[string]any{},
			IsError:  true,
			ToolName: ToolAstGrep,
		}, nil
	}
	pattern, ok := stringArg(input, "pat")
	if !ok {
		return invalid("pat is required and must be a string")
	}
	if strings.TrimSpace(pattern) == "" {
		return invalid("pat is required and must not be empty")
	}
	pathArg := ""
	if value, present := input["path"]; present && value != nil {
		pathArg, ok = stringArg(input, "path")
		if !ok {
			return invalid("path must be a string")
		}
		if strings.TrimSpace(pathArg) != "" {
			if err := requireAbsolutePath(pathArg); err != nil {
				return invalid("%v", err)
			}
		}
	}
	langArg := ""
	if value, present := input["lang"]; present && value != nil {
		langArg, ok = stringArg(input, "lang")
		if !ok {
			return invalid("lang must be a string")
		}
	}
	skip := 0
	if value, present := input["skip"]; present && value != nil {
		number, ok := finiteNumberArg(input, "skip")
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) ||
			number < 0 || number != math.Trunc(number) || number > float64(math.MaxInt) {
			return invalid("skip must be a non-negative integer")
		}
		skip = int(number)
	}
	root := ""
	if t != nil {
		root = t.root
	}
	result, err := astgrep.Search(ctx, astgrep.Config{WorkspaceRoot: root}, astgrep.Request{
		Pattern: pattern,
		Path:    pathArg,
		Lang:    langArg,
		Skip:    skip,
	})
	if err != nil {
		return AstGrepResult{
			Content:  "Error: " + err.Error(),
			Details:  map[string]any{},
			IsError:  true,
			ToolName: ToolAstGrep,
		}, nil
	}
	return AstGrepResult{
		Content:  astgrep.Render(result),
		Details:  astgrep.Details(result),
		IsError:  false,
		ToolName: ToolAstGrep,
	}, nil
}

// requireAbsolutePath validates a semicolon-separated model path. go_hive has
// no project cwd, so a relative path is ambiguous; the model-facing contract is
// absolute-only. The core astgrep package still resolves relative paths for
// standalone library callers and its own tests.
func requireAbsolutePath(raw string) error {
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return fmt.Errorf("empty entry in semicolon-separated path list")
		}
		if !filepath.IsAbs(entry) {
			return fmt.Errorf("path must be absolute: %s", entry)
		}
	}
	return nil
}

// Execute adapts Run to the builtin.Result shape used by the existing tool
// dispatcher. The coordinator wires this method into its catalogue.
func (t *AstGrepTool) Execute(ctx context.Context, input map[string]any) (Result, error) {
	run, err := t.Run(ctx, input)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: run.Content, IsError: run.IsError}, nil
}
