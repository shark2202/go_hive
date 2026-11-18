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

	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/server/uiembed"
	"github.com/aden-hive/hive-go/internal/tasks"
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
	Executor     func() ExecutorState
	Home         hivehome.Home
	Reveal       func(string) error
	Colony       ColonyLifecycle
	Config       ConfigRouteDeps
	FrontendDist string
	DesktopToken *string
	Logger       *log.Logger

	CreateSession          func(CreateSessionParams) (*Session, error)
	CreateColonyFromSource func(CreateColonyFromSourceParams) (*Session, error)
	// ColonyRuntimeFor resolves the live colony runtime behind a session for
	// the worker/sentinel read surfaces. Nil falls back to inspecting
	// Session.Colony itself.
	ColonyRuntimeFor   func(*Session) *colony.ColonyRuntime
	VacateSoftDeleted  func(colonyID string) error
	ConnectedProviders func() []string
	AgentRoots         *agents.AllowedRoots
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

	processStart float64
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
	mux.HandleFunc("GET /api/health", app.handleHealth)
	mux.HandleFunc("GET /api/startup-status", app.handleStartupStatus)
	RegisterConfigRoutes(mux, deps.Config)
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
	})
	RegisterAgentRoutes(mux, AgentRouteDeps{
		Manager: deps.Manager,
		Home:    deps.Home,
		Roots:   deps.AgentRoots,
	})
	RegisterChatRoutes(mux, ChatRouteDeps{Manager: deps.Manager})

	// Wave 1 route tables that only need the home directory, the session
	// manager and the process clock. Each seam that is not implemented yet
	// answers with an explicit failure inside its handler (no empty 200s).
	RegisterCredentialRoutes(mux, CredentialRouteDeps{
		Home:  deps.Home,
		Specs: credentialSpecsFromCatalog,
	})
	RegisterSoulRoutes(mux, SoulRouteDeps{Home: deps.Home})
	RegisterChannelRoutes(mux, ChannelRouteDeps{Home: deps.Home})
	RegisterPromptRoutes(mux, PromptRouteDeps{Home: deps.Home})
	RegisterMemoryRoutes(mux, MemoryRouteDeps{Home: deps.Home})
	RegisterMessageRoutes(mux, MessageRouteDeps{Home: deps.Home})
	RegisterMCPRoutes(mux, MCPRouteDeps{Home: deps.Home})
	RegisterMaintenanceRoutes(mux, MaintenanceRouteDeps{
		Home:           deps.Home,
		Manager:        deps.Manager,
		ProcessStartTS: app.processStartTS,
	})
	RegisterEventRoutes(mux, EventRouteDeps{})
	RegisterAppExtraRoutes(mux, AppExtraRouteDeps{})
	RegisterModelRoutes(mux, ModelRouteDeps{Home: deps.Home, Manager: deps.Manager})
	tasks.RegisterTaskRoutes(mux)

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
	RegisterSentinelRoutes(mux, SentinelRouteDeps{
		Home:     deps.Home,
		Store:    newSentinelStoreAdapter(deps.Home.Root),
		Notifier: newSentinelNotifierAdapter(),
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
		Home:          deps.Home,
		Manager:       deps.Manager,
		CreateSession: queenCreateSessionAdapter(deps.CreateSession),
	})
	RegisterQueenToolRoutes(mux, QueenToolRouteDeps{
		Home:    deps.Home,
		Manager: deps.Manager,
	})
	// The queen-mail runtime (delivery, depth accounting) has not been ported,
	// so every request answers the route's own 401 unauthorized rather than a
	// fabricated delivery.
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{})
	// Claude Code queens are off in this build: the list reports `queens: []`
	// and transcripts 404, exactly as upstream does when the feature is off.
	RegisterClaudeCodeQueenRoutes(mux, ClaudeCodeQueenRouteDeps{})

	if app.HasFrontend() {
		mux.HandleFunc("GET /", app.handleSPA)
	}

	app.handler = app.accessLog(app.desktopAuth(app.noCacheAPI(app.errorRecovery(mux))))
	return app
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
// other path (including client-side routes) returns index.html.
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
