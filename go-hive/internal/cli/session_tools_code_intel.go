package cli

// Independent code-intelligence adapters for the session catalogue.
//
// ast_grep (internal/astgrep) and lsp (internal/lsp) are standalone packages
// with their own adapters in internal/tools/builtin. They are not integrations,
// so they do not go through tools.ToolRegistry: the catalogue owns one adapter
// each, appends the definition to its pool and routes calls explicitly.
//
// Neither definition joins the frozen 39-name default surface. ast_grep is a
// post-1.0 opt-in capability (docs/ast-grep-design.json): ASTGrepEnabled reads
// ast_grep.json and a disabled, missing or unreadable config keeps the tool
// out of the catalogue entirely. lsp is additionally gated on its frozen
// config chain: LSPEnabled reads lsp.json settings.enabled, and a disabled or
// unreadable config keeps the tool out of the catalogue entirely. Both are
// flat directory tools in the existing lazy tier (LazyModelDefs ->
// search_tools), so a model loads them on demand and then calls them by name.

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// registerCodeIntelligenceTools appends the adapters this session may use.
// It runs after the integration registry definitions so a colliding name is
// skipped instead of advertised twice; execution still prefers the adapter
// route (the builtin-wins rule).
//
// The gates read only $HIVE_HOME/ast_grep.json and $HIVE_HOME/lsp.json. The
// session directory is deliberately not consulted: it is the file tools'
// sandbox root and is agent-writable, so a session-scoped config would let the
// model opt itself into a tool (or a language-server command) the operator
// disabled. See go_hive/docs/config-roots.html.
func (c *sessionToolCatalogue) registerCodeIntelligenceTools() {
	if builtin.ASTGrepEnabled(c.home.Root) {
		c.astGrepTool = builtin.NewAstGrepTool(c.root)
		c.appendCodeIntelligenceDef(c.astGrepTool.Def())
	}
	if builtin.LSPEnabled(c.home.Root) {
		c.lspToolbox = builtin.NewLSPToolbox(c.root, c.home.Root)
		c.appendCodeIntelligenceDef(builtin.LSPToolDef())
	}
}

// appendCodeIntelligenceDef adds one definition unless the pool already owns
// the name, so a same-named integration tool cannot produce a duplicate.
func (c *sessionToolCatalogue) appendCodeIntelligenceDef(def llm.ToolDef) {
	for _, existing := range c.defs {
		if existing.Name == def.Name {
			return
		}
	}
	c.defs = append(c.defs, def)
}

// Close releases the language servers and idle HTTP connections a session
// started. Safe on a nil receiver and when neither adapter was enabled; the
// catalogue itself never closes during a chat turn, so callers use this when
// tearing a session down.
func (c *sessionToolCatalogue) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	if c.lspToolbox != nil {
		firstErr = c.lspToolbox.Close()
	}
	if c.httpToolbox != nil {
		if err := c.httpToolbox.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// executeCodeIntelligenceTool routes one ast_grep or lsp call through its
// adapter. handled is false for every other name so the normal catalogue
// dispatch (and, ultimately, the registry) still runs.
func (c *sessionToolCatalogue) executeCodeIntelligenceTool(ctx context.Context, name string, input map[string]any) (string, bool, bool, error) {
	if c == nil {
		return "", false, false, nil
	}
	switch name {
	case builtin.ToolAstGrep:
		if c.astGrepTool == nil {
			return "", false, false, nil
		}
		result, err := c.astGrepTool.Execute(ctx, input)
		if err != nil {
			return "", false, true, err
		}
		return result.Content, result.IsError, true, nil
	case builtin.ToolLSP:
		if c.lspToolbox == nil {
			return "", false, false, nil
		}
		result, err := c.lspToolbox.Execute(ctx, input)
		if err != nil {
			return "", false, true, err
		}
		return result.Content, result.IsError, true, nil
	default:
		return "", false, false, nil
	}
}
