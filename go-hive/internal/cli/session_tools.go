package cli

// Per-session tool catalogue: the builtin workspace tools, the session task
// tools, and every ported integration installed into one tools.ToolRegistry.
// The registry is the process's integration entry point
// (internal/tools/integrations.Install); the builtins keep executing through
// builtin.Toolbox so their sandbox root, error flag and output limits are
// unchanged, and the task tools execute against the session's own task list.

import (
	"context"
	"fmt"
	"os"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

type sessionToolCatalogue struct {
	root         string
	toolbox      *builtin.Toolbox
	builtinNames map[string]struct{}
	taskTools    *taskToolSet
	registry     *tools.ToolRegistry
	defs         []llm.ToolDef
	installed    int
}

// newSessionToolCatalogue installs the integration tools for one session root
// and merges their definitions with the builtin ones. A name collision keeps
// the builtin: it is the more restricted, sandboxed implementation.
//
// Three groups register here: the session task tools bound to root's directory,
// the declarative catalog (integrations.New, which covers the hand-written
// first batch) and every ported batch installer in sessionIntegrationInstallers.
// A new batch is one line in that table; install_references_test.go fails when
// an exported installer is left out.
//
// The network scanners keep their own opt-in: they probe third-party hosts, so
// they are dropped from the registry unless HIVE_NETWORK_SCANNERS (or the
// network_scanners key in hive.config.json) turns them on.
func newSessionToolCatalogue(root string, toolbox *builtin.Toolbox, home hivehome.Home) *sessionToolCatalogue {
	registry := tools.NewToolRegistry()
	installed := integrations.New(integrations.WithRoots(root)).Install(registry)
	installed += installIntegrationBatches(registry, home)
	if senders, err := hiveserver.RegisterSenderTools(registry, home); err != nil {
		// A broken sender store costs the senders tool, not the whole
		// catalogue; the feature gate itself returns (0, nil).
		fmt.Fprintf(os.Stderr, "Warning: email sender tools unavailable: %v\n", err)
	} else {
		installed += senders
	}

	catalogue := &sessionToolCatalogue{
		root:         root,
		toolbox:      toolbox,
		builtinNames: map[string]struct{}{},
		taskTools:    newTaskToolSet(root, home),
		registry:     registry,
		installed:    installed,
	}
	for _, def := range toolbox.Defs() {
		catalogue.builtinNames[def.Name] = struct{}{}
		catalogue.defs = append(catalogue.defs, def)
	}
	for _, def := range catalogue.taskTools.Defs() {
		if _, taken := catalogue.builtinNames[def.Name]; taken {
			continue
		}
		catalogue.defs = append(catalogue.defs, def)
	}
	// run_worker is a queen lifecycle tool, not an integration: it dispatches
	// through the live colony runtime the session binds (see
	// ChatRuntime.BindColony) and is filtered out of turns while no colony is
	// bound, matching upstream's colony-phase tool surface.
	if _, taken := catalogue.builtinNames[colony.RunWorkerToolName]; !taken {
		catalogue.defs = append(catalogue.defs, colony.RunWorkerToolDef())
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

// sessionIntegrationInstallers lists every ported batch installer with the
// label used for its commit/lane. Order matters only for readability: the
// batches register disjoint tool names (verified by the session catalogue
// test), so a collision would be a port bug rather than a precedence rule.
var sessionIntegrationInstallers = []struct {
	name    string
	install func(*tools.ToolRegistry) int
}{
	{"batch2", integrations.InstallBatch2},
	{"batch3-doc", integrations.InstallBatch3Doc},
	{"batch3-excel", integrations.InstallBatch3Excel},
	{"batch3-media", integrations.InstallBatch3Media},
	{"batch4", integrations.InstallBatch4},
	{"batch5-lane1", integrations.InstallBatch5Lane1},
	{"batch5-lane2", integrations.InstallBatch5Lane2},
	{"batch5-lane3", integrations.InstallBatch5Lane3},
	{"batch5-lane4", integrations.InstallBatch5Lane4},
	{"batch6-lane1", integrations.InstallBatch6Lane1},
	{"batch6-lane2", integrations.InstallBatch6Lane2},
	{"batch6-lane3", integrations.InstallBatch6Lane3},
	{"batch6-lane4", integrations.InstallBatch6Lane4},
	// Lane 5 predates the Install prefix; keep its published name.
	{"batch7-lane5", integrations.Batch7Lane5Install},
	{"batch7-lane1", integrations.InstallBatch7Lane1},
	{"batch7-lane2", integrations.InstallBatch7Lane2},
	{"batch7-lane3", integrations.InstallBatch7Lane3},
	{"batch7-lane4", integrations.InstallBatch7Lane4},
	{"batch7-lane6", integrations.InstallBatch7Lane6},
	{"batch7-lane7", integrations.InstallBatch7Lane7},
	{"batch7-lane8", integrations.InstallBatch7Lane8},
	{"batch7-lane9", integrations.InstallBatch7Lane9},
	{"batch8-final", integrations.InstallBatch8Final},
}

// networkScannerTools are the tools that send probe traffic at third-party
// hosts. They are registered and then dropped unless
// hiveserver.NetworkScannersEnabled says otherwise.
var networkScannerTools = []string{
	"port_scan",
	"dns_security_scan",
	"ssl_tls_scan",
	"http_headers_scan",
	"subdomain_enumerate",
	"tech_stack_detect",
}

// installIntegrationBatches publishes every ported batch into registry and
// returns the number of tools left registered. The scanner filter runs after
// the batch install so the opt-in stays a catalogue concern and every installer
// keeps one behaviour.
func installIntegrationBatches(registry *tools.ToolRegistry, home hivehome.Home) int {
	installed := 0
	for _, installer := range sessionIntegrationInstallers {
		installed += installer.install(registry)
	}
	if hiveserver.NetworkScannersEnabled(home) {
		return installed
	}
	for _, name := range networkScannerTools {
		if !registry.HasTool(name) {
			continue
		}
		registry.Unregister(name)
		installed--
	}
	return installed
}

// Defs returns the tool definitions advertised to the model.
func (c *sessionToolCatalogue) Defs() []llm.ToolDef { return c.defs }

// Execute runs one tool call, dispatching builtins to their sandbox, session
// task tools to the session's task list, and integration tools to the registry
// executor.
//
// attach_file needs the session working directory, which upstream passes as a
// hidden CONTEXT_PARAM; sessionToolContext injects it here (never in the schema).
// Attachments get their chip copies published against c.root before the result
// is handed to the text-only chat runtime, so a publish failure surfaces as a
// tool error instead of a silent "attached" claim.
func (c *sessionToolCatalogue) Execute(ctx context.Context, name string, input map[string]any) (string, bool, error) {
	if _, ok := c.builtinNames[name]; ok {
		result, err := c.toolbox.Execute(ctx, name, input)
		if err != nil {
			return "", false, err
		}
		return result.Content, result.IsError, nil
	}
	if c.taskTools.Has(name) {
		return c.taskTools.Execute(name, input)
	}
	// The colony runtime travels on the context (ChatRuntime binds it per
	// call), so this executor stays valid even though the catalogue is built
	// before a session's colony host exists.
	if name == colony.RunWorkerToolName {
		content, isError := colony.RunWorker(ctx, colony.RunWorkerSpawnerFromContext(ctx), input)
		return content, isError, nil
	}
	result, err := c.registry.GetExecutor().Execute(ctx, tools.ToolUse{
		Name:  name,
		Input: c.sessionToolContext(name, input),
	})
	if err != nil {
		return "", false, err
	}
	content, isError := result.Content, result.IsError
	if len(result.Attachments) > 0 {
		content, isError = integrations.PublishAttachFileResult(content, result.Attachments, c.root)
	}
	return content, isError, nil
}

// sessionToolContext injects the per-session values attach_file resolves
// against (the caller's working directory and the storage root). Inputs the
// model already supplied win, and the caller's map is never mutated.
func (c *sessionToolCatalogue) sessionToolContext(name string, input map[string]any) map[string]any {
	if name != "attach_file" || c.root == "" {
		return input
	}
	merged := make(map[string]any, len(input)+2)
	for key, value := range input {
		merged[key] = value
	}
	if _, ok := merged["session_cwd"]; !ok {
		merged["session_cwd"] = c.root
	}
	if _, ok := merged["storage_path"]; !ok {
		merged["storage_path"] = c.root
	}
	return merged
}
