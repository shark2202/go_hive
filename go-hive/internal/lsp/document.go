package lsp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxDocumentBytes caps a document synchronized to a server.
const MaxDocumentBytes = 8 << 20

// ErrStaleVersion is returned when a change carries a version that is not
// newer than the open document.
var ErrStaleVersion = errors.New("lsp: stale document version")

// Document is one open text document.
type Document struct {
	URI        string
	Path       string
	LanguageID string
	Version    int
	Text       string
	LastUsed   time.Time
	// SyncedAt is when the current version was sent to the server. Diagnostics
	// published before it cannot describe the current content.
	SyncedAt time.Time

	signature fileSignature
}

// DocumentSync receives the three full-text synchronization notifications.
// Service implements it on top of Client; tests record it directly.
type DocumentSync interface {
	DidOpen(doc Document) error
	DidChange(doc Document) error
	DidClose(uri string) error
}

type fileSignature struct {
	size    int64
	modTime int64
	hash    [32]byte
}

// DocumentManager owns the open documents for one language server. Versions
// are per document and monotonic; text changes are always full-text in v1.
type DocumentManager struct {
	root     string
	rootReal string
	idle     time.Duration
	now      func() time.Time
	readFile func(string) ([]byte, error)
	sync     DocumentSync

	mu   sync.Mutex
	docs map[string]*Document
}

// NewDocumentManager validates root and returns an empty manager.
func NewDocumentManager(root string, idle time.Duration, sync DocumentSync) (*DocumentManager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("lsp: document root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("lsp: resolve workspace root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("lsp: stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("lsp: workspace root %s is not a directory", abs)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("lsp: resolve workspace root symlinks: %w", err)
	}
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	return &DocumentManager{
		root:     filepath.Clean(abs),
		rootReal: filepath.Clean(real),
		idle:     idle,
		now:      time.Now,
		readFile: os.ReadFile,
		sync:     sync,
		docs:     map[string]*Document{},
	}, nil
}

// SetClock overrides the clock for idle-close tests.
func (m *DocumentManager) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// Root returns the cleaned workspace root.
func (m *DocumentManager) Root() string { return m.root }

// ResolveWorkspacePath resolves raw against m.root, follows symlinks, refuses
// paths whose real target leaves the workspace, refuses non-regular files and
// caps size.
func (m *DocumentManager) ResolveWorkspacePath(raw string) (string, error) {
	return resolveWorkspacePath(m.rootReal, raw)
}

// resolveWorkspacePath is the path-safety primitive shared by documents and
// workspace scans.
func resolveWorkspacePath(rootReal, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", NewError(CodePathRejected, "file is required")
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootReal, candidate)
	}
	candidate = filepath.Clean(candidate)
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", NewError(CodePathRejected, "file not found: %s", raw)
		}
		return "", NewError(CodePathRejected, "cannot resolve %s: %v", raw, err)
	}
	rel, err := filepath.Rel(rootReal, real)
	if err != nil {
		return "", NewError(CodePathRejected, "cannot relate %s to the workspace: %v", raw, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", NewError(CodePathRejected, "path escapes the workspace: %s", raw)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", NewError(CodePathRejected, "cannot stat %s: %v", raw, err)
	}
	if !info.Mode().IsRegular() {
		return "", NewError(CodePathRejected, "not a regular file: %s", raw)
	}
	if info.Size() > MaxDocumentBytes {
		return "", NewError(CodePathRejected, "file exceeds %d bytes: %s", MaxDocumentBytes, raw)
	}
	return real, nil
}

func signatureOf(data []byte, info os.FileInfo) fileSignature {
	return fileSignature{
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
		hash:    sha256.Sum256(data),
	}
}

// Open synchronizes the document to the server: didOpen on first use, a
// full-text didChange when the file changed on disk, otherwise only activity.
func (m *DocumentManager) Open(path, languageID string) (Document, error) {
	real, err := m.ResolveWorkspacePath(path)
	if err != nil {
		return Document{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if doc, ok := m.docs[real]; ok {
		data, err := m.readFile(real)
		if err != nil {
			return Document{}, NewError(CodePathRejected, "read %s: %v", path, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return Document{}, NewError(CodePathRejected, "stat %s: %v", path, err)
		}
		sig := signatureOf(data, info)
		if sig.size != doc.signature.size || sig.modTime != doc.signature.modTime || sig.hash != doc.signature.hash || string(data) != doc.Text {
			text := string(data)
			if text != doc.Text {
				doc.Text = text
				doc.Version++
				doc.LastUsed = now
				doc.SyncedAt = now
				if languageID != "" {
					doc.LanguageID = languageID
				}
				doc.signature = sig
				if m.sync != nil {
					if err := m.sync.DidChange(*doc); err != nil {
						return Document{}, err
					}
				}
				return *doc, nil
			}
			doc.signature = sig
		}
		doc.LastUsed = now
		if languageID != "" {
			doc.LanguageID = languageID
		}
		return *doc, nil
	}
	data, err := m.readFile(real)
	if err != nil {
		return Document{}, NewError(CodePathRejected, "read %s: %v", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return Document{}, NewError(CodePathRejected, "stat %s: %v", path, err)
	}
	doc := &Document{
		URI:        fileURI(real),
		Path:       real,
		LanguageID: languageID,
		Version:    1,
		Text:       string(data),
		LastUsed:   now,
		SyncedAt:   now,
		signature:  signatureOf(data, info),
	}
	if m.sync != nil {
		if err := m.sync.DidOpen(*doc); err != nil {
			return Document{}, err
		}
	}
	m.docs[real] = doc
	return *doc, nil
}

// Change applies an explicit full-text change. A version that is not newer
// than the open document is rejected before any notification is sent.
func (m *DocumentManager) Change(path, text string, version int) (Document, error) {
	real, err := m.ResolveWorkspacePath(path)
	if err != nil {
		return Document{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.docs[real]
	if !ok {
		return Document{}, NewError(CodeInvalidArgument, "document is not open: %s", path)
	}
	if version <= doc.Version {
		return Document{}, fmt.Errorf("%w: got %d, open version is %d", ErrStaleVersion, version, doc.Version)
	}
	doc.Text = text
	doc.Version = version
	doc.LastUsed = m.now()
	doc.SyncedAt = doc.LastUsed
	if m.sync != nil {
		if err := m.sync.DidChange(*doc); err != nil {
			return Document{}, err
		}
	}
	return *doc, nil
}

// Touch refreshes the idle timer.
func (m *DocumentManager) Touch(path string) {
	real, err := m.ResolveWorkspacePath(path)
	if err != nil {
		return
	}
	m.mu.Lock()
	if doc, ok := m.docs[real]; ok {
		doc.LastUsed = m.now()
	}
	m.mu.Unlock()
}

// Close sends didClose and forgets the document.
func (m *DocumentManager) Close(path string) error {
	real, err := m.ResolveWorkspacePath(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	doc, ok := m.docs[real]
	if ok {
		delete(m.docs, real)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	if m.sync != nil {
		return m.sync.DidClose(doc.URI)
	}
	return nil
}

// CloseURI closes by URI (used by sweeps that already hold a document).
func (m *DocumentManager) CloseURI(uri string) error {
	m.mu.Lock()
	path := ""
	for candidate, doc := range m.docs {
		if doc.URI == uri {
			path = candidate
			break
		}
	}
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	return m.Close(path)
}

// CloseIdle closes documents unused for the manager's idle timeout.
func (m *DocumentManager) CloseIdle(now time.Time) []string {
	m.mu.Lock()
	var expired []*Document
	for path, doc := range m.docs {
		if now.Sub(doc.LastUsed) >= m.idle {
			expired = append(expired, doc)
			delete(m.docs, path)
		}
	}
	m.mu.Unlock()
	closed := make([]string, 0, len(expired))
	for _, doc := range expired {
		if m.sync != nil {
			_ = m.sync.DidClose(doc.URI)
		}
		closed = append(closed, doc.URI)
	}
	return closed
}

// Documents returns a snapshot sorted by path.
func (m *DocumentManager) Documents() []Document {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Document, 0, len(m.docs))
	for _, doc := range m.docs {
		out = append(out, *doc)
	}
	return out
}

// Document returns one open document by path.
func (m *DocumentManager) Document(path string) (Document, bool) {
	real, err := m.ResolveWorkspacePath(path)
	if err != nil {
		return Document{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.docs[real]
	if !ok {
		return Document{}, false
	}
	return *doc, true
}
