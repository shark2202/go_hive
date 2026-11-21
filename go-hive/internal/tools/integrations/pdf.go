// Batch 3 of the integration port: document tools that must run without any
// third-party dependency. This file implements pdf_read from scratch —
// classic and stream xref tables, trailer dictionaries, FlateDecode streams
// and a content-stream text interpreter.
//
// Scope, stated honestly:
//   - Simple text PDFs (Type1/TrueType fonts, WinAnsi or ToUnicode CMaps) are
//     extracted page by page.
//   - Encrypted PDFs are rejected explicitly; password decryption is not
//     implemented (upstream delegates that to pdfplumber).
//   - Scanned/image-only PDFs are rejected with an explicit needs_vision
//     error instead of silently returning an empty body.
//   - Inline images, AcroForm fields, annotations, JavaScript and PostScript
//     functions are ignored.
package integrations

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch3PDFDefaultMaxPages   = 100
	batch3PDFHardMaxPages      = 1000
	batch3PDFDefaultMaxBytesMB = 25.0
	batch3PDFMaxFiles          = 10
	batch3PDFVisionThreshold   = 200 // chars; mirrors upstream _VISION_TEXT_THRESHOLD
	batch3PDFMaxDecodedStream  = 64 << 20
)

// batch3PDFTerminalHint is the upstream terminal-tool cheatsheet, kept so the
// Go port yields the same actionable fallback as the Python tool.
const batch3PDFTerminalHint = "pdftotext '<path>' - | head -200        text extraction (whole file)\n" +
	"  pdfinfo '<path>'                        page count + metadata\n" +
	"  pdfimages -list '<path>'                image inventory (scanned docs)\n" +
	"  qpdf --decrypt '<path>' /tmp/decrypted.pdf  decrypt first when encrypted"

var (
	errPDFEncrypted = errors.New("pdf: encrypted")
	errPDFInvalid   = errors.New("pdf: invalid document")
)

// ---------------------------------------------------------------- registry --

// InstallBatch3Doc publishes the batch-3 document tools (pdf_read and
// web_scrape) into a shared registry and returns the number of tools
// registered.
func InstallBatch3Doc(target *tools.ToolRegistry) int {
	return newBatch3Registry().Install(target)
}

// newBatch3Registry builds a registry containing only the batch-3 tools. The
// scrape-side dependencies stay injectable so tests never touch the network.
func newBatch3Registry(opts ...batch3Option) *Registry {
	deps := defaultBatch3Deps()
	for _, opt := range opts {
		opt(&deps)
	}
	runtime := &Runtime{
		HTTP:        newHTTPClient(nil),
		Now:         time.Now,
		Modules:     map[string]*ModuleConfig{},
		Getenv:      os.Getenv,
		ResolveHost: deps.scrapeResolver,
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{batch3PDFTool, batch3ScrapeTool} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	registry.addImpl("pdf_read", "pdf_read", pdfReadAction)
	registry.addImpl("web_scrape", "web_scrape", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return webScrapeAction(ctx, rt, input, deps.scrapeResolver, deps.scrapeRenderer)
	})
	return registry
}

var batch3PDFTool = &Tool{
	ID:          "pdf_read_tool",
	Name:        "pdf_read",
	DisplayName: "Pdf Read",
	Summary:     "Read and extract text from one or many PDF files (local path or http(s) URL).",
	Actions: []Action{
		{
			Name: "pdf_read",
			Summary: "Extract per-page text and metadata from a text-layer PDF. " +
				"Scanned PDFs return an explicit needs_vision error; encrypted PDFs return an explicit error.",
			Source: "pdf_read_tool/pdf_read_tool.py",
			Params: map[string]any{
				"file_path":        param("string"),
				"files":            paramItems("array", "string"),
				"pages":            paramDefault("string", "all"),
				"max_pages":        paramDefault("integer", batch3PDFDefaultMaxPages),
				"max_bytes_mb":     paramDefault("number", batch3PDFDefaultMaxBytesMB),
				"password":         param("string"),
				"include_metadata": paramDefault("boolean", true),
			},
			Requires: nil,
		},
	},
}

// ---------------------------------------------------------------- action ----

type batch3PDFOptions struct {
	pages           string
	maxPages        int
	maxBytes        int
	password        string
	includeMetadata bool
}

func pdfReadAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	opts := batch3PDFOptions{
		pages:           stringArg(input, "pages"),
		maxPages:        intArg(input, "max_pages", batch3PDFDefaultMaxPages),
		password:        stringArg(input, "password"),
		includeMetadata: boolArg(input, "include_metadata", true),
	}
	if opts.maxPages < 1 {
		opts.maxPages = 1
	}
	if opts.maxPages > batch3PDFHardMaxPages {
		opts.maxPages = batch3PDFHardMaxPages
	}
	opts.maxBytes = int(floatArg(input, "max_bytes_mb", batch3PDFDefaultMaxBytesMB) * 1024 * 1024)
	if opts.maxBytes < 1 {
		opts.maxBytes = 1
	}

	filePath := strings.TrimSpace(stringArg(input, "file_path"))
	files := stringSliceArg(input, "files")
	if filePath != "" && len(files) > 0 {
		return map[string]any{"error": "Pass either file_path or files, not both."}, nil
	}
	if len(files) > batch3PDFMaxFiles {
		return map[string]any{"error": fmt.Sprintf("Too many PDFs: %d provided, max %d.", len(files), batch3PDFMaxFiles)}, nil
	}
	multiMode := len(files) > 0
	targets := files
	if !multiMode {
		if filePath == "" {
			return map[string]any{"error": "Provide file_path (single PDF) or files (list, up to 10)."}, nil
		}
		targets = []string{filePath}
	}

	results := make([]any, 0, len(targets))
	for _, ref := range targets {
		results = append(results, pdfReadOne(ctx, rt, ref, opts))
	}
	if multiMode {
		return map[string]any{"pdfs": results}, nil
	}
	return results[0], nil
}

func pdfReadOne(ctx context.Context, rt *Runtime, ref string, opts batch3PDFOptions) any {
	displayPath := ref
	name := ref
	isURL := strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://")
	var data []byte
	if isURL {
		payload, problem := pdfDownload(ctx, rt, ref, opts.maxBytes)
		if problem != nil {
			return problem
		}
		data = payload
		name = pdfURLName(ref)
	} else {
		path, problem := pdfLocalPath(ref)
		if problem != nil {
			return problem
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return map[string]any{"error": "Permission denied: " + ref}
			}
			return pdfOpenFailure(path, err)
		}
		data = payload
		displayPath = path
		name = filepath.Base(path)
	}

	doc, err := pdfOpen(data)
	if err != nil {
		if errors.Is(err, errPDFEncrypted) {
			out := pdfTerminalYield(displayPath, "pdf_encrypted", "")
			out["error"] = "Cannot read encrypted PDF: password decryption is not supported by the Go port. " +
				"Decrypt it first (for example qpdf --decrypt file.pdf out.pdf) and retry."
			return out
		}
		return pdfOpenFailure(displayPath, err)
	}

	pages, err := doc.pages()
	if err != nil {
		return pdfOpenFailure(displayPath, err)
	}
	indices, truncated, requested, problem := pdfParsePageRange(opts.pages, len(pages), opts.maxPages)
	if problem != "" {
		return map[string]any{"error": problem}
	}

	pageEntries := make([]map[string]any, 0, len(indices))
	needsVisionPages := []int{}
	contentParts := make([]string, 0, len(indices))
	anyText := false
	for _, index := range indices {
		text, extractErr := doc.pageText(pages[index])
		if extractErr != nil {
			text = ""
		}
		text = pdfNormalizeText(text)
		if text != "" {
			anyText = true
		}
		number := index + 1
		needsVision := len(text) < batch3PDFVisionThreshold
		if needsVision {
			needsVisionPages = append(needsVisionPages, number)
		}
		pageEntries = append(pageEntries, map[string]any{
			"number":       number,
			"text":         text,
			"needs_vision": needsVision,
		})
		contentParts = append(contentParts, fmt.Sprintf("--- Page %d ---\n%s", number, text))
	}

	if !anyText {
		// Every requested page is image-only. Returning empty content would be
		// indistinguishable from a broken parser, so make the scan explicit.
		return map[string]any{
			"error": "pdf_scanned_no_text: this PDF has no extractable text layer (scanned or image-only). " +
				"Render the pages and use OCR or a vision-capable model.",
			"needs_vision":       true,
			"needs_vision_pages": needsVisionPages,
			"path":               displayPath,
			"name":               name,
			"total_pages":        len(pages),
			"pages_extracted":    len(indices),
			"next_step":          "Attach the original PDF to a vision-capable model, or run OCR (for example pdftoppm + tesseract).",
			"terminal_hint":      batch3PDFTerminalHint,
		}
	}

	content := strings.Join(contentParts, "\n\n")
	result := map[string]any{
		"path":            displayPath,
		"name":            name,
		"total_pages":     len(pages),
		"pages_extracted": len(indices),
		"content":         content,
		"char_count":      len(content),
		"pages":           pageEntries,
	}
	if len(needsVisionPages) > 0 {
		result["needs_vision_pages"] = needsVisionPages
		result["next_step"] = "Pages with little or no text were flagged needs_vision. Attach the original PDF to a vision-capable model for those pages."
		result["terminal_hint"] = batch3PDFTerminalHint
	}
	if truncated {
		result["truncated"] = true
		result["truncation_warning"] = fmt.Sprintf(
			"Requested %d page(s), but max_pages=%d. Only the first %d page(s) were processed.",
			requested, opts.maxPages, len(indices))
	}
	if opts.includeMetadata {
		result["metadata"] = doc.metadata()
	}
	return result
}

// pdfParsePageRange mirrors the upstream "all" / "5" / "1-10" / "1,3,5" syntax.
func pdfParsePageRange(spec string, total, max int) (indices []int, truncated bool, requested int, problem string) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" || spec == "all" {
		limited := total
		if limited > max {
			limited = max
		}
		indices = make([]int, limited)
		for i := range indices {
			indices[i] = i
		}
		return indices, total > max, total, ""
	}
	if !strings.Contains(spec, "-") && !strings.Contains(spec, ",") {
		value, err := strconv.Atoi(spec)
		if err != nil {
			return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
		}
		if value < 1 || value > total {
			return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", value, total)
		}
		return []int{value - 1}, false, 1, ""
	}
	if strings.Contains(spec, "-") && !strings.Contains(spec, ",") {
		parts := strings.SplitN(spec, "-", 2)
		start, errStart := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, errEnd := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errStart != nil || errEnd != nil {
			return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
		}
		if start > end {
			return nil, false, 0, fmt.Sprintf("Invalid page range: %s. Start must be less than end.", spec)
		}
		if start < 1 {
			return nil, false, 0, fmt.Sprintf("Page numbers start at 1, got %d.", start)
		}
		if end > total {
			return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", end, total)
		}
		requested := end - start + 1
		limitedEnd := end
		if limitedEnd > start-1+max {
			limitedEnd = start - 1 + max
		}
		indices = make([]int, 0, limitedEnd-start+1)
		for page := start; page <= limitedEnd; page++ {
			indices = append(indices, page-1)
		}
		return indices, requested > max, requested, ""
	}
	if strings.Contains(spec, ",") {
		values := strings.Split(spec, ",")
		numbers := make([]int, 0, len(values))
		for _, value := range values {
			number, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
			}
			if number < 1 || number > total {
				return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", number, total)
			}
			numbers = append(numbers, number)
		}
		requested = len(numbers)
		if requested > max {
			numbers = numbers[:max]
		}
		indices = make([]int, 0, len(numbers))
		for _, number := range numbers {
			indices = append(indices, number-1)
		}
		return indices, requested > max, requested, ""
	}
	return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
}

// ----------------------------------------------------------------- inputs ---

func pdfLocalPath(raw string) (string, map[string]any) {
	path, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", map[string]any{"error": "Failed to open PDF: " + err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", map[string]any{"error": "PDF file not found: " + raw}
		}
		return "", map[string]any{"error": "Failed to open PDF: " + err.Error()}
	}
	if info.IsDir() {
		return "", map[string]any{"error": "Not a file: " + raw}
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", map[string]any{"error": "Not a PDF file (expected .pdf): " + raw}
	}
	return path, nil
}

func pdfURLName(target string) string {
	trimmed := strings.SplitN(target, "?", 2)[0]
	trimmed = strings.SplitN(trimmed, "#", 2)[0]
	name := filepath.Base(trimmed)
	if name == "" || name == "." || name == "/" {
		return "download.pdf"
	}
	return name
}

// pdfIsPrivateURL mirrors the upstream string-level SSRF deny-list. DNS
// rebinding is out of scope here, exactly as in the Python tool.
func pdfIsPrivateURL(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" || host == "::1" || host == "0.0.0.0" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	for _, prefix := range []string{"127.", "10.", "192.168.", "169.254."} {
		if strings.HasPrefix(host, prefix) {
			return true
		}
	}
	if strings.HasPrefix(host, "172.") {
		parts := strings.Split(host, ".")
		if len(parts) >= 2 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return true
			}
		}
	}
	return false
}

func pdfDownload(ctx context.Context, rt *Runtime, target string, maxBytes int) ([]byte, map[string]any) {
	if pdfIsPrivateURL(target) {
		return nil, map[string]any{"error": "URL refused (private/loopback host): " + target}
	}
	if block := batch3CheckURLTarget(ctx, target, rt.ResolveHost); block != "" {
		return nil, map[string]any{
			"error":                      "URL refused (SSRF protection): " + block,
			"blocked_by_ssrf_protection": true,
			"url":                        target,
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	request.Header.Set("User-Agent", "AdenBot/1.0 (PDF Reader)")
	client := &http.Client{Timeout: 30 * time.Second}
	if rt.HTTP != nil && rt.HTTP.Inner != nil {
		clone := *rt.HTTP.Inner
		client = &clone
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if block := batch3CheckURLTarget(req.Context(), req.URL.String(), rt.ResolveHost); block != "" {
			return &batch3SSRFError{url: req.URL.String(), message: block}
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		var blocked *batch3SSRFError
		if errors.As(err, &blocked) {
			return nil, map[string]any{
				"error":                      "URL refused (SSRF protection): " + blocked.Error(),
				"blocked_by_ssrf_protection": true,
				"url":                        blocked.url,
			}
		}
		if ctx.Err() != nil || errTimeout(err) {
			return nil, map[string]any{"error": "PDF download timed out"}
		}
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, map[string]any{"error": fmt.Sprintf("Failed to download PDF: HTTP %d", response.StatusCode)}
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/pdf") {
		return nil, map[string]any{
			"error":        "URL does not point to a PDF file. Content-Type: " + contentType,
			"content_type": contentType,
			"url":          target,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	if len(body) > maxBytes {
		return nil, map[string]any{"error": fmt.Sprintf(
			"PDF exceeds max_bytes (%d > %d). Increase max_bytes_mb or use a smaller PDF.", len(body), maxBytes)}
	}
	return body, nil
}

func pdfTerminalYield(path, reason, message string) map[string]any {
	if message == "" {
		message = reason
	}
	return map[string]any{
		"error":         message,
		"path":          path,
		"next_step":     pdfNextStep(reason, path),
		"terminal_hint": batch3PDFTerminalHint,
	}
}

func pdfOpenFailure(path string, err error) map[string]any {
	return pdfTerminalYield(path, "pdf_open_failed", "Failed to open PDF: "+err.Error())
}

func pdfNextStep(reason, path string) string {
	switch reason {
	case "pdf_encrypted":
		return "qpdf --decrypt --password=<pw> '" + path + "' /tmp/decrypted.pdf, then call pdf_read again on /tmp/decrypted.pdf"
	case "pdf_scanned_no_text":
		return "Attach the original PDF to a vision-capable model, or run pdftoppm + tesseract."
	default:
		return "pdfinfo '" + path + "'"
	}
}

// ------------------------------------------------------------ PDF document --

type pdfRef struct {
	num int
	gen int
}

type pdfName string

type pdfKeyword string

type pdfStream struct {
	dict map[string]any
	raw  []byte
}

type pdfDocument struct {
	data      []byte
	objects   map[pdfRef]any
	trailer   map[string]any
	fontCache map[pdfRef]*pdfFont
}

type pdfPage struct {
	dict      map[string]any
	resources map[string]any
}

func pdfOpen(data []byte) (*pdfDocument, error) {
	header := bytes.Index(data, []byte("%PDF-"))
	if header < 0 || header > 1024 {
		return nil, fmt.Errorf("%w: missing %%PDF- header", errPDFInvalid)
	}
	doc := &pdfDocument{data: data, objects: map[pdfRef]any{}, fontCache: map[pdfRef]*pdfFont{}}
	doc.loadXref()
	if len(doc.objects) == 0 {
		doc.objects = map[pdfRef]any{}
		doc.scanObjects()
	}
	doc.expandObjectStreams()
	if len(doc.trailer) == 0 {
		doc.findTrailer()
	}
	if _, ok := doc.trailer["Root"]; !ok {
		doc.findCatalog()
	}
	if doc.trailer["Encrypt"] != nil {
		return nil, errPDFEncrypted
	}
	if len(doc.objects) == 0 {
		return nil, fmt.Errorf("%w: no indirect objects", errPDFInvalid)
	}
	return doc, nil
}

type pdfXrefEntry struct {
	typ    int
	offset int
}

func (d *pdfDocument) loadXref() {
	startxref := bytes.LastIndex(d.data, []byte("startxref"))
	if startxref < 0 {
		return
	}
	lexer := &pdfLexer{data: d.data}
	offset, _, ok := lexer.readInteger(lexer.skip(startxref + len("startxref")))
	if !ok {
		return
	}
	seen := map[int]bool{}
	for depth := 0; depth < 64 && offset > 0 && offset < len(d.data) && !seen[offset]; depth++ {
		seen[offset] = true
		entries, trailer, prev := d.parseXrefSection(offset)
		for ref, entry := range entries {
			if entry.typ != 1 {
				continue
			}
			if _, exists := d.objects[ref]; exists {
				continue
			}
			if _, obj, err := d.parseIndirectAt(entry.offset); err == nil {
				d.objects[ref] = obj
			}
		}
		if len(trailer) > 0 && len(d.trailer) == 0 {
			d.trailer = trailer
		}
		if prev <= 0 || prev == offset {
			break
		}
		offset = prev
	}
}

func (d *pdfDocument) parseXrefSection(offset int) (map[pdfRef]pdfXrefEntry, map[string]any, int) {
	entries := map[pdfRef]pdfXrefEntry{}
	if bytes.HasPrefix(d.data[offset:], []byte("xref")) {
		lexer := &pdfLexer{data: d.data}
		pos := lexer.skip(offset + len("xref"))
		for {
			pos = lexer.skip(pos)
			if pos >= len(d.data) {
				return entries, nil, 0
			}
			if bytes.HasPrefix(d.data[pos:], []byte("trailer")) {
				trailerValue, after, err := lexer.parseValue(lexer.skip(pos + len("trailer")))
				if err != nil {
					return entries, nil, 0
				}
				trailer, _ := trailerValue.(map[string]any)
				prev := 0
				if trailer != nil {
					prev = pdfIntValue(trailer["Prev"])
				}
				_ = after
				return entries, trailer, prev
			}
			start, next, ok := lexer.readInteger(pos)
			if !ok {
				return entries, nil, 0
			}
			count, nextCount, ok := lexer.readInteger(lexer.skip(next))
			if !ok {
				return entries, nil, 0
			}
			pos = nextCount
			for i := 0; i < count && i < 1_000_000; i++ {
				pos = lexer.skip(pos)
				entryOffset, nextOffset, ok := lexer.readInteger(pos)
				if !ok {
					return entries, nil, 0
				}
				gen, nextGen, ok := lexer.readInteger(lexer.skip(nextOffset))
				if !ok {
					return entries, nil, 0
				}
				pos = lexer.skip(nextGen)
				if pos < len(d.data) {
					kind := d.data[pos]
					pos++
					if kind == 'n' {
						entries[pdfRef{num: start + i, gen: gen}] = pdfXrefEntry{typ: 1, offset: entryOffset}
					}
				}
			}
		}
	}
	// PDF 1.5+ cross-reference stream.
	_, object, err := d.parseIndirectAt(offset)
	if err != nil {
		return entries, nil, 0
	}
	stream, ok := object.(*pdfStream)
	if !ok || pdfNameOf(stream.dict["Type"]) != "XRef" {
		return entries, nil, 0
	}
	decoded, err := d.decodeStream(stream)
	if err != nil {
		return entries, nil, 0
	}
	widths := d.array(stream.dict["W"])
	if len(widths) < 3 {
		return entries, nil, 0
	}
	w0, w1, w2 := pdfIntValue(d.resolve(widths[0])), pdfIntValue(d.resolve(widths[1])), pdfIntValue(d.resolve(widths[2]))
	if w0 < 0 || w1 < 0 || w2 < 0 || w0+w1+w2 <= 0 {
		return entries, nil, 0
	}
	index := d.array(stream.dict["Index"])
	if len(index) == 0 {
		index = []any{0, pdfIntValue(d.resolve(stream.dict["Size"]))}
	}
	record := w0 + w1 + w2
	cursor := 0
	for pair := 0; pair+1 < len(index); pair += 2 {
		first := pdfIntValue(d.resolve(index[pair]))
		count := pdfIntValue(d.resolve(index[pair+1]))
		for i := 0; i < count && cursor+record <= len(decoded); i++ {
			fields := make([]int, 3)
			position := cursor
			for field := 0; field < 3; field++ {
				width := []int{w0, w1, w2}[field]
				value := 0
				for b := 0; b < width; b++ {
					value = value<<8 | int(decoded[position])
					position++
				}
				fields[field] = value
			}
			cursor += record
			typ := fields[0]
			if w0 == 0 {
				typ = 1
			}
			if typ == 1 {
				entries[pdfRef{num: first + i, gen: fields[2]}] = pdfXrefEntry{typ: 1, offset: fields[1]}
			}
		}
	}
	trailer := stream.dict
	prev := pdfIntValue(trailer["Prev"])
	return entries, trailer, prev
}

// parseIndirectAt parses "num gen obj ..." at an absolute offset.
func (d *pdfDocument) parseIndirectAt(offset int) (pdfRef, any, error) {
	if offset < 0 || offset >= len(d.data) {
		return pdfRef{}, nil, fmt.Errorf("%w: xref offset out of range", errPDFInvalid)
	}
	lexer := &pdfLexer{data: d.data}
	pos := lexer.skip(offset)
	num, next, ok := lexer.readInteger(pos)
	if !ok {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	gen, nextGen, ok := lexer.readInteger(lexer.skip(next))
	if !ok {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	pos = lexer.skip(nextGen)
	if !bytes.HasPrefix(d.data[pos:], []byte("obj")) {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	pos += len("obj")
	value, after, err := lexer.parseValue(pos)
	if err != nil {
		return pdfRef{}, nil, err
	}
	pos = lexer.skip(after)
	if bytes.HasPrefix(d.data[pos:], []byte("stream")) {
		return pdfRef{num: num, gen: gen}, d.readStream(value, pos+len("stream")), nil
	}
	return pdfRef{num: num, gen: gen}, value, nil
}

func (d *pdfDocument) readStream(dictValue any, pos int) *pdfStream {
	dict, _ := dictValue.(map[string]any)
	if dict == nil {
		dict = map[string]any{}
	}
	if pos < len(d.data) && d.data[pos] == '\r' {
		pos++
		if pos < len(d.data) && d.data[pos] == '\n' {
			pos++
		}
	} else if pos < len(d.data) && d.data[pos] == '\n' {
		pos++
	}
	length := -1
	switch value := dict["Length"].(type) {
	case int:
		length = value
	case float64:
		length = int(value)
	case pdfRef:
		if resolved, ok := d.resolve(value).(int); ok {
			length = resolved
		}
	}
	var raw []byte
	if length >= 0 && pos+length <= len(d.data) {
		raw = d.data[pos : pos+length]
	} else {
		end := bytes.Index(d.data[pos:], []byte("endstream"))
		if end < 0 {
			end = len(d.data) - pos
		}
		raw = bytes.TrimRight(d.data[pos:pos+end], "\r\n")
	}
	return &pdfStream{dict: dict, raw: append([]byte(nil), raw...)}
}

// scanObjects rebuilds the object map for PDFs whose xref table is broken
// (or absent). It only accepts "N G obj ... endobj/stream" sequences.
func (d *pdfDocument) scanObjects() {
	lexer := &pdfLexer{data: d.data}
	for pos := 0; pos < len(d.data); {
		index := bytes.Index(d.data[pos:], []byte("obj"))
		if index < 0 {
			return
		}
		at := pos + index
		pos = at + len("obj")
		if at+3 < len(d.data) && !pdfIsDelimiter(d.data[at+3]) {
			continue
		}
		ref, _, ok := pdfObjectHeaderBefore(d.data, at)
		if !ok {
			continue
		}
		value, after, err := lexer.parseValue(lexer.skip(at + len("obj")))
		if err != nil {
			continue
		}
		after = lexer.skip(after)
		if bytes.HasPrefix(d.data[after:], []byte("endobj")) {
			if _, exists := d.objects[ref]; !exists {
				d.objects[ref] = value
			}
			continue
		}
		if bytes.HasPrefix(d.data[after:], []byte("stream")) {
			stream := d.readStream(value, after+len("stream"))
			if _, exists := d.objects[ref]; !exists {
				d.objects[ref] = stream
			}
			continue
		}
	}
}

func pdfObjectHeaderBefore(data []byte, objPos int) (pdfRef, int, bool) {
	i := objPos - 1
	for i >= 0 && pdfIsSpace(data[i]) {
		i--
	}
	endGen := i + 1
	for i >= 0 && pdfIsDigit(data[i]) {
		i--
	}
	if i+1 == endGen {
		return pdfRef{}, 0, false
	}
	gen, err := strconv.Atoi(string(data[i+1 : endGen]))
	if err != nil {
		return pdfRef{}, 0, false
	}
	for i >= 0 && pdfIsSpace(data[i]) {
		i--
	}
	endNum := i + 1
	for i >= 0 && pdfIsDigit(data[i]) {
		i--
	}
	if i+1 == endNum {
		return pdfRef{}, 0, false
	}
	num, err := strconv.Atoi(string(data[i+1 : endNum]))
	if err != nil {
		return pdfRef{}, 0, false
	}
	if i >= 0 && !pdfIsSpace(data[i]) && !pdfIsDelimiter(data[i]) {
		return pdfRef{}, 0, false
	}
	return pdfRef{num: num, gen: gen}, i + 1, true
}

func (d *pdfDocument) expandObjectStreams() {
	for _, object := range d.objects {
		stream, ok := object.(*pdfStream)
		if !ok || pdfNameOf(stream.dict["Type"]) != "ObjStm" {
			continue
		}
		decoded, err := d.decodeStream(stream)
		if err != nil {
			continue
		}
		count := pdfIntValue(d.resolve(stream.dict["N"]))
		first := pdfIntValue(d.resolve(stream.dict["First"]))
		if count <= 0 || first < 0 || first > len(decoded) {
			continue
		}
		lexer := &pdfLexer{data: decoded}
		pos := 0
		numbers := make([]int, 0, count)
		offsets := make([]int, 0, count)
		for i := 0; i < count; i++ {
			number, next, ok := lexer.readInteger(lexer.skip(pos))
			if !ok {
				break
			}
			offset, nextOffset, ok := lexer.readInteger(lexer.skip(next))
			if !ok {
				break
			}
			numbers = append(numbers, number)
			offsets = append(offsets, offset)
			pos = nextOffset
		}
		for i := range numbers {
			ref := pdfRef{num: numbers[i], gen: 0}
			if _, exists := d.objects[ref]; exists {
				continue
			}
			value, _, err := lexer.parseValue(first + offsets[i])
			if err != nil {
				continue
			}
			d.objects[ref] = value
		}
	}
}

func (d *pdfDocument) findTrailer() {
	lexer := &pdfLexer{data: d.data}
	pos := 0
	for {
		index := bytes.Index(d.data[pos:], []byte("trailer"))
		if index < 0 {
			break
		}
		at := pos + index
		value, _, err := lexer.parseValue(lexer.skip(at + len("trailer")))
		if err == nil {
			if trailer, ok := value.(map[string]any); ok {
				d.trailer = trailer
			}
		}
		pos = at + len("trailer")
	}
	if len(d.trailer) > 0 {
		return
	}
	for _, object := range d.objects {
		stream, ok := object.(*pdfStream)
		if !ok || pdfNameOf(stream.dict["Type"]) != "XRef" {
			continue
		}
		if stream.dict["Root"] != nil {
			d.trailer = stream.dict
			return
		}
	}
}

func (d *pdfDocument) findCatalog() {
	for ref, object := range d.objects {
		dict, ok := object.(map[string]any)
		if !ok {
			continue
		}
		if pdfNameOf(dict["Type"]) == "Catalog" {
			if d.trailer == nil {
				d.trailer = map[string]any{}
			}
			d.trailer["Root"] = ref
			return
		}
	}
}

func (d *pdfDocument) resolve(value any) any {
	if ref, ok := value.(pdfRef); ok {
		return d.objects[ref]
	}
	return value
}

func (d *pdfDocument) dict(value any) map[string]any {
	switch typed := d.resolve(value).(type) {
	case map[string]any:
		return typed
	case *pdfStream:
		return typed.dict
	}
	return nil
}

func (d *pdfDocument) array(value any) []any {
	array, _ := d.resolve(value).([]any)
	return array
}

func (d *pdfDocument) integer(value any) int {
	return pdfIntValue(d.resolve(value))
}

func (d *pdfDocument) decodeStream(stream *pdfStream) ([]byte, error) {
	data := stream.raw
	filters := d.resolve(stream.dict["Filter"])
	var names []string
	switch typed := filters.(type) {
	case pdfName:
		names = []string{string(typed)}
	case []any:
		for _, item := range typed {
			if name, ok := d.resolve(item).(pdfName); ok {
				names = append(names, string(name))
			}
		}
	}
	for _, name := range names {
		switch name {
		case "FlateDecode", "Fl":
			decoded, err := pdfInflate(data)
			if err != nil {
				return nil, err
			}
			data = decoded
		case "ASCIIHexDecode", "AHx":
			data = pdfASCIIHexDecode(data)
		default:
			return nil, fmt.Errorf("%w: unsupported stream filter %s", errPDFInvalid, name)
		}
	}
	return d.applyPredictor(data, stream.dict["DecodeParms"]), nil
}

// applyPredictor undoes the PNG predictors (10-15) used by xref streams and
// occasional content streams. Other predictor values need no work.
func (d *pdfDocument) applyPredictor(data []byte, parms any) []byte {
	parmsValue := d.resolve(parms)
	if array, ok := parmsValue.([]any); ok && len(array) > 0 {
		parmsValue = d.resolve(array[0])
	}
	dict := d.dict(parmsValue)
	if dict == nil {
		return data
	}
	predictor := d.integer(dict["Predictor"])
	if predictor < 10 || predictor > 15 || len(data) == 0 {
		return data
	}
	columns := d.integer(dict["Columns"])
	if columns <= 0 {
		return data
	}
	colors := d.integer(dict["Colors"])
	if colors <= 0 {
		colors = 1
	}
	bits := d.integer(dict["BitsPerComponent"])
	if bits <= 0 {
		bits = 8
	}
	rowLength := (columns*colors*bits + 7) / 8
	bytesPerPixel := (colors*bits + 7) / 8
	if bytesPerPixel < 1 {
		bytesPerPixel = 1
	}
	out := make([]byte, 0, len(data))
	previous := make([]byte, rowLength)
	cursor := 0
	for cursor < len(data) && cursor+1+rowLength <= len(data) {
		filter := data[cursor]
		cursor++
		row := append([]byte(nil), data[cursor:cursor+rowLength]...)
		cursor += rowLength
		switch filter {
		case 0: // None
		case 1: // Sub
			for i := bytesPerPixel; i < len(row); i++ {
				row[i] += row[i-bytesPerPixel]
			}
		case 2: // Up
			for i := 0; i < len(row); i++ {
				row[i] += previous[i]
			}
		case 3: // Average
			for i := 0; i < len(row); i++ {
				left := 0
				if i >= bytesPerPixel {
					left = int(row[i-bytesPerPixel])
				}
				row[i] += byte((left + int(previous[i])) / 2)
			}
		case 4: // Paeth
			for i := 0; i < len(row); i++ {
				left, upperLeft := 0, 0
				if i >= bytesPerPixel {
					left = int(row[i-bytesPerPixel])
					upperLeft = int(previous[i-bytesPerPixel])
				}
				row[i] += byte(pdfPaeth(left, int(previous[i]), upperLeft))
			}
		default:
			return data
		}
		out = append(out, row...)
		previous = row
	}
	return out
}

func pdfPaeth(left, above, upperLeft int) int {
	estimate := left + above - upperLeft
	leftDistance := estimate - left
	if leftDistance < 0 {
		leftDistance = -leftDistance
	}
	aboveDistance := estimate - above
	if aboveDistance < 0 {
		aboveDistance = -aboveDistance
	}
	upperLeftDistance := estimate - upperLeft
	if upperLeftDistance < 0 {
		upperLeftDistance = -upperLeftDistance
	}
	if leftDistance <= aboveDistance && leftDistance <= upperLeftDistance {
		return left
	}
	if aboveDistance <= upperLeftDistance {
		return above
	}
	return upperLeft
}

func pdfInflate(data []byte) ([]byte, error) {
	if reader, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		defer reader.Close()
		return io.ReadAll(io.LimitReader(reader, batch3PDFMaxDecodedStream))
	}
	reader := flate.NewReader(bytes.NewReader(data))
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, batch3PDFMaxDecodedStream))
}

func pdfASCIIHexDecode(data []byte) []byte {
	out := make([]byte, 0, len(data)/2)
	high := -1
	for _, char := range data {
		if char == '>' {
			break
		}
		value := -1
		switch {
		case char >= '0' && char <= '9':
			value = int(char - '0')
		case char >= 'a' && char <= 'f':
			value = int(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value = int(char-'A') + 10
		}
		if value < 0 {
			continue
		}
		if high < 0 {
			high = value
			continue
		}
		out = append(out, byte(high<<4|value))
		high = -1
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return out
}

func (d *pdfDocument) pages() ([]pdfPage, error) {
	root := d.dict(d.trailer["Root"])
	if root == nil {
		return nil, fmt.Errorf("%w: missing catalog", errPDFInvalid)
	}
	tree := d.dict(root["Pages"])
	if tree == nil {
		return nil, fmt.Errorf("%w: missing page tree", errPDFInvalid)
	}
	pages := make([]pdfPage, 0, 8)
	visiting := map[pdfRef]bool{}
	var walk func(node map[string]any, inherited map[string]any, depth int)
	walk = func(node map[string]any, inherited map[string]any, depth int) {
		if node == nil || depth > 64 || len(pages) > 100_000 {
			return
		}
		resources := d.dict(node["Resources"])
		if resources == nil {
			resources = inherited
		}
		kids := d.array(node["Kids"])
		isPage := pdfNameOf(node["Type"]) == "Page"
		if !isPage && kids != nil {
			for _, kid := range kids {
				if ref, ok := kid.(pdfRef); ok {
					if visiting[ref] {
						continue
					}
					visiting[ref] = true
				}
				walk(d.dict(kid), resources, depth+1)
			}
			return
		}
		pages = append(pages, pdfPage{dict: node, resources: resources})
	}
	walk(tree, nil, 0)
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: no pages", errPDFInvalid)
	}
	return pages, nil
}

func (d *pdfDocument) pageText(page pdfPage) (string, error) {
	content, err := d.pageContent(page)
	if err != nil {
		return "", err
	}
	fonts := d.pageFonts(page.resources)
	return pdfExtractText(content, fonts), nil
}

func (d *pdfDocument) pageContent(page pdfPage) ([]byte, error) {
	contents := d.resolve(page.dict["Contents"])
	switch typed := contents.(type) {
	case *pdfStream:
		return d.decodeStream(typed)
	case []any:
		var buffer bytes.Buffer
		for _, item := range typed {
			stream, ok := d.resolve(item).(*pdfStream)
			if !ok {
				continue
			}
			decoded, err := d.decodeStream(stream)
			if err != nil {
				return nil, err
			}
			buffer.Write(decoded)
			buffer.WriteByte('\n')
		}
		return buffer.Bytes(), nil
	}
	return nil, nil
}

func (d *pdfDocument) metadata() map[string]any {
	info := d.dict(d.trailer["Info"])
	result := map[string]any{
		"title": nil, "author": nil, "subject": nil, "creator": nil,
		"producer": nil, "created": nil, "modified": nil,
	}
	if info == nil {
		return result
	}
	fields := map[string]string{
		"Title": "title", "Author": "author", "Subject": "subject",
		"Creator": "creator", "Producer": "producer",
		"CreationDate": "created", "ModDate": "modified",
	}
	for key, field := range fields {
		if text, ok := pdfTextString(info[key]); ok && text != "" {
			result[field] = text
		}
	}
	return result
}

// ------------------------------------------------------------ PDF lexer -----

type pdfLexer struct {
	data []byte
}

func pdfIsSpace(char byte) bool {
	switch char {
	case 0x00, 0x09, 0x0A, 0x0C, 0x0D, 0x20:
		return true
	}
	return false
}

func pdfIsDelimiter(char byte) bool {
	switch char {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return pdfIsSpace(char)
}

func pdfIsDigit(char byte) bool { return char >= '0' && char <= '9' }

func (l *pdfLexer) skip(pos int) int {
	for pos < len(l.data) {
		switch {
		case l.data[pos] == '%':
			for pos < len(l.data) && l.data[pos] != '\n' && l.data[pos] != '\r' {
				pos++
			}
		case pdfIsSpace(l.data[pos]):
			pos++
		default:
			return pos
		}
	}
	return pos
}

func (l *pdfLexer) readInteger(pos int) (int, int, bool) {
	pos = l.skip(pos)
	start := pos
	if pos < len(l.data) && (l.data[pos] == '+' || l.data[pos] == '-') {
		pos++
	}
	digits := pos
	for pos < len(l.data) && pdfIsDigit(l.data[pos]) {
		pos++
	}
	if pos == digits {
		return 0, start, false
	}
	value, err := strconv.Atoi(string(l.data[start:pos]))
	if err != nil {
		return 0, pos, false
	}
	return value, pos, true
}

func (l *pdfLexer) parseValue(pos int) (any, int, error) {
	pos = l.skip(pos)
	if pos >= len(l.data) {
		return nil, pos, io.EOF
	}
	switch char := l.data[pos]; {
	case char == '<':
		if pos+1 < len(l.data) && l.data[pos+1] == '<' {
			return l.parseDict(pos + 2)
		}
		return l.parseHexString(pos)
	case char == '[':
		return l.parseArray(pos + 1)
	case char == '(':
		return l.parseLiteralString(pos)
	case char == '/':
		return l.parseName(pos)
	case char == ']' || char == '>' || char == ')' || char == '}' || char == '{':
		return pdfKeyword(string(char)), pos + 1, nil
	default:
		start := pos
		for pos < len(l.data) && !pdfIsDelimiter(l.data[pos]) {
			pos++
		}
		if pos == start {
			return pdfKeyword(string(l.data[pos : pos+1])), pos + 1, nil
		}
		token := string(l.data[start:pos])
		switch token {
		case "true":
			return true, pos, nil
		case "false":
			return false, pos, nil
		case "null":
			return nil, pos, nil
		}
		if number, err := strconv.ParseInt(token, 10, 64); err == nil {
			// Possible indirect reference "N G R".
			after := l.skip(pos)
			if gen, next, ok := l.readInteger(after); ok && gen >= 0 {
				after = l.skip(next)
				if after < len(l.data) && l.data[after] == 'R' && (after+1 == len(l.data) || pdfIsDelimiter(l.data[after+1])) {
					return pdfRef{num: int(number), gen: gen}, after + 1, nil
				}
			}
			return int(number), pos, nil
		}
		if float, err := strconv.ParseFloat(token, 64); err == nil {
			return float, pos, nil
		}
		return pdfKeyword(token), pos, nil
	}
}

func (l *pdfLexer) parseDict(pos int) (map[string]any, int, error) {
	dict := map[string]any{}
	for {
		pos = l.skip(pos)
		if pos+1 < len(l.data) && l.data[pos] == '>' && l.data[pos+1] == '>' {
			return dict, pos + 2, nil
		}
		keyValue, next, err := l.parseValue(pos)
		if err != nil {
			return nil, pos, err
		}
		key, ok := keyValue.(pdfName)
		if !ok {
			return nil, pos, fmt.Errorf("%w: dictionary key", errPDFInvalid)
		}
		value, after, err := l.parseValue(next)
		if err != nil {
			return nil, pos, err
		}
		dict[string(key)] = value
		pos = after
	}
}

func (l *pdfLexer) parseArray(pos int) ([]any, int, error) {
	array := []any{}
	for {
		pos = l.skip(pos)
		if pos >= len(l.data) {
			return array, pos, nil
		}
		if l.data[pos] == ']' {
			return array, pos + 1, nil
		}
		value, next, err := l.parseValue(pos)
		if err != nil {
			return array, pos, err
		}
		array = append(array, value)
		pos = next
	}
}

func (l *pdfLexer) parseLiteralString(pos int) (string, int, error) {
	pos++ // opening paren
	depth := 1
	out := make([]byte, 0, 32)
	for pos < len(l.data) {
		char := l.data[pos]
		switch char {
		case '\\':
			pos++
			if pos >= len(l.data) {
				return string(out), pos, nil
			}
			switch escaped := l.data[pos]; escaped {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, escaped)
			case '\r':
				if pos+1 < len(l.data) && l.data[pos+1] == '\n' {
					pos++
				}
			case '\n':
				// line continuation
			default:
				if escaped >= '0' && escaped <= '7' {
					value := 0
					for count := 0; count < 3 && pos < len(l.data) && l.data[pos] >= '0' && l.data[pos] <= '7'; count++ {
						value = value*8 + int(l.data[pos]-'0')
						pos++
					}
					out = append(out, byte(value))
					continue
				}
				out = append(out, escaped)
			}
			pos++
		case '(':
			depth++
			out = append(out, char)
			pos++
		case ')':
			depth--
			if depth == 0 {
				return string(out), pos + 1, nil
			}
			out = append(out, char)
			pos++
		default:
			out = append(out, char)
			pos++
		}
	}
	return string(out), pos, nil
}

func (l *pdfLexer) parseHexString(pos int) (string, int, error) {
	pos++ // opening <
	high := -1
	out := make([]byte, 0, 16)
	for pos < len(l.data) {
		char := l.data[pos]
		pos++
		if char == '>' {
			break
		}
		value := -1
		switch {
		case char >= '0' && char <= '9':
			value = int(char - '0')
		case char >= 'a' && char <= 'f':
			value = int(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value = int(char-'A') + 10
		}
		if value < 0 {
			continue
		}
		if high < 0 {
			high = value
			continue
		}
		out = append(out, byte(high<<4|value))
		high = -1
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return string(out), pos, nil
}

func (l *pdfLexer) parseName(pos int) (pdfName, int, error) {
	pos++ // slash
	out := make([]byte, 0, 16)
	for pos < len(l.data) && !pdfIsDelimiter(l.data[pos]) {
		if l.data[pos] == '#' && pos+2 < len(l.data) {
			if value, err := strconv.ParseUint(string(l.data[pos+1:pos+3]), 16, 8); err == nil {
				out = append(out, byte(value))
				pos += 3
				continue
			}
		}
		out = append(out, l.data[pos])
		pos++
	}
	return pdfName(out), pos, nil
}

// -------------------------------------------------------------- text ops ----

type pdfTextSink struct {
	lines   []string
	current strings.Builder
	font    *pdfFont
	leading float64
	lastY   float64
	haveY   bool
}

func (s *pdfTextSink) newline() {
	line := strings.Join(strings.Fields(s.current.String()), " ")
	if line != "" {
		s.lines = append(s.lines, line)
	}
	s.current.Reset()
}

func (s *pdfTextSink) write(text string) {
	s.current.WriteString(text)
}

func (s *pdfTextSink) space() {
	text := s.current.String()
	if text != "" && !strings.HasSuffix(text, " ") {
		s.current.WriteByte(' ')
	}
}

func pdfExtractText(content []byte, fonts map[string]*pdfFont) string {
	if len(content) == 0 {
		return ""
	}
	sink := &pdfTextSink{}
	lexer := &pdfLexer{data: content}
	operands := make([]any, 0, 8)
	pos := 0
	for pos < len(content) {
		value, next, err := lexer.parseValue(pos)
		if err != nil {
			break
		}
		pos = next
		if keyword, ok := value.(pdfKeyword); ok {
			pdfRunTextOperator(sink, fonts, string(keyword), operands)
			operands = operands[:0]
			continue
		}
		operands = append(operands, value)
	}
	sink.newline()
	return strings.Join(sink.lines, "\n")
}

func pdfRunTextOperator(sink *pdfTextSink, fonts map[string]*pdfFont, operator string, operands []any) {
	switch operator {
	case "Tf":
		if len(operands) >= 2 {
			if name, ok := operands[len(operands)-2].(pdfName); ok {
				sink.font = fonts[string(name)]
			}
		}
	case "Td", "TD":
		if len(operands) >= 2 {
			moveX := pdfNumberValue(operands[len(operands)-2])
			moveY := pdfNumberValue(operands[len(operands)-1])
			if moveY != 0 {
				sink.newline()
			} else if moveX != 0 {
				sink.space()
			}
			if operator == "TD" {
				sink.leading = -moveY
			}
		}
	case "Tm":
		if len(operands) >= 6 {
			y := pdfNumberValue(operands[len(operands)-1])
			if !sink.haveY || y != sink.lastY {
				sink.newline()
				sink.lastY = y
				sink.haveY = true
			}
		}
	case "T*":
		sink.newline()
	case "Tj":
		if len(operands) >= 1 {
			if text, ok := operands[len(operands)-1].(string); ok {
				sink.write(pdfDecodeFontString(sink.font, text))
			}
		}
	case "TJ":
		if len(operands) >= 1 {
			if array, ok := operands[len(operands)-1].([]any); ok {
				for _, item := range array {
					switch typed := item.(type) {
					case string:
						sink.write(pdfDecodeFontString(sink.font, typed))
					case int:
						if typed < -200 {
							sink.space()
						}
					case float64:
						if typed < -200 {
							sink.space()
						}
					}
				}
			}
		}
	case "'", "\"":
		sink.newline()
		for i := len(operands) - 1; i >= 0; i-- {
			if text, ok := operands[i].(string); ok {
				sink.write(pdfDecodeFontString(sink.font, text))
				break
			}
		}
	}
}

func pdfNumberValue(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case float64:
		return typed
	}
	return 0
}

// --------------------------------------------------------------- fonts ------

type pdfFont struct {
	twoByte   bool
	toUnicode map[uint32]string
}

func (d *pdfDocument) pageFonts(resources map[string]any) map[string]*pdfFont {
	fonts := map[string]*pdfFont{}
	fontDict := d.dict(resources["Font"])
	if fontDict == nil {
		return fonts
	}
	for key, raw := range fontDict {
		font := &pdfFont{}
		dict := d.dict(raw)
		if dict != nil {
			if pdfNameOf(dict["Subtype"]) == "Type0" {
				font.twoByte = true
			}
			if stream, ok := d.resolve(dict["ToUnicode"]).(*pdfStream); ok {
				if decoded, err := d.decodeStream(stream); err == nil {
					mapping, codeBytes := pdfParseCMap(decoded)
					font.toUnicode = mapping
					if codeBytes == 2 {
						font.twoByte = true
					}
				}
			}
		}
		fonts[key] = font
	}
	return fonts
}

// pdfParseCMap reads the bfchar/bfrange sections of a ToUnicode CMap and
// returns the code→unicode map plus the source code width in bytes.
func pdfParseCMap(data []byte) (map[uint32]string, int) {
	mapping := map[uint32]string{}
	codeBytes := 0
	lexer := &pdfLexer{data: data}
	mode := ""
	operands := make([]any, 0, 8)
	flush := func() {
		switch mode {
		case "bfchar":
			for len(operands) >= 2 {
				pdfCMapChar(mapping, operands[0], operands[1], &codeBytes)
				operands = operands[2:]
			}
		case "bfrange":
			for len(operands) >= 3 {
				pdfCMapRange(mapping, operands[0], operands[1], operands[2], &codeBytes)
				operands = operands[3:]
			}
		case "codespace":
			for len(operands) >= 2 {
				if source, ok := operands[0].(string); ok && len(source) > codeBytes {
					codeBytes = len(source)
				}
				operands = operands[2:]
			}
		}
	}
	pos := 0
	for pos < len(data) {
		value, next, err := lexer.parseValue(pos)
		if err != nil {
			break
		}
		pos = next
		if keyword, ok := value.(pdfKeyword); ok {
			switch string(keyword) {
			case "beginbfchar":
				mode, operands = "bfchar", operands[:0]
			case "beginbfrange":
				mode, operands = "bfrange", operands[:0]
			case "begincodespacerange":
				mode, operands = "codespace", operands[:0]
			case "endbfchar", "endbfrange", "endcodespacerange":
				flush()
				mode, operands = "", operands[:0]
			default:
				flush()
				operands = operands[:0]
			}
			continue
		}
		operands = append(operands, value)
		flush()
	}
	return mapping, codeBytes
}

func pdfCMapChar(mapping map[uint32]string, source, destination any, codeBytes *int) {
	sourceText, ok := source.(string)
	if !ok {
		return
	}
	destinationText, ok := destination.(string)
	if !ok {
		return
	}
	if len(sourceText) > *codeBytes {
		*codeBytes = len(sourceText)
	}
	mapping[pdfCodeValue([]byte(sourceText))] = pdfCMapDestination([]byte(destinationText))
}

func pdfCMapRange(mapping map[uint32]string, low, high, destination any, codeBytes *int) {
	lowText, ok := low.(string)
	if !ok {
		return
	}
	highText, ok := high.(string)
	if !ok {
		return
	}
	if len(lowText) > *codeBytes {
		*codeBytes = len(lowText)
	}
	from := pdfCodeValue([]byte(lowText))
	to := pdfCodeValue([]byte(highText))
	if to < from || to-from > 65535 {
		return
	}
	if array, ok := destination.([]any); ok {
		for index, item := range array {
			text, ok := item.(string)
			if !ok {
				break
			}
			mapping[from+uint32(index)] = pdfCMapDestination([]byte(text))
		}
		return
	}
	base, ok := destination.(string)
	if !ok {
		return
	}
	bytes := []byte(base)
	for code := from; code <= to; code++ {
		mapping[code] = pdfCMapDestination(pdfIncrementUTF16(bytes, int(code-from)))
	}
}

func pdfCodeValue(data []byte) uint32 {
	var value uint32
	for _, char := range data {
		value = value<<8 | uint32(char)
	}
	return value
}

func pdfCMapDestination(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if len(data) == 1 {
		return string(pdfWinAnsiRune(data[0]))
	}
	if len(data)%2 == 1 {
		return pdfLatin1String(string(data))
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return string(utf16.Decode(units))
}

// pdfIncrementUTF16 adds delta to the last UTF-16 code unit of base.
func pdfIncrementUTF16(base []byte, delta int) []byte {
	if len(base) < 2 {
		return base
	}
	out := append([]byte(nil), base...)
	last := len(out) - 2
	value := int(out[last])<<8 | int(out[last+1])
	value += delta
	if value > 0xFFFF {
		value = 0xFFFF
	}
	out[last] = byte(value >> 8)
	out[last+1] = byte(value)
	return out
}

func pdfDecodeFontString(font *pdfFont, raw string) string {
	if font == nil {
		return pdfLatin1String(raw)
	}
	var builder strings.Builder
	if font.twoByte {
		for i := 0; i+1 < len(raw); i += 2 {
			code := uint32(raw[i])<<8 | uint32(raw[i+1])
			if text, ok := font.toUnicode[code]; ok {
				builder.WriteString(text)
			} else if code >= 0x20 && code != 0xFFFD {
				builder.WriteRune(rune(code))
			}
		}
		return builder.String()
	}
	for i := 0; i < len(raw); i++ {
		code := uint32(raw[i])
		if text, ok := font.toUnicode[code]; ok {
			builder.WriteString(text)
		} else {
			builder.WriteRune(pdfWinAnsiRune(raw[i]))
		}
	}
	return builder.String()
}

func pdfWinAnsiRune(char byte) rune {
	switch char {
	case 0x80:
		return '€'
	case 0x82:
		return '‚'
	case 0x83:
		return 'ƒ'
	case 0x84:
		return '„'
	case 0x85:
		return '…'
	case 0x86:
		return '†'
	case 0x87:
		return '‡'
	case 0x88:
		return 'ˆ'
	case 0x89:
		return '‰'
	case 0x8A:
		return 'Š'
	case 0x8B:
		return '‹'
	case 0x8C:
		return 'Œ'
	case 0x8E:
		return 'Ž'
	case 0x91:
		return '‘'
	case 0x92:
		return '’'
	case 0x93:
		return '“'
	case 0x94:
		return '”'
	case 0x95:
		return '•'
	case 0x96:
		return '–'
	case 0x97:
		return '—'
	case 0x98:
		return '˜'
	case 0x99:
		return '™'
	case 0x9A:
		return 'š'
	case 0x9B:
		return '›'
	case 0x9C:
		return 'œ'
	case 0x9E:
		return 'ž'
	case 0x9F:
		return 'Ÿ'
	}
	return rune(char)
}

func pdfLatin1String(raw string) string {
	var builder strings.Builder
	for i := 0; i < len(raw); i++ {
		builder.WriteRune(rune(raw[i]))
	}
	return builder.String()
}

// -------------------------------------------------------------- helpers -----

func pdfTextString(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		units := make([]uint16, 0, (len(raw)-2)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(units)), true
	}
	return pdfLatin1String(raw), true
}

func pdfIntValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	}
	return 0
}

func pdfNameOf(value any) string {
	name, _ := value.(pdfName)
	return string(name)
}

func pdfNormalizeText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}
