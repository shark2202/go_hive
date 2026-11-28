package cli

// Repository AST analysis wiring for the session catalogue.
//
// ast_analyze is Go-only and read-only. The catalogue owns one adapter per
// session, advertises it through the lazy tier and routes calls explicitly.
// The analysis root is the session sandbox root (c.root); user path inputs are
// absolute and must stay inside it. Configuration is $HIVE_HOME/ast_analyze.json
// only (see go_hive/docs/ast-analyze-design.json).

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// registerAstAnalyzeTools appends ast_analyze when the family is enabled.
func (c *sessionToolCatalogue) registerAstAnalyzeTools() {
	if c == nil || !builtin.AstAnalyzeEnabled(c.home.Root) {
		return
	}
	c.astAnalyzeTool = builtin.NewAstAnalyzeToolbox(c.root, c.home.Root)
	def := c.astAnalyzeTool.Def()
	if _, taken := c.builtinNames[def.Name]; !taken {
		c.defs = append(c.defs, def)
	}
}

// executeAstAnalyzeTool routes one ast_analyze call. handled is false for every
// other name so the normal catalogue dispatch still runs.
func (c *sessionToolCatalogue) executeAstAnalyzeTool(ctx context.Context, name string, input map[string]any) (string, bool, bool, error) {
	if c == nil || c.astAnalyzeTool == nil || name != builtin.ToolAstAnalyze {
		return "", false, false, nil
	}
	result, err := c.astAnalyzeTool.Execute(ctx, input)
	return result.Content, result.IsError, true, err
}
