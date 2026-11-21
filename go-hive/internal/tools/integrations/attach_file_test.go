package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// Smallest valid PDF (one blank page) — same fixture as the upstream tests.
var batch8TinyPDF = mustBase64(
	"JVBERi0xLjQKJeLjz9MKMyAwIG9iago8PC9UeXBlIC9QYWdlCi9QYXJlbnQg" +
		"MSAwIFIKL01lZGlhQm94IFswIDAgNjEyIDc5Ml0KL0NvbnRlbnRzIDQgMCBS" +
		"Ci9SZXNvdXJjZXMgPDwvRm9udCA8PC9GMSAyIDAgUj4+Pj4+PgplbmRvYmoK" +
		"NyAwIG9iago8PC9MZW5ndGggNDQ+PgpzdHJlYW0KQlQKL0YxIDI0IFRmCjEw" +
		"MCA3MDAgVGQKKHRlc3QpIFRqCkVUCmVuZHN0cmVhbQplbmRvYmoKMiAwIG9i" +
		"ago8PC9UeXBlIC9Gb250Ci9TdWJ0eXBlIC9UeXBlMQovQmFzZUZvbnQgL0hl" +
		"bHZldGljYT4+CmVuZG9iagoxIDAgb2JqCjw8L1R5cGUgL1BhZ2VzCi9LaWRz" +
		"IFszIDAgUl0KL0NvdW50IDE+PgplbmRvYmoKNSAwIG9iago8PC9UeXBlIC9D" +
		"YXRhbG9nCi9QYWdlcyAxIDAgUj4+CmVuZG9iagp0cmFpbGVyCjw8L1NpemUg" +
		"NgovUm9vdCA1IDAgUj4+CnN0YXJ0eHJlZgozMjQKJSVFT0Y=")

// Smallest valid 1×1 PNG (8-bit grayscale).
var batch8TinyPNG = mustBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

func mustBase64(value string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

// batch8AttachResult executes attach_file and requires a tools.ToolResult, the
// multi-block shape the tool returns (Content + ImageContent + Attachments).
func batch8AttachResult(t *testing.T, registry *Registry, input map[string]any) tools.ToolResult {
	t.Helper()
	result, err := registry.Execute(context.Background(), "attach_file", input)
	if err != nil {
		t.Fatalf("attach_file: %v", err)
	}
	typed, ok := result.(tools.ToolResult)
	if !ok {
		t.Fatalf("attach_file returned %T, want tools.ToolResult", result)
	}
	return typed
}

func batch8AttachSummary(t *testing.T, content string) map[string]any {
	t.Helper()
	// attach_file appends text preview blocks after the JSON summary, the way
	// upstream returns the summary TextContent followed by the body blocks.
	var payload map[string]any
	if err := json.NewDecoder(strings.NewReader(content)).Decode(&payload); err != nil {
		t.Fatalf("summary is not JSON (%q): %v", content, err)
	}
	return payload
}

// batch8AttachTrailer returns everything after the leading JSON summary — the
// upstream text-preview blocks.
func batch8AttachTrailer(t *testing.T, content string) string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(content))
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("summary is not JSON (%q): %v", content, err)
	}
	return content[decoder.InputOffset():]
}

func batch8AttachedEntries(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["attached"].([]any)
	if !ok {
		t.Fatalf("attached = %#v", payload["attached"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("attached entry = %#v", item)
		}
		out = append(out, entry)
	}
	return out
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_single_pdf
func TestBatch8AttachFileSinglePDF(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdf, batch8TinyPDF, 0o600); err != nil {
		t.Fatal(err)
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": pdf})
	summary := batch8AttachSummary(t, result.Content)
	entries := batch8AttachedEntries(t, summary)
	if len(entries) != 1 {
		t.Fatalf("attached = %v", entries)
	}
	entry := entries[0]
	if entry["kind"] != "pdf" || entry["mime"] != "application/pdf" || entry["filename"] != "doc.pdf" {
		t.Fatalf("entry = %v", entry)
	}
	if entry["bytes"].(float64) != float64(len(batch8TinyPDF)) {
		t.Fatalf("bytes = %v", entry["bytes"])
	}
	// Upstream forwards the pdf bytes as ImageContent; here they travel as the
	// provider-neutral `file` block the OpenAI/Python transports speak. The
	// entry itself keeps upstream's shape — the block is the signal.
	if _, present := entry["inline"]; present {
		t.Fatalf("inline = %v, want the key absent (upstream entry shape)", entry["inline"])
	}
	if len(result.ImageContent) != 1 {
		t.Fatalf("pdf produced %d content blocks, want 1", len(result.ImageContent))
	}
	block := result.ImageContent[0]
	if block["type"] != "file" {
		t.Fatalf("block type = %v, want file", block["type"])
	}
	file, _ := block["file"].(map[string]any)
	fileData, _ := file["file_data"].(string)
	if !strings.HasPrefix(fileData, "data:application/pdf;base64,") {
		t.Fatalf("file_data = %q", fileData)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(fileData, "data:application/pdf;base64,"))
	if err != nil || string(data) != string(batch8TinyPDF) {
		t.Fatalf("pdf bytes mismatch: %v", err)
	}
	if file["filename"] != "doc.pdf" {
		t.Fatalf("filename = %v", file["filename"])
	}
	serializedBlocks, err := json.Marshal(result.ImageContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serializedBlocks), dir) {
		t.Fatalf("content block leaked the source path: %s", serializedBlocks)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Kind != "pdf" {
		t.Fatalf("attachments = %#v", result.Attachments)
	}
}

// Parity-exempt: provider block caps are a Go-side delivery rule upstream
// keeps in channel/transport code; upstream's attach_file suite has no
// equivalent over-cap case.
func TestBatch8AttachFileImageBlockOverCap(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "huge.png")
	file, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(attachFileImageBlockMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": big})
	summary := batch8AttachSummary(t, result.Content)
	entries := batch8AttachedEntries(t, summary)
	if len(entries) != 1 {
		t.Fatalf("attached = %v", entries)
	}
	if _, present := entries[0]["inline"]; present {
		t.Fatalf("inline = %v, want the key absent (upstream entry shape)", entries[0]["inline"])
	}
	inlineError, _ := entries[0]["inline_error"].(string)
	if !strings.Contains(inlineError, "NOT sent to the model") {
		t.Fatalf("inline_error = %q", inlineError)
	}
	if len(result.ImageContent) != 0 {
		t.Fatalf("over-cap image produced %d blocks, want 0", len(result.ImageContent))
	}
	note, _ := summary["note"].(string)
	if !strings.Contains(note, "inline_error") {
		t.Fatalf("note = %q", note)
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("attachments = %#v", result.Attachments)
	}
}

// Parity-exempt: the previous Go build reported "content_blocks"/"note" on the
// success path; upstream sends the blocks themselves and says nothing, so the
// summary must stay {"attached", "errors"} and the block must be on the result.
func TestBatch8AttachFileBlockSuccessKeepsUpstreamShape(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "pic.png")
	_ = os.WriteFile(png, batch8TinyPNG, 0o600)

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": png})
	summary := batch8AttachSummary(t, result.Content)
	if len(result.ImageContent) != 1 {
		t.Fatalf("image blocks = %d, want 1", len(result.ImageContent))
	}
	if _, present := summary["content_blocks"]; present {
		t.Fatalf("content_blocks = %v, want the key absent (upstream summary)", summary["content_blocks"])
	}
	if _, present := summary["note"]; present {
		t.Fatalf("note = %q, want the key absent on the success path", summary["note"])
	}
	entries := batch8AttachedEntries(t, summary)
	if len(entries) != 1 || entries[0]["kind"] != "image" {
		t.Fatalf("attached = %v", entries)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_single_png
func TestBatch8AttachFileSinglePNG(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(png, batch8TinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": png})
	entries := batch8AttachedEntries(t, batch8AttachSummary(t, result.Content))
	if len(entries) != 1 || entries[0]["kind"] != "image" || entries[0]["mime"] != "image/png" {
		t.Fatalf("entries = %v", entries)
	}
	if _, present := entries[0]["inline"]; present {
		t.Fatalf("inline = %v, want the key absent (upstream entry shape)", entries[0]["inline"])
	}
	if len(result.ImageContent) != 1 {
		t.Fatalf("image blocks = %d, want 1", len(result.ImageContent))
	}
	url, _ := result.ImageContent[0]["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image url = %q", url)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
	if err != nil || string(data) != string(batch8TinyPNG) {
		t.Fatalf("image bytes mismatch: %v", err)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_jpg_and_jpeg_both_map_to_image_jpeg
func TestBatch8AttachFileJPGExtensions(t *testing.T) {
	dir := t.TempDir()
	registry := newBatch8FinalRegistry()
	for _, ext := range []string{".jpg", ".jpeg"} {
		path := filepath.Join(dir, "pic"+ext)
		if err := os.WriteFile(path, []byte("\xff\xd8\xff"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries := batch8AttachedEntries(t, batch8AttachSummary(t,
			batch8AttachResult(t, registry, map[string]any{"paths": path}).Content))
		if entries[0]["mime"] != "image/jpeg" {
			t.Fatalf("%s mime = %v", ext, entries[0]["mime"])
		}
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_multiple_files
func TestBatch8AttachFileMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "a.pdf")
	png := filepath.Join(dir, "b.png")
	webp := filepath.Join(dir, "c.webp")
	_ = os.WriteFile(pdf, batch8TinyPDF, 0o600)
	_ = os.WriteFile(png, batch8TinyPNG, 0o600)
	_ = os.WriteFile(webp, []byte("RIFF\x00\x00\x00\x00WEBP"), 0o600)

	result := batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": []any{pdf, png, webp}})
	entries := batch8AttachedEntries(t, batch8AttachSummary(t, result.Content))
	if len(entries) != 3 {
		t.Fatalf("attached = %d, want 3", len(entries))
	}
	// Upstream emits 3 blocks (it forwards pdf bytes as ImageContent); here the
	// pdf rides as a `file` block and the two images as image_url blocks.
	if len(result.ImageContent) != 3 {
		t.Fatalf("content blocks = %d, want 3", len(result.ImageContent))
	}
	if result.ImageContent[0]["type"] != "file" {
		t.Fatalf("first block = %#v, want the pdf file block", result.ImageContent[0])
	}
	for _, block := range result.ImageContent[1:] {
		if block["type"] != "image_url" {
			t.Fatalf("image block = %#v", block)
		}
	}
	if len(result.Attachments) != 3 {
		t.Fatalf("attachments = %d, want 3", len(result.Attachments))
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_partial_failure_keeps_valid
func TestBatch8AttachFilePartialFailureKeepsValid(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.pdf")
	_ = os.WriteFile(good, batch8TinyPDF, 0o600)
	missing := filepath.Join(dir, "missing.pdf")

	result := batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": []string{good, missing}})
	summary := batch8AttachSummary(t, result.Content)
	entries := batch8AttachedEntries(t, summary)
	if len(entries) != 1 || entries[0]["filename"] != "good.pdf" {
		t.Fatalf("attached = %v", entries)
	}
	problems, _ := summary["errors"].([]any)
	if len(problems) != 1 {
		t.Fatalf("errors = %v", summary["errors"])
	}
	if problems[0].(map[string]any)["path"] != missing {
		t.Fatalf("error path = %v", problems[0])
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_too_many_paths_hard_fails
func TestBatch8AttachFileTooManyPaths(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "x.pdf")
	_ = os.WriteFile(pdf, batch8TinyPDF, 0o600)
	paths := make([]string, 11)
	for index := range paths {
		paths[index] = pdf
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": paths})
	summary := batch8AttachSummary(t, result.Content)
	if entries := batch8AttachedEntries(t, summary); len(entries) != 0 {
		t.Fatalf("attached = %v, want none", entries)
	}
	problems, _ := summary["errors"].([]any)
	message := problems[0].(map[string]any)["error"].(string)
	if !strings.Contains(message, "Too many paths") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_all_paths_invalid_returns_summary_only
func TestBatch8AttachFileAllPathsInvalid(t *testing.T) {
	dir := t.TempDir()
	result := batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": []string{filepath.Join(dir, "a.pdf"), filepath.Join(dir, "b.png")}})
	summary := batch8AttachSummary(t, result.Content)
	if entries := batch8AttachedEntries(t, summary); len(entries) != 0 {
		t.Fatalf("attached = %v", entries)
	}
	problems, _ := summary["errors"].([]any)
	if len(problems) != 2 {
		t.Fatalf("errors = %v", summary["errors"])
	}
	if len(result.ImageContent) != 0 || len(result.Attachments) != 0 {
		t.Fatalf("unexpected blocks: %d images, %d refs", len(result.ImageContent), len(result.Attachments))
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_hive_storage_path_relative
func TestBatch8AttachFileStoragePathRelative(t *testing.T) {
	dir := t.TempDir()
	attachmentsDir := filepath.Join(dir, "data", "attachments")
	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(attachmentsDir, "from_chat.pdf")
	_ = os.WriteFile(pdf, batch8TinyPDF, 0o600)
	t.Setenv("HIVE_STORAGE_PATH", dir)

	entries := batch8AttachedEntries(t, batch8AttachSummary(t,
		batch8AttachResult(t, newBatch8FinalRegistry(),
			map[string]any{"paths": "data/attachments/from_chat.pdf"}).Content))
	if len(entries) != 1 || entries[0]["filename"] != "from_chat.pdf" {
		t.Fatalf("entries = %v", entries)
	}
	resolvedAbs, _ := filepath.Abs(pdf)
	if entries[0]["resolved"] != resolvedAbs {
		t.Fatalf("resolved = %v, want %s", entries[0]["resolved"], resolvedAbs)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_bare_name_resolves_in_the_session_working_directory
func TestBatch8AttachFileSessionCwd(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "session")
	if err := os.MkdirAll(session, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, "report.csv"), []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_STORAGE_PATH", "")

	entries := batch8AttachedEntries(t, batch8AttachSummary(t,
		batch8AttachResult(t, newBatch8FinalRegistry(),
			map[string]any{"paths": "report.csv", "session_cwd": session}).Content))
	want, _ := filepath.Abs(filepath.Join(session, "report.csv"))
	if len(entries) != 1 || entries[0]["resolved"] != want {
		t.Fatalf("entries = %v, want resolved %s", entries, want)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_absolute_path_always_wins
func TestBatch8AttachFileAbsolutePathWins(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "absolute.pdf")
	_ = os.WriteFile(pdf, batch8TinyPDF, 0o600)
	t.Setenv("HIVE_STORAGE_PATH", "/nonexistent/path")

	entries := batch8AttachedEntries(t, batch8AttachSummary(t,
		batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": pdf}).Content))
	want, _ := filepath.Abs(pdf)
	if len(entries) != 1 || entries[0]["resolved"] != want {
		t.Fatalf("entries = %v, want %s", entries, want)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_oversize_file_rejected
func TestBatch8AttachFileOversizeRejected(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "huge.pdf")
	if err := os.WriteFile(big, append([]byte("%PDF-1.4\n"), make([]byte, 3*1024*1024)...), 0o600); err != nil {
		t.Fatal(err)
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": big, "max_bytes_mb": 1.0})
	summary := batch8AttachSummary(t, result.Content)
	if entries := batch8AttachedEntries(t, summary); len(entries) != 0 {
		t.Fatalf("attached = %v", entries)
	}
	problems, _ := summary["errors"].([]any)
	message := problems[0].(map[string]any)["error"].(string)
	if !strings.Contains(message, "max_bytes") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_unknown_binary_extension_accepted_as_blob
func TestBatch8AttachFileUnknownBinaryIsBlob(t *testing.T) {
	dir := t.TempDir()
	weird := filepath.Join(dir, "file.bin")
	_ = os.WriteFile(weird, []byte{0, 1, 2}, 0o600)

	entries := batch8AttachedEntries(t, batch8AttachSummary(t,
		batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": weird}).Content))
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	if entries[0]["kind"] != "blob" || entries[0]["mime"] != "application/octet-stream" ||
		entries[0]["filename"] != "file.bin" {
		t.Fatalf("entry = %v", entries[0])
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_unknown_text_extension_accepted_as_text
func TestBatch8AttachFileUnknownTextIsText(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "Dockerfile")
	_ = os.WriteFile(plain, []byte("FROM python:3.12\nRUN echo hi\n"), 0o600)

	content := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": plain}).Content
	entries := batch8AttachedEntries(t, batch8AttachSummary(t, content))
	if len(entries) != 1 || entries[0]["kind"] != "text" || entries[0]["mime"] != "text/plain" {
		t.Fatalf("entry = %v", entries)
	}
	// Upstream ships the body as a TextContent block after the summary.
	trailer := batch8AttachTrailer(t, content)
	if !strings.Contains(trailer, "[attached Dockerfile (text/plain)]") || !strings.Contains(trailer, "FROM python") {
		t.Fatalf("trailer = %q", trailer)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_yaml_inlined_as_text
func TestBatch8AttachFileYAMLText(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	_ = os.WriteFile(spec, []byte("openapi: 3.0.0\ninfo:\n  title: Test\n"), 0o600)

	entries := batch8AttachedEntries(t, batch8AttachSummary(t,
		batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": spec}).Content))
	if len(entries) != 1 || entries[0]["kind"] != "text" || entries[0]["mime"] != "application/yaml" {
		t.Fatalf("entry = %v", entries)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_directory_rejected
func TestBatch8AttachFileDirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	summary := batch8AttachSummary(t, batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": dir}).Content)
	problems, _ := summary["errors"].([]any)
	if len(problems) != 1 || problems[0].(map[string]any)["error"] != "file not found" {
		t.Fatalf("errors = %v", summary["errors"])
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_paths_must_be_string_or_list
func TestBatch8AttachFilePathsShape(t *testing.T) {
	summary := batch8AttachSummary(t, batch8AttachResult(t, newBatch8FinalRegistry(),
		map[string]any{"paths": 123}).Content)
	problems, _ := summary["errors"].([]any)
	if len(problems) != 1 {
		t.Fatalf("errors = %v", summary["errors"])
	}
	message := problems[0].(map[string]any)["error"].(string)
	if !strings.Contains(message, "paths must be") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_attach_file_tool.py::test_large_text_inline_is_capped_so_chip_survives
func TestBatch8AttachFileTextPreviewCapped(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "engagers_export.csv")
	if err := os.WriteFile(big, []byte("col_a,col_b\n"+strings.Repeat("x,y\n", 30_000)), 0o600); err != nil {
		t.Fatal(err)
	}

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": big})
	if len(result.Content) >= 30_000 {
		t.Fatalf("result is %d chars — it would be spilled and break the chip", len(result.Content))
	}
	entries := batch8AttachedEntries(t, batch8AttachSummary(t, result.Content))
	if len(entries) != 1 || entries[0]["kind"] != "text" {
		t.Fatalf("entry = %v", entries)
	}
	preview := batch8AttachTrailer(t, result.Content)
	if !strings.Contains(preview, "preview truncated") {
		t.Fatalf("preview is not truncated: %d chars", len(preview))
	}
	if len(preview) > attachFileTextInlineCapBytes+256 {
		t.Fatalf("preview is %d bytes, cap is %d", len(preview), attachFileTextInlineCapBytes)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_publishes_chip_and_injects_url
func TestBatch8PublishAttachFileCopiesAndInjectsURL(t *testing.T) {
	source := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(source, []byte("%PDF-1.4\nstub\n%%EOF"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(t.TempDir(), "session")

	input := map[string]any{"paths": source}
	registry := newBatch8FinalRegistry()
	result := batch8AttachResult(t, registry, input)
	content, isError := PublishAttachFileResult(result.Content, result.Attachments, sessionDir)
	if isError {
		t.Fatalf("publish reported an error: %s", content)
	}
	payload := batch8AttachSummary(t, content)
	entries := batch8AttachedEntries(t, payload)
	url, _ := entries[0]["hive_attachment_url"].(string)
	if url != "hive-attachment://data/attachments/report.pdf" {
		t.Fatalf("url = %q", url)
	}
	copied := filepath.Join(sessionDir, "data", "attachments", "report.pdf")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if string(data) != "%PDF-1.4\nstub\n%%EOF" {
		t.Fatalf("copied bytes = %q", data)
	}
	resolved, _ := entries[0]["resolved"].(string)
	if resolved != copied {
		t.Fatalf("resolved = %q, want the copy", resolved)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_none_conversation_store_returns_error_result
func TestBatch8PublishAttachFileWithoutSessionFailsLoudly(t *testing.T) {
	source := filepath.Join(t.TempDir(), "report.pdf")
	_ = os.WriteFile(source, []byte("%PDF-1.4\n%%EOF"), 0o600)

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": source})
	content, isError := PublishAttachFileResult(result.Content, result.Attachments, "")
	if !isError {
		t.Fatalf("publish without a session dir claimed success: %s", content)
	}
	payload := batch8AttachSummary(t, content)
	if entries := batch8AttachedEntries(t, payload); len(entries) != 0 {
		t.Fatalf("attached = %v, want none", entries)
	}
	problems, _ := payload["errors"].([]any)
	message := problems[0].(map[string]any)["error"].(string)
	if !strings.Contains(message, "NOT delivered") {
		t.Fatalf("error does not state the failure: %q", message)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_unparseable_content_returns_error_result
func TestBatch8PublishAttachFileUnparseableContent(t *testing.T) {
	refs := []tools.AttachmentRef{{Resolved: "/tmp/x.pdf", Filename: "x.pdf", Kind: "pdf"}}
	content, isError := PublishAttachFileResult("not json at all", refs, t.TempDir())
	if !isError {
		t.Fatalf("unparseable content claimed success: %s", content)
	}
	if !strings.Contains(content, "NOT delivered") {
		t.Fatalf("content = %s", content)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_empty_attached_passes_through_unchanged
func TestBatch8PublishAttachFileNoRefsPassesThrough(t *testing.T) {
	content := `{"attached": [], "errors": []}`
	out, isError := PublishAttachFileResult(content, nil, t.TempDir())
	if isError || out != content {
		t.Fatalf("passthrough = (%q, %v)", out, isError)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_source_vanished_returns_error_result
func TestBatch8PublishAttachFileSourceVanished(t *testing.T) {
	source := filepath.Join(t.TempDir(), "gone.pdf")
	_ = os.WriteFile(source, []byte("%PDF-1.4\n%%EOF"), 0o600)
	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": source})
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	content, isError := PublishAttachFileResult(result.Content, result.Attachments, t.TempDir())
	if !isError || !strings.Contains(content, "NOT delivered") {
		t.Fatalf("vanished source = (%q, %v)", content, isError)
	}
}

// Parity: core/tests/test_attach_file_publish.py::test_multiple_files_all_published
func TestBatch8PublishAttachFileMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "a.pdf")
	note := filepath.Join(dir, "note.md")
	_ = os.WriteFile(pdf, batch8TinyPDF, 0o600)
	_ = os.WriteFile(note, []byte("hi there"), 0o600)
	sessionDir := filepath.Join(t.TempDir(), "session")

	result := batch8AttachResult(t, newBatch8FinalRegistry(), map[string]any{"paths": []string{pdf, note}})
	content, isError := PublishAttachFileResult(result.Content, result.Attachments, sessionDir)
	if isError {
		t.Fatalf("publish failed: %s", content)
	}
	entries := batch8AttachedEntries(t, batch8AttachSummary(t, content))
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if url, _ := entry["hive_attachment_url"].(string); !strings.HasPrefix(url, "hive-attachment://data/attachments/") {
			t.Fatalf("entry url = %q", url)
		}
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "data", "attachments", "note.md")); err != nil {
		t.Fatalf("note copy: %v", err)
	}
}

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise the MCP tool function, not the registry surface.
func TestBatch8AttachFileCatalogIntegrity(t *testing.T) {
	registry := newBatch8FinalRegistry()
	if got := len(registry.Names()); got != 3 {
		t.Fatalf("tools = %d, want 3", got)
	}
	if got := registry.ActionCount(); got != 3 {
		t.Fatalf("actions = %d, want 3", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	if _, ok := registry.Lookup("attach_file"); !ok {
		t.Fatal("attach_file missing")
	}
}
