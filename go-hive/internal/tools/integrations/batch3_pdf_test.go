package integrations

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// batch3RewriteTransport keeps the logical host (example.com) while routing
// the actual connection at an httptest server, so URL-based tools can be
// exercised without DNS or real network access.
type batch3RewriteTransport struct {
	target string
}

func (t *batch3RewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	clone.Host = target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// batch3TestRegistry builds the batch-3 registry with a public-result DNS
// stub and, when serverURL is set, a transport that reaches the fake server.
func batch3TestRegistry(t *testing.T, serverURL string, opts ...batch3Option) *Registry {
	t.Helper()
	options := []batch3Option{withBatch3Resolver(func(ctx context.Context, host string) ([]string, error) {
		return []string{"93.184.216.34"}, nil
	})}
	options = append(options, opts...)
	registry := newBatch3Registry(options...)
	if serverURL != "" {
		registry.Runtime().HTTP = newHTTPClient(&http.Client{Transport: &batch3RewriteTransport{target: serverURL}})
	}
	return registry
}

func batch3Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

// ---------------------------------------------------------------- fixture ---

// batch3PDFFixture builds a real PDF 1.4 file: classic xref table, trailer,
// one Helvetica font object, FlateDecode content streams and optional Info /
// Encrypt dictionaries. An empty page text produces an image-only (scanned)
// page with no text operators.
func batch3PDFFixture(t *testing.T, pageTexts []string, metadata map[string]string, encrypted bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	maxObject := 0
	addObject := func(number int, body []byte) {
		offsets[number] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n", number)
		buffer.Write(body)
		buffer.WriteString("\nendobj\n")
		if number > maxObject {
			maxObject = number
		}
	}

	pageCount := len(pageTexts)
	fontObject := 3 + 2*pageCount
	infoObject := fontObject + 1
	encryptObject := infoObject + 1

	addObject(1, []byte("<< /Type /Catalog /Pages 2 0 R >>"))
	kids := make([]string, 0, pageCount)
	for i := 0; i < pageCount; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+i))
	}
	addObject(2, []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pageCount)))
	for i, text := range pageTexts {
		pageNumber := 3 + i
		contentNumber := 3 + pageCount + i
		content := batch3PDFContentStream(text)
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatalf("compress: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("compress close: %v", err)
		}
		addObject(contentNumber, []byte(fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", compressed.Len(), compressed.String())))
		addObject(pageNumber, []byte(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontObject, contentNumber)))
	}
	addObject(fontObject, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"))

	trailerExtra := ""
	if len(metadata) > 0 {
		var info strings.Builder
		info.WriteString("<< ")
		for key, value := range metadata {
			fmt.Fprintf(&info, "/%s (%s) ", key, batch3PDFEscape(value))
		}
		info.WriteString(">>")
		addObject(infoObject, []byte(info.String()))
		trailerExtra += fmt.Sprintf(" /Info %d 0 R", infoObject)
	}
	if encrypted {
		addObject(encryptObject, []byte("<< /Filter /Standard /V 1 /R 2 /O <00112233445566778899AABBCCDDEEFF> /U <00112233445566778899AABBCCDDEEFF> /P -4 >>"))
		trailerExtra += fmt.Sprintf(" /Encrypt %d 0 R", encryptObject)
	}

	xrefOffset := buffer.Len()
	fmt.Fprintf(&buffer, "xref\n0 %d\n", maxObject+1)
	buffer.WriteString("0000000000 65535 f \n")
	for number := 1; number <= maxObject; number++ {
		offset, ok := offsets[number]
		if !ok {
			buffer.WriteString("0000000000 00000 f \n")
			continue
		}
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		maxObject+1, trailerExtra, xrefOffset)
	return buffer.Bytes()
}

func batch3PDFContentStream(text string) string {
	if text == "" {
		return "q 100 0 0 100 0 0 cm /Im1 Do Q"
	}
	var builder strings.Builder
	builder.WriteString("BT /F1 12 Tf 72 720 Td ")
	for index, line := range strings.Split(text, "\n") {
		if index > 0 {
			builder.WriteString("0 -14 Td ")
		}
		builder.WriteString("(" + batch3PDFEscape(line) + ") Tj ")
	}
	builder.WriteString("ET")
	return builder.String()
}

func batch3PDFEscape(text string) string {
	return strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
}

func batch3WritePDFFixture(t *testing.T, pageTexts []string, metadata map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(path, batch3PDFFixture(t, pageTexts, metadata, false), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// ------------------------------------------------------------------ tests ---

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_pages_array_shape
func TestBatch3PDFReadLocalTextPDF(t *testing.T) {
	path := batch3WritePDFFixture(t,
		[]string{"First page text", "Second page text"},
		map[string]string{"Title": "Fixture Title", "Author": "Fixture Author"})
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if result["total_pages"] != 2 || result["pages_extracted"] != 2 {
		t.Fatalf("page counts = %v", result)
	}
	content := result["content"].(string)
	if !strings.Contains(content, "--- Page 1 ---\nFirst page text") {
		t.Fatalf("content = %q", content)
	}
	if !strings.Contains(content, "--- Page 2 ---\nSecond page text") {
		t.Fatalf("content = %q", content)
	}
	if result["char_count"] != len(content) {
		t.Fatalf("char_count = %v len = %d", result["char_count"], len(content))
	}
	pages, _ := result["pages"].([]map[string]any)
	if len(pages) != 2 || pages[1]["text"] != "Second page text" {
		t.Fatalf("pages = %v", result["pages"])
	}
	if pages[0]["needs_vision"] != true {
		t.Fatalf("short page should be flagged needs_vision: %v", pages[0])
	}
	metadata, _ := result["metadata"].(map[string]any)
	if metadata["title"] != "Fixture Title" || metadata["author"] != "Fixture Author" {
		t.Fatalf("metadata = %v", result["metadata"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_truncation_flag_for_page_range
func TestBatch3PDFReadPageRangeTruncation(t *testing.T) {
	path := batch3WritePDFFixture(t, []string{"p1", "p2", "p3", "p4", "p5"}, nil)
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{
		"file_path": path,
		"pages":     "1-4",
		"max_pages": 2,
	})
	if result["truncated"] != true {
		t.Fatalf("truncated = %v", result["truncated"])
	}
	if result["pages_extracted"] != 2 {
		t.Fatalf("pages_extracted = %v", result["pages_extracted"])
	}
	if _, ok := result["truncation_warning"]; !ok {
		t.Fatalf("missing truncation_warning: %v", result)
	}
	pages := result["pages"].([]map[string]any)
	if pages[0]["number"] != 1 || pages[1]["number"] != 2 {
		t.Fatalf("pages = %v", pages)
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_pages_parameter_accepted
func TestBatch3PDFReadPageSpecForms(t *testing.T) {
	path := batch3WritePDFFixture(t, []string{"a", "b", "c", "d", "e"}, nil)
	registry := batch3TestRegistry(t, "")
	cases := map[string]int{"all": 5, "2": 1, "2-4": 3, "1,3,5": 3}
	for spec, want := range cases {
		result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path, "pages": spec})
		if _, failed := result["error"]; failed {
			t.Fatalf("pages=%q errored: %v", spec, result)
		}
		if result["pages_extracted"] != want {
			t.Fatalf("pages=%q extracted = %v, want %d", spec, result["pages_extracted"], want)
		}
	}
	outOfRange := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path, "pages": "9"})
	if !strings.Contains(fmt.Sprint(outOfRange["error"]), "out of range") {
		t.Fatalf("error = %v", outOfRange["error"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_no_needs_vision_pages_key_when_all_have_text
func TestBatch3PDFReadLongPageNoVisionFlag(t *testing.T) {
	long := strings.Repeat("lorem ipsum ", 50)
	path := batch3WritePDFFixture(t, []string{long}, nil)
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if _, present := result["needs_vision_pages"]; present {
		t.Fatalf("unexpected needs_vision_pages: %v", result["needs_vision_pages"])
	}
	if pages := result["pages"].([]map[string]any); pages[0]["needs_vision"] != false {
		t.Fatalf("needs_vision = %v", pages[0]["needs_vision"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadFailFast::test_scanned_pdf_response_includes_terminal_hint
// The Go port is stricter than upstream: an all-empty document is an explicit
// error, while still carrying needs_vision_pages / next_step / terminal_hint.
func TestBatch3PDFReadScannedReturnsExplicitError(t *testing.T) {
	path := batch3WritePDFFixture(t, []string{"", ""}, nil)
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path})
	message, _ := result["error"].(string)
	if !strings.Contains(strings.ToLower(message), "scanned") {
		t.Fatalf("error = %v", result["error"])
	}
	if result["needs_vision"] != true {
		t.Fatalf("needs_vision = %v", result["needs_vision"])
	}
	pages, _ := result["needs_vision_pages"].([]int)
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Fatalf("needs_vision_pages = %v", result["needs_vision_pages"])
	}
	if !strings.Contains(fmt.Sprint(result["next_step"]), "vision") {
		t.Fatalf("next_step = %v", result["next_step"])
	}
	if !strings.Contains(fmt.Sprint(result["terminal_hint"]), "pdftotext") {
		t.Fatalf("terminal_hint = %v", result["terminal_hint"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadFailFast::test_encrypted_pdf_yields_with_legacy_error_message
func TestBatch3PDFReadEncryptedReturnsExplicitError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.pdf")
	if err := os.WriteFile(path, batch3PDFFixture(t, []string{"secret"}, nil, true), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path})
	message, _ := result["error"].(string)
	if !strings.Contains(strings.ToLower(message), "encrypted") {
		t.Fatalf("error = %v", result["error"])
	}
	if !strings.Contains(fmt.Sprint(result["next_step"]), "qpdf") {
		t.Fatalf("next_step = %v", result["next_step"])
	}
	if !strings.Contains(fmt.Sprint(result["terminal_hint"]), "pdftotext") {
		t.Fatalf("terminal_hint = %v", result["terminal_hint"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_read_pdf_file_not_found, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_read_pdf_invalid_extension, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_read_pdf_directory
func TestBatch3PDFReadLocalPathErrors(t *testing.T) {
	registry := batch3TestRegistry(t, "")
	dir := t.TempDir()

	missing := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": filepath.Join(dir, "missing.pdf")})
	if !strings.Contains(strings.ToLower(fmt.Sprint(missing["error"])), "not found") {
		t.Fatalf("missing = %v", missing)
	}

	textFile := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textFile, []byte("not a pdf"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	extension := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": textFile})
	if !strings.Contains(strings.ToLower(fmt.Sprint(extension["error"])), "not a pdf") {
		t.Fatalf("extension = %v", extension)
	}

	directory := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": dir})
	if !strings.Contains(strings.ToLower(fmt.Sprint(directory["error"])), "not a file") {
		t.Fatalf("directory = %v", directory)
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_neither_file_path_nor_files_errors, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_both_file_path_and_files_errors, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadMultiFile::test_files_too_many_errors
func TestBatch3PDFReadArgumentValidation(t *testing.T) {
	registry := batch3TestRegistry(t, "")
	path := batch3WritePDFFixture(t, []string{"text"}, nil)

	neither := batch3Execute(t, registry, "pdf_read", map[string]any{})
	if !strings.Contains(fmt.Sprint(neither["error"]), "file_path") {
		t.Fatalf("neither = %v", neither)
	}

	both := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path, "files": []string{path}})
	if both["error"] == nil {
		t.Fatalf("both = %v", both)
	}

	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = path
	}
	excess := batch3Execute(t, registry, "pdf_read", map[string]any{"files": tooMany})
	if !strings.Contains(fmt.Sprint(excess["error"]), "Too many") {
		t.Fatalf("excess = %v", excess)
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadMultiFile::test_files_returns_pdfs_array
func TestBatch3PDFReadMultipleFiles(t *testing.T) {
	first := batch3WritePDFFixture(t, []string{"alpha"}, nil)
	second := batch3WritePDFFixture(t, []string{"beta"}, nil)
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"files": []string{first, second}})
	pdfs, ok := result["pdfs"].([]any)
	if !ok || len(pdfs) != 2 {
		t.Fatalf("pdfs = %v", result["pdfs"])
	}
	entry := pdfs[1].(map[string]any)
	if !strings.Contains(entry["content"].(string), "beta") {
		t.Fatalf("second entry = %v", entry)
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_url_download_succeeds
func TestBatch3PDFReadURLDownload(t *testing.T) {
	fixture := batch3PDFFixture(t, []string{"Remote page text"}, nil, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/doc.pdf" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": "https://example.com/doc.pdf"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if !strings.Contains(result["content"].(string), "Remote page text") {
		t.Fatalf("content = %q", result["content"])
	}
	if result["path"] != "https://example.com/doc.pdf" {
		t.Fatalf("path = %v", result["path"])
	}
	if result["name"] != "doc.pdf" {
		t.Fatalf("name = %v", result["name"])
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_url_non_pdf_content_type, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_url_http_404_error
func TestBatch3PDFReadURLRejectsNonPDFAndHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wrong-type":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"not":"a pdf"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server.URL)

	wrongType := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": "https://example.com/wrong-type"})
	if !strings.Contains(fmt.Sprint(wrongType["error"]), "Content-Type") {
		t.Fatalf("wrong type = %v", wrongType)
	}

	notFound := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": "https://example.com/missing"})
	if !strings.Contains(fmt.Sprint(notFound["error"]), "HTTP 404") {
		t.Fatalf("404 = %v", notFound)
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_ssrf_localhost_rejected, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_ssrf_link_local_rejected, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadUrlSupport::test_ssrf_rfc1918_rejected
func TestBatch3PDFReadURLRejectsPrivateHosts(t *testing.T) {
	registry := batch3TestRegistry(t, "")
	for _, target := range []string{
		"http://127.0.0.1/doc.pdf",
		"http://169.254.169.254/doc.pdf",
		"http://192.168.1.10/doc.pdf",
		"http://10.0.0.5/doc.pdf",
	} {
		result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": target})
		if !strings.Contains(strings.ToLower(fmt.Sprint(result["error"])), "refused") {
			t.Fatalf("%s -> %v", target, result)
		}
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_max_pages_clamped_low, tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_max_pages_clamped_high
func TestBatch3PDFReadMaxPagesClamped(t *testing.T) {
	path := batch3WritePDFFixture(t, []string{"one", "two", "three"}, nil)
	registry := batch3TestRegistry(t, "")

	low := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path, "max_pages": 0})
	if low["pages_extracted"] != 1 {
		t.Fatalf("low = %v", low)
	}
	high := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path, "max_pages": 2000})
	if high["pages_extracted"] != 3 {
		t.Fatalf("high = %v", high)
	}
}

// Parity-exempt: the Go port's xref-less fallback has no upstream equivalent
// (pdfplumber handles broken xref internally).
func TestBatch3PDFReadFallsBackToObjectScan(t *testing.T) {
	fixture := batch3PDFFixture(t, []string{"Recovered by scanning"}, nil, false)
	cut := bytes.Index(fixture, []byte("\nxref"))
	if cut < 0 {
		t.Fatal("fixture missing xref")
	}
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, fixture[:cut], 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	registry := batch3TestRegistry(t, "")

	result := batch3Execute(t, registry, "pdf_read", map[string]any{"file_path": path})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if !strings.Contains(result["content"].(string), "Recovered by scanning") {
		t.Fatalf("content = %q", result["content"])
	}
}

// Parity-exempt: installation and schema invariants are Go-side contracts.
func TestInstallBatch3Doc(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch3Doc(shared); installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	for _, name := range []string{"pdf_read", "web_scrape"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
		tool, _ := shared.GetTool(name)
		properties, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s parameters = %v", name, tool.Parameters)
		}
		if _, present := properties["action"]; present {
			t.Fatalf("%s is single-action and must not expose action selector", name)
		}
	}
	pdf, _ := shared.GetTool("pdf_read")
	properties := pdf.Parameters["properties"].(map[string]any)
	for _, required := range []string{"file_path", "files", "pages", "max_pages", "include_metadata"} {
		if _, present := properties[required]; !present {
			t.Fatalf("pdf_read missing parameter %q: %v", required, properties)
		}
	}
	scrape, _ := shared.GetTool("web_scrape")
	scrapeProps := scrape.Parameters["properties"].(map[string]any)
	if _, present := scrapeProps["selector"]; !present {
		t.Fatalf("web_scrape missing selector: %v", scrapeProps)
	}
}
