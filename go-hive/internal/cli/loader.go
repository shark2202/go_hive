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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/queen"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	hivesession "github.com/aden-hive/hive-go/internal/session"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

var goos = runtime.GOOS

// serveReminders is the process-wide reminder runtime for `hive serve`: one
// instance feeds both the HTTP routes and each chat session's turn hook, so a
// reminder generated during a turn is visible to the next polling request.
var serveReminders *hiveserver.ReminderRuntime

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
	serveReminders = hiveserver.NewReminderRuntime()

	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: cannot resolve HIVE_HOME: %v\n", err)
	}
	// The dashboard stores keys in the encrypted credential store while the
	// ported tools read os.Getenv. Export the store before the app (and any
	// tool registry) is built so saved credentials are live from the start.
	hiveserver.SyncCredentialEnv(home)

	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)

	for _, colonyArg := range args.list("colony") {
		colonyPath, ok := r.resolveColonyPath(colonyArg)
		if !ok {
			fmt.Fprintf(r.Stdout, "Colony not found: %s\n", colonyArg)
			continue
		}
		colonyID := filepath.Base(colonyPath)
		sessionID, err := hivesession.NewStore("").GenerateID(time.Now())
		if err != nil {
			fmt.Fprintf(r.Stdout, "Error loading colony %s: %v\n", colonyArg, err)
			continue
		}
		record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
		record.ColonyID = colonyID
		record.WorkerPath = colonyPath
		record.LoadedAt = float64(time.Now().UnixNano()) / 1e9
		// The live runtime behind the session: colony spawn/run/SSE entry.
		// Nil when no provider is configured, in which case the worker and
		// colony routes keep their on-disk fallbacks.
		record.Colony = r.buildColonyHost(colonyPath, colonyID, home, args.str("model"))
		manager.AddSession(record)
		liveMu.Lock()
		live = append(live, record)
		liveMu.Unlock()
		fmt.Fprintf(r.Stdout, "Loaded colony: %s → session %s\n", colonyID, sessionID)
	}

	app := hiveserver.NewApp(hiveserver.AppDeps{
		Reminders: serveReminders,
		Sessions: func() []hiveserver.FeatureSession {
			liveMu.Lock()
			defer liveMu.Unlock()
			out := make([]hiveserver.FeatureSession, 0, len(live))
			for _, record := range live {
				out = append(out, hiveserver.FeatureSession{ColonyID: record.ColonyID})
			}
			return out
		},
		LiveSessions: manager.ListSessions,
		Manager:      manager,
		Home:         home,
		CreateSession: func(params hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
			return r.createChatSession(params, manager, &liveMu, &live, home, args.str("model"))
		},
	})

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

	dashboardURL := fmt.Sprintf("http://%s:%d", host, port)
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

// buildColonyHost constructs the live colony runtime a `serve --colony`
// session holds (colony spawn/run/SSE entry). It returns nil when no provider
// is configured: a host without an LLM could never run a worker, so the
// session keeps its cold record and the runtime routes fall back to disk.
func (r *Runner) buildColonyHost(colonyPath, colonyID string, home hivehome.Home, model string) hiveserver.ColonyRuntime {
	provider, err := resolveChatProvider(home, model)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: colony runtime unavailable for %s: %v\n", colonyID, err)
		return nil
	}
	// The worker the session runs is the one the colony defines on disk. A
	// colony with no worker config cannot be a real worker, so it keeps its
	// cold record instead of running a synthetic prompt.
	spec, err := agents.LoadAgentSpec(colonyPath)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: colony runtime unavailable for %s: %v\n", colonyID, err)
		return nil
	}
	fields := spec.HostFields()
	toolbox := builtin.New(colonyPath)
	built, err := colonyhost.New(colonyhost.Config{
		ColonyID:    colonyID,
		AgentSpec:   fields.AgentSpec,
		Goal:        fields.Goal,
		StoragePath: colonyPath,
		LLM:         provider,
		Tools:       toolbox.Defs(),
		ToolExecutor: func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			result, execErr := toolbox.Execute(context.Background(), request.Name, request.Input)
			if execErr != nil {
				return agentloop.ToolResult{}, execErr
			}
			return agentloop.ToolResult{Content: result.Content, IsError: result.IsError}, nil
		},
	})
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: colony runtime unavailable for %s: %v\n", colonyID, err)
		return nil
	}
	return built
}

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
	sessionID := ""
	if params.SessionID != nil {
		sessionID = strings.TrimSpace(*params.SessionID)
		if !hivesession.ValidSegment(sessionID) {
			return nil, &hiveserver.SessionCreateValueError{Message: "invalid session_id"}
		}
	}
	if sessionID == "" {
		generated, err := hivesession.NewStore("").GenerateID(time.Now())
		if err != nil {
			return nil, err
		}
		sessionID = generated
	}
	hiveserver.ReportBootStage("Preparing queen session", sessionID)

	record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	record.LoadedAt = float64(time.Now().UnixNano()) / 1e9
	if params.ColonyID != nil {
		record.ColonyID = *params.ColonyID
	}
	if params.QueenName != nil {
		record.QueenName = *params.QueenName
	}
	// The transcript tree and the memory scope share one queen identifier: a
	// queen_growth session keeps its conversation under
	// queens/queen_growth/sessions and recalls queen_growth's memories. An
	// unbound session belongs to the shared "default" queen. queen_name comes
	// from request bodies and queen routes, so reject a name that would step
	// outside the queen tree rather than joining it raw.
	queenScope := chatSessionQueenScope(record.QueenName)
	if !hivesession.ValidSegment(queenScope) {
		return nil, &hiveserver.SessionCreateValueError{Message: "invalid queen_name"}
	}

	requestedModel := ""
	if params.Model != nil {
		requestedModel = *params.Model
	}
	if strings.TrimSpace(requestedModel) == "" {
		// Fall back to the serve --model flag before the configured default.
		requestedModel = defaultModel
	}

	var runtime *hiveserver.ChatRuntime
	provider, providerErr := resolveChatProvider(home, requestedModel)
	if providerErr != nil {
		// Credentials are missing for a remote model: keep the session
		// listable but leave Chat nil so POST /chat answers 503 rather than
		// faking delivery against an endpoint that would 401.
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", sessionID, providerErr)
	} else {
		toolbox := builtin.New("")
		sessionDir := chatSessionDir(home, queenScope, sessionID)
		tools := newSessionToolCatalogue(sessionDir, toolbox, home)
		built, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
			LLM:       provider,
			Tools:     tools.Defs(),
			Execute:   tools.Execute,
			Root:      sessionDir,
			Bus:       record.EventBus,
			SessionID: sessionID,
			QueenName: record.QueenName,
			HomeRoot:  home.Root,
			Reminders: serveReminders,
		})
		if err != nil {
			fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", sessionID, err)
		} else {
			runtime = built
			record.Chat = built
			record.LLM = provider
		}
	}

	manager.AddSession(record)
	liveMu.Lock()
	*live = append(*live, record)
	liveMu.Unlock()

	if runtime != nil && params.InitialPrompt != nil && strings.TrimSpace(*params.InitialPrompt) != "" {
		if err := runtime.Deliver(*params.InitialPrompt); err != nil {
			return nil, err
		}
	}
	hiveserver.MarkBootReady("Queen ready")
	return record, nil
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

// chatProviderEnvVars mirrors the provider/env mapping the HTTP routes use for
// /api/llm config, so a session created from the CLI authenticates the same way
// as one created from the dashboard.
var chatProviderEnvVars = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"gemini":     "GEMINI_API_KEY",
	"google":     "GOOGLE_API_KEY",
	"minimax":    "MINIMAX_API_KEY",
	"groq":       "GROQ_API_KEY",
	"cerebras":   "CEREBRAS_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"mistral":    "MISTRAL_API_KEY",
	"together":   "TOGETHER_API_KEY",
	"deepseek":   "DEEPSEEK_API_KEY",
	"kimi":       "KIMI_API_KEY",
	"hive":       "HIVE_API_KEY",
	"zai":        "ZAI_API_KEY",
	"xai":        "XAI_API_KEY",
}

// resolveChatProvider builds the HTTP transport for a new session, resolving
// model, API base and credentials the same way the settings routes persist
// them: explicit request > configured llm section > environment.
func resolveChatProvider(home hivehome.Home, requestedModel string) (*llm.Provider, error) {
	llmSection, _ := hiveserver.HiveConfig(home)["llm"].(map[string]any)

	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = configString(llmSection, "model")
	}
	if model == "" {
		model = llm.DefaultModel
	}
	apiBase := configString(llmSection, "api_base")

	if llm.IsLocalModel(model) {
		// Local servers (ollama, vllm, lm studio, llama.cpp) take no key.
		return llm.NewHTTPProvider(llm.ProviderOptions{Model: model, APIBase: apiBase}), nil
	}

	provider := strings.ToLower(configString(llmSection, "provider"))
	if provider == "" || provider == "custom" {
		provider = providerForModel(model)
	}
	envVar := configString(llmSection, "api_key_env_var")
	if envVar == "" {
		envVar = chatProviderEnvVars[provider]
	}
	if apiBase == "" {
		switch provider {
		case "anthropic":
			apiBase = firstNonEmptySetting("ANTHROPIC_API_BASE", "ANTHROPIC_BASE_URL")
		case "openai":
			apiBase = firstNonEmptySetting("OPENAI_BASE_URL", "OPENAI_API_BASE")
		}
	}

	apiKey := strings.TrimSpace(os.Getenv(envVar))
	if apiKey == "" {
		fallback := chatProviderEnvVars[provider]
		apiKey = strings.TrimSpace(os.Getenv(fallback))
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("HIVE_API_KEY"))
	}
	if apiKey == "" {
		keyVar := envVar
		if keyVar == "" {
			keyVar = chatProviderEnvVars[provider]
		}
		if keyVar == "" {
			keyVar = "OPENAI_API_KEY"
		}
		return nil, fmt.Errorf("no API key configured for model %q (set %s or configure the LLM in hive settings)", model, keyVar)
	}

	return llm.NewHTTPProvider(llm.ProviderOptions{Model: model, APIKey: apiKey, APIBase: apiBase}), nil
}

func providerForModel(model string) string {
	lower := strings.ToLower(model)
	if index := strings.Index(lower, "/"); index > 0 {
		if _, known := chatProviderEnvVars[lower[:index]]; known {
			return lower[:index]
		}
	}
	switch {
	case llm.IsAnthropicModel(lower):
		return "anthropic"
	case strings.HasPrefix(lower, "gemini-"):
		return "gemini"
	}
	return "openai"
}

func configString(section map[string]any, key string) string {
	if section == nil {
		return ""
	}
	value, ok := section[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmptySetting(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
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
