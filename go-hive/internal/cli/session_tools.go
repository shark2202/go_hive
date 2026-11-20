package cli

// Per-session tool catalogue: the builtin workspace tools, the session task
// tools, and every ported integration installed into one tools.ToolRegistry.
// The registry is the process's integration entry point
// (internal/tools/integrations.Install); the builtins keep executing through
// builtin.Toolbox so their sandbox root, error flag and output limits are
// unchanged, and the task tools execute against the session's own task list.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/skills"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tools/integrations"
	"github.com/aden-hive/hive-go/internal/tracker"
)

type sessionToolCatalogue struct {
	root         string
	home         hivehome.Home
	toolbox      *builtin.Toolbox
	builtinNames map[string]struct{}
	taskTools    *taskToolSet
	registry     *tools.ToolRegistry
	// trackerTools is the queen-role tracker table. The binding is resolved
	// per call from the session's bound colony, because the catalogue is
	// built before the colony attaches (same reason run_worker resolves its
	// spawner per call).
	trackerTools *tracker.Registry
	// queenMail is the session's list_queens/message_queen binding. It stays
	// nil for colony workers and when queen_mail is off: enableQueenMail is
	// only called for queen chat sessions.
	queenMail *queenMailToolBinding
	// queenLifecycle is the session's queen lifecycle tool table
	// (get_worker_status, stop_worker, triggers, worker profiles, ...). It
	// stays nil for colony workers and for non-queen catalogues, so a worker's
	// tool inventory keeps the upstream worker surface.
	queenLifecycle *hiveserver.QueenLifecycleBinding
	defs           []llm.ToolDef
	installed      int
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
		home:         home,
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
	catalogue.recordFilesToolGroup()
	// chart_render is the chart-tools harness group's only tool; it runs in
	// process (headless Chrome) and records its group catalog the same way.
	catalogue.registerChartTool()
	catalogue.recordHarnessGroups()
	for _, def := range catalogue.taskTools.Defs() {
		if _, taken := catalogue.builtinNames[def.Name]; taken {
			continue
		}
		catalogue.defs = append(catalogue.defs, def)
	}
	// Skill tools: write_skill authors into the bound colony's skills/ folder
	// (or the queen's own), skill_view/list_skills read the same catalog. They
	// stay advertised without a colony because the catalogue is built before
	// the colony attaches; write_skill(scope='colony') and the executor return
	// an actionable no-colony error, and the default scope falls back to queen.
	for _, def := range skills.AgentToolDefs() {
		if _, taken := catalogue.builtinNames[def.Name]; taken {
			continue
		}
		catalogue.defs = append(catalogue.defs, def)
	}
	// History search: search_messages / search_timeline read every past session
	// of this session's own queen. Scope comes from c.root, never from model
	// input, so the tools cannot read another queen's (or colony's) tree.
	for _, def := range searchToolDefs(home) {
		if _, taken := catalogue.builtinNames[def.Name]; taken {
			continue
		}
		catalogue.defs = append(catalogue.defs, def)
	}
	// memory_timeline is the browse-by-day view over the same timeline data,
	// gated by the same feature flag. It takes no pattern, so the queen can
	// look at what happened lately without inventing a search term.
	for _, def := range memoryTimelineToolDefs(home) {
		if _, taken := catalogue.builtinNames[def.Name]; taken {
			continue
		}
		catalogue.defs = append(catalogue.defs, def)
	}
	// Credential status: a read-only availability view. It is session-scoped
	// like the search tools and intentionally exposes no write path.
	if _, taken := catalogue.builtinNames[CredentialStatusToolName]; !taken {
		catalogue.defs = append(catalogue.defs, credentialStatusToolDef())
	}
	// run_playbook is the colony-bound queen's fan-out tool. Like run_worker it
	// dispatches through the live runtime bound per turn; unlike run_worker it
	// is filtered at execution time (not from defs) because the same catalogue
	// also feeds the colony's declared-tool pool.
	if _, taken := catalogue.builtinNames[colony.RunPlaybookToolName]; !taken {
		catalogue.defs = append(catalogue.defs, colony.RunPlaybookToolDef())
	}
	// run_worker is a queen lifecycle tool, not an integration: it dispatches
	// through the live colony runtime the session binds (see
	// ChatRuntime.BindColony) and is filtered out of turns while no colony is
	// bound, matching upstream's colony-phase tool surface.
	if _, taken := catalogue.builtinNames[colony.RunWorkerToolName]; !taken {
		catalogue.defs = append(catalogue.defs, colony.RunWorkerToolDef())
	}
	// Tracker tools: the queen gets the full table (tracker_sql,
	// tracker_register_writable, tracker_upsert, tracker_query), matching
	// upstream queen_orchestrator's register_tracker_tools(queen_registry).
	// Workers are handled by the colony runtime with role="worker".
	queenTracker := tracker.NewRegistry(nil)
	if err := tracker.RegisterTrackerTools(queenTracker, "queen"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: tracker tools unavailable: %v\n", err)
	} else {
		catalogue.trackerTools = queenTracker
		for _, name := range queenTracker.Names() {
			tool, ok := queenTracker.Get(name)
			if !ok {
				continue
			}
			if _, taken := catalogue.builtinNames[name]; taken {
				continue
			}
			catalogue.defs = append(catalogue.defs, llm.ToolDef{
				Name:            tool.Name,
				Description:     tool.Description,
				Parameters:      tool.Parameters,
				ConcurrencySafe: tool.ConcurrencySafe,
			})
		}
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
			Name:            tool.Name,
			Description:     tool.Description,
			Parameters:      tool.Parameters,
			ConcurrencySafe: tool.ConcurrencySafe,
		})
	}
	// Freeze the model-visible description/schema of the same-name core tools
	// to the Python contract (see tool_contract_parity.go). This is the last
	// step so every owner — builtin, task, search, chart, integration — is
	// already in defs; execution paths are untouched.
	catalogue.defs = applyToolContractParity(catalogue.defs)
	return catalogue
}

// recordFilesToolGroup publishes the four in-process files-tools tools under
// the harness group name of the same name. They execute through the builtin
// sandbox, not the registry, but the Tool Library and @server:files-tools
// categories read the registry's catalog, so without this record the group
// would look empty. An already-registered group (a real MCP server) wins.
func (c *sessionToolCatalogue) recordFilesToolGroup() {
	if len(c.registry.GetServerToolNames("files-tools")) > 0 {
		return
	}
	wanted := map[string]struct{}{
		builtin.ToolReadFile:    {},
		builtin.ToolWriteFile:   {},
		builtin.ToolEditFile:    {},
		builtin.ToolSearchFiles: {},
	}
	group := make([]tools.Tool, 0, len(wanted))
	for _, def := range c.toolbox.Defs() {
		if _, ok := wanted[def.Name]; !ok {
			continue
		}
		group = append(group, tools.Tool{
			Name:            def.Name,
			Description:     def.Description,
			Parameters:      def.Parameters,
			ConcurrencySafe: def.ConcurrencySafe,
		})
	}
	if len(group) == 0 {
		return
	}
	c.registry.RecordBuiltinToolGroup("files-tools", group)
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
	// Upstream per-tool spreadsheet names on top of the aggregate csv/excel
	// tools, and the browser discovery tool.
	{"spreadsheet-aliases", integrations.InstallSpreadsheetAliases},
	{"browser-setup", integrations.InstallBrowserSetup},
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
// executor. It is the text-only view of execute and keeps the historical
// signature every existing caller uses.
//
// attach_file and the terminal tools need the session working directory,
// which upstream passes as a hidden CONTEXT_PARAM; injectContextParams adds it
// at call time (never in the model-visible schema).
// Attachments get their chip copies published against c.root before the result
// is handed to the chat runtime, so a publish failure surfaces as a tool error
// instead of a silent "attached" claim.
func (c *sessionToolCatalogue) Execute(ctx context.Context, name string, input map[string]any) (string, bool, error) {
	content, isError, _, err := c.execute(ctx, name, input)
	return content, isError, err
}

// ExecuteBlocks is Execute plus the provider-neutral content blocks the tool
// returned (attach_file images/PDFs, browser screenshots). The chat runtime
// puts them in the model request instead of dropping the bytes at the text
// boundary.
func (c *sessionToolCatalogue) ExecuteBlocks(ctx context.Context, name string, input map[string]any) (string, bool, []map[string]any, error) {
	return c.execute(ctx, name, input)
}

func (c *sessionToolCatalogue) execute(ctx context.Context, name string, input map[string]any) (string, bool, []map[string]any, error) {
	if _, ok := c.builtinNames[name]; ok {
		result, err := c.toolbox.Execute(ctx, name, c.injectContextParams(name, input))
		if err != nil {
			return "", false, nil, err
		}
		return result.Content, result.IsError, nil, nil
	}
	// chart_render renders through the local Chrome renderer, not the
	// registry. The check is inside the helper so this file stays unaware of
	// the charts package.
	if content, isError, handled := c.executeChartTool(ctx, name, input); handled {
		return content, isError, nil, nil
	}
	if c.taskTools.Has(name) {
		content, isError, err := c.taskTools.Execute(name, input)
		return content, isError, nil, err
	}
	// list_queens / message_queen go through the shared queen-mail service.
	if c.queenMail != nil {
		if content, handled := c.queenMail.execute(name, input); handled {
			return content, false, nil, nil
		}
	}
	// Queen lifecycle tools (worker status / stop / inject, worker questions,
	// triggers, worker profiles) resolve the live session per call — the
	// catalogue is built before the colony attaches.
	if c.queenLifecycle != nil && c.queenLifecycle.Has(name) {
		content, isError := c.queenLifecycle.Execute(ctx, name, input)
		return content, isError, nil, nil
	}
	// Past-session search runs against this session's queen directory; the
	// catalogue carries the root, the memory package does the reading.
	if content, isError, handled := c.executeSearchTool(name, input); handled {
		return content, isError, nil, nil
	}
	// memory_timeline browses the same past-session timeline by day; its
	// feature flag and scope resolution match search_timeline.
	if content, isError, handled := c.executeMemoryTimelineTool(name, input); handled {
		return content, isError, nil, nil
	}
	// Credential status is a metadata-only read of the host's credential
	// store; it never decrypts a key.
	if name == CredentialStatusToolName {
		content, isError, err := executeCredentialStatusTool()
		return content, isError, nil, err
	}
	// The colony runtime travels on the context (ChatRuntime binds it per
	// call), so this executor stays valid even though the catalogue is built
	// before a session's colony host exists.
	if name == skills.WriteSkillToolName || name == skills.SkillViewToolName || name == skills.ListSkillsToolName {
		content, isError, _ := skills.ExecuteAgentTool(name, input, c.skillToolDirs(ctx))
		return content, isError, nil, nil
	}
	if name == colony.RunPlaybookToolName {
		content, isError := colony.RunPlaybook(ctx, colony.RunWorkerSpawnerFromContext(ctx), input)
		return content, isError, nil, nil
	}
	if name == colony.RunWorkerToolName {
		content, isError := colony.RunWorker(ctx, colony.RunWorkerSpawnerFromContext(ctx), input)
		return content, isError, nil, nil
	}
	// Tracker tools resolve their binding per call: the catalogue is built
	// before a colony attaches, so the session's bound runtime is the only
	// source of truth for which tracker.db this turn writes.
	if c.trackerTools != nil {
		if _, registered := c.trackerTools.Get(name); registered {
			binding := colony.TrackerBindingFromContext(ctx)
			if binding == nil {
				return tracker.ErrNoBinding.Error(), true, nil, nil
			}
			result, _ := c.trackerTools.WithResolver(tracker.StaticResolver(binding)).Execute(name, input)
			payload, err := json.Marshal(result)
			if err != nil {
				return "tracker tool result could not be encoded: " + err.Error(), true, nil, nil
			}
			success, _ := result["success"].(bool)
			return string(payload), !success, nil, nil
		}
	}
	// chart_render may be catalog-only until the chart lane lands its renderer;
	// answer those calls with an explicit not_implemented envelope instead of a
	// generic unknown-tool error.
	if content, isError, handled := c.executeHarnessPlaceholder(name); handled {
		return content, isError, nil, nil
	}
	result, err := c.registry.GetExecutor().Execute(ctx, tools.ToolUse{
		Name:  name,
		Input: c.injectContextParams(name, input),
	})
	if err != nil {
		return "", false, nil, err
	}
	content, isError := result.Content, result.IsError
	if len(result.Attachments) > 0 {
		content, isError = integrations.PublishAttachFileResult(content, result.Attachments, c.root)
	}
	return content, isError, result.ImageContent, nil
}

// contextParamInjection maps a tool to the framework context values the host
// injects at execution time. Every injected name is a CONTEXT_PARAM: it is
// stripped from the model-visible schema (builtin.Defs and
// tools.StripContextParams) and supplied here, never by the model.
var contextParamInjection = map[string][]string{
	"attach_file":                {"session_cwd", "storage_path"},
	builtin.ToolTerminalExec:     {"session_cwd"},
	builtin.ToolTerminalGlob:     {"session_cwd"},
	builtin.ToolTerminalRG:       {"session_cwd"},
	builtin.ToolTerminalJobStart: {"session_cwd"},
	builtin.ToolTerminalPTYOpen:  {"session_cwd"},
}

// injectContextParams merges the per-session values a tool resolves against
// (session working directory, attach_file's storage root). Framework values
// are authoritative over anything in input, matching upstream
// ToolRegistry._prepare_call_inputs; the caller's map is never mutated.
func (c *sessionToolCatalogue) injectContextParams(name string, input map[string]any) map[string]any {
	names := contextParamInjection[name]
	if len(names) == 0 || c.root == "" {
		return input
	}
	merged := make(map[string]any, len(input)+len(names))
	for key, value := range input {
		merged[key] = value
	}
	for _, key := range names {
		merged[key] = c.root
	}
	return merged
}

// skillToolDirs resolves the on-disk scope for a skill tool call: the bound
// colony from the per-turn context (the catalogue is built before the binding
// exists) and the queen home derived from this session's directory. skill_view
// searches the colony directory first — that is the catalog a worker actually
// sees — then the queen's own skills.
func (c *sessionToolCatalogue) skillToolDirs(ctx context.Context) skills.ToolDirs {
	dirs := skills.ToolDirs{SessionDir: c.root}
	if c.home.Root != "" {
		queenID := queenScopeFromSessionRoot(c.home, c.root)
		dirs.QueenID = queenID
		dirs.QueenDir = c.home.QueenDir(queenID)
	}
	if binding := colony.TrackerBindingFromContext(ctx); binding != nil {
		dirs.ColonyDir = binding.Dir
	}
	return dirs
}

// queenScopeFromSessionRoot recovers the queen a chat session belongs to from
// its on-disk tree (…/queens/<queen>/sessions/<id>). A catalogue built for a
// non-standard root (tests, a colony path) falls back to the shared "default"
// queen, which is the same identity the loader uses for queen_name="".
func queenScopeFromSessionRoot(home hivehome.Home, root string) string {
	rel, err := filepath.Rel(home.QueensDir(), filepath.Clean(root))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "default"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return "default"
	}
	queen := strings.TrimSpace(parts[0])
	if queen == "" || queen == "." || queen == ".." || strings.ContainsAny(queen, `/\`) {
		return "default"
	}
	return queen
}
