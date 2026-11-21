package cli

// Parity: framework/loader/cli.py

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/queen"
	"github.com/aden-hive/hive-go/internal/resources"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

var goos = runtime.GOOS

var reservedJSONStems = map[string]bool{
	"agent":         true,
	"flowchart":     true,
	"triggers":      true,
	"configuration": true,
	"metadata":      true,
}

const defaultServerURL = "http://127.0.0.1:8787"

const shutdownGraceSeconds = 6.0

// Parity: framework/loader/cli.py::register_commands
func (r *Runner) loaderCommands() []*commandSpec {
	return []*commandSpec{
		r.serveCommand(false),
		r.serveCommand(true),
		r.queenCommands(),
		r.colonyCommands(),
		r.sessionCommands(),
		r.chatCommand(),
	}
}

// Parity: framework/loader/cli.py::_register_serve, _register_open
func (r *Runner) serveCommand(open bool) *commandSpec {
	modelDefault := any("")
	colonyDefault := any([]string{})
	if open {
		return &commandSpec{
			Prog:        "hive open",
			Help:        "Start the server and open the dashboard",
			Description: "Shortcut for 'hive serve --open'.",
			Flags: []argSpec{
				{Dest: "host", Long: "host", Kind: kindString, Default: "127.0.0.1"},
				{Dest: "port", Short: "p", Long: "port", Kind: kindInt, Default: 8787},
				{Dest: "colony", Short: "c", Long: "colony", Kind: kindAppend, Default: colonyDefault},
				{Dest: "model", Short: "m", Long: "model", Kind: kindString, Default: modelDefault},
				{Dest: "verbose", Short: "v", Long: "verbose", Kind: kindBool},
				{Dest: "debug", Long: "debug", Kind: kindBool},
			},
			Run: r.cmdOpen,
		}
	}
	return &commandSpec{
		Prog:        "hive serve",
		Help:        "Start the HTTP API server",
		Description: "Start the aiohttp server exposing REST + SSE for queens, colonies, and sessions.",
		Flags: []argSpec{
			{Dest: "host", Long: "host", Kind: kindString, Default: "127.0.0.1",
				Help: "Host to bind (default: 127.0.0.1)"},
			{Dest: "port", Short: "p", Long: "port", Kind: kindInt, Default: 8787,
				Help: "Port to listen on (default: 8787)"},
			{Dest: "colony", Short: "c", Long: "colony", Kind: kindAppend, Default: colonyDefault,
				Help: "Colony path or name to preload (repeatable)"},
			{Dest: "model", Short: "m", Long: "model", Kind: kindString, Default: modelDefault,
				Help: "LLM model for preloaded colonies"},
			{Dest: "open", Long: "open", Kind: kindBool,
				Help: "Open dashboard in browser after start"},
			{Dest: "verbose", Short: "v", Long: "verbose", Kind: kindBool,
				Help: "Enable INFO log level"},
			{Dest: "debug", Long: "debug", Kind: kindBool,
				Help: "Enable DEBUG log level"},
		},
		Run: r.cmdServe,
	}
}

// Parity: framework/loader/cli.py::_register_queen
func (r *Runner) queenCommands() *commandSpec {
	jsonFlag := argSpec{Dest: "json", Long: "json", Kind: kindBool, Help: "Output as JSON"}
	return &commandSpec{
		Prog:        "hive queen",
		Help:        "Manage queen profiles",
		Description: "List, inspect, and explore queen identities.",
		Dest:        "subcommand",
		Subcommands: []*commandSpec{
			{
				Prog:  "hive queen list",
				Help:  "List all queen profiles",
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdQueenList,
			},
			{
				Prog: "hive queen show",
				Help: "Show a queen profile",
				Positionals: []positionalSpec{{Dest: "queen_id", Required: true,
					Help: "Queen identity (e.g. queen_technology)"}},
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdQueenShow,
			},
			{
				Prog: "hive queen sessions",
				Help: "List sessions belonging to a queen",
				Positionals: []positionalSpec{{Dest: "queen_id", Required: true,
					Help: "Queen identity"}},
				Flags: []argSpec{{Dest: "json", Long: "json", Kind: kindBool}},
				Run:   r.cmdQueenSessions,
			},
		},
	}
}

// Parity: framework/loader/cli.py::_register_colony
func (r *Runner) colonyCommands() *commandSpec {
	jsonFlag := argSpec{Dest: "json", Long: "json", Kind: kindBool, Help: "Output as JSON"}
	return &commandSpec{
		Prog:        "hive colony",
		Help:        "Manage colonies",
		Description: "List, inspect, and delete colonies on disk.",
		Dest:        "subcommand",
		Subcommands: []*commandSpec{
			{
				Prog:  "hive colony list",
				Help:  "List all colonies",
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdColonyList,
			},
			{
				Prog: "hive colony info",
				Help: "Show colony details",
				Positionals: []positionalSpec{{Dest: "name", Required: true,
					Help: "Colony name or path"}},
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdColonyInfo,
			},
			{
				Prog: "hive colony delete",
				Help: "Delete a colony from disk",
				Positionals: []positionalSpec{{Dest: "name", Required: true,
					Help: "Colony name"}},
				Flags: []argSpec{
					{Dest: "purge_storage", Long: "purge-storage", Kind: kindBool,
						Help: "Also delete worker storage at ~/.hive/agents/{name}/"},
					{Dest: "yes", Short: "y", Long: "yes", Kind: kindBool,
						Help: "Skip confirmation"},
				},
				Run: r.cmdColonyDelete,
			},
		},
	}
}

// Parity: framework/loader/cli.py::_register_session
func (r *Runner) sessionCommands() *commandSpec {
	serverFlag := argSpec{Dest: "server", Long: "server", Kind: kindString,
		Default: defaultServerURL, Help: "Hive server URL"}
	return &commandSpec{
		Prog:        "hive session",
		Help:        "Manage sessions",
		Description: "List live and cold sessions, stop running sessions.",
		Dest:        "subcommand",
		Subcommands: []*commandSpec{
			{
				Prog: "hive session list",
				Help: "List sessions",
				Flags: []argSpec{
					{Dest: "cold", Long: "cold", Kind: kindBool,
						Help: "Include cold (on-disk) sessions"},
					serverFlag,
					{Dest: "json", Long: "json", Kind: kindBool, Help: "Output as JSON"},
				},
				Run: r.cmdSessionList,
			},
			{
				Prog: "hive session stop",
				Help: "Stop a live session",
				Positionals: []positionalSpec{{Dest: "session_id", Required: true,
					Help: "Session ID to stop"}},
				Flags: []argSpec{serverFlag},
				Run:   r.cmdSessionStop,
			},
		},
	}
}

// Parity: framework/loader/cli.py::_register_chat
func (r *Runner) chatCommand() *commandSpec {
	return &commandSpec{
		Prog:        "hive chat",
		Help:        "Send a message to a live queen session",
		Description: "POST a chat message to a running session via the HTTP API.",
		Positionals: []positionalSpec{
			{Dest: "session_id", Required: true, Help: "Session ID"},
			{Dest: "message", Required: true, Help: "Message text"},
		},
		Flags: []argSpec{{Dest: "server", Long: "server", Kind: kindString,
			Default: defaultServerURL, Help: "Hive server URL"}},
		Run: r.cmdChat,
	}
}

// Parity: framework/loader/cli.py::cmd_serve
func (r *Runner) cmdServe(args *parsedArgs) int {
	return r.Serve(args)
}

// Parity: framework/loader/cli.py::cmd_open
func (r *Runner) cmdOpen(args *parsedArgs) int {
	r.StartPing("hive-open")
	args.values["open"] = true
	args.provided["open"] = true
	return r.Serve(args)
}

// isLoopbackListenHost reports whether binding host only exposes the server on
// the loopback interface. An empty host means all interfaces to net.Listen.
func isLoopbackListenHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Parity: framework/loader/cli.py::cmd_serve (the run_server body)
func (r *Runner) defaultServe(args *parsedArgs) int {
	host := args.str("host")
	port := args.intValue("port")
	if !isLoopbackListenHost(host) && strings.TrimSpace(os.Getenv("HIVE_DESKTOP_TOKEN")) == "" {
		fmt.Fprintln(r.Stdout, "Refusing to bind a non-loopback host without HIVE_DESKTOP_TOKEN. "+
			"Set the token (desktop auth) before exposing the API on the network.")
		return 1
	}
	_ = os.Setenv("HIVE_RUNTIME_PORT", strconv.Itoa(port))
	runtimeOwnerPID()

	manager := hiveserver.NewSessionManager()

	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: cannot resolve HIVE_HOME: %v\n", err)
	}
	// The dashboard stores keys in the encrypted credential store while the
	// ported tools read os.Getenv. Export the store before the app (and any
	// tool registry) is built so saved credentials are live from the start.
	hiveserver.SyncCredentialEnv(home)

	// llm.aux_max_tokens governs every small utility call the process makes
	// (recall selection, sentinel classification, judge, skill tests); install
	// it before any session or reminder source exists.
	llm.SetAuxMaxTokens(hiveserver.AuxMaxTokens(home))

	// MCP servers configured for this home are connected before any session
	// exists: every catalogue built afterwards (queen chat, colony worker
	// pool) starts with their namespace-prefixed tools. Unreachable servers
	// degrade to a warning and stay visible in GET /api/mcp/servers.
	stopMCP := r.startMCPRuntime(home)
	defer stopMCP()

	// channels.<name>.enabled is a real configuration field upstream gates a
	// channel plugin on. This runtime has no plugin host, so the flag cannot
	// start anything: say so once at boot instead of ignoring it silently.
	for _, name := range hiveserver.UnhostedChannels(home) {
		fmt.Fprintf(r.Stdout, "Warning: channels.%s.enabled is set, but this runtime has no channel host; the channel will not run\n", name)
	}

	// Sentinel is process-wide: it owns the parked-queen ladder for every
	// colony-queen chat session built below (nudge / done / escalate) and the
	// outbound delivery drain. Without it the escalation source's gate never
	// opens and no park is ever classified. Start it before any session so
	// NewChatRuntime sees the manager when it registers its reminder sources.
	sentinelManager := hiveserver.NewSentinelManager(home.Root, manager)
	defer sentinelManager.Stop()
	if startErr := sentinelManager.Start(context.Background()); startErr != nil {
		fmt.Fprintf(r.Stdout, "Warning: sentinel unavailable: %v\n", startErr)
	}

	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	lifecycle := r.newColonyLifecycle(manager, &liveMu, &live, home, args.str("model"))

	// One queen-mail service for the process: routes, deliverer and the chat
	// tools share it. Built before preloaded sessions so they register too.
	queenMail, createSession := r.newQueenMailService(manager, &liveMu, &live, home, args.str("model"))
	defer queenMail.Close()

	for _, colonyArg := range args.list("colony") {
		r.loadPreloadedColonySession(colonyArg, manager, &liveMu, &live, home, args.str("model"))
	}

	// Runtime logs: the route reads the process-wide ring; stderr still sees all,
	// and the rotating server log file joins unless HIVE_NO_FILE_LOG is set.
	installRuntimeLogBuffer(home)
	// Host-resource monitor: the sampler loop behind /api/health/resources and
	// the `resources` rollup on /api/health. It starts sampling immediately so
	// the first request has a real snapshot (upstream's monitor does the same).
	resourceMonitor := resources.NewMonitor()
	resourceMonitor.Start(context.Background(), 0)
	soulProbeAnswerFor := newSoulProbeAnswerFor(home, args.str("model"))

	app := hiveserver.NewApp(newServeAppDeps(home, hiveserver.AppDeps{
		Resources:        func() any { return resourceMonitor.Rollup() },
		ResourceSnapshot: resourceMonitor.Snapshot,
		Sessions: func() []hiveserver.FeatureSession {
			liveMu.Lock()
			defer liveMu.Unlock()
			out := make([]hiveserver.FeatureSession, 0, len(live))
			for _, record := range live {
				out = append(out, hiveserver.FeatureSession{ColonyID: record.ColonyID})
			}
			return out
		},
		LiveSessions:           manager.ListSessions,
		Manager:                manager,
		Home:                   home,
		Colony:                 lifecycle,
		CreateColonyFromSource: lifecycle.CreateColonyFromSource,
		CreateSession:          createSession,
		QueenMail:              queenMail,
		SoulProbeAnswerFor:     soulProbeAnswerFor,
		SoulReflect:            newSoulReflectFor(home, args.str("model")),
	}))
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		fmt.Fprintf(r.Stdout, "Error: %v\n", err)
		return 1
	}
	server := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	hiveserver.MarkBootReady("Server ready")
	// Bridge + soul lifecycles: loader_background_services.go.
	defer startServeBackgroundServices(r, home, manager)()
	dashboardURL := fmt.Sprintf("http://%s:%d", host, port)
	// A Claude Code queen's runs reach this server here (upstream sets
	// queen_mail.base_url once serve listens).
	queenMail.Mail().SetBaseURL(dashboardURL)
	hasFrontend := app.HasFrontend()

	liveMu.Lock()
	liveCount, queenOnly := 0, 0
	for _, record := range live {
		if record.ColonyID != "" {
			liveCount++
		} else {
			queenOnly++
		}
	}
	liveMu.Unlock()

	fmt.Fprintln(r.Stdout)
	fmt.Fprintf(r.Stdout, "Hive API server running on %s\n", dashboardURL)
	if hasFrontend {
		fmt.Fprintf(r.Stdout, "Dashboard:  %s\n", dashboardURL)
	}
	fmt.Fprintf(r.Stdout, "Health:     %s/api/health\n", dashboardURL)
	fmt.Fprintf(r.Stdout, "Sessions:   %d colony, %d queen-only\n", liveCount, queenOnly)
	fmt.Fprintln(r.Stdout)
	fmt.Fprintln(r.Stdout, "Press Ctrl+C to stop")

	if args.boolean("open") && hasFrontend {
		r.OpenBrowser(dashboardURL)
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	received := 0
	for {
		select {
		case sig := <-signals:
			received++
			name := signalName(sig)
			if received > 1 {
				fmt.Fprintf(r.Stdout, "\nReceived %s again — force quitting.\n", name)
				os.Exit(130)
			}
			fmt.Fprintf(r.Stdout, "\nReceived %s, shutting down gracefully… (press Ctrl+C again to force quit)\n", name)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(shutdownGraceSeconds*float64(time.Second)))
				defer cancel()
				_ = server.Shutdown(ctx)
			}()
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(r.Stdout, "Server error: %v\n", err)
				return 1
			}
			return 0
		}
	}
}

// loadPreloadedColonySession builds and registers the live session behind one
// `serve --colony <path>` argument; see loader_preloaded_session.go.
// attachPreloadedChatRuntime lives there too, keeping this file inside the
// repository's frozen size budget.

// createChatSession is the standard (non-colony) POST /api/sessions branch:
// it builds a live queen session with a real provider, tool box, agent loop and
// on-disk transcript. The session is registered before the initial prompt is
// delivered so a slow first turn never blocks the create response.
//
// Parity: framework/server/session_manager.py::SessionManager.create_session
func (r *Runner) createChatSession(
	params hiveserver.CreateSessionParams,
	manager *hiveserver.SessionManager,
	liveMu *sync.Mutex,
	live *[]*hiveserver.Session,
	home hivehome.Home,
	defaultModel string,
) (*hiveserver.Session, error) {
	return r.createSessionWithRuntime(params, manager, liveMu, live, home, defaultModel, nil, "")
}

// createSessionWithRuntime is createChatSession with an optional colony
// runtime already built by the caller. The colony fork path builds and arms the
// runtime before the session exists (so a start failure is a clean 503 with no
// half-created session), then hands it here to avoid building it twice.
func (r *Runner) createSessionWithRuntime(
	params hiveserver.CreateSessionParams,
	manager *hiveserver.SessionManager,
	liveMu *sync.Mutex,
	live *[]*hiveserver.Session,
	home hivehome.Home,
	defaultModel string,
	prebuilt *colonyhost.Host,
	prebuiltPath string,
) (*hiveserver.Session, error) {
	// Resolve the id, queen scope and any resume target together: a resume is
	// only valid inside the queen it would write under, and the id it adopts
	// is the one /events and /chat already address.
	identity, err := planChatSession(params, home)
	if err != nil {
		return nil, err
	}
	sessionID := identity.SessionID
	queenScope := identity.QueenScope
	hiveserver.ReportBootStage("Preparing queen session", sessionID)

	record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	record.LoadedAt = float64(time.Now().UnixNano()) / 1e9
	if params.ColonyID != nil {
		record.ColonyID = *params.ColonyID
	}
	if params.QueenName != nil {
		record.QueenName = *params.QueenName
	}
	// The persisted session a resume reopened; empty for a fresh create.
	record.QueenResumeFrom = identity.ResumeFrom
	// The session's on-disk root: the phase state stamps meta.json here and
	// the colony lifecycle (spawn lock, agent path) writes the same file.
	// Without it those writes silently degraded to an in-memory flag.
	sessionDir := chatSessionDir(home, queenScope, sessionID)
	if identity.ResumedDir != "" {
		sessionDir = identity.ResumedDir
	}
	record.QueenDir = sessionDir
	// Phase cascade (initial_phase > meta.json["phase"] > bindings >
	// independent) is resolved and stamped before any runtime is built, so a
	// session whose provider is unavailable still records its phase.
	phaseState := hiveserver.AttachSessionPhaseState(record, params.InitialPhase, sessionDir)
	if phaseState != nil {
		// The queen identity rides the phase state, as the upstream identity
		// hook stamps it at session start.
		if queenScope != "" && queenScope != "default" {
			if profile, profileErr := queen.LoadQueenProfile(home.QueensDir(), queenScope); profileErr == nil {
				phaseState.SetQueenIdentity(queenScope, profile)
			}
		}
	}
	// A resume retry must reuse the runtime already holding the session
	// instead of building a second one on the same transcript.
	if existing, resumeErr := resumeLiveSession(manager, identity, params.InitialPrompt, params.InitialPhase); resumeErr != nil || existing != nil {
		return existing, resumeErr
	}

	requestedModel := ""
	if params.Model != nil {
		requestedModel = *params.Model
	}
	if strings.TrimSpace(requestedModel) == "" {
		// Fall back to the serve --model flag before the configured default.
		requestedModel = defaultModel
	}

	skillsCatalog := liveSkillsCatalog(record, home, queenScope)
	var runtime *hiveserver.ChatRuntime
	provider, providerErr := queenAwareChatProvider(home, requestedModel, queenScope)
	if providerErr != nil {
		// Credentials are missing for a remote model: keep the session
		// listable but leave Chat nil so POST /chat answers 503 rather than
		// faking delivery against an endpoint that would 401.
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", sessionID, providerErr)
	} else {
		toolbox := builtin.New("")
		tools := newSessionToolCatalogue(sessionDir, toolbox, home)
		onTurnStart := r.enableQueenTools(tools, manager, sessionID, record, home, liveMu, live, defaultModel)
		// Flat lazy layer: first request advertises the Python reference's 24
		// always-on names; the rest of the default surface and the flat
		// directory tools load on demand via search_tools. Grouped mode
		// returns nils and keeps the full catalogue.
		alwaysOnTools, deferredTools := tools.LazySurfaceDeps()
		built, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
			LLM:               provider,
			Tools:             tools.ModelDefs(),
			AlwaysOnToolNames: alwaysOnTools,
			DeferredTools:     deferredTools,
			Execute:           tools.Execute,
			ExecuteBlocks:     tools.ExecuteBlocks,
			Root:              sessionDir,
			Bus:               record.EventBus,
			SessionID:         sessionID,
			QueenName:         record.QueenName,
			HomeRoot:          home.Root,
			SkillsCatalog:     skillsCatalog.Catalog,
			OnTurnStart:       onTurnStart,
			// A text-only main model gets its tool images captioned by the
			// vision_fallback slot; nil when there is none to use.
			VisionCaptioner: visionCaptionerFor(home),
			// The queen's phase rides node_loop_iteration, as upstream's
			// iteration_metadata_provider reports it.
			Phase: func() string { return hiveserver.SessionPhaseLabel(record) },
		})
		if err != nil {
			fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", sessionID, err)
		} else {
			runtime = built
			record.Chat = built
			record.LLM = provider
			// Session title: written by the worker model after the first
			// finished turn, exactly as upstream's subscribe_title_trigger
			// registers it on the session bus.
			if record.EventBus != nil && record.QueenDir != "" {
				record.TitleTrigger = hiveserver.SubscribeTitleTrigger(record.EventBus, record.QueenDir, provider)
			}
			// The session's queen executor is the chat runtime: timer and
			// webhook triggers reach the queen through it (queenNode →
			// InjectTrigger), which is the assignment the audit found missing.
			record.QueenExecutor = &hiveserver.QueenExecutor{
				NodeRegistry: map[string]hiveserver.QueenNode{"queen": built},
			}
		}
	}

	// A session bound to a colony carries that colony's live runtime, not
	// just its id: arm the host (overseer included) before the session is
	// registered. The fork path hands in a runtime it already started; a
	// plain create-session with colony_id builds it here. Either way a
	// missing colony is a 404 and a runtime that cannot start is a 500, so
	// the create route reports the failure instead of returning a session
	// that silently has no colony behind it.
	switch {
	case prebuilt != nil:
		record.Colony = prebuilt
		record.WorkerPath = prebuiltPath
	case record.ColonyID != "":
		colonyPath := home.ColonyDir(record.ColonyID)
		info, statErr := os.Stat(colonyPath)
		if statErr != nil || !info.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("colony '%s' is not a directory", record.ColonyID)
			}
			return nil, &hiveserver.SessionCreateColonyNotFoundError{Err: statErr}
		}
		built, buildErr := r.newColonyHost(colonyPath, record.ColonyID, home, requestedModel)
		if buildErr != nil {
			return nil, fmt.Errorf("colony runtime unavailable for %s: %w", record.ColonyID, buildErr)
		}
		if startErr := startColonyHost(context.Background(), built, home, record.QueenName, record.ColonyID); startErr != nil {
			return nil, startErr
		}
		record.Colony = built
		record.WorkerPath = colonyPath
	}

	// A colony-bound session rebuilds its triggers.json runtime before it is
	// registered: enabled timers and webhook routes survive a restart, and the
	// queen executor above is already in place for the first fire.
	if record.WorkerPath != "" {
		if loaded, started := hiveserver.RestoreSessionTriggers(record); loaded > 0 {
			fmt.Fprintf(r.Stdout, "Restored %d trigger(s) (%d running) for %s\n", loaded, started, record.ID)
		}
	}

	manager.AddSession(record)
	liveMu.Lock()
	*live = append(*live, record)
	liveMu.Unlock()

	if err := startSessionTurns(record, runtime, params.InitialPrompt); err != nil {
		return nil, err
	}
	hiveserver.MarkBootReady("Queen ready")
	return record, nil
}

// colonyLifecycle is the loader's implementation of the server's colony
// lifecycle seam. It owns the live colony hosts behind sessions: LoadColony
// builds, starts and arms a colony runtime for an existing session, UnloadColony
// stops it, and CreateColonyFromSource forks a queen session into a new colony
// on disk before resuming it live. Every failure is returned as an error the
// route maps to 4xx/5xx — a session never looks colony-backed while its runtime
// is missing.
type colonyLifecycle struct {
	manager *hiveserver.SessionManager
	home    hivehome.Home
	model   string
	out     io.Writer

	// build is the host factory. It is a field so tests can substitute a
	// runtime backed by a scripted completer; production points at
	// Runner.newColonyHost.
	build func(colonyPath, colonyID, model string) (*colonyhost.Host, error)

	// createSession registers the colony session on an already-armed runtime.
	// Field for the same reason as build: it closes over the runner's live
	// session bookkeeping without those locks living on the lifecycle.
	createSession func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error)

	// mu serialises load/unload/fork: a session's Colony field is read by
	// concurrent HTTP handlers, so two lifecycle calls must not race to
	// replace it.
	mu sync.Mutex
}

var colonySlugPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func (r *Runner) newColonyLifecycle(manager *hiveserver.SessionManager, liveMu *sync.Mutex, live *[]*hiveserver.Session, home hivehome.Home, model string) *colonyLifecycle {
	out := r.Stdout
	if out == nil {
		out = io.Discard
	}
	return &colonyLifecycle{
		manager: manager,
		home:    home,
		model:   model,
		out:     out,
		build: func(colonyPath, colonyID, model string) (*colonyhost.Host, error) {
			return r.newColonyHost(colonyPath, colonyID, home, model)
		},
		createSession: func(params hiveserver.CreateSessionParams, built *colonyhost.Host, colonyPath string) (*hiveserver.Session, error) {
			return r.createSessionWithRuntime(params, manager, liveMu, live, home, model, built, colonyPath)
		},
	}
}

func (l *colonyLifecycle) modelFor(model *string) string {
	if model != nil && strings.TrimSpace(*model) != "" {
		return strings.TrimSpace(*model)
	}
	return l.model
}

// LoadColony is the POST /api/sessions/{id}/colony branch: resolve the colony
// on disk, start its runtime (overseer included) and bind it to the session.
//
// Parity: framework/server/session_manager.py::SessionManager.load_colony
func (l *colonyLifecycle) LoadColony(sessionID, colonyID string, model *string) (*hiveserver.Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	session := l.manager.GetSession(sessionID)
	if session == nil {
		return nil, &hiveserver.ColonyLoadConflictError{Message: fmt.Sprintf("Session '%s' not found", sessionID)}
	}
	if err := l.attach(session, strings.TrimSpace(colonyID), l.modelFor(model)); err != nil {
		return nil, err
	}
	return session, nil
}

// UnloadColony stops the session's colony runtime. False means the session had
// no colony loaded, which the route reports as a 409 (or 404 for an unknown
// session).
//
// Parity: framework/server/session_manager.py::SessionManager.unload_colony
func (l *colonyLifecycle) UnloadColony(sessionID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	session := l.manager.GetSession(sessionID)
	if session == nil || session.Colony == nil {
		return false
	}
	// A trigger belongs to the colony: without it, a timer reading the old
	// WorkerPath would keep firing at a queen whose colony is gone.
	session.StopAllTriggers()
	stopColonyRuntime(session.Colony)
	session.DetachColony()
	return true
}

// attach builds and arms the colony runtime and stores it on the session. Any
// failure aborts before the session is mutated, so a broken colony never
// replaces a working binding.
func (l *colonyLifecycle) attach(session *hiveserver.Session, colonyID, model string) error {
	if colonyID == "" {
		return &hiveserver.ColonyLoadConflictError{Message: "colony_id is required"}
	}
	colonyPath := l.home.ColonyDir(colonyID)
	info, statErr := os.Stat(colonyPath)
	if statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("colony '%s' is not a directory", colonyID)
		}
		return fmt.Errorf("colony not found: %s: %w", colonyID, statErr)
	}
	queenName := strings.TrimSpace(session.QueenName)
	if queenName == "" {
		queenName = colonyQueenName(l.home, colonyID)
	}
	built, buildErr := l.build(colonyPath, colonyID, model)
	if buildErr != nil {
		return fmt.Errorf("colony runtime unavailable for %s: %w", colonyID, buildErr)
	}
	if startErr := startColonyHost(context.Background(), built, l.home, queenName, colonyID); startErr != nil {
		return startErr
	}
	stopColonyRuntime(session.Colony)
	session.Colony = built
	session.ColonyID = colonyID
	session.WorkerPath = colonyPath
	// The bindings now say colony, so the phase follows: the session was
	// independent (or had no phase yet) and load_colony is the transition.
	hiveserver.ApplyQueenPhase(session, hiveserver.QueenPhaseColony)
	l.persistAgentMeta(session, colonyID, colonyPath)
	if loaded, started := hiveserver.RestoreSessionTriggers(session); loaded > 0 {
		fmt.Fprintf(l.out, "Restored %d trigger(s) (%d running) for %s\n", loaded, started, colonyID)
	}
	return nil
}

// CreateColonyFromSource backs the "Create Colony" popup: fork the source
// queen session into a new colony directory, lock the source so it cannot fork
// again, then resume the colony as a live session.
//
// Parity: framework/server/routes_sessions.py::_create_colony_from_source
func (l *colonyLifecycle) CreateColonyFromSource(params hiveserver.CreateColonyFromSourceParams) (*hiveserver.Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	colonyID := strings.TrimSpace(params.ColonyID)
	if !colonySlugPattern.MatchString(colonyID) {
		return nil, &hiveserver.ColonyForkError{
			Message: "colony_id must be lowercase alphanumeric with underscores",
			Status:  http.StatusBadRequest,
		}
	}
	source := l.manager.GetSession(params.SourceSessionID)
	if source == nil {
		return nil, &hiveserver.ColonyForkError{
			Message: fmt.Sprintf("source_session_id '%s' is not a live session", params.SourceSessionID),
			Status:  http.StatusNotFound,
		}
	}
	if source.ColonySpawned {
		return nil, &hiveserver.ColonyForkError{
			Message: "source session already spawned a colony — cannot fork again.",
			Status:  http.StatusConflict,
		}
	}
	colonyPath := l.home.ColonyDir(colonyID)
	if _, statErr := os.Stat(colonyPath); statErr == nil {
		return nil, &hiveserver.ColonyForkError{
			Message: fmt.Sprintf("colony '%s' already exists", colonyID),
			Status:  http.StatusConflict,
		}
	}

	queenName := strings.TrimSpace(source.QueenName)
	if params.QueenName != nil && strings.TrimSpace(*params.QueenName) != "" {
		queenName = strings.TrimSpace(*params.QueenName)
	}
	model := l.modelFor(params.Model)

	// 1. The colony exists on disk before any runtime does: a failed start
	//    removes the directory again, so a 503 never leaves a half-colony.
	if err := writeColonyFromSession(colonyPath, colonyID, queenName, params); err != nil {
		return nil, &hiveserver.ColonyForkError{Message: "failed to create colony: " + err.Error(), Status: http.StatusInternalServerError}
	}
	built, buildErr := l.build(colonyPath, colonyID, model)
	if buildErr != nil {
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "colony runtime unavailable: " + buildErr.Error(), Status: http.StatusServiceUnavailable}
	}
	if startErr := startColonyHost(context.Background(), built, l.home, queenName, colonyID); startErr != nil {
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: startErr.Error(), Status: http.StatusServiceUnavailable}
	}

	// 2. Resume the colony live on the runtime already armed above.
	session, createErr := l.createSession(hiveserver.CreateSessionParams{
		ColonyID:      &colonyID,
		QueenName:     stringPtrIfNotEmpty(queenName),
		Model:         params.Model,
		InitialPrompt: params.InitialPrompt,
		// Upstream forks with `initial_phase or "colony"`; the create cascade
		// derives colony from the binding when the caller passes none.
		InitialPhase: params.InitialPhase,
	}, built, colonyPath)
	if createErr != nil {
		built.Stop()
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "failed to start colony session: " + createErr.Error(), Status: http.StatusInternalServerError}
	}

	// 3. Lock the source session. Without this marker a second fork would be
	//    accepted, so a persist failure tears the fork back down.
	if lockErr := persistSessionColonySpawn(source, colonyID); lockErr != nil {
		l.manager.StopSession(session.ID)
		built.Stop()
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "failed to persist colony spawn lock: " + lockErr.Error(), Status: http.StatusInternalServerError}
	}
	return session, nil
}

func stopColonyRuntime(runtime hiveserver.ColonyRuntime) {
	hiveserver.StopColonyRuntime(runtime)
}

// persistAgentMeta mirrors load_colony's meta.json stamp (agent_name /
// agent_path) so a cold restore discovers the session's colony. It is a
// best-effort write, as upstream: the live binding above is the source of
// truth for this process.
func (l *colonyLifecycle) persistAgentMeta(session *hiveserver.Session, colonyID, colonyPath string) {
	if session.QueenDir == "" {
		return
	}
	metaPath := filepath.Join(session.QueenDir, "meta.json")
	meta := map[string]any{}
	if data, err := os.ReadFile(metaPath); err == nil {
		decoded := map[string]any{}
		if json.Unmarshal(data, &decoded) == nil {
			meta = decoded
		}
	}
	meta["agent_name"] = humanizeColonyName(colonyID)
	meta["agent_path"] = colonyPath
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		fmt.Fprintf(l.out, "Warning: colony session meta unavailable for %s: %v\n", session.ID, err)
		return
	}
	if err := atomicfs.WriteJSON(metaPath, meta, 0o644); err != nil {
		fmt.Fprintf(l.out, "Warning: colony session meta unavailable for %s: %v\n", session.ID, err)
	}
}

// persistSessionColonySpawn marks the source session as having spawned a
// colony and, when the session root is known, persists the lock to meta.json
// so a restart keeps refusing a second fork.
//
// Parity: framework/server/routes_execution.py::persist_colony_spawn_lock
func persistSessionColonySpawn(session *hiveserver.Session, colonyID string) error {
	if session.QueenDir == "" {
		session.ColonySpawned = true
		session.SpawnedColonyID = colonyID
		return nil
	}
	metaPath := filepath.Join(session.QueenDir, "meta.json")
	meta := map[string]any{}
	if data, err := os.ReadFile(metaPath); err == nil {
		decoded := map[string]any{}
		if json.Unmarshal(data, &decoded) == nil {
			meta = decoded
		}
	}
	meta["colony_spawned"] = true
	meta["spawned_colony_id"] = colonyID
	meta["spawned_colony_at"] = time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return err
	}
	if err := atomicfs.WriteJSON(metaPath, meta, 0o644); err != nil {
		return err
	}
	session.ColonySpawned = true
	session.SpawnedColonyID = colonyID
	return nil
}

// colonyQueenName reads the queen bound to a colony from its metadata, falling
// back to the shared default queen when the colony has no binding.
func colonyQueenName(home hivehome.Home, colonyID string) string {
	data, err := os.ReadFile(filepath.Join(home.ColoniesDir(), colonyID, "metadata.json"))
	if err != nil {
		return ""
	}
	meta := map[string]any{}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	name, _ := meta["queen_name"].(string)
	return strings.TrimSpace(name)
}

// humanizeColonyName turns a colony slug into the display name the fork writes
// into worker.json (framework/utils/text.py::humanize_slug for the ASCII slugs
// colony names are validated to).
func humanizeColonyName(slug string) string {
	words := strings.Split(strings.ReplaceAll(slug, "_", " "), " ")
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// chatSessionQueenScope is the queen a chat session belongs to, on disk and in
// memory: the requested queen, or the shared "default" queen when the session
// has none. ChatRuntime resolves its memory scope from the same identifier, so
// a session's transcript never lives under a different queen than the memories
// recalled beside it.
//
// Parity: framework/server/session_manager.py::_queen_session_dir
func chatSessionQueenScope(queenName string) string {
	queen := strings.TrimSpace(queenName)
	if queen == "" {
		return "default"
	}
	return queen
}

// chatSessionDir resolves a session's on-disk tree as
// queens/<queen>/sessions/<session_id>. A session that only exists under the
// legacy queens/default/sessions/<session_id> tree (written before sessions
// were queen-scoped) keeps that location, so resuming an old session reads its
// transcript instead of starting a blank copy under the queen that now owns it.
//
// Parity: framework/server/session_manager.py::_find_queen_session_dir
func chatSessionDir(home hivehome.Home, queenName, sessionID string) string {
	target := home.QueenSessionDir(queenName, sessionID)
	if isSessionDir(target) {
		return target
	}
	legacy := home.QueenSessionDir("default", sessionID)
	if queenName != "default" && isSessionDir(legacy) {
		return legacy
	}
	return target
}

func isSessionDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// chatProviderFactory builds the completion transport for a live session. It
// is a package variable so a test can substitute a scripted completer without
// standing up an HTTP endpoint; production always resolves the configured
// provider.
var chatProviderFactory = func(home hivehome.Home, model string) (llm.Completer, error) {
	return resolveChatProvider(home, model)
}

// resolveChatProvider builds the HTTP transport for a new session from the
// main llm slot. Model, key, base, key pool and extra request fields are the
// server package's MainSlotProvider, so a session started here authenticates
// exactly like the one the dashboard builds (literal key, endpoint key, pool,
// subscriptions, credential store, env var — in Python's order).
func resolveChatProvider(home hivehome.Home, requestedModel string) (*llm.Provider, error) {
	return hiveserver.MainSlotProvider(home, requestedModel)
}

func signalName(sig os.Signal) string {
	switch sig {
	case os.Interrupt:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return sig.String()
}

// Parity: framework/loader/cli.py::_runtime_owner_pid
func runtimeOwnerPID() int {
	owner := os.Getenv("HIVE_DESKTOP_PARENT_PID")
	if !allDigits(owner) {
		owner = strconv.Itoa(os.Getpid())
		_ = os.Setenv("HIVE_DESKTOP_PARENT_PID", owner)
	}
	pid, err := strconv.Atoi(owner)
	if err != nil {
		return os.Getpid()
	}
	return pid
}

func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, letter := range text {
		if letter < '0' || letter > '9' {
			return false
		}
	}
	return true
}

// Parity: framework/loader/cli.py::_ping_hive_gateway_availability
func defaultPing(source string) {
	target := "https://api.adenhq.com/v1/gateway/availability?" + url.Values{"from": {source}}.Encode()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

// Parity: framework/loader/cli.py::_open_browser
func (r *Runner) openBrowser(target string) {
	if chrome := r.findChromeBin(); chrome != "" {
		if startDetached([]string{chrome, target}) == nil {
			return
		}
	}
	var command []string
	switch r.goos() {
	case "darwin":
		command = []string{"open", target}
	case "windows":
		command = []string{"cmd", "/c", "start", "", target}
	default:
		command = []string{"xdg-open", target}
	}
	_ = startDetached(command)
}

// Parity: framework/loader/cli.py::_find_chrome_bin
func (r *Runner) findChromeBin() string {
	candidates := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"microsoft-edge-stable",
	}
	for _, candidate := range candidates {
		if _, err := r.LookPath(candidate); err == nil {
			return candidate
		}
	}
	if r.goos() == "darwin" {
		home, _ := os.UserHomeDir()
		paths := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}

func startDetached(command []string) error {
	if len(command) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Parity: framework/loader/cli.py::cmd_queen_list
func (r *Runner) cmdQueenList(args *parsedArgs) int {
	home, err := r.home()
	if err != nil {
		return 1
	}
	queens := queen.ListQueens(home.QueensDir())
	if args.boolean("json") {
		writeJSONOut(r.Stdout, queens, true)
		return 0
	}
	if len(queens) == 0 {
		fmt.Fprintln(r.Stdout, "No queen profiles found.")
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s  %s  TITLE\n", padRight("ID", 32), padRight("NAME", 24))
	fmt.Fprintln(r.Stdout, strings.Repeat("-", 80))
	for _, entry := range queens {
		fmt.Fprintf(r.Stdout, "%s  %s  %s\n",
			padRight(mapStringDefault(entry, "id", ""), 32),
			padRight(mapStringDefault(entry, "name", ""), 24),
			mapStringDefault(entry, "title", ""))
	}
	return 0
}

// Parity: framework/loader/cli.py::cmd_queen_show
func (r *Runner) cmdQueenShow(args *parsedArgs) int {
	queenID := args.str("queen_id")
	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stdout, "Error: %v\n", err)
		return 1
	}
	profile, err := queen.LoadQueenProfile(home.QueensDir(), queenID)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Error: %v\n", err)
		return 1
	}
	if args.boolean("json") {
		writeJSONOut(r.Stdout, profile, true)
		return 0
	}
	fmt.Fprintf(r.Stdout, "Queen ID:  %s\n", queenID)
	fmt.Fprintf(r.Stdout, "Name:      %s\n", mapStringDefault(profile, "name", ""))
	fmt.Fprintf(r.Stdout, "Title:     %s\n", mapStringDefault(profile, "title", ""))

	description := mapValueDefault(profile, "description", nil)
	if !pyTruthy(description) {
		description = mapValueDefault(profile, "core_traits", nil)
	}
	if !pyTruthy(description) {
		description = ""
	}
	text := pyStr(description)
	if values, ok := description.([]any); ok {
		text = joinValues(values, ", ")
	}
	if text != "" {
		fmt.Fprintf(r.Stdout, "Traits:    %s\n", text)
	}

	skills := mapValueDefault(profile, "skills", nil)
	if !pyTruthy(skills) {
		skills = []any{}
	}
	if pyTruthy(skills) {
		rendered := pyStr(skills)
		if values, ok := skills.([]any); ok {
			rendered = joinValues(values, ", ")
		}
		fmt.Fprintf(r.Stdout, "Skills:    %s\n", rendered)
	}
	return 0
}

// Parity: framework/loader/cli.py::cmd_queen_sessions
func (r *Runner) cmdQueenSessions(args *parsedArgs) int {
	queenID := args.str("queen_id")
	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stdout, "No sessions for queen '%s'\n", queenID)
		return 0
	}
	sessionsDir := filepath.Join(home.QueenDir(queenID), "sessions")
	info, err := os.Stat(sessionsDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(r.Stdout, "No sessions for queen '%s'\n", queenID)
		return 0
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		fmt.Fprintf(r.Stdout, "No sessions for queen '%s'\n", queenID)
		return 0
	}

	rows := make([]map[string]any, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, _ := readJSONObject(filepath.Join(sessionsDir, entry.Name(), "meta.json"))
		if meta == nil {
			meta = map[string]any{}
		}
		rows = append(rows, map[string]any{
			"session_id":  entry.Name(),
			"phase":       mapValueDefault(meta, "phase", "?"),
			"agent_path":  mapStringDefault(meta, "agent_path", ""),
			"colony_fork": pyTruthy(mapValueDefault(meta, "colony_fork", nil)),
		})
	}

	if args.boolean("json") {
		writeJSONOut(r.Stdout, rows, true)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintf(r.Stdout, "No sessions for queen '%s'\n", queenID)
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s  %s  %s  FLAGS\n",
		padRight("SESSION", 40), padRight("PHASE", 10), padRight("COLONY", 20))
	fmt.Fprintln(r.Stdout, strings.Repeat("-", 90))
	for _, row := range rows {
		flags := ""
		if fork, _ := row["colony_fork"].(bool); fork {
			flags = "fork"
		}
		agentPath, _ := row["agent_path"].(string)
		colony := ""
		if agentPath != "" {
			colony = filepath.Base(agentPath)
		}
		fmt.Fprintf(r.Stdout, "%s  %s  %s  %s\n",
			padRight(pyStr(row["session_id"]), 40),
			padRight(pyStr(row["phase"]), 10),
			padRight(colony, 20),
			flags)
	}
	return 0
}

// Parity: framework/loader/cli.py::cmd_colony_list
func (r *Runner) cmdColonyList(args *parsedArgs) int {
	jsonMode := args.boolean("json")
	home, err := r.home()
	if err != nil {
		return r.emptyColonies(jsonMode)
	}
	coloniesDir := home.ColoniesDir()
	info, err := os.Stat(coloniesDir)
	if err != nil || !info.IsDir() {
		return r.emptyColonies(jsonMode)
	}
	entries, err := os.ReadDir(coloniesDir)
	if err != nil {
		return r.emptyColonies(jsonMode)
	}

	rows := make([]map[string]any, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		colonyPath := filepath.Join(coloniesDir, entry.Name())
		meta, _ := readJSONObject(filepath.Join(colonyPath, "metadata.json"))
		if meta == nil {
			meta = map[string]any{}
		}
		rows = append(rows, map[string]any{
			"name":             entry.Name(),
			"queen_name":       mapStringDefault(meta, "queen_name", ""),
			"queen_session_id": mapStringDefault(meta, "queen_session_id", ""),
			"workers":          countColonyWorkers(colonyPath),
			"created_at":       mapValueDefault(meta, "created_at", ""),
			"path":             colonyPath,
		})
	}

	if jsonMode {
		writeJSONOut(r.Stdout, rows, true)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(r.Stdout, "No colonies found.")
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s  %s  %s  CREATED\n",
		padRight("NAME", 24), padRight("QUEEN", 28), padRight("WORKERS", 8))
	fmt.Fprintln(r.Stdout, strings.Repeat("-", 90))
	for _, row := range rows {
		created := pyStr(row["created_at"])
		fmt.Fprintf(r.Stdout, "%s  %s  %s  %s\n",
			padRight(pyStr(row["name"]), 24),
			padRight(pyStr(row["queen_name"]), 28),
			padRight(pyStr(row["workers"]), 8),
			truncateRunes(created, 19))
	}
	return 0
}

func (r *Runner) emptyColonies(jsonMode bool) int {
	if jsonMode {
		fmt.Fprintln(r.Stdout, "[]")
		return 0
	}
	fmt.Fprintln(r.Stdout, "No colonies found.")
	return 0
}

// Parity: framework/loader/cli.py::cmd_colony_info
func (r *Runner) cmdColonyInfo(args *parsedArgs) int {
	name := args.str("name")
	colonyPath, ok := r.resolveColonyPath(name)
	if !ok {
		fmt.Fprintf(r.Stdout, "Colony not found: %s\n", name)
		return 1
	}
	metadata, _ := readJSONObject(filepath.Join(colonyPath, "metadata.json"))
	if metadata == nil {
		metadata = map[string]any{}
	}

	workers := map[string]map[string]any{}
	stems := make([]string, 0)
	entries, err := os.ReadDir(colonyPath)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			filename := entry.Name()
			ext := filepath.Ext(filename)
			stem := strings.TrimSuffix(filename, ext)
			if ext != ".json" || reservedJSONStems[stem] {
				continue
			}
			data, ok := readJSONObject(filepath.Join(colonyPath, filename))
			if !ok {
				continue
			}
			worker, ok := colonyWorker(data, stem)
			if !ok {
				continue
			}
			workers[stem] = worker
			stems = append(stems, stem)
		}
	}

	if args.boolean("json") {
		writeJSONOut(r.Stdout, map[string]any{
			"path":     colonyPath,
			"metadata": metadata,
			"workers":  workers,
		}, true)
		return 0
	}

	fmt.Fprintf(r.Stdout, "Colony:           %s\n", filepath.Base(colonyPath))
	fmt.Fprintf(r.Stdout, "Path:             %s\n", colonyPath)
	fmt.Fprintf(r.Stdout, "Queen:            %s\n", pyStr(mapValueDefault(metadata, "queen_name", "?")))
	fmt.Fprintf(r.Stdout, "Queen Session:    %s\n", pyStr(mapValueDefault(metadata, "queen_session_id", "?")))
	fmt.Fprintf(r.Stdout, "Source Session:   %s\n", pyStr(mapValueDefault(metadata, "source_session_id", "?")))
	fmt.Fprintf(r.Stdout, "Created:          %s\n", pyStr(mapValueDefault(metadata, "created_at", "?")))
	fmt.Fprintln(r.Stdout)
	fmt.Fprintf(r.Stdout, "Workers (%d):\n", len(workers))
	for _, stem := range stems {
		worker := workers[stem]
		fmt.Fprintf(r.Stdout, "  • %s\n", stem)
		if goal := pyStr(worker["goal"]); goal != "" {
			fmt.Fprintf(r.Stdout, "      goal:  %s\n", truncateRunes(goal, 80))
		}
		fmt.Fprintf(r.Stdout, "      tools: %s\n", pyStr(worker["tools"]))
		if spawnedFrom := pyStr(worker["spawned_from"]); spawnedFrom != "" {
			fmt.Fprintf(r.Stdout, "      from:  %s\n", spawnedFrom)
		}
	}
	return 0
}

// Parity: framework/loader/cli.py::cmd_colony_info
func colonyWorker(data map[string]any, stem string) (map[string]any, bool) {
	toolsValue, present := data["tools"]
	if !present {
		toolsValue = []any{}
	}
	tools, ok := pyLen(toolsValue)
	if !ok {
		return nil, false
	}
	var goal any = ""
	if rawGoal, present := data["goal"]; present {
		goalMap, ok := rawGoal.(map[string]any)
		if !ok {
			return nil, false
		}
		if description, present := goalMap["description"]; present {
			goal = description
		}
	}
	name, present := data["name"]
	if !present {
		name = stem
	}
	description, present := data["description"]
	if !present {
		description = ""
	}
	spawnedFrom, present := data["spawned_from"]
	if !present {
		spawnedFrom = ""
	}
	return map[string]any{
		"name":         name,
		"description":  description,
		"tools":        tools,
		"goal":         goal,
		"spawned_from": spawnedFrom,
	}, true
}

func pyLen(value any) (int, bool) {
	switch typed := value.(type) {
	case string:
		return len([]rune(typed)), true
	case []any:
		return len(typed), true
	case map[string]any:
		return len(typed), true
	}
	return 0, false
}

// Parity: framework/loader/cli.py::cmd_colony_delete
func (r *Runner) cmdColonyDelete(args *parsedArgs) int {
	name := args.str("name")
	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stdout, "Colony not found: %s\n", name)
		return 1
	}
	colonyPath := filepath.Join(home.ColoniesDir(), name)
	info, err := os.Stat(colonyPath)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(r.Stdout, "Colony not found: %s\n", name)
		return 1
	}
	storagePath := filepath.Join(home.Root, "agents", name)
	purgeStorage := args.boolean("purge_storage")
	if storageInfo, err := os.Stat(storagePath); err != nil || !storageInfo.IsDir() {
		purgeStorage = false
	}
	if !args.boolean("yes") {
		fmt.Fprintf(r.Stdout, "This will permanently delete: %s\n", colonyPath)
		if purgeStorage {
			fmt.Fprintf(r.Stdout, "And worker storage at:        %s\n", storagePath)
		}
		line, _ := r.readPromptLine("Type the colony name to confirm: ")
		if strings.TrimSpace(line) != name {
			fmt.Fprintln(r.Stdout, "Cancelled.")
			return 1
		}
	}
	if err := os.RemoveAll(colonyPath); err != nil {
		fmt.Fprintf(r.Stdout, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(r.Stdout, "Deleted %s\n", colonyPath)
	if purgeStorage {
		if err := os.RemoveAll(storagePath); err != nil {
			fmt.Fprintf(r.Stdout, "Error: %v\n", err)
			return 1
		}
		fmt.Fprintf(r.Stdout, "Deleted %s\n", storagePath)
	}
	return 0
}

func countColonyWorkers(colonyPath string) int {
	entries, err := os.ReadDir(colonyPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		ext := filepath.Ext(filename)
		if ext == ".json" && !reservedJSONStems[strings.TrimSuffix(filename, ext)] {
			count++
		}
	}
	return count
}

// Parity: framework/loader/cli.py::cmd_session_list
func (r *Runner) cmdSessionList(args *parsedArgs) int {
	var rows []map[string]any
	if args.boolean("cold") {
		rows = r.ColdSessions()
	} else {
		server := args.str("server")
		data, err := r.HTTPGet(server+"/api/sessions", 10*time.Second)
		if err != nil {
			fmt.Fprintf(r.Stdout, "Could not reach server at %s: %v\n", server, err)
			fmt.Fprintln(r.Stdout, "Tip: pass --cold to read on-disk sessions, or start 'hive serve' first.")
			return 1
		}
		rows = mapList(mapValueDefault(data, "sessions", nil))
	}
	if args.boolean("json") {
		if rows == nil {
			rows = []map[string]any{}
		}
		writeJSONOut(r.Stdout, rows, true)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(r.Stdout, "No sessions.")
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s  %s  %s  WORKER\n",
		padRight("SESSION", 40), padRight("COLONY", 20), padRight("PHASE", 12))
	fmt.Fprintln(r.Stdout, strings.Repeat("-", 90))
	for _, row := range rows {
		sessionID := pyStr(mapValueDefault(row, "session_id", "?"))
		colony := mapValueDefault(row, "colony_id", nil)
		if !pyTruthy(colony) {
			colony = mapValueDefault(row, "colony_id", nil)
		}
		if !pyTruthy(colony) {
			colony = ""
		}
		phase := pyStr(mapValueDefault(row, "queen_phase", "?"))
		hasWorker := "no"
		if pyTruthy(mapValueDefault(row, "has_worker", nil)) {
			hasWorker = "yes"
		}
		fmt.Fprintf(r.Stdout, "%s  %s  %s  %s\n",
			padRight(sessionID, 40),
			padRight(pyStr(colony), 20),
			padRight(phase, 12),
			hasWorker)
	}
	return 0
}

// Parity: framework/loader/cli.py::cmd_session_stop
func (r *Runner) cmdSessionStop(args *parsedArgs) int {
	server := args.str("server")
	sessionID := args.str("session_id")
	data, err := r.HTTPDelete(server+"/api/sessions/"+sessionID, 10*time.Second)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Could not reach server at %s: %v\n", server, err)
		return 1
	}
	if pyTruthy(mapValueDefault(data, "stopped", nil)) {
		fmt.Fprintf(r.Stdout, "Stopped session %s\n", sessionID)
		return 0
	}
	fmt.Fprintf(r.Stdout, "Failed to stop session: %s\n", pyRepr(data))
	return 1
}

// Parity: framework/loader/cli.py::cmd_chat
func (r *Runner) cmdChat(args *parsedArgs) int {
	server := args.str("server")
	sessionID := args.str("session_id")
	data, err := r.HTTPPost(server+"/api/sessions/"+sessionID+"/chat",
		map[string]any{"message": args.str("message")}, 30*time.Second)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Could not reach server at %s: %v\n", server, err)
		return 1
	}
	if message, present := data["error"]; present {
		fmt.Fprintf(r.Stdout, "Error: %s\n", pyStr(message))
		return 1
	}
	fmt.Fprintf(r.Stdout, "Sent. Tail the SSE stream at %s/api/sessions/%s/events\n", server, sessionID)
	return 0
}

func (r *Runner) home() (hivehome.Home, error) {
	if r.HomeRoot != nil {
		root, err := r.HomeRoot()
		if err != nil {
			return hivehome.Home{}, err
		}
		return hivehome.ResolveAt(root)
	}
	return hivehome.Resolve()
}

// Parity: framework/loader/cli.py::_resolve_colony_path
func (r *Runner) resolveColonyPath(nameOrPath string) (string, bool) {
	candidate := expandUser(nameOrPath)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate, true
	}
	home, err := r.home()
	if err == nil {
		byName := filepath.Join(home.ColoniesDir(), nameOrPath)
		if info, err := os.Stat(byName); err == nil && info.IsDir() {
			return byName, true
		}
	}
	return "", false
}

func readJSONObject(path string) (map[string]any, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, false
	}
	return object, true
}

func mapList(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	}
	return nil
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// Parity: framework/loader/cli.py::_build_frontend
func (r *Runner) buildFrontend() bool {
	frontendDir := ""
	for _, candidate := range []string{
		filepath.Join(r.workDir(), "core", "frontend"),
		filepath.Join(r.workDir(), "frontend"),
	} {
		if info, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil && !info.IsDir() {
			frontendDir = resolvePath(candidate)
			break
		}
	}
	if frontendDir == "" {
		return false
	}

	distDir := filepath.Join(frontendDir, "dist")
	srcDir := filepath.Join(frontendDir, "src")
	if distInfo, err := os.Stat(filepath.Join(distDir, "index.html")); err == nil && !distInfo.IsDir() {
		if srcInfo, err := os.Stat(srcDir); err == nil && srcInfo.IsDir() {
			if !sourceNewerThan(srcDir, distInfo.ModTime()) {
				return true
			}
		}
	}

	fmt.Fprintln(r.Stdout, "Building frontend...")
	npmCommand := "npm"
	if r.goos() == "windows" {
		npmCommand = "npm.cmd"
	}
	if matches, err := filepath.Glob(filepath.Join(frontendDir, "tsconfig*.tsbuildinfo")); err == nil {
		for _, match := range matches {
			_ = os.Remove(match)
		}
	}

	if _, err := r.RunCommand(frontendDir, []string{npmCommand, "install", "--no-fund", "--no-audit"}); err != nil {
		return r.reportFrontendFailure(err, distDir)
	}
	if _, err := r.RunCommand(frontendDir, []string{npmCommand, "run", "build"}); err != nil {
		return r.reportFrontendFailure(err, distDir)
	}
	fmt.Fprintln(r.Stdout, "Frontend built.")
	return true
}

func sourceNewerThan(dir string, marker time.Time) bool {
	newer := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || newer {
			return nil
		}
		if !info.IsDir() && info.ModTime().After(marker) {
			newer = true
		}
		return nil
	})
	return newer
}

func (r *Runner) reportFrontendFailure(err error, distDir string) bool {
	var missing *commandNotFound
	if errors.As(err, &missing) {
		fmt.Fprintln(r.Stdout, "Node.js not found — skipping frontend build.")
		return isDir(distDir)
	}
	var exitErr *commandExitError
	if errors.As(err, &exitErr) {
		parts := make([]string, 0, 2)
		if stdout := formatSubprocessOutput(exitErr.Stdout, 2000); stdout != "" {
			parts = append(parts, stdout)
		}
		if stderr := formatSubprocessOutput(exitErr.Stderr, 2000); stderr != "" {
			parts = append(parts, stderr)
		}
		details := strings.TrimSpace(strings.Join(parts, "\n"))
		command := strings.Join(exitErr.Command, " ")
		if details != "" {
			fmt.Fprintf(r.Stdout, "Frontend build failed while running %s:\n%s\n", command, details)
		} else {
			fmt.Fprintf(r.Stdout, "Frontend build failed while running %s (exit %d).\n",
				command, exitErr.ReturnCode)
		}
		return isDir(distDir)
	}
	fmt.Fprintf(r.Stdout, "Frontend build failed: %v\n", err)
	return isDir(distDir)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
