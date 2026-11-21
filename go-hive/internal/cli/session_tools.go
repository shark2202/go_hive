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
	// agentName is the agent package this catalogue belongs to (upstream's
	// agent_path.name). Empty for a plain session catalogue; colony workers set
	// it so their spawned subprocesses carry HIVE_AGENT_NAME.
	agentName string
	defs      []llm.ToolDef
	installed int
}

// SetAgentName records the agent package this catalogue belongs to and mirrors
// upstream's per-agent registry env (agent_loader.set_mcp_extra_env): every MCP
// server config built from this registry carries HIVE_AGENT_NAME, and the
// terminal tools that spawn subprocesses receive it in their env input.
func (c *sessionToolCatalogue) SetAgentName(name string) {
	c.agentName = strings.TrimSpace(name)
	if c.agentName == "" {
		return
	}
	// Parity: framework/loader/agent_loader.py::AgentLoader.__init__
	// (set_mcp_extra_env with HIVE_AGENT_NAME + HIVE_STORAGE_PATH; the storage
	// half is injected per call from the session context below).
	c.registry.SetMCPExtraEnv(map[string]string{"HIVE_AGENT_NAME": c.agentName})
}

// injectAgentEnv merges the agent identity into a subprocess-spawning tool's
// env input. Upstream gates this on the tool declaring `env`
// (ToolRegistry._prepare_call_inputs), which is what keeps it off every other
// tool's arguments.
//
// Parity: framework/loader/tool_registry.py::_prepare_call_inputs
func (c *sessionToolCatalogue) injectAgentEnv(name string, merged map[string]any) {
	if c.agentName == "" || !c.toolDeclaresEnvParam(name) {
		return
	}
	env := map[string]any{}
	switch existing := merged["env"].(type) {
	case map[string]any:
		for key, value := range existing {
			env[key] = value
		}
	case map[string]string:
		for key, value := range existing {
			env[key] = value
		}
	}
	if _, set := env["HIVE_AGENT_NAME"]; !set {
		env["HIVE_AGENT_NAME"] = c.agentName
	}
	merged["env"] = env
}

// toolDeclaresEnvParam reports whether the tool's schema declares an `env`
// object (the terminal and browser CLIs do).
func (c *sessionToolCatalogue) toolDeclaresEnvParam(name string) bool {
	for _, def := range c.defs {
		if def.Name != name {
			continue
		}
		properties, _ := def.Parameters["properties"].(map[string]any)
		if _, ok := properties["env"]; ok {
			return true
		}
	}
	return false
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
// The network scanners are part of that default surface, matching upstream
// aden_tools (tools/src/aden_tools/tools/__init__.py registers all seven
// unconditionally). They send probe traffic at the host the model names, so
// callers must only scan hosts they are authorized to test; there is no
// Go-only opt-in.
func newSessionToolCatalogue(root string, toolbox *builtin.Toolbox, home hivehome.Home) *sessionToolCatalogue {
	registry := tools.NewToolRegistry()
	// The session's credential view is what makes get_account_info real and what
	// lets account-aware actions resolve an `account` alias. It is attached to
	// the shared registry (not to any single batch runtime), and the registry is
	// the process's integration entry point, so every installed batch reads it.
	registry.SetAccountSource(newSessionAccountSource(home))
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
	// The playbook tools: run_playbook is the colony-bound queen's fan-out
	// tool; Python's three standalone control tools (get_playbook_status /
	// list_playbook_runs / stop_playbook) are registered next to it and route
	// onto the same runner. Like run_worker they dispatch through the live
	// runtime bound per turn; likewise they are filtered at execution time
	// (not from defs) because the same catalogue also feeds the colony's
	// declared-tool pool.
	playbookDefs := []llm.ToolDef{colony.RunPlaybookToolDef()}
	playbookDefs = append(playbookDefs, colony.PlaybookControlToolDefs()...)
	for _, def := range applyToolContractParity(playbookDefs) {
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
	// gate, when set, decides whether the installer runs for the session
	// being built. The browser tool definitions follow the bridge switch:
	// with the feature off their executor has no bridge to POST to, so the
	// catalogue must not advertise or record them (upstream retires its
	// bundled gcu-tools server the same way).
	gate func(hivehome.Home) bool
}{
	{"batch2", integrations.InstallBatch2, nil},
	{"batch3-doc", integrations.InstallBatch3Doc, nil},
	{"batch3-excel", integrations.InstallBatch3Excel, nil},
	{"batch3-media", integrations.InstallBatch3Media, nil},
	{"batch4", integrations.InstallBatch4, nil},
	{"batch5-lane1", integrations.InstallBatch5Lane1, nil},
	{"batch5-lane2", integrations.InstallBatch5Lane2, nil},
	{"batch5-lane3", integrations.InstallBatch5Lane3, nil},
	{"batch5-lane4", integrations.InstallBatch5Lane4, nil},
	{"batch6-lane1", integrations.InstallBatch6Lane1, nil},
	{"batch6-lane2", integrations.InstallBatch6Lane2, nil},
	{"batch6-lane3", integrations.InstallBatch6Lane3, nil},
	{"batch6-lane4", integrations.InstallBatch6Lane4, nil},
	// Lane 5 predates the Install prefix; keep its published name.
	{"batch7-lane5", integrations.Batch7Lane5Install, nil},
	{"batch7-lane1", integrations.InstallBatch7Lane1, nil},
	{"batch7-lane2", integrations.InstallBatch7Lane2, nil},
	{"batch7-lane3", integrations.InstallBatch7Lane3, nil},
	{"batch7-lane4", integrations.InstallBatch7Lane4, nil},
	{"batch7-lane6", integrations.InstallBatch7Lane6, nil},
	{"batch7-lane7", integrations.InstallBatch7Lane7, nil},
	{"batch7-lane8", integrations.InstallBatch7Lane8, nil},
	{"batch7-lane9", integrations.InstallBatch7Lane9, nil},
	{"batch8-final", integrations.InstallBatch8Final, nil},
	// Upstream per-tool spreadsheet names on top of the aggregate csv/excel
	// tools, and the browser discovery tool.
	{"spreadsheet-aliases", integrations.InstallSpreadsheetAliases, nil},
	{"browser-setup", integrations.InstallBrowserSetup, nil},
	// The 22 active browser tools (gcu FastMCP surface) are a lazy-tier
	// extension of the Go bridge, not part of the reference registry: only
	// install them while the browser feature is on (browserToolsEnabled).
	{"browser-tools", integrations.InstallBrowserTools, browserToolsEnabled},
}

// installIntegrationBatches publishes every ported batch into registry and
// returns the number of tools registered. The network scanners are registered
// like every other batch: upstream has no opt-in gate for them, so the Go
// session surface keeps the same seven default tools. A batch whose gate
// declines (the browser tools, while the bridge switch is off) is skipped.
func installIntegrationBatches(registry *tools.ToolRegistry, home hivehome.Home) int {
	installed := 0
	for _, installer := range sessionIntegrationInstallers {
		if installer.gate != nil && !installer.gate(home) {
			continue
		}
		installed += installer.install(registry)
	}
	return installed
}

// browserToolsEnabled is the session-catalogue gate for the gcu browser tool
// definitions: the same switch the bridge supervisor obeys (HIVE_BROWSER_BRIDGE
// env, then configuration.json's gcu_enabled, default on).
func browserToolsEnabled(home hivehome.Home) bool {
	return browserBridgeRequested(home, nil)
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
	// Python's standalone playbook-control tools (get_playbook_status /
	// list_playbook_runs / stop_playbook) are real definitions of this
	// catalogue; they route onto the run_playbook status/stop actions. Not
	// gated on the flat surface mode: the definitions exist on both surfaces.
	if action, ok := flatSurfacePlaybookActions[name]; ok {
		mapped := make(map[string]any, len(input)+1)
		for key, value := range input {
			if key == "action" {
				continue
			}
			mapped[key] = value
		}
		mapped["action"] = action
		content, isError := colony.RunPlaybook(ctx, colony.RunWorkerSpawnerFromContext(ctx), mapped)
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
	// The default flat surface advertises Python-only names; route the ones
	// this build serves through their existing implementation (playbook
	// control -> run_playbook actions) and answer the rest explicitly.
	if content, isError, handled := c.executeFlatSurfaceTool(ctx, name, input); handled {
		return content, isError, nil, nil
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
	merged := input
	if len(names) > 0 && c.root != "" {
		merged = make(map[string]any, len(input)+len(names))
		for key, value := range input {
			merged[key] = value
		}
		for _, key := range names {
			merged[key] = c.root
		}
	}
	if c.agentName == "" {
		return merged
	}
	// The agent identity is added without mutating the caller's map, even
	// when no context param applied (an env-carrying tool with no session
	// root still gets its agent name).
	withEnv := make(map[string]any, len(merged)+1)
	for key, value := range merged {
		withEnv[key] = value
	}
	c.injectAgentEnv(name, withEnv)
	return withEnv
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
