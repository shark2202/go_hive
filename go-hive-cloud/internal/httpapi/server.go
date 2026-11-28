// Package httpapi wires the go-hive-cloud HTTP surface: the client-observable
// subset of hive-backend (global-db, inbox, gateway availability, stream-token
// refresh) plus one explicit private-deploy extension (internal inbox reply).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
	"github.com/shark2202/go_hive/go-hive-cloud/internal/store"
)

// SignedOutMessage is the 401 body text, byte-for-byte identical to the
// client-side message in go_hive/go-hive/internal/globaldb/client.go:45 and
// core/framework/global_db/client.py:45.
const SignedOutMessage = "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."

// StreamTokenInvalid is the error code the hive-llm proxy returns for a bad or
// expired streamToken (core/framework/server/streamtoken_refresh.py header).
const StreamTokenInvalid = "hive_stream_token_invalid"

const (
	maxGlobalDBBody = 8 << 20
	maxInboxBody    = 1 << 20
	maxSmallBody    = 1 << 20
)

// Config configures the server.
type Config struct {
	// DataDir is the SQLite root (data/teams/<team_id>.db).
	DataDir string
	// JWTSecret signs operator JWTs.
	JWTSecret []byte
	// StreamSecrets verify streamTokens; the first entry signs new ones.
	StreamSecrets [][]byte
	// StatementTimeout bounds SQL statements (default 5s).
	StatementTimeout time.Duration
	// RowCapDefault applies when row_cap is omitted (0 = unlimited).
	RowCapDefault int
	// StreamTTL is the streamToken lifetime (default 24h).
	StreamTTL time.Duration
	// KeepAlive is the SSE keepalive interval (default 15s).
	KeepAlive time.Duration
	// InboxRateLimit is the optional per-team inbox write allowance per
	// InboxRateWindow (0 = disabled; the observed cloud contract has no
	// rate-limit semantics).
	InboxRateLimit int
	// InboxRateWindow is the rate-limit window (default 1m).
	InboxRateWindow time.Duration
	// LLMUpstreamURL is the explicit Anthropic-compatible Messages upstream
	// base/full endpoint. Empty means no upstream: /v1/messages answers 400
	// llm_upstream_not_configured and never fakes a completion.
	LLMUpstreamURL string
	// LLMUpstreamKey is the credential sent to the Messages upstream.
	LLMUpstreamKey string
	// LLMUpstreamAuth is "x-api-key" (default) or "bearer".
	LLMUpstreamAuth string
	// LLMAnthropicVersion is sent when the client omits anthropic-version.
	LLMAnthropicVersion string
	// LLMRequestTimeout bounds one upstream Messages call (default 10m).
	LLMRequestTimeout time.Duration
	// LLMUpstreamClient overrides the HTTP client (tests); nil builds one.
	LLMUpstreamClient *http.Client
	// ImagesUpstreamURL is the explicit OpenAI-compatible images endpoint.
	// Empty means /v1/images/generations answers 503.
	ImagesUpstreamURL string
	// ImagesUpstreamKey is the credential sent to the images upstream.
	ImagesUpstreamKey string
	// ImagesRequestTimeout bounds one upstream images call (default 225s).
	ImagesRequestTimeout time.Duration
	// ImagesUpstreamClient overrides the images HTTP client (tests).
	ImagesUpstreamClient *http.Client
	// Version is reported by /health.
	Version string
	// Logger receives access/error logs; nil means stderr.
	Logger *log.Logger
	// Now is injectable for tests.
	Now func() time.Time
}

// Server is the HTTP application.
type Server struct {
	cfg    Config
	store  *store.Store
	signer *auth.Signer
	hub    *replyHub
	// llm is the configured upstream proxy (endpoints may be empty = disabled).
	llm *llmProxy
	// inboxLimiter is the optional private-deploy inbox write limiter.
	inboxLimiter *inboxLimiter
	mux          *http.ServeMux
	routes       []Route
	logger       *log.Logger
	now          func() time.Time
}

// Route is one registered pattern (used by the contract coverage test).
type Route struct {
	Method  string
	Pattern string
	Public  bool
}

// New opens the store and builds the router.
func New(cfg Config) (*Server, error) {
	if len(cfg.JWTSecret) == 0 {
		return nil, errors.New("httpapi: jwt secret is required")
	}
	if len(cfg.StreamSecrets) == 0 {
		cfg.StreamSecrets = [][]byte{cfg.JWTSecret}
	}
	if cfg.StatementTimeout <= 0 {
		cfg.StatementTimeout = 5 * time.Second
	}
	if cfg.StreamTTL <= 0 {
		cfg.StreamTTL = 24 * time.Hour
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 15 * time.Second
	}
	if cfg.InboxRateWindow <= 0 {
		cfg.InboxRateWindow = time.Minute
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0-m5"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	llm, err := newLLMProxy(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(store.Options{
		Dir:              cfg.DataDir,
		StatementTimeout: cfg.StatementTimeout,
		RowCapDefault:    cfg.RowCapDefault,
		Now:              cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:          cfg,
		store:        st,
		signer:       auth.NewSigner(cfg.JWTSecret, cfg.Now),
		hub:          newReplyHub(),
		llm:          llm,
		inboxLimiter: newInboxLimiter(cfg.InboxRateLimit, cfg.InboxRateWindow, cfg.Now),
		mux:          http.NewServeMux(),
		logger:       cfg.Logger,
		now:          cfg.Now,
	}
	s.register()
	return s, nil
}

// Close releases the store.
func (s *Server) Close() error { return s.store.Close() }

// Handler returns the middleware-wrapped mux.
func (s *Server) Handler() http.Handler { return s.withAccessLog(s.mux) }

// Routes returns the registered patterns in registration order.
func (s *Server) Routes() []Route {
	out := make([]Route, len(s.routes))
	copy(out, s.routes)
	return out
}

func (s *Server) handle(method, pattern string, public bool, handler http.HandlerFunc) {
	if !public {
		handler = s.requireAuth(handler)
	}
	s.routes = append(s.routes, Route{Method: method, Pattern: pattern, Public: public})
	s.mux.HandleFunc(method+" "+pattern, handler)
}

func (s *Server) register() {
	// Public (contract: no auth on the availability ping; health is ops-only).
	s.handle(http.MethodGet, "/health", true, s.handleHealth)
	s.handle(http.MethodGet, "/v1/gateway/availability", true, s.handleGatewayAvailability)
	// Stream-token refresh authenticates by the streamToken itself.
	s.handle(http.MethodPost, "/user/refresh-stream-token-from-stream", true, s.handleRefreshStreamTokenFromStream)

	// Global DB (hive-backend contract §3.1).
	s.handle(http.MethodPost, "/v1/global-db/sql", false, s.handleGlobalSQL)
	s.handle(http.MethodPost, "/v1/global-db/query", false, s.handleGlobalQuery)
	s.handle(http.MethodPost, "/v1/global-db/upsert", false, s.handleGlobalUpsert)
	s.handle(http.MethodPost, "/v1/global-db/import", false, s.handleGlobalImport)
	s.handle(http.MethodGet, "/v1/global-db/tables", false, s.handleGlobalTables)
	s.handle(http.MethodGet, "/v1/global-db/changes", false, s.handleGlobalChanges)
	s.handle(http.MethodGet, "/v1/global-db/tables/{table}/rows", false, s.handleGlobalRows)
	s.handle(http.MethodPatch, "/v1/global-db/tables/{table}/rows", false, s.handleGlobalPatchRows)

	// CRM grid (contract §3.3). Only the evidence-backed generic grid subset
	// is registered; schema/claim/release stay planned and unregistered (docs/m4-notes.md).
	s.handle(http.MethodPost, "/v1/crm/grid/{entity}/query", false, s.handleCRMQuery)
	s.handle(http.MethodPost, "/v1/crm/grid/{entity}/group-counts", false, s.handleCRMGroupCounts)
	s.handle(http.MethodGet, "/v1/crm/grid/{entity}/{id}", false, s.handleCRMGet)
	s.handle(http.MethodPost, "/v1/crm/grid/{entity}", false, s.handleCRMCreate)
	s.handle(http.MethodPatch, "/v1/crm/grid/{entity}/{id}", false, s.handleCRMPatch)
	s.handle(http.MethodDelete, "/v1/crm/grid/{entity}/{id}", false, s.handleCRMDelete)
	s.handle(http.MethodPost, "/v1/crm/grid/{entity}/{id}/stage", false, s.handleCRMStage)

	// Inbox (contract §3.2).
	s.handle(http.MethodPost, "/v1/inbox/messages", false, s.handleInboxMessage)
	s.handle(http.MethodPost, "/v1/inbox/resolve", false, s.handleInboxResolve)
	s.handle(http.MethodGet, "/v1/inbox/runtime/stream", false, s.handleInboxRuntimeStream)

	// LLM proxy: both routes authenticate the Bearer credential themselves.
	// /v1/messages forwards Anthropic Messages (JSON or SSE); images forwards
	// the observed JSON contract. With no configured upstream they answer an
	// explicit error — never an empty 200.
	s.handle(http.MethodPost, "/v1/messages", true, s.handleLLMMessages)
	s.handle(http.MethodPost, "/v1/images/generations", true, s.handleLLMImages)

	// Private-deploy extension (NOT part of the observed cloud contract).
	s.handle(http.MethodPost, "/internal/inbox/reply", false, s.handleInternalInboxReply)
}

// ---- auth -----------------------------------------------------------------

type authContextKey struct{}

// RequestAuth is the identity derived from the JWT.
type RequestAuth struct {
	UserID string
	TeamID string
	Claims *auth.Claims
}

func authFromContext(ctx context.Context) (*RequestAuth, bool) {
	value, ok := ctx.Value(authContextKey{}).(*RequestAuth)
	return value, ok
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth.BearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeSignedOut(w)
			return
		}
		claims, err := auth.Verify(s.cfg.JWTSecret, token)
		if err != nil {
			writeSignedOut(w)
			return
		}
		if claims.Typ == auth.TokenTypeStream {
			// A streamToken is not an operator session.
			writeSignedOut(w)
			return
		}
		if claims.UserID == "" || claims.TeamID == "" || !auth.ValidTeamID(claims.TeamID) {
			writeSignedOut(w)
			return
		}
		reqAuth := &RequestAuth{UserID: claims.UserID, TeamID: claims.TeamID, Claims: claims}
		next(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, reqAuth)))
	}
}

// ---- health / gateway -----------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "hive-cloud",
		"version": s.cfg.Version,
		"time":    s.now().UTC().Format(time.RFC3339),
	})
}

// handleGatewayAvailability answers the fire-and-forget startup ping
// (`hive open`). The client never parses the body (loader.go:1133), so the
// shape is ours; the path and query passthrough are the contract.
func (s *Server) handleGatewayAvailability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"gateway":   "hive-cloud",
		"from":      r.URL.Query().Get("from"),
	})
}

// ---- stream token refresh -------------------------------------------------

func (s *Server) handleRefreshStreamTokenFromStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StreamToken string `json:"streamToken"`
	}
	if err := decodeJSON(w, r, maxSmallBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.StreamToken) == "" {
		writeStreamTokenInvalid(w)
		return
	}
	claims, err := auth.VerifyStreamToken(s.cfg.StreamSecrets, body.StreamToken)
	if err != nil {
		writeStreamTokenInvalid(w)
		return
	}
	signer := auth.NewSigner(s.cfg.StreamSecrets[0], s.now)
	newToken, err := signer.MintStreamToken(claims.UserID, claims.TeamID, s.cfg.StreamTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"streamToken": newToken})
}

func writeStreamTokenInvalid(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": StreamTokenInvalid})
}

func writeSignedOut(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": SignedOutMessage})
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// decodeJSON decodes a JSON body with numbers preserved as json.Number.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	// Reject trailing garbage.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func (s *Server) teamFrom(r *http.Request) (*store.Team, *RequestAuth, bool) {
	reqAuth, ok := authFromContext(r.Context())
	if !ok {
		return nil, nil, false
	}
	team, err := s.store.Team(reqAuth.TeamID)
	if err != nil {
		return nil, reqAuth, false
	}
	return team, reqAuth, true
}

// writeStoreError maps store errors to the contract's status semantics:
// 401 = signed out (handled by the middleware), 4xx = client error with
// {"error": str}, 5xx = server error.
func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	var gateErr *store.GateError
	var conflict *store.ConflictError
	var sqlErr *store.SQLError
	switch {
	case errors.As(err, &gateErr):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": gateErr.Message})
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "conflict",
			"message": "row already exists",
			"pk":      conflict.PK,
		})
	case errors.As(err, &sqlErr):
		// The statement reached SQLite and failed: client-correctable.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": sqlErr.Message})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": trimStorePrefix(err)})
	case errors.Is(err, store.ErrBadData):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": trimStorePrefix(err)})
	case errors.Is(err, store.ErrTimeout):
		writeJSON(w, http.StatusRequestTimeout, map[string]any{"error": trimStorePrefix(err)})
	default:
		s.internalError(w, r, err)
	}
}

func trimStorePrefix(err error) string {
	text := err.Error()
	for _, prefix := range []string{"not found: ", "bad data: ", "statement timeout: "} {
		text = strings.TrimPrefix(text, prefix)
	}
	return text
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Printf("[cloud] 500 %s %s: %v", r.Method, r.URL.Path, err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
}

// ---- access log -----------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

// Flush keeps SSE working through the recorder.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Printf("[cloud] %d %s %s %s", status, r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Microsecond))
	})
}
