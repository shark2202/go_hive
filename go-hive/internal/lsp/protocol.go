package lsp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Position is a zero-based LSP position. Character is measured in UTF-16 code
// units, not bytes and not runes.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a file URI plus a range.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink is the link form a server may return for definition-like
// requests when linkSupport was negotiated.
type LocationLink struct {
	OriginSelectionRange *Range `json:"originSelectionRange,omitempty"`
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// TextDocumentIdentifier identifies a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier identifies a document and version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentItem is sent by textDocument/didOpen.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentPositionParams is the shared body of positional requests.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceContext carries the includeDeclaration flag for references.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// MarkupContent is LSP MarkupContent.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Hover is the textDocument/hover result.
type Hover struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

// DocumentSymbol is the hierarchical textDocument/documentSymbol result.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Tags           []int            `json:"tags,omitempty"`
	Deprecated     bool             `json:"deprecated,omitempty"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the flat symbol form used by workspace/symbol and by
// servers without hierarchical document symbols.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Tags          []int    `json:"tags,omitempty"`
	Deprecated    bool     `json:"deprecated,omitempty"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
}

// Diagnostic is one LSP diagnostic. Code is kept raw because servers use both
// strings and numbers.
type Diagnostic struct {
	Range              Range           `json:"range"`
	Severity           int             `json:"severity,omitempty"`
	Code               json.RawMessage `json:"code,omitempty"`
	CodeDescription    json.RawMessage `json:"codeDescription,omitempty"`
	Source             string          `json:"source,omitempty"`
	Message            string          `json:"message"`
	Tags               []int           `json:"tags,omitempty"`
	RelatedInformation json.RawMessage `json:"relatedInformation,omitempty"`
	Data               json.RawMessage `json:"data,omitempty"`
}

// PublishDiagnosticsParams is textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// DiagnosticSeverity values.
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

// SeverityLabel returns the lowercase label used in rendered output.
func SeverityLabel(severity int) string {
	switch severity {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// SymbolKind names (LSP 3.17). Unknown kinds render as "symbol".
func SymbolKindName(kind int) string {
	switch kind {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 15:
		return "string"
	case 16:
		return "number"
	case 17:
		return "boolean"
	case 18:
		return "array"
	case 19:
		return "object"
	case 20:
		return "key"
	case 21:
		return "null"
	case 22:
		return "enum_member"
	case 23:
		return "struct"
	case 24:
		return "event"
	case 25:
		return "operator"
	case 26:
		return "type_parameter"
	default:
		return "symbol"
	}
}

// ServerCapabilities keeps the negotiated capability object as raw JSON. A
// capability is either a boolean or an options object; both mean "supported".
type ServerCapabilities map[string]json.RawMessage

// Has reports whether the capability is present and not literal false/null.
func (c ServerCapabilities) Has(name string) bool {
	raw, ok := c[name]
	if !ok {
		return false
	}
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "false" && trimmed != "null"
}

// SyncKind returns the textDocumentSync change kind: 0 none, 1 full, 2
// incremental. Both the numeric and the object form are accepted.
func (c ServerCapabilities) SyncKind() int {
	raw, ok := c["textDocumentSync"]
	if !ok {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var obj struct {
		Change int `json:"change"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Change
	}
	return 0
}

// InitializeResult is the subset of the initialize response the client keeps.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// ResponseError is a JSON-RPC error object.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("lsp rpc error %d: %s", e.Code, e.Message)
}

// JSON-RPC error codes used by the client.
const (
	RPCParseError       = -32700
	RPCInvalidRequest   = -32600
	RPCMethodNotFound   = -32601
	RPCInvalidParams    = -32602
	RPCInternalError    = -32603
	RPCServerCancelled  = -32800
	RPCContentModified  = -32801
	RPCRequestCancelled = -32802
)

// fileURI converts an absolute filesystem path to a file:// URI. Path
// separators are normalized and characters are escaped by net/url.
func fileURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		// Windows drive letters become file:///C:/...
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// pathFromURI converts a file:// URI to a local path. Non-file URIs are
// rejected so callers never silently read an unrelated scheme.
func pathFromURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("lsp: invalid document URI %q: %w", uri, err)
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("lsp: unsupported document URI scheme %q", parsed.Scheme)
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", fmt.Errorf("lsp: unsupported file URI host %q", parsed.Host)
	}
	path := parsed.Path
	if path == "" {
		return "", fmt.Errorf("lsp: file URI %q has no path", uri)
	}
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		// Windows drive letter: file:///C:/dir becomes C:\dir.
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}

func decodeIntPointer(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func parseIntID(raw json.RawMessage) (int64, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}
	if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// decodeLocations accepts every shape the specification allows for
// definition-like results: null, one Location, an array of Location, or an
// array of LocationLink.
func decodeLocations(raw json.RawMessage) ([]Location, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("lsp: decode locations: %w", err)
		}
	} else {
		items = []json.RawMessage{raw}
	}
	out := make([]Location, 0, len(items))
	for _, item := range items {
		item = json.RawMessage(strings.TrimSpace(string(item)))
		if len(item) == 0 || string(item) == "null" {
			continue
		}
		var probe struct {
			URI       string `json:"uri"`
			TargetURI string `json:"targetUri"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			return nil, fmt.Errorf("lsp: decode location: %w", err)
		}
		if probe.TargetURI != "" {
			var link LocationLink
			if err := json.Unmarshal(item, &link); err != nil {
				return nil, fmt.Errorf("lsp: decode location link: %w", err)
			}
			loc := Location{URI: link.TargetURI, Range: link.TargetSelectionRange}
			if loc.Range == (Range{}) {
				loc.Range = link.TargetRange
			}
			out = append(out, loc)
			continue
		}
		var loc Location
		if err := json.Unmarshal(item, &loc); err != nil {
			return nil, fmt.Errorf("lsp: decode location: %w", err)
		}
		if loc.URI == "" {
			continue
		}
		out = append(out, loc)
	}
	return out, nil
}

// decodeSymbols accepts both DocumentSymbol[] and SymbolInformation[] by
// probing each element for the hierarchical fields.
func decodeSymbols(raw json.RawMessage) ([]DocumentSymbol, []SymbolInformation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, fmt.Errorf("lsp: decode symbols: %w", err)
	}
	var hierarchical []DocumentSymbol
	var flat []SymbolInformation
	for _, item := range items {
		var probe struct {
			SelectionRange *Range `json:"selectionRange"`
		}
		if err := json.Unmarshal(item, &probe); err == nil && probe.SelectionRange != nil {
			var sym DocumentSymbol
			if err := json.Unmarshal(item, &sym); err != nil {
				return nil, nil, fmt.Errorf("lsp: decode document symbol: %w", err)
			}
			hierarchical = append(hierarchical, sym)
			continue
		}
		var sym SymbolInformation
		if err := json.Unmarshal(item, &sym); err != nil {
			return nil, nil, fmt.Errorf("lsp: decode symbol information: %w", err)
		}
		if sym.Name != "" {
			flat = append(flat, sym)
		}
	}
	return hierarchical, flat, nil
}

// PullDiagnosticsResult is the textDocument/diagnostic result.
type PullDiagnosticsResult struct {
	Kind     string       `json:"kind"`
	ResultID string       `json:"resultId,omitempty"`
	Items    []Diagnostic `json:"items"`
}

// WorkspaceFolder is one entry of workspaceFolders.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}
