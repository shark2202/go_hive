package server

// Parity: framework/server/app.py::create_app

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/hivehome"
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
	VacateSoftDeleted      func(colonyID string) error
	ConnectedProviders     func() []string
	AgentRoots             *agents.AllowedRoots
}

type App struct {
	handler http.Handler
	deps    AppDeps
	distDir string
	token   string
	logger  *log.Logger

	executorMu  sync.Mutex
	executorMax int
}

func NewApp(deps AppDeps) *App {
	app := &App{deps: deps, logger: deps.Logger}
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
	if app.distDir != "" {
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
			r.Method, r.URL.RequestURI(), status, elapsed.Seconds()*1000, slowMark)
	})
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
