package cli

// Per-session tool catalogue: the builtin workspace tools plus every ported
// integration installed into one tools.ToolRegistry. The registry is the
// process's integration entry point (internal/tools/integrations.Install); the
// builtins keep executing through builtin.Toolbox so their sandbox root, error
// flag and output limits are unchanged.

import (
	"context"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

type sessionToolCatalogue struct {
	toolbox      *builtin.Toolbox
	builtinNames map[string]struct{}
	registry     *tools.ToolRegistry
	defs         []llm.ToolDef
	installed    int
}

// newSessionToolCatalogue installs the integration tools for one session root
// and merges their definitions with the builtin ones. A name collision keeps
// the builtin: it is the more restricted, sandboxed implementation.
func newSessionToolCatalogue(root string, toolbox *builtin.Toolbox) *sessionToolCatalogue {
	registry := tools.NewToolRegistry()
	installed := integrations.New(integrations.WithRoots(root)).Install(registry)

	catalogue := &sessionToolCatalogue{
		toolbox:      toolbox,
		builtinNames: map[string]struct{}{},
		registry:     registry,
		installed:    installed,
	}
	for _, def := range toolbox.Defs() {
		catalogue.builtinNames[def.Name] = struct{}{}
		catalogue.defs = append(catalogue.defs, def)
	}
	for _, name := range registry.RegisteredNames() {
		if _, taken := catalogue.builtinNames[name]; taken {
			continue
		}
		tool, ok := registry.GetTool(name)
		if !ok {
			continue
		}
		catalogue.defs = append(catalogue.defs, llm.ToolDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return catalogue
}

// Defs returns the tool definitions advertised to the model.
func (c *sessionToolCatalogue) Defs() []llm.ToolDef { return c.defs }

// Execute runs one tool call, dispatching builtins to their sandbox and
// integration tools to the registry executor.
func (c *sessionToolCatalogue) Execute(ctx context.Context, name string, input map[string]any) (string, bool, error) {
	if _, ok := c.builtinNames[name]; ok {
		result, err := c.toolbox.Execute(ctx, name, input)
		if err != nil {
			return "", false, err
		}
		return result.Content, result.IsError, nil
	}
	result, err := c.registry.GetExecutor().Execute(ctx, tools.ToolUse{Name: name, Input: input})
	if err != nil {
		return "", false, err
	}
	return result.Content, result.IsError, nil
}
