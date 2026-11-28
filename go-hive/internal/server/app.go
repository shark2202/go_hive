package server

// Parity: framework/server/app.py::create_app

import (
	"bytes"
	"crypto/subtle"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/server/routesagents"
	"github.com/shark2202/go_hive/go-hive/internal/server/routesapp"
	"github.com/shark2202/go_hive/go-hive/internal/server/routesbrowser"
	"github.com/shark2202/go_hive/go-hive/internal/server/uiembed"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

const runtimeLogsPath = "/api/runtime-logs"

const defaultRequestExecutorMax = 32

var desktopTokenFromEnv = os.Getenv("HIVE_DESKTOP_TOKEN")

type ExecutorState struct {
	MaxWorkers int `json:"max_workers"`
	Active     int `json:"active"`
	Queued     int `json:"queued"`
}

type AppDeps struct {
	Sessions     func() []FeatureSession
	LiveSessions func() []*Session
	Manager      *SessionManager
	Resources    func() any
	// ResourceSnapshot is the /api/health/resources body source. Nil keeps the
	// route's pre-sampler snapshot (thresholds/cadence from the env, empty
	// history, verdict unknown), exactly like upstream before the first sample.
	ResourceSnapshot func(historyN *int) map[string]any
	Executor         func() ExecutorState
	Home             hivehome.Home
	Reveal           func(string) error
	Colony           ColonyLifecycle
	Config           ConfigRouteDeps
	// Reminders is the process-wide reminder hub runtime shared by the HTTP
	// routes and chat sessions. Nil builds a fresh one for this app.
	Reminders    *ReminderRuntime
	FrontendDist string
	DesktopToken *string
	Logger       *log.Logger

	CreateSession          func(CreateSessionParams) (*Session, error)
	CreateColonyFromSource func(CreateColonyFromSourceParams) (*Session, error)
	// BuildLLM mirrors SessionManager.build_llm for the message classifier
	// routes. Nil resolves the main llm slot from configuration.json at each
	// call, so a Settings edit reaches /api/messages/classify without a
	// restart.
	BuildLLM func() llm.Completer
	// RefreshSentinelListeners runs after a colony notification config save so
	// a newly connected channel starts (or a disconnected one stops) its
	// inbound listener without a restart. Nil refreshes the process-global
	// sentinel manager (what upstream's get_sentinel_manager().refresh_listeners
	// does).
	RefreshSentinelListeners func()
	// DefaultModel is the main-slot model reported by GET /api/queen/{id}/llm
	// (Python's manager model, else the provider-prefixed preferred model).
	// Nil leaves the route reporting an empty default.
	DefaultModel func() string
	// ColonyRuntimeFor resolves the live colony runtime behind a session for
	// the worker/sentinel read surfaces. Nil falls back to inspecting
	// Session.Colony itself.
	ColonyRuntimeFor   func(*Session) *colony.ColonyRuntime
	VacateSoftDeleted  func(colonyID string) error
	ConnectedProviders func() []string
	AgentRoots         *agents.AllowedRoots
	// SoulProbeAnswerFor resolves the queen's model for a soul probe. Nil
	// leaves POST .../soul/proposals/{id}/probe answering 503, like a server
	// without a configured model.
	SoulProbeAnswerFor func(queenID string, cfg *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error)
	// SoulReflect runs the manual "reflect now" request. Nil leaves
	// POST .../soul/reflect answering 503 instead of faking a run.
	SoulReflect func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error
	// QueenMail is the process's queen-mail service. The loader builds it
	// before preloaded sessions so their chat tool tables and the
	// /api/queen-mail routes share one instance (limits, tokens, return
	// addresses). Nil builds one here for tests and embedded servers.
	QueenMail *QueenMailAdapter
	// QueenToolCatalog / QueenLifecycleTools feed GET /api/queen/{id}/tools.
	// The loader owns the per-session tool catalogue; these hooks project it
	// (or the process-wide Tool Library catalog when no session is live) into
	// the route's snapshot shape. Nil leaves the route reporting an empty,
	// stale catalog rather than inventing tools.
	QueenToolCatalog    func(queenID string, session *Session) map[string][]map[string]any
	QueenLifecycleTools func(queenID string, session *Session) []map[string]any
	// ChannelStates reports the process channel host's configured-channel
	// states for GET /api/channels. Nil means this build runs no host, and the
	// route answers with the upstream empty list (no installed channels).
	ChannelStates func() map[string]map[string]any
}

type App struct {
	handler http.Handler
	deps    AppDeps
	distDir string
	// distFS and distFiles serve the in-binary dashboard of embed_ui builds.
	// They stay nil in builds without the tag and whenever an on-disk dist
	// directory is available (disk keeps precedence for development).
	distFS    fs.FS
	distFiles http.Handler
	token     string
	logger    *log.Logger

	executorMu  sync.Mutex
	executorMax int

	// skillsWatcher polls the queen-catalog scopes and reloads every live
	// session's catalog (skills_watcher.go). Nil when the home has no queens
	// tree yet or the app has no session manager.
	skillsWatcher *skills.Watcher

	processStart float64
}

// configRouteDeps resolves the seams the config routes need before they are
// registered: the home fallback (an empty Home would resolve
// "configuration.json" relative to the cwd) and the model-slot hot-swap
// bridges. Model-slot writes must hot-swap running sessions when the writer
// did not supply its own walker: the same reconfigure path the Models page
// uses, so PUT /api/config/llm and PUT /api/config/llm-sections (role=llm)
// stop being config-only.
func configRouteDeps(deps AppDeps) ConfigRouteDeps {
	configDeps := deps.Config
	if configDeps.Home.Root == "" {
		configDeps.Home = deps.Home
	}
	// PUT /api/config/features hot-applies adaptive_tool_budget to the live
	// colony runtimes. Upstream reads manager.list_sessions(); the loader
	// publishes that same snapshot through AppDeps.Sessions, so without this
	// fallback the route always reports colonies_applied: 0.
	if configDeps.Sessions == nil {
		configDeps.Sessions = deps.Sessions
	}
	// A saved global sentinel block can change which channels are tuned, so the
	// manager must re-evaluate its inbound listeners — the same production hook
	// the per-colony notification route uses. An injected handler wins, which
	// keeps the config route tests hermetic.
	if configDeps.SentinelRefresh == nil {
		configDeps.SentinelRefresh = deps.RefreshSentinelListeners
		if configDeps.SentinelRefresh == nil {
			configDeps.SentinelRefresh = refreshSentinelListeners
		}
	}
	if deps.Manager == nil {
		return configDeps
	}
	if configDeps.Role.HotSwap == nil {
		configDeps.Role.HotSwap = func(fullModel, apiKey, apiBase string) int {
			var key, base *string
			if apiKey != "" {
				key = &apiKey
			}
			if apiBase != "" {
				base = &apiBase
			}
			return SwapMainModel(configDeps.Home, deps.Manager, fullModel, key, base, nil)
		}
	}
	if configDeps.LLM.HotSwap == nil {
		configDeps.LLM.HotSwap = func(fullModel string, apiKey, apiBase *string) int {
			return SwapMainModel(configDeps.Home, deps.Manager, fullModel, apiKey, apiBase, nil)
		}
	}
	return configDeps
}

func NewApp(deps AppDeps) *App {
	app := &App{deps: deps, logger: deps.Logger, processStart: float64(time.Now().UnixNano()) / 1e9}
	if app.logger == nil {
		app.logger = log.Default()
	}
	app.token = desktopTokenFromEnv
	if deps.DesktopToken != nil {
		app.token = *deps.DesktopToken
	}
	app.distDir = deps.FrontendDist
	if app.distDir == "" {
		app.distDir = FrontendDistDir()
	}
	app.distDir = resolveDistDir(app.distDir)
	// Embedded assets are the fallback for a standalone binary: a checkout
	// with a built frontend keeps serving it from disk.
	if app.distDir == "" {
		if assets, ok := uiembed.Assets(); ok {
			app.distFS = assets
			app.distFiles = http.FileServer(http.FS(assets))
		}
	}

	mux := http.NewServeMux()
	registerHomeRouteTables(app, mux, deps)
	registerLiveRouteTables(mux, deps)

	if app.HasFrontend() {
		mux.HandleFunc("GET /", app.handleSPA)
	}

	app.handler = app.accessLog(app.desktopAuth(app.noCacheAPI(app.rejectDotSegmentsAPI(app.errorRecovery(mux)))))
	// The queen catalog's hot-reload loop (upstream's agent-host watcher). It
	// starts only once the hive home has a queens tree, which EnsureDirs
	// creates before serve.
	app.startQueenSkillsWatcher()
	return app
}

// registerHomeRouteTables installs the route tables that need only the home
// directory, the session manager and the process clock. It creates the
// process-wide reminder runtime so the session stop hook and the
// /api/reminders routes share one instance.
func registerHomeRouteTables(app *App, mux *http.ServeMux, deps AppDeps) {
	mux.HandleFunc("GET /api/health", app.handleHealth)
	mux.HandleFunc("GET /api/startup-status", app.handleStartupStatus)
	// Config routes read/write <HIVE_HOME>/configuration.json. AppDeps.Config is
	// optional, so configRouteDeps falls back to the app home instead of
	// leaving Home zero-valued (an empty Home resolves to "configuration.json"
	// relative to the cwd) and wires the model-slot hot-swap bridges.
	configDeps := configRouteDeps(deps)
	RegisterConfigRoutes(mux, configDeps)
	reminders := deps.Reminders
	if reminders == nil {
		reminders = NewReminderRuntime()
	}
	RegisterSessionRoutes(mux, SessionRouteDeps{
		LiveSessions:           deps.LiveSessions,
		Manager:                deps.Manager,
		Home:                   deps.Home,
		Reveal:                 deps.Reveal,
		Colony:                 deps.Colony,
		CreateSession:          deps.CreateSession,
		CreateColonyFromSource: deps.CreateColonyFromSource,
		VacateSoftDeleted:      deps.VacateSoftDeleted,
		ConnectedProviders:     deps.ConnectedProviders,
		OnSessionStopped:       reminders.DropSession,
	})
	routesagents.RegisterRoutes(mux, routesagents.Deps{
		Home:  deps.Home,
		Roots: deps.AgentRoots,
		Sessions: func() []routesagents.SessionRef {
			if deps.Manager == nil {
				return nil
			}
			sessions := deps.Manager.ListSessions()
			refs := make([]routesagents.SessionRef, 0, len(sessions))
			for _, session := range sessions {
				refs = append(refs, routesagents.SessionRef{ID: session.ID, WorkerPath: session.WorkerPath})
			}
			return refs
		},
		StopSession: func(id string) {
			if deps.Manager != nil {
				deps.Manager.StopSession(id)
			}
		},
		CheckAgentCredentials: func(path string, verify bool) (map[string]any, error) {
			return checkAgentCredentialRequirements(deps.Home, nil, path, verify)
		},
	})
	RegisterChatRoutes(mux, ChatRouteDeps{Manager: deps.Manager})

	// Wave 1 route tables that only need the home directory, the session
	// manager and the process clock. Each seam that is not implemented yet
	// answers with an explicit failure inside its handler (no empty 200s).
	RegisterCredentialRoutes(mux, CredentialRouteDeps{
		Home:                   deps.Home,
		Manager:                deps.Manager,
		Specs:                  credentialSpecsFromRegistry,
		PublishCredentialEvent: publishCredentialCatalogEvent,
	})
	RegisterSoulRoutes(mux, SoulRouteDeps{Home: deps.Home, Reflect: deps.SoulReflect})
	RegisterSoulProbeRoutes(mux, SoulProbeRouteDeps{
		Home:      deps.Home,
		AnswerFor: deps.SoulProbeAnswerFor,
	})
	RegisterLocalCredentialRoutes(mux, LocalCredentialRouteDeps{
		Home:                   deps.Home,
		PublishCredentialEvent: publishCredentialCatalogEvent,
		ProviderFor:            credentialProviderForSpec,
	})
	RegisterRuntimeLogRoutes(mux, RuntimeLogRouteDeps{})
	RegisterChannelRoutes(mux, ChannelRouteDeps{Home: deps.Home, States: deps.ChannelStates})
	RegisterPromptRoutes(mux, PromptRouteDeps{Home: deps.Home})
	// SchedulePush stays nil, exactly like upstream in this checkout:
	// cloud_sync_hooks.schedule_push imports framework.cloud_sync, which is not
	// shipped here, so every call is a no-op (see MemoryRouteDeps and the
	// memory/soul parity report's S2 platform_diff). The local write is complete
	// either way.
	RegisterMemoryRoutes(mux, MemoryRouteDeps{Home: deps.Home})
	RegisterMessageRoutes(mux, MessageRouteDeps{Home: deps.Home, BuildLLM: messageRouteLLM(deps)})
	RegisterMCPRoutes(mux, MCPRouteDeps{Home: deps.Home})
	RegisterMaintenanceRoutes(mux, MaintenanceRouteDeps{
		Home:           deps.Home,
		Manager:        deps.Manager,
		ProcessStartTS: app.processStartTS,
	})
	RegisterEventRoutes(mux, EventRouteDeps{})
	routesapp.RegisterRoutes(mux, routesapp.Deps{Resources: deps.ResourceSnapshot})
	routesbrowser.RegisterRoutes(mux, routesbrowser.Deps{})
	RegisterModelRoutes(mux, ModelRouteDeps{Home: deps.Home, Manager: deps.Manager})
	RegisterTaskRoutes(mux, TaskRouteDeps{Store: tasks.NewTaskStore(deps.Home.Root)})
	RegisterReminderRoutes(mux, ReminderRouteDeps{Runtime: reminders})
}

// registerLiveRouteTables installs the tables that read live runtime state:
// the colony/worker read surfaces, sentinel listener refresh, and the
// execution, colony, queen, skills and queen-mail routes.
func registerLiveRouteTables(mux *http.ServeMux, deps AppDeps) {
	// Wave 1 route tables that read live runtime state. The colony adapters
	// project *colony.ColonyRuntime onto the worker read surfaces; a session
	// without one keeps the routes' explicit fallbacks (on-disk workers, 503
	// "No active colony", 404 unknown session).
	colonyRuntimeFor := func(session *Session) *colony.ColonyRuntime {
		if deps.ColonyRuntimeFor != nil {
			if runtime := deps.ColonyRuntimeFor(session); runtime != nil {
				return runtime
			}
		}
		return sessionColonyRuntime(session)
	}
	RegisterColonyWorkerRoutes(mux, ColonyWorkerDeps{
		Manager: deps.Manager,
		Home:    deps.Home,
		Runtime: func(session *Session) ColonyWorkerReadSurface {
			return colonyWorkerReadSurfaceFor(colonyRuntimeFor(session))
		},
	})
	RegisterWorkerRoutes(mux, WorkerRouteDeps{
		Manager: deps.Manager,
		Runtime: func(session *Session) LiveWorkerRuntime {
			return liveWorkerRuntimeFor(colonyRuntimeFor(session))
		},
	})
	refreshListeners := deps.RefreshSentinelListeners
	if refreshListeners == nil {
		refreshListeners = refreshSentinelListeners
	}
	RegisterSentinelRoutes(mux, SentinelRouteDeps{
		Home:             deps.Home,
		Store:            newSentinelStoreAdapter(deps.Home.Root),
		Notifier:         newSentinelNotifierAdapter(),
		RefreshListeners: refreshListeners,
	})
	RegisterExecutionRoutes(mux, ExecutionRouteDeps{
		Manager:       deps.Manager,
		Home:          deps.Home,
		CreateSession: deps.CreateSession,
	})
	RegisterColonyRoutes(mux, ColonyRouteDeps{
		Home:    deps.Home,
		Manager: deps.Manager,
		Reveal:  deps.Reveal,
	})
	RegisterColonyToolRoutes(mux, ColonyToolsDeps{
		Home:    deps.Home,
		Manager: deps.Manager,
	})
	RegisterQueenRoutes(mux, QueenRouteDeps{
		Home:                deps.Home,
		Manager:             deps.Manager,
		CreateSession:       queenCreateSessionAdapter(deps.CreateSession),
		DefaultModel:        deps.DefaultModel,
		ValidateProviderKey: queenValidateProviderKey,
		RefreshQueenIdentity: func(queenID string, profile map[string]any) {
			refreshQueenLiveIdentity(deps.Manager, queenID, profile)
		},
	})
	RegisterQueenToolRoutes(mux, QueenToolRouteDeps{
		Home:    deps.Home,
		Manager: deps.Manager,
		// The catalogue hook owns what the Tool Library sees: a live session's
		// registry when one exists, else the process-wide harness catalog the
		// loader built at boot (the same surface the queen sees).
		MCPCatalog:     deps.QueenToolCatalog,
		LifecycleTools: deps.QueenLifecycleTools,
		// A live session's MCP tier is rebuilt when the Tool Library saves a
		// new allowlist; a session with no phase state or no MCP tools
		// reports no refresh instead of a fabricated count.
		RefreshSessionTools: RefreshQueenToolAllowlist,
	})
	// Skills library + per-queen skill editing; the shared Skills page and
	// queen profile call these routes directly.
	RegisterSkillsRoutes(mux, SkillsRouteDeps{
		Home:    deps.Home,
		Manager: deps.Manager,
	})
	// Queen mail runs on the ported queenmail backend: tokens, inboxes, queue and
	// delivery through the same session-create hook as the queen routes. With
	// the feature off the adapter keeps the routes' own 401 contract. The
	// adapter is recorded on the manager so chat sessions register the two
	// tools against the same service the routes use.
	queenMail := deps.QueenMail
	if queenMail == nil {
		queenMail = NewQueenMailAdapter(QueenMailAdapterOptions{
			Home:          deps.Home,
			Manager:       deps.Manager,
			CreateSession: deps.CreateSession,
		})
	}
	if deps.Manager != nil {
		deps.Manager.SetQueenMail(queenMail)
	}
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{QueenMail: queenMail})
	// The dashboard's read-only view of the Claude Code queens: the configured
	// ones, their conversations and the transcript of one, from the same
	// queen-mail service the routes above use (her runner included, for the
	// "running" mark).
	RegisterClaudeCodeQueenRoutes(mux, NewClaudeCodeQueenRouteDeps(queenMail.Mail(), queenMail.ClaudeStore(), queenMail.ClaudeRunner()))
}

// rejectDotSegmentsAPI answers JSON 400 for any /api/ path carrying a ".."
// segment. Without it Go's ServeMux cleans the path first and replies 307 to
// the cleaned (non-API) URL, so per-route traversal guards never run and a
// client can be redirected out of the API surface.
func (a *App) rejectDotSegmentsAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			candidate := r.URL.Path
			if r.URL.RawPath != "" {
				candidate = r.URL.RawPath
			}
			if containsDotSegment(candidate) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func containsDotSegment(path string) bool {
	lowered := strings.ToLower(strings.ReplaceAll(path, "%2e", "."))
	for _, segment := range strings.Split(lowered, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *App) DistDir() string {
	return a.distDir
}

// HasFrontend reports whether NewApp found a dashboard to serve, either on
// disk or embedded in an embed_ui build.
func (a *App) HasFrontend() bool {
	return a.distDir != "" || a.distFS != nil
}

// Parity: framework/server/app.py::handle_health
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	sessions := []FeatureSession(nil)
	if a.deps.Sessions != nil {
		sessions = a.deps.Sessions()
	}
	agentsLoaded := 0
	for _, session := range sessions {
		if session.ColonyID != "" {
			agentsLoaded++
		}
	}
	resources := any(map[string]any{"verdict": "unknown"})
	if a.deps.Resources != nil {
		resources = a.deps.Resources()
	}
	executor := ExecutorState{MaxWorkers: a.requestExecutorMax()}
	if a.deps.Executor != nil {
		executor = a.deps.Executor()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"sessions":         len(sessions),
		"agents_loaded":    agentsLoaded,
		"resources":        resources,
		"request_executor": executor,
	})
}

// Parity: framework/server/app.py::handle_startup_status
func (a *App) handleStartupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BootStatus())
}

// messageRouteLLM is MessageRouteDeps.BuildLLM. An injected hook (tests,
// embedded hosts) wins; otherwise the main llm slot is rebuilt from
// configuration.json on every call — the same resolver session creation uses —
// so a Settings edit reaches the classifier without a restart. A provider that
// cannot be resolved (no key configured) answers nil, which the routes surface
// as their explicit 500 instead of a classifier quietly routing to the
// fallback queen.
//
// Parity: framework/server/routes_messages.py (manager.build_llm)
func messageRouteLLM(deps AppDeps) func() llm.Completer {
	if deps.BuildLLM != nil {
		return deps.BuildLLM
	}
	return func() llm.Completer {
		requested := ""
		if deps.DefaultModel != nil {
			requested = deps.DefaultModel()
		}
		provider, err := MainSlotProvider(deps.Home, requested)
		if err != nil {
			log.Printf("messages: main llm provider unavailable: %v", err)
			return nil
		}
		return provider
	}
}

// queenCreateSessionAdapter bridges the queen route hook to the session-create
// hook the CLI wires: the queen table passes loosely typed prompt/phase values,
// the create hook takes typed pointers.
func queenCreateSessionAdapter(create func(CreateSessionParams) (*Session, error)) func(QueenSessionCreateParams) (*Session, error) {
	if create == nil {
		return nil
	}
	return func(params QueenSessionCreateParams) (*Session, error) {
		out := CreateSessionParams{
			ColonyID:        params.ColonyID,
			QueenResumeFrom: params.QueenResumeFrom,
			QueenName:       params.QueenName,
		}
		if prompt, ok := params.InitialPrompt.(string); ok && prompt != "" {
			out.InitialPrompt = &prompt
		}
		if phase, ok := params.InitialPhase.(string); ok && phase != "" {
			out.InitialPhase = &phase
		}
		return create(out)
	}
}

// processStartTS is the /api/maintenance uptime source: the process start as
// Unix seconds, captured when the app was constructed.
func (a *App) processStartTS() *float64 {
	started := a.processStart
	return &started
}

// credentialSpecsFromCatalog derives the settings credential rows from the
// embedded model catalog: one row per provider that ships an API-key env var.
// The catalogue is the same source /api/models/overview reads, so a provider
// listed there always has a matching credential row.
func credentialSpecsFromCatalog() []CredentialSpecView {
	catalog, err := llm.LoadModelCatalog()
	if err != nil {
		return nil
	}
	envByProvider := map[string]string{}
	for _, presetID := range catalog.PresetOrder {
		preset := catalog.Presets[presetID]
		if preset == nil || preset.Provider == "" || !preset.HasAPIKeyEnvVar || preset.APIKeyEnvVar == "" {
			continue
		}
		if _, present := envByProvider[preset.Provider]; !present {
			envByProvider[preset.Provider] = preset.APIKeyEnvVar
		}
	}
	specs := make([]CredentialSpecView, 0, len(envByProvider))
	for _, provider := range catalog.ProviderOrder {
		envVar := envByProvider[provider]
		if envVar == "" {
			continue
		}
		specs = append(specs, CredentialSpecView{
			Name:                  provider,
			CredentialID:          provider,
			EnvVar:                envVar,
			Description:           "API key for " + provider,
			DirectAPIKeySupported: true,
			CredentialKey:         "api_key",
			AdenProviderName:      provider,
		})
	}
	return specs
}

func (a *App) requestExecutorMax() int {
	a.executorMu.Lock()
	defer a.executorMu.Unlock()
	if a.executorMax == 0 {
		a.executorMax = defaultRequestExecutorMax
		if value := os.Getenv("HIVE_REQUEST_EXECUTOR_MAX"); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				a.executorMax = parsed
			}
		}
	}
	return a.executorMax
}

// Parity: framework/server/app.py::access_log_middleware
func (a *App) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		quiet := r.URL.Path == runtimeLogsPath
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.Status()
		elapsed := time.Since(started)
		slow := elapsed > time.Second
		if quiet && status < 400 && !slow {
			return
		}
		slowMark := ""
		if slow {
			slowMark = " SLOW"
		}
		a.logger.Printf("[access] %s %s -> %d (%.0fms)%s",
			r.Method, redactedRequestURI(r.URL), status, elapsed.Seconds()*1000, slowMark)
	})
}

// sensitiveQueryKeys are the query names whose values are credentials or
// one-time codes. The access log keeps the shape of the request (useful for
// debugging) but never the secret itself.
func sensitiveQueryKey(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "token", "access_token", "refresh_token", "id_token", "code", "key",
		"api_key", "apikey", "secret", "client_secret", "password", "signature",
		"sig", "auth", "jwt":
		return true
	}
	return strings.HasSuffix(name, "_token") || strings.HasSuffix(name, "_secret") || strings.HasSuffix(name, "_key") ||
		strings.HasSuffix(name, "-token") || strings.HasSuffix(name, "-secret") || strings.HasSuffix(name, "-key")
}

// redactedRequestURI renders the request URI with sensitive query values
// replaced, so provider callbacks and webhook secrets do not land in logs.
func redactedRequestURI(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.RequestURI()
	}
	query := u.Query()
	redacted := false
	for name := range query {
		if sensitiveQueryKey(name) {
			query.Set(name, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return u.RequestURI()
	}
	copy := *u
	copy.RawQuery = query.Encode()
	return copy.RequestURI()
}

// Parity: framework/server/app.py::desktop_auth_middleware
func (a *App) desktopAuth(next http.Handler) http.Handler {
	if a.token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/queen-mail/") {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-Hive-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Parity: framework/server/app.py::no_cache_api_middleware
func (a *App) noCacheAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&noCacheWriter{ResponseWriter: w}, r)
	})
}

// Parity: framework/server/app.py::error_middleware
func (a *App) errorRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Printf("[error_middleware] %s %s\n%s", r.Method, r.URL.Path, debug.Stack())
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

type noCacheWriter struct {
	http.ResponseWriter
}

func (w *noCacheWriter) WriteHeader(code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(code)
}

func (w *noCacheWriter) Write(p []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(p)
}

func (w *noCacheWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Parity: framework/server/app.py::frontend_dist_dir
func FrontendDistDir() string {
	for _, candidate := range frontendDistCandidates() {
		index := filepath.Join(candidate, "index.html")
		info, err := os.Stat(index)
		if err != nil || info.IsDir() {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			candidate = absolute
		}
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			return resolved
		}
		return candidate
	}
	return ""
}

func frontendDistCandidates() []string {
	candidates := []string{filepath.Join("frontend", "dist")}
	if here := packageSourceDir(); here != "" {
		candidates = append(candidates,
			filepath.Join(here, "..", "..", "frontend", "dist"),
			filepath.Join(here, "..", "..", "..", "frontend", "dist"),
			filepath.Join(here, "frontend"),
		)
	}
	return candidates
}

func packageSourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}

// Parity: framework/server/app.py::_setup_static_serving
//
// The SPA catch-all is the last route, so it also sees requests no handler
// claimed. Upstream's contract is one rule: an existing file inside the dist
// directory is served as-is, and every other path — an unregistered /api/*
// URL, a missing asset, a client-side route — answers with index.html.
//
// Upstream pins the /api/* half in
// framework/server/tests/test_api.py::TestErrorMiddleware::test_unknown_api_route_falls_back_to_frontend
// (200 + index.html, Cache-Control: no-store). This port used to answer those
// with a JSON 404 instead; it now follows the same rule so both servers expose
// the same surface. The traversal guard is shared: a resolved path outside the
// dist directory is not a file to serve, so it falls through to index.html.
func (a *App) handleSPA(w http.ResponseWriter, r *http.Request) {
	if a.distFS != nil {
		a.handleEmbeddedSPA(w, r)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/")
	candidate := filepath.Join(a.distDir, filepath.FromSlash(relative))
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && pathWithin(candidate, a.distDir) {
		http.ServeFile(w, r, candidate)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.distDir, "index.html"))
}

// handleEmbeddedSPA serves the in-binary dashboard with the same fallback
// contract as the disk path: existing regular files are served as-is, every
// other path returns index.html.
func (a *App) handleEmbeddedSPA(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" {
		if info, err := fs.Stat(a.distFS, name); err == nil && info.Mode().IsRegular() {
			a.distFiles.ServeHTTP(w, r)
			return
		}
	}
	index, err := fs.ReadFile(a.distFS, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}

func pathWithin(path, dir string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolveDistDir(dir string) string {
	if dir == "" {
		return ""
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}
