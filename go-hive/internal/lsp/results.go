package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Result caps from the frozen design.
const (
	MaxReferences        = 50
	MaxContextualRefs    = 50
	MaxSymbols           = 200
	MaxWorkspaceSymbols  = 200
	MaxWorkspaceFiles    = 20
	MaxWorkspaceCheckers = 2
	MaxRunningProcesses  = 8
	MaxDiagnostics       = 200
	MaxHoverBytes        = 4000
	MaxResultBytes       = 64 << 10
	MaxPreviewRunes      = 120
)

// locationView is a rendered, human-facing location.
type locationView struct {
	URI      string
	Path     string
	Line     int // 1-based
	Column   int // 1-based rune column
	EndLine  int
	EndCol   int
	Preview  string
	external bool
}

// symbolView is a flattened symbol.
type symbolView struct {
	Name      string
	Kind      string
	Path      string
	Line      int
	Column    int
	Detail    string
	Container string
	Depth     int
}

func displayPath(root, path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// lineReader caches file lines during one render.
type lineReader struct {
	cache map[string][]string
}

func newLineReader() *lineReader {
	return &lineReader{cache: map[string][]string{}}
}

func (r *lineReader) line(path string, line1Based int) (string, bool) {
	if path == "" || line1Based < 1 {
		return "", false
	}
	lines, ok := r.cache[path]
	if !ok {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxDocumentBytes {
			r.cache[path] = nil
			return "", false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			r.cache[path] = nil
			return "", false
		}
		ix := NewTextIndex(string(data))
		count := ix.LineCount()
		lines = make([]string, count)
		for i := 0; i < count; i++ {
			lines[i], _ = ix.LineText(i)
		}
		r.cache[path] = lines
	}
	if line1Based > len(lines) {
		return "", false
	}
	return lines[line1Based-1], true
}

// viewLocations converts, sorts and dedupes locations. Non-file URIs are
// skipped and counted.
func viewLocations(root string, locations []Location) ([]locationView, int) {
	reader := newLineReader()
	views := make([]locationView, 0, len(locations))
	skipped := 0
	for _, loc := range locations {
		path, err := pathFromURI(loc.URI)
		if err != nil {
			skipped++
			continue
		}
		path = filepath.Clean(path)
		view := locationView{
			URI:      loc.URI,
			Path:     path,
			Line:     loc.Range.Start.Line + 1,
			Column:   loc.Range.Start.Character + 1,
			EndLine:  loc.Range.End.Line + 1,
			EndCol:   loc.Range.End.Character + 1,
			external: !pathWithin(root, path),
		}
		if line, ok := reader.line(path, view.Line); ok {
			ix := NewTextIndex(line)
			view.Column = ix.DisplayColumn(0, loc.Range.Start.Character)
			view.Preview = trimPreview(line)
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.URI < b.URI
	})
	deduped := views[:0]
	seen := map[string]struct{}{}
	for _, view := range views {
		key := fmt.Sprintf("%s:%d:%d:%d:%d", view.Path, view.Line, view.Column, view.EndLine, view.EndCol)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, view)
	}
	return deduped, skipped
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func trimPreview(line string) string {
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) <= MaxPreviewRunes {
		return line
	}
	runes := []rune(line)
	return string(runes[:MaxPreviewRunes]) + "..."
}

// renderLocations renders definition-like results deterministically.
func renderLocations(action, server, root string, locations []Location, cap int) (string, map[string]any) {
	views, skipped := viewLocations(root, locations)
	if len(views) == 0 {
		data := map[string]any{
			"action":    action,
			"server":    server,
			"results":   []any{},
			"skipped":   skipped,
			"truncated": false,
		}
		if skipped > 0 {
			return fmt.Sprintf("%s [%s]: no file results (%d non-file location(s) skipped)", action, server, skipped), data
		}
		return fmt.Sprintf("%s [%s]: no results", action, server), data
	}
	total := len(views)
	limit := total
	if cap > 0 && limit > cap {
		limit = cap
	}
	data := map[string]any{
		"action":    action,
		"server":    server,
		"results":   locationData(views[:limit]),
		"skipped":   skipped,
		"truncated": limit < total,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %d result(s)\n", action, server, total)
	for _, view := range views[:limit] {
		fmt.Fprintf(&b, "  %s:%d:%d", displayPath(root, view.Path), view.Line, view.Column)
		if view.Preview != "" {
			fmt.Fprintf(&b, "  %s", view.Preview)
		}
		b.WriteByte('\n')
	}
	if limit < total {
		fmt.Fprintf(&b, "  ... %d more result(s) omitted (cap %d)\n", total-limit, cap)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "  %d non-file location(s) skipped\n", skipped)
	}
	return boundText(strings.TrimRight(b.String(), "\n"), MaxResultBytes), data
}

func locationData(views []locationView) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, view := range views {
		out = append(out, map[string]any{
			"path":       filepath.ToSlash(view.Path),
			"uri":        view.URI,
			"line":       view.Line,
			"column":     view.Column,
			"end_line":   view.EndLine,
			"end_column": view.EndCol,
			"preview":    view.Preview,
			"external":   view.external,
		})
	}
	return out
}

// renderHover flattens the three allowed hover content shapes.
func renderHover(server string, hover *Hover) (string, map[string]any) {
	if hover == nil {
		return fmt.Sprintf("hover [%s]: no hover information", server), map[string]any{
			"action": "hover", "server": server, "contents": "",
		}
	}
	contents := flattenHover(hover.Contents)
	data := map[string]any{
		"action":   "hover",
		"server":   server,
		"contents": contents,
	}
	if hover.Range != nil {
		data["line"] = hover.Range.Start.Line + 1
		data["column"] = hover.Range.Start.Character + 1
	}
	if strings.TrimSpace(contents) == "" {
		return fmt.Sprintf("hover [%s]: no hover information", server), data
	}
	return fmt.Sprintf("hover [%s]\n%s", server, boundText(contents, MaxHoverBytes)), data
}

func flattenHover(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return normalizeHoverText(plain)
	}
	var markup MarkupContent
	if err := json.Unmarshal(raw, &markup); err == nil && markup.Value != "" {
		return normalizeHoverText(markup.Value)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		var parts []string
		for _, item := range items {
			var text string
			if err := json.Unmarshal(item, &text); err == nil {
				parts = append(parts, text)
				continue
			}
			var marked struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			}
			if err := json.Unmarshal(item, &marked); err == nil && marked.Value != "" {
				parts = append(parts, marked.Value)
			}
		}
		return normalizeHoverText(strings.Join(parts, "\n"))
	}
	return ""
}

func normalizeHoverText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(text)
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

// flattenSymbols converts both symbol shapes into one depth-tagged view.
// Hierarchical symbols have no URI; the caller supplies the document path.
func flattenSymbols(hierarchical []DocumentSymbol, flat []SymbolInformation) []symbolView {
	views := make([]symbolView, 0, len(hierarchical)+len(flat))
	var walk func(sym DocumentSymbol, prefix string, depth int)
	walk = func(sym DocumentSymbol, prefix string, depth int) {
		name := sym.Name
		if prefix != "" {
			name = prefix + "." + sym.Name
		}
		views = append(views, symbolView{
			Name:   name,
			Kind:   SymbolKindName(sym.Kind),
			Line:   sym.SelectionRange.Start.Line + 1,
			Column: sym.SelectionRange.Start.Character + 1,
			Detail: sym.Detail,
			Depth:  depth,
		})
		for _, child := range sym.Children {
			walk(child, name, depth+1)
		}
	}
	for _, sym := range hierarchical {
		walk(sym, "", 0)
	}
	for _, sym := range flat {
		path, err := pathFromURI(sym.Location.URI)
		if err != nil {
			continue
		}
		views = append(views, symbolView{
			Name:      sym.Name,
			Kind:      SymbolKindName(sym.Kind),
			Path:      filepath.Clean(path),
			Line:      sym.Location.Range.Start.Line + 1,
			Column:    sym.Location.Range.Start.Character + 1,
			Container: sym.ContainerName,
		})
	}
	return views
}

// renderSymbols renders document symbols (documentPath set) or workspace
// symbols (documentPath empty).
func renderSymbols(server, root, documentPath string, views []symbolView) (string, map[string]any) {
	if len(views) == 0 {
		return fmt.Sprintf("symbols [%s]: no symbols", server), map[string]any{
			"action": "symbols", "server": server, "symbols": []any{},
		}
	}
	if documentPath == "" {
		sort.SliceStable(views, func(i, j int) bool {
			a, b := views[i], views[j]
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			if a.Column != b.Column {
				return a.Column < b.Column
			}
			return a.Name < b.Name
		})
	}
	total := len(views)
	limit := total
	truncated := false
	if limit > MaxSymbols {
		limit = MaxSymbols
		truncated = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "symbols [%s] %d symbol(s)\n", server, total)
	for _, view := range views[:limit] {
		indent := "  " + strings.Repeat("  ", view.Depth)
		path := documentPath
		if view.Path != "" {
			path = view.Path
		}
		fmt.Fprintf(&b, "%s%s (%s)", indent, view.Name, view.Kind)
		if path != "" {
			fmt.Fprintf(&b, " %s:%d:%d", displayPath(root, path), view.Line, view.Column)
		}
		if view.Container != "" {
			fmt.Fprintf(&b, " [%s]", view.Container)
		}
		if view.Detail != "" {
			fmt.Fprintf(&b, " — %s", view.Detail)
		}
		b.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&b, "  ... %d more symbol(s) omitted (cap %d)\n", total-limit, MaxSymbols)
	}
	data := map[string]any{
		"action":    "symbols",
		"server":    server,
		"truncated": truncated,
		"symbols":   symbolData(views[:limit]),
	}
	return boundText(strings.TrimRight(b.String(), "\n"), MaxResultBytes), data
}

func symbolData(views []symbolView) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, view := range views {
		item := map[string]any{
			"name":   view.Name,
			"kind":   view.Kind,
			"line":   view.Line,
			"column": view.Column,
			"depth":  view.Depth,
		}
		if view.Path != "" {
			item["path"] = filepath.ToSlash(view.Path)
		}
		if view.Container != "" {
			item["container"] = view.Container
		}
		if view.Detail != "" {
			item["detail"] = view.Detail
		}
		out = append(out, item)
	}
	return out
}

// diagnosticView attaches the file a diagnostic belongs to.
type diagnosticView struct {
	Diagnostic
	Path string
}

func buildDiagnosticViews(snapshots []DiagnosticSnapshot) []diagnosticView {
	views := make([]diagnosticView, 0)
	for _, snap := range snapshots {
		path, err := pathFromURI(snap.URI)
		if err != nil {
			continue
		}
		for _, item := range snap.Items {
			views = append(views, diagnosticView{Diagnostic: item, Path: filepath.Clean(path)})
		}
	}
	// Dedupe by path+range+message (the frozen key), then sort.
	seen := map[string]struct{}{}
	deduped := views[:0]
	for _, view := range views {
		key := fmt.Sprintf("%s\x00%d:%d-%d:%d\x00%s", view.Path,
			view.Range.Start.Line, view.Range.Start.Character,
			view.Range.End.Line, view.Range.End.Character, view.Message)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, view)
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		a, b := deduped[i], deduped[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		if a.Range.Start.Character != b.Range.Start.Character {
			return a.Range.Start.Character < b.Range.Start.Character
		}
		return a.Message < b.Message
	})
	return deduped
}

// renderDiagnostics renders deduped, severity-sorted diagnostics with the
// freshness note. Snapshots may cover several files for workspace scans.
func renderDiagnostics(server, root string, snapshots []DiagnosticSnapshot, fresh bool, note string) (string, map[string]any) {
	views := buildDiagnosticViews(snapshots)
	truncated := false
	if len(views) > MaxDiagnostics {
		views = views[:MaxDiagnostics]
		truncated = true
	}
	total := len(views)
	data := map[string]any{
		"action":      "diagnostics",
		"server":      server,
		"fresh":       fresh,
		"versions":    snapshotVersions(snapshots),
		"count":       total,
		"truncated":   truncated,
		"diagnostics": diagnosticData(views),
	}
	if !fresh && note != "" {
		data["note"] = note
	}
	if total == 0 {
		if fresh {
			return fmt.Sprintf("diagnostics [%s]: no diagnostics", server), data
		}
		text := fmt.Sprintf("diagnostics [%s]: no current diagnostics", server)
		if note != "" {
			text += " (" + note + ")"
		}
		return text, data
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics [%s] %d diagnostic(s)\n", server, total)
	for _, view := range views {
		fmt.Fprintf(&b, "  %s:%d:%d  %s  %s",
			displayPath(root, view.Path), view.Range.Start.Line+1, view.Range.Start.Character+1,
			SeverityLabel(view.Severity), view.Message)
		if view.Source != "" {
			fmt.Fprintf(&b, "  (%s)", view.Source)
		}
		if code := diagnosticCode(view.Code); code != "" {
			fmt.Fprintf(&b, " [%s]", code)
		}
		b.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&b, "  ... more diagnostics omitted (cap %d)\n", MaxDiagnostics)
	}
	if !fresh && note != "" {
		fmt.Fprintf(&b, "  (%s)\n", note)
	}
	return boundText(strings.TrimRight(b.String(), "\n"), MaxResultBytes), data
}

func diagnosticCode(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return fmt.Sprintf("%d", n)
	}
	return ""
}

func diagnosticData(views []diagnosticView) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, view := range views {
		out = append(out, map[string]any{
			"path":     filepath.ToSlash(view.Path),
			"line":     view.Range.Start.Line + 1,
			"column":   view.Range.Start.Character + 1,
			"severity": SeverityLabel(view.Severity),
			"message":  view.Message,
			"source":   view.Source,
			"code":     diagnosticCode(view.Code),
		})
	}
	return out
}

func snapshotVersions(snapshots []DiagnosticSnapshot) map[string]int {
	out := make(map[string]int, len(snapshots))
	for _, snap := range snapshots {
		out[snap.URI] = snap.Version
	}
	return out
}

// boundText truncates text to max bytes on a rune boundary.
func boundText(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n... (truncated)"
}
