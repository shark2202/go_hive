package colony

import (
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/pipeline"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

type RuntimeOptions struct {
	AgentSpec    agentloop.AgentSpec
	Goal         *schemas.Goal
	StoragePath  string
	LLM          llm.Completer
	Tools        []llm.ToolDef
	ToolExecutor func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)
	// ToolProber and ToolKiller are the tool executor's liveness handles for
	// the shared MCP connection pool. ToolExecutor is a plain closure and
	// cannot carry methods, so the host passes the registry executor here:
	// a worker call that times out is probed before the loop reclaims it, and
	// only a provably dead server is force-disconnected. Nil keeps the
	// pre-wiring behaviour (probe reads "unknown", nothing disconnects).
	//
	// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
	// executor.kill_for_tool wired onto the tool executor)
	ToolProber         tools.LivenessProber
	ToolKiller         tools.ToolKiller
	EventBus           *eventbus.EventBus
	StreamID           string
	MCPToolNames       map[string]struct{}
	WorkerKeepSet      func() map[string]struct{}
	AdaptiveToolBudget *bool
	// DeclaredTools is the optional tool pool a worker definition's "tools"
	// list selects from (integration/MCP tools the caller resolved). A worker
	// only receives pool entries it declares; an empty declaration keeps the
	// colony's default worker surface. Nil is the same as empty.
	DeclaredTools []llm.ToolDef
	// MaxConcurrentWorkers caps how many spawned workers execute at once.
	// Extra workers stay QUEUED until a slot opens. <=0 uses the colony
	// default (HIVE_MAX_CONCURRENT_WORKERS, 4).
	MaxConcurrentWorkers int

	// WorkerLLM is the completer workers call. Nil falls back to LLM (the Go
	// port does not split queen/worker models by default; upstream resolves
	// worker_llm separately).
	WorkerLLM llm.Completer
	// SkillsProvider supplies the colony's skill catalog for the read surface
	// (GET /colony/skills). Nil auto-wires the default colony hierarchy
	// (framework + preset + user + queen_ui + colony_ui), matching upstream's
	// always-constructed SkillsManager.
	SkillsProvider func() ([]*skills.ParsedSkill, bool)
	// QueenDir is the owning queen's home directory (…/queens/<queen_id>).
	// When set, workers spawned by this colony see the queen's skill scope
	// (queen_ui) ahead of the colony's own (colony_ui), with a colony skill
	// overriding the queen's copy on a name collision — upstream's queen
	// scope is visible to every colony she owns
	// (framework/host/colony_runtime.py::_build_default_skills_config;
	// 07-memory-and-skills.md §4.2). Empty omits the queen_ui scope; the
	// framework/preset/user/colony scopes still load.
	QueenDir string
	// ProtocolsPrompt is the operational-protocols block every worker and the
	// overseer carry in their system prompt (upstream ColonyRuntime's
	// protocols_prompt, inherited from the forking queen's skills manager and
	// written to worker.json). Empty (this build's normal case — the upstream
	// protocols prompt is empty under progressive disclosure) drops the
	// section rather than rendering an empty one.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime.__init__
	// (protocols_prompt); framework/agents/queen/worker_definition.py::build_meta
	ProtocolsPrompt string
	// DefaultPresetSkills is the in-memory set of preset-scope skills this
	// colony enables by default (upstream SkillsManagerConfig
	// .default_preset_skills). Nil means "resolve it from the owning queen's
	// profile at QueenDir" (framework/agents/queen/queen_profiles.py::
	// default_skills_for plus a custom profile.yaml); an explicit empty map
	// disables every preset.
	DefaultPresetSkills map[string]struct{}
	// ToolProviderMap attributes tool names to providers for the tools read
	// surface (upstream ColonyRuntime._tool_provider_map).
	ToolProviderMap map[string]string
	// Binding is the colony's tracker binding. Hosts that already resolved
	// one (fork flow, tests) pass it here; otherwise the runtime derives it
	// from StoragePath when that path is a materialised colony directory.
	Binding *tracker.Binding
	// PipelineStages injects the trigger pipeline, upstream's
	// `ColonyRuntime(pipeline_stages=[...])` argument. When empty the
	// runtime loads `pipeline.stages` from configuration.json (empty is the
	// no-op default).
	PipelineStages []pipeline.Stage
	// CredentialAccounts is the live credential-account view the spawn-time
	// MCP filter reads (upstream CredentialStoreAdapter.default()
	// .get_all_account_info()). Each account is expected to carry a
	// "provider" key. Nil disables the filter, the same degradation upstream
	// uses when its adapter cannot be built: no tool is dropped.
	CredentialAccounts func() []map[string]any
	// ConnectedBrowserProfiles overrides the local browser-bridge /profiles
	// probe for the spawn pre-flight (tests, remote bridges). Nil uses the
	// HIVE_BRIDGE_PORT+1 / 9230 localhost probe the Python worker uses.
	// Returning an error means "unknown" and proceeds, never false-fails.
	ConnectedBrowserProfiles func() ([]string, error)
	// BrowserClose closes a stopped worker's bound browser tab group (the
	// bridge's close_profile_context): the runtime's BrowserReaper calls it
	// on a per-worker stop and on colony stop. Each close is bounded by
	// DefaultBrowserReapTimeout, and a worker is closed at most once per
	// runtime so the two stop paths cannot double-close it. Nil disables
	// reaping — the same degradation upstream has when its gcu browser tools
	// are not loaded.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._reap_worker_browsers
	BrowserClose CloseProfileContext
	// StopTimeout bounds ColonyRuntime.Stop's wait for workers to unwind
	// before they are forced terminal. <=0 uses DefaultStopTimeout (10 s,
	// upstream's STOP_TIMEOUT_SEC).
	//
	// Parity: framework/host/worker.py::STOP_TIMEOUT_SEC
	StopTimeout time.Duration
	// SkillsWatchInterval/SkillsWatchDebounce override the colony skill
	// watcher's polling cadence; zero uses the skills package defaults. The
	// runtime starts the watcher in Start and stops it in Stop, matching
	// upstream's ColonyRuntime.start/stop calling the shared SkillsManager's
	// start_watching/stop_watching.
	SkillsWatchInterval time.Duration
	SkillsWatchDebounce time.Duration
	// QueenName is the id of the queen that owns this colony. It is written
	// into every worker's meta.json (as queen_name) so the lineage survives
	// the runtime: the server's colony worker walk can then answer "which
	// queen session spawned this worker?" without a live runtime. Empty means
	// the colony has no recorded queen — the meta field is then written as
	// null, exactly as upstream's `self._queen_id` (None) renders.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime.__init__ (queen_id)
	QueenName string
}
