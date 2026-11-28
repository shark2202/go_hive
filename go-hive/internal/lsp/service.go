package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configure one Service. Zero values pick the frozen defaults.
type Options struct {
	Root     string
	Home     string
	LookPath LookPathFunc
	// ServerEnv is appended to the server process environment. It is a host
	// concern (tests, PATH), never model input.
	ServerEnv []string

	Backoff             time.Duration
	IdleTimeout         time.Duration
	DiagnosticsWait     time.Duration
	WorkspaceFileBudget time.Duration
	// RequestTimeout overrides settings.requestTimeout when non-zero. It
	// exists for bounded tests; production reads the config value.
	RequestTimeout time.Duration
	Now            func() time.Time
	MaxFrameBytes  int

	Loader *Loader
}

// Service owns the per-cwd config, clients, documents and diagnostics.
type Service struct {
	root     string
	rootReal string
	opts     Options
	loader   *Loader

	mu      sync.Mutex
	records map[string]*serverRecord
	closed  bool
}

type serverRecord struct {
	name string
	def  *ServerDef
	// root is the server's effective workspace root (rootDir or cwd).
	root     string
	disc     Discovery
	client   *Client
	docs     *DocumentManager
	diags    *diagnosticStore
	identity string
	lastUsed time.Time

	failedUntil time.Time
	lastErr     string

	starting  bool
	startDone chan struct{}
}

// NewService resolves root and prepares the config loader.
func NewService(opts Options) (*Service, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, NewError(CodeInvalidArgument, "workspace root is empty")
	}
	abs, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, NewError(CodeInternal, "resolve workspace root: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, NewError(CodeInvalidArgument, "workspace root %s: %v", abs, err)
	}
	if !info.IsDir() {
		return nil, NewError(CodeInvalidArgument, "workspace root %s is not a directory", abs)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, NewError(CodeInternal, "resolve workspace root symlinks: %v", err)
	}
	if opts.Home == "" {
		opts.Home = os.Getenv("HIVE_HOME")
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 3 * time.Minute
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.DiagnosticsWait <= 0 {
		opts.DiagnosticsWait = 3 * time.Second
	}
	if opts.WorkspaceFileBudget <= 0 {
		opts.WorkspaceFileBudget = 400 * time.Millisecond
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	loader := opts.Loader
	if loader == nil {
		loader = NewLoader(opts.Home)
	}
	return &Service{
		root:     filepath.Clean(real),
		rootReal: filepath.Clean(real),
		opts:     opts,
		loader:   loader,
		records:  map[string]*serverRecord{},
	}, nil
}

// Root returns the workspace root.
func (s *Service) Root() string { return s.root }

// Config returns the cached merged configuration.
func (s *Service) Config() (*Config, error) { return s.loader.Load() }

// Reload clears the cwd config cache and stops clients whose identity changed.
func (s *Service) Reload() (*Config, error) {
	cfg, err := s.loader.Reload()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	var stopped []*serverRecord
	for name, rec := range s.records {
		def := cfg.Servers[name]
		if def == nil || identityOf(def) != rec.identity {
			stopped = append(stopped, rec)
			delete(s.records, name)
		}
	}
	s.mu.Unlock()
	for _, rec := range stopped {
		s.stopRecord(rec)
	}
	return cfg, nil
}

func (s *Service) stopRecord(rec *serverRecord) {
	if rec == nil {
		return
	}
	if rec.docs != nil {
		for _, doc := range rec.docs.Documents() {
			_ = rec.docs.CloseURI(doc.URI)
		}
	}
	if rec.client != nil {
		_ = rec.client.Shutdown(context.Background())
	}
}

func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func (s *Service) requestTimeout(cfg *Config) time.Duration {
	if s.opts.RequestTimeout > 0 {
		return s.opts.RequestTimeout
	}
	return cfg.Settings.RequestTimeoutDuration()
}

// Execute validates and runs one read-only action.
func (s *Service) Execute(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Result{}, NewError(CodeInternal, "lsp service is closed")
	}
	cfg, err := s.loader.Load()
	if err != nil {
		return Result{}, NewError(CodeInternal, "load lsp config: %v", err)
	}
	now := s.now()
	s.sweep(now, cfg)

	if req.Action == ActionStatus {
		return s.status(cfg, now)
	}
	if !cfg.Settings.Enabled {
		return Result{}, NewError(CodeDisabled, "lsp is disabled; set settings.enabled=true in lsp.json")
	}

	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout(cfg))
	defer cancel()

	switch req.Action {
	case ActionCapabilities:
		return s.capabilities(ctx, cfg, req)
	case ActionDiagnostics:
		if req.File == "*" {
			return s.workspaceDiagnostics(ctx, cfg)
		}
		return s.singleDiagnostics(ctx, cfg, req)
	case ActionSymbols:
		if req.File == "*" {
			return s.workspaceSymbols(ctx, cfg, req)
		}
		return s.documentSymbols(ctx, cfg, req)
	case ActionDefinition, ActionTypeDefinition, ActionImplementation, ActionReferences, ActionHover:
		return s.positionAction(ctx, cfg, req)
	default:
		return Result{}, NewError(CodeUnsupportedAction, "lsp action %q is not available in read-only v1", req.Action)
	}
}

// Close shuts every server down and clears state.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	records := make([]*serverRecord, 0, len(s.records))
	for _, rec := range s.records {
		records = append(records, rec)
	}
	s.records = map[string]*serverRecord{}
	s.mu.Unlock()
	for _, rec := range records {
		s.stopRecord(rec)
	}
	return nil
}

func (s *Service) sweep(now time.Time, cfg *Config) {
	s.mu.Lock()
	records := make([]*serverRecord, 0, len(s.records))
	for _, rec := range s.records {
		records = append(records, rec)
	}
	s.mu.Unlock()
	for _, rec := range records {
		if rec.docs != nil {
			rec.docs.CloseIdle(now)
		}
		if rec.client == nil || rec.docs == nil {
			continue
		}
		if len(rec.docs.Documents()) == 0 && rec.client.IdleSince(now) >= cfg.IdleTimeout {
			s.mu.Lock()
			delete(s.records, rec.name)
			s.mu.Unlock()
			s.stopRecord(rec)
		}
	}
}

func (s *Service) discover(cfg *Config) []Discovery {
	// Discovery starts from the service cwd. Each definition may pin its own
	// rootDir; DiscoverServers resolves that per server.
	return DiscoverServers(s.rootReal, cfg, s.opts.LookPath)
}

func (s *Service) status(cfg *Config, now time.Time) (Result, error) {
	discs := s.discover(cfg)
	statuses := make([]ServerStatus, 0, len(discs))
	s.mu.Lock()
	records := make(map[string]*serverRecord, len(s.records))
	for name, rec := range s.records {
		records[name] = rec
	}
	s.mu.Unlock()
	for _, disc := range discs {
		status := ServerStatus{
			Name:        disc.Name,
			Source:      cfg.Origin(disc.Name),
			Command:     disc.Definition.Command,
			CommandPath: disc.CommandPath,
			RootMarker:  disc.RootMarker,
			Rooted:      disc.Rooted,
			Disabled:    disc.Definition.Disabled,
			Available:   disc.Available(),
			Reason:      disc.Reason,
			FileTypes:   append([]string(nil), disc.Definition.FileTypes...),
			LanguageID:  disc.Definition.LanguageID,
			Linter:      disc.Definition.IsLinter,
		}
		if rec := records[disc.Name]; rec != nil {
			status.LastError = rec.lastErr
			status.FailedUntil = rec.failedUntil
			if rec.client != nil && rec.client.Alive() {
				status.Running = true
				status.IdleFor = rec.client.IdleSince(now)
				status.ServerName, status.ServerVer = rec.client.ServerInfo()
			}
		}
		statuses = append(statuses, status)
	}
	content, data := renderStatus(StatusInput{
		Settings:   cfg.Settings,
		Idle:       cfg.IdleTimeout,
		Sources:    cfg.Sources,
		Servers:    statuses,
		Catalog:    EmbeddedCatalogSize(),
		CatalogMax: CatalogTarget,
		Warnings:   cfg.Warnings,
		Now:        now,
	})
	return Result{Content: content, Data: data}, nil
}

func (s *Service) capabilities(ctx context.Context, cfg *Config, req Request) (Result, error) {
	discs := s.discover(cfg)
	if req.File != "" && req.File != "*" {
		disc, err := selectServer(discs, req.File)
		if err != nil {
			return Result{}, err
		}
		view, err := s.capabilityFor(ctx, disc)
		if err != nil {
			return Result{}, err
		}
		content, data := renderCapabilities([]CapabilityView{view})
		return Result{Content: content, Data: data}, nil
	}
	views := make([]CapabilityView, 0, len(discs))
	for _, disc := range discs {
		view := CapabilityView{
			Name:      disc.Name,
			FileTypes: append([]string(nil), disc.Definition.FileTypes...),
			State:     disc.State(),
		}
		s.mu.Lock()
		rec := s.records[disc.Name]
		s.mu.Unlock()
		if rec != nil && rec.client != nil && rec.client.Alive() {
			view.Started = true
			view.ServerName, view.ServerVersion = rec.client.ServerInfo()
			view.SyncKind = rec.client.Capabilities().SyncKind()
			view.Providers = capabilityProviders(rec.client.Capabilities())
		}
		views = append(views, view)
	}
	content, data := renderCapabilities(views)
	return Result{Content: content, Data: data}, nil
}

func (s *Service) capabilityFor(ctx context.Context, disc Discovery) (CapabilityView, error) {
	_, client, err := s.ensureClient(ctx, disc)
	if err != nil {
		return CapabilityView{}, err
	}
	name, version := client.ServerInfo()
	return CapabilityView{
		Name:          disc.Name,
		Started:       true,
		ServerName:    name,
		ServerVersion: version,
		SyncKind:      client.Capabilities().SyncKind(),
		Providers:     capabilityProviders(client.Capabilities()),
		FileTypes:     append([]string(nil), disc.Definition.FileTypes...),
		State:         "running",
	}, nil
}

// selectServer resolves file to one available server, preferring primary
// servers over linters and then alphabetical order.
func selectServer(discs []Discovery, file string) (Discovery, error) {
	ext := extension(file)
	var matching []Discovery
	for _, disc := range discs {
		if disc.Definition != nil && disc.Definition.SupportsExtension(ext) {
			matching = append(matching, disc)
		}
	}
	if len(matching) == 0 {
		return Discovery{}, NewError(CodeNoServer, "no configured language server handles %q files", ext)
	}
	var available []Discovery
	var reasons []string
	for _, disc := range matching {
		if disc.Available() {
			available = append(available, disc)
			continue
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", disc.Name, disc.State(), disc.Reason))
	}
	if len(available) == 0 {
		sort.Strings(reasons)
		return Discovery{}, NewError(CodeNoServer, "no available language server for %q files: %s", ext, strings.Join(reasons, "; "))
	}
	sort.SliceStable(available, func(i, j int) bool { return available[i].Name < available[j].Name })
	for _, disc := range available {
		if !disc.Definition.IsLinter {
			return disc, nil
		}
	}
	return available[0], nil
}

func identityOf(def *ServerDef) string {
	if def == nil {
		return ""
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return def.Command
	}
	return string(encoded)
}

func (s *Service) ensureClient(ctx context.Context, disc Discovery) (*serverRecord, *Client, error) {
	if disc.Definition == nil {
		return nil, nil, NewError(CodeNoServer, "server %q has no definition", disc.Name)
	}
	identity := identityOf(disc.Definition)
	for {
		s.mu.Lock()
		rec := s.records[disc.Name]
		if rec != nil && rec.starting {
			done := rec.startDone
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		now := s.now()
		if rec != nil && rec.failedUntil.After(now) {
			lastErr := rec.lastErr
			failedUntil := rec.failedUntil
			s.mu.Unlock()
			return nil, nil, NewError(CodeServerUnavailable,
				"server %q is in failure backoff until %s: %s",
				disc.Name, failedUntil.Format(time.RFC3339), lastErr)
		}
		if rec != nil && rec.client != nil && rec.client.Alive() && rec.identity == identity {
			rec.lastUsed = now
			client := rec.client
			s.mu.Unlock()
			client.Touch()
			return rec, client, nil
		}
		var stale *Client
		if rec != nil && rec.client != nil {
			stale = rec.client
		}
		running := 0
		for _, other := range s.records {
			if other.client != nil && other.client.Alive() {
				running++
			}
		}
		if running >= MaxRunningProcesses {
			s.mu.Unlock()
			return nil, nil, NewError(CodeServerUnavailable,
				"language-server process cap %d reached; close idle servers before starting %q",
				MaxRunningProcesses, disc.Name)
		}
		rec = &serverRecord{
			name:      disc.Name,
			def:       disc.Definition,
			root:      disc.Root,
			disc:      disc,
			identity:  identity,
			starting:  true,
			startDone: make(chan struct{}),
		}
		s.records[disc.Name] = rec
		s.mu.Unlock()
		if stale != nil {
			_ = stale.Shutdown(context.Background())
		}

		client, docs, store, err := s.startServer(ctx, disc)
		s.mu.Lock()
		rec.starting = false
		close(rec.startDone)
		if err != nil {
			rec.failedUntil = s.now().Add(s.opts.Backoff)
			rec.lastErr = err.Error()
			s.mu.Unlock()
			return nil, nil, err
		}
		rec.client = client
		rec.docs = docs
		rec.diags = store
		rec.lastUsed = s.now()
		s.mu.Unlock()
		return rec, client, nil
	}
}

func (s *Service) startServer(ctx context.Context, disc Discovery) (*Client, *DocumentManager, *diagnosticStore, error) {
	store := newDiagnosticStore()
	root := disc.Root
	if root == "" {
		root = s.rootReal
	}
	client, err := StartClient(ctx, ClientOptions{
		Name:          disc.Name,
		Command:       disc.CommandPath,
		Args:          append([]string(nil), disc.Definition.Args...),
		Env:           append([]string(nil), s.opts.ServerEnv...),
		Dir:           root,
		Root:          root,
		InitOptions:   disc.Definition.InitOptions,
		Settings:      disc.Definition.Settings,
		Warmup:        disc.Definition.Warmup(),
		MaxFrameBytes: s.opts.MaxFrameBytes,
		OnNotification: func(method string, params json.RawMessage) {
			if method != "textDocument/publishDiagnostics" {
				return
			}
			var published PublishDiagnosticsParams
			if err := json.Unmarshal(params, &published); err != nil {
				return
			}
			store.Publish(published.URI, published.Version, published.Diagnostics)
		},
	})
	if err != nil {
		return nil, nil, store, NewError(CodeServerUnavailable, "server %q failed to start: %v", disc.Name, err)
	}
	docs, err := NewDocumentManager(root, s.documentIdleTimeout(), clientDocSync{client: client})
	if err != nil {
		_ = client.Kill()
		return nil, nil, store, NewError(CodeInternal, "server %q document manager: %v", disc.Name, err)
	}
	return client, docs, store, nil
}

func (s *Service) documentIdleTimeout() time.Duration {
	cfg, err := s.loader.Load()
	if err != nil || cfg.IdleTimeout <= 0 {
		return s.opts.IdleTimeout
	}
	return cfg.IdleTimeout
}

type clientDocSync struct {
	client *Client
}

func (c clientDocSync) DidOpen(doc Document) error {
	return c.client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": TextDocumentItem{
			URI:        doc.URI,
			LanguageID: doc.LanguageID,
			Version:    doc.Version,
			Text:       doc.Text,
		},
	})
}

func (c clientDocSync) DidChange(doc Document) error {
	return c.client.Notify("textDocument/didChange", map[string]any{
		"textDocument":   VersionedTextDocumentIdentifier{URI: doc.URI, Version: doc.Version},
		"contentChanges": []map[string]any{{"text": doc.Text}},
	})
}

func (c clientDocSync) DidClose(uri string) error {
	return c.client.Notify("textDocument/didClose", map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	})
}

func (s *Service) requestError(rec *serverRecord, server string, err error) error {
	var timeout *TimeoutError
	if errors.As(err, &timeout) {
		return NewError(CodeTimeout, "%s: %v", server, err)
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		s.markFailed(rec, err)
		return NewError(CodeServerUnavailable, "%s: %v", server, err)
	}
	var rpcErr *ResponseError
	if errors.As(err, &rpcErr) {
		return NewError(CodeRequestFailed, "%s: %v", server, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewError(CodeTimeout, "%s: %v", server, err)
	}
	s.markFailed(rec, err)
	return NewError(CodeInternal, "%s: %v", server, err)
}

func (s *Service) markFailed(rec *serverRecord, err error) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.lastErr = err.Error()
	rec.failedUntil = s.now().Add(s.opts.Backoff)
	if rec.client != nil && !rec.client.Alive() {
		rec.client = nil
		if rec.docs != nil {
			for _, doc := range rec.docs.Documents() {
				_ = rec.docs.CloseURI(doc.URI)
			}
		}
		rec.docs = nil
	}
}
