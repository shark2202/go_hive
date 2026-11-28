package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Service) positionAction(ctx context.Context, cfg *Config, req Request) (Result, error) {
	disc, err := selectServer(s.discover(cfg), req.File)
	if err != nil {
		return Result{}, err
	}
	rec, client, err := s.ensureClient(ctx, disc)
	if err != nil {
		return Result{}, err
	}
	doc, err := rec.docs.Open(req.File, languageIDFor(disc.Definition, req.File))
	if err != nil {
		return Result{}, wrapError(err)
	}
	position, runeColumn, err := ResolvePosition(doc.Text, PositionQuery{
		Line:   req.Line,
		Column: req.Column,
		Symbol: req.Symbol,
	})
	displayRoot := s.serverRoot(rec)
	if err != nil {
		return Result{}, NewError(CodePositionRejected, "%s: %v", displayPath(displayRoot, doc.Path), err)
	}
	method, capability := methodFor(req.Action)
	if !client.Capabilities().Has(capability) {
		return Result{}, NewError(CodeUnsupported, "server %q does not advertise %s", disc.Name, capability)
	}
	params := map[string]any{
		"textDocument": TextDocumentIdentifier{URI: doc.URI},
		"position":     position,
	}
	if req.Action == ActionReferences {
		params["context"] = ReferenceContext{IncludeDeclaration: true}
	}
	var raw json.RawMessage
	if err := client.Request(ctx, method, params, &raw); err != nil {
		return Result{}, s.requestError(rec, disc.Name, err)
	}
	var result Result
	switch req.Action {
	case ActionHover:
		var hover Hover
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &hover); err != nil {
				return Result{}, NewError(CodeRequestFailed, "%s: decode hover: %v", disc.Name, err)
			}
		}
		content, data := renderHover(disc.Name, &hover)
		data["file"] = displayPath(displayRoot, doc.Path)
		data["line"] = req.Line
		data["column"] = runeColumn
		result = Result{Content: content, Data: data}
	default:
		locations, err := decodeLocations(raw)
		if err != nil {
			return Result{}, NewError(CodeRequestFailed, "%s: %v", disc.Name, err)
		}
		limit := 0
		if req.Action == ActionReferences {
			limit = MaxReferences
		}
		content, data := renderLocations(string(req.Action), disc.Name, displayRoot, locations, limit)
		data["file"] = displayPath(displayRoot, doc.Path)
		data["line"] = req.Line
		data["column"] = runeColumn
		result = Result{Content: content, Data: data}
	}
	client.Touch()
	rec.lastUsed = s.now()
	return result, nil
}

func methodFor(action Action) (string, string) {
	switch action {
	case ActionDefinition:
		return "textDocument/definition", "definitionProvider"
	case ActionTypeDefinition:
		return "textDocument/typeDefinition", "typeDefinitionProvider"
	case ActionImplementation:
		return "textDocument/implementation", "implementationProvider"
	case ActionReferences:
		return "textDocument/references", "referencesProvider"
	case ActionHover:
		return "textDocument/hover", "hoverProvider"
	default:
		return "", ""
	}
}

func (s *Service) singleDiagnostics(ctx context.Context, cfg *Config, req Request) (Result, error) {
	disc, err := selectServer(s.discover(cfg), req.File)
	if err != nil {
		return Result{}, err
	}
	rec, client, err := s.ensureClient(ctx, disc)
	if err != nil {
		return Result{}, err
	}
	doc, err := rec.docs.Open(req.File, languageIDFor(disc.Definition, req.File))
	if err != nil {
		return Result{}, wrapError(err)
	}
	snapshots, fresh, note, err := s.collectDiagnostics(ctx, rec, client, doc)
	if err != nil {
		return Result{}, err
	}
	content, data := renderDiagnostics(disc.Name, s.serverRoot(rec), snapshots, fresh, note)
	data["file"] = displayPath(s.serverRoot(rec), doc.Path)
	data["document_version"] = doc.Version
	client.Touch()
	rec.lastUsed = s.now()
	return Result{Content: content, Data: data}, nil
}

func (s *Service) collectDiagnostics(ctx context.Context, rec *serverRecord, client *Client, doc Document) ([]DiagnosticSnapshot, bool, string, error) {
	if client.Capabilities().Has("diagnosticProvider") {
		var pull PullDiagnosticsResult
		if err := client.Request(ctx, "textDocument/diagnostic", map[string]any{
			"textDocument": TextDocumentIdentifier{URI: doc.URI},
		}, &pull); err != nil {
			return nil, false, "", s.requestError(rec, rec.name, err)
		}
		version := doc.Version
		if pull.Kind == "unchanged" {
			latest, ok := rec.diags.Latest(doc.URI)
			if !ok {
				return nil, false, "server returned unchanged diagnostics without a cached set", nil
			}
			latest.Fresh = true
			return []DiagnosticSnapshot{latest}, true, "", nil
		}
		rec.diags.Publish(doc.URI, &version, pull.Items)
		snapshot := DiagnosticSnapshot{URI: doc.URI, Version: version, Items: pull.Items, Fresh: true}
		return []DiagnosticSnapshot{snapshot}, true, "", nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.opts.DiagnosticsWait)
	defer cancel()
	snapshot := rec.diags.WaitSince(waitCtx, doc.URI, doc.Version, 50*time.Millisecond, doc.SyncedAt)
	return []DiagnosticSnapshot{snapshot}, snapshot.Fresh, snapshot.Note, nil
}

func (s *Service) workspaceDiagnostics(ctx context.Context, cfg *Config) (Result, error) {
	discs := s.discover(cfg)
	displayRoot := s.sharedRoot(discs)
	files, err := s.workspaceFiles(discs)
	if err != nil {
		return Result{}, NewError(CodeInternal, "scan workspace: %v", err)
	}
	if len(files) == 0 {
		return Result{Content: "diagnostics [workspace]: no matching files", Data: map[string]any{
			"action": "diagnostics", "server": "workspace", "count": 0, "files": []string{},
		}}, nil
	}
	var snapshots []DiagnosticSnapshot
	var serverNames []string
	seenServers := map[string]struct{}{}
	stale := 0
	noteParts := []string{}
	for _, file := range files {
		disc, err := selectServer(discs, file)
		if err != nil {
			continue
		}
		rec, client, err := s.ensureClient(ctx, disc)
		if err != nil {
			noteParts = append(noteParts, fmt.Sprintf("%s: %v", displayPath(displayRoot, file), err))
			stale++
			continue
		}
		doc, err := rec.docs.Open(file, languageIDFor(disc.Definition, file))
		if err != nil {
			noteParts = append(noteParts, fmt.Sprintf("%s: %v", displayPath(displayRoot, file), err))
			stale++
			continue
		}
		fileCtx, cancel := context.WithTimeout(ctx, s.opts.WorkspaceFileBudget)
		snaps, fresh, note, err := s.collectDiagnostics(fileCtx, rec, client, doc)
		cancel()
		if err != nil {
			noteParts = append(noteParts, fmt.Sprintf("%s: %v", displayPath(displayRoot, file), err))
			stale++
			continue
		}
		snapshots = append(snapshots, snaps...)
		if !fresh {
			stale++
			if note != "" {
				noteParts = append(noteParts, displayPath(displayRoot, file)+": "+note)
			}
		}
		if _, ok := seenServers[disc.Name]; !ok {
			seenServers[disc.Name] = struct{}{}
			serverNames = append(serverNames, disc.Name)
		}
	}
	sort.Strings(serverNames)
	label := "workspace"
	if len(serverNames) == 1 {
		label = serverNames[0]
	}
	fresh := stale == 0
	note := ""
	if !fresh {
		note = fmt.Sprintf("%d file(s) not refreshed", stale)
		if len(noteParts) > 0 {
			note += ": " + strings.Join(noteParts, "; ")
		}
	}
	content, data := renderDiagnostics(label, displayRoot, snapshots, fresh, note)
	data["files"] = relativePaths(displayRoot, files)
	data["servers"] = serverNames
	return Result{Content: content, Data: data}, nil
}

// sharedRoot returns the one workspace root all available servers share, or
// the service root when they differ (a mixed workspace has no common display
// root, so paths outside it stay absolute).
func (s *Service) sharedRoot(discs []Discovery) string {
	root := ""
	for _, disc := range discs {
		if !disc.Available() {
			continue
		}
		candidate := disc.Root
		if candidate == "" {
			candidate = s.root
		}
		if root == "" {
			root = candidate
			continue
		}
		if candidate != root {
			return s.root
		}
	}
	if root == "" {
		return s.root
	}
	return root
}

// serverRoot is the effective workspace root for one started server.
func (s *Service) serverRoot(rec *serverRecord) string {
	if rec != nil && rec.root != "" {
		return rec.root
	}
	return s.root
}

func relativePaths(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, displayPath(root, file))
	}
	return out
}

func (s *Service) workspaceFiles(discs []Discovery) ([]string, error) {
	type rootGroup struct {
		root       string
		extensions map[string]struct{}
	}
	groups := map[string]*rootGroup{}
	var order []string
	for _, disc := range discs {
		if !disc.Available() {
			continue
		}
		root := disc.Root
		if root == "" {
			root = s.rootReal
		}
		group := groups[root]
		if group == nil {
			group = &rootGroup{root: root, extensions: map[string]struct{}{}}
			groups[root] = group
			order = append(order, root)
		}
		for _, ext := range disc.Definition.FileTypes {
			group.extensions[strings.ToLower(ext)] = struct{}{}
		}
	}
	var files []string
	seen := map[string]struct{}{}
	for _, root := range order {
		group := groups[root]
		if len(group.extensions) == 0 {
			continue
		}
		found, err := walkWorkspaceFiles(group.root, group.extensions)
		if err != nil {
			return nil, err
		}
		for _, file := range found {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
		if len(files) >= MaxWorkspaceFiles {
			files = files[:MaxWorkspaceFiles]
			break
		}
	}
	sort.Strings(files)
	return files, nil
}

// walkWorkspaceFiles scans one server root for the extensions it handles.
func walkWorkspaceFiles(root string, extensions map[string]struct{}) ([]string, error) {
	var files []string
	visited := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > 20000 {
			return fs.SkipAll
		}
		if _, ok := extensions[extension(path)]; !ok {
			return nil
		}
		files = append(files, path)
		if len(files) >= MaxWorkspaceFiles {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) documentSymbols(ctx context.Context, cfg *Config, req Request) (Result, error) {
	disc, err := selectServer(s.discover(cfg), req.File)
	if err != nil {
		return Result{}, err
	}
	rec, client, err := s.ensureClient(ctx, disc)
	if err != nil {
		return Result{}, err
	}
	doc, err := rec.docs.Open(req.File, languageIDFor(disc.Definition, req.File))
	if err != nil {
		return Result{}, wrapError(err)
	}
	if client.Capabilities().Has("documentSymbolProvider") {
		var raw json.RawMessage
		if err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{
			"textDocument": TextDocumentIdentifier{URI: doc.URI},
		}, &raw); err != nil {
			return Result{}, s.requestError(rec, disc.Name, err)
		}
		hierarchical, flat, err := decodeSymbols(raw)
		if err != nil {
			return Result{}, NewError(CodeRequestFailed, "%s: %v", disc.Name, err)
		}
		views := flattenSymbols(hierarchical, flat)
		displayRoot := s.serverRoot(rec)
		content, data := renderSymbols(disc.Name, displayRoot, doc.Path, views)
		data["file"] = displayPath(displayRoot, doc.Path)
		client.Touch()
		rec.lastUsed = s.now()
		return Result{Content: content, Data: data}, nil
	}
	if !client.Capabilities().Has("workspaceSymbolProvider") {
		return Result{}, NewError(CodeUnsupported, "server %q supports neither documentSymbol nor workspaceSymbol", disc.Name)
	}
	views, err := s.fetchWorkspaceSymbols(ctx, rec, client, "")
	if err != nil {
		return Result{}, err
	}
	filtered := views[:0]
	for _, view := range views {
		if filepath.Clean(view.Path) == filepath.Clean(doc.Path) {
			filtered = append(filtered, view)
		}
	}
	content, data := renderSymbols(disc.Name, s.serverRoot(rec), doc.Path, filtered)
	data["file"] = displayPath(s.serverRoot(rec), doc.Path)
	return Result{Content: content, Data: data}, nil
}

func (s *Service) workspaceSymbols(ctx context.Context, cfg *Config, req Request) (Result, error) {
	discs := s.discover(cfg)
	var available []Discovery
	for _, disc := range discs {
		if disc.Available() {
			available = append(available, disc)
		}
	}
	if len(available) == 0 {
		return Result{}, NewError(CodeNoServer, "no available language server for workspace symbols")
	}
	if len(available) > MaxWorkspaceCheckers {
		available = available[:MaxWorkspaceCheckers]
	}
	var views []symbolView
	var used []string
	var failures []string
	for _, disc := range available {
		rec, client, err := s.ensureClient(ctx, disc)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", disc.Name, err))
			continue
		}
		if !client.Capabilities().Has("workspaceSymbolProvider") {
			continue
		}
		found, err := s.fetchWorkspaceSymbols(ctx, rec, client, req.Query)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", disc.Name, err))
			continue
		}
		views = append(views, found...)
		used = append(used, disc.Name)
	}
	if len(used) == 0 {
		if len(failures) > 0 {
			return Result{}, NewError(CodeServerUnavailable, "workspace symbols failed: %s", strings.Join(failures, "; "))
		}
		return Result{}, NewError(CodeUnsupported, "no available server advertises workspaceSymbolProvider")
	}
	sort.Strings(used)
	label := used[0]
	if len(used) > 1 {
		label = "workspace"
	}
	content, data := renderSymbols(label, s.sharedRoot(available), "", views)
	data["servers"] = used
	if len(failures) > 0 {
		data["warnings"] = failures
	}
	return Result{Content: content, Data: data}, nil
}

func (s *Service) fetchWorkspaceSymbols(ctx context.Context, rec *serverRecord, client *Client, query string) ([]symbolView, error) {
	var raw json.RawMessage
	if err := client.Request(ctx, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
		return nil, s.requestError(rec, rec.name, err)
	}
	var flat []SymbolInformation
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil, NewError(CodeRequestFailed, "%s: decode workspace symbols: %v", rec.name, err)
		}
	}
	views := flattenSymbols(nil, flat)
	client.Touch()
	rec.lastUsed = s.now()
	return views, nil
}

func wrapError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return NewError(CodeInternal, "%v", err)
}
