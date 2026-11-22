package server

// Tests for the chat images/display_message attachment input path.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// onePixelPNG is a valid 1×1 PNG, so the dimension probe and the saved-bytes
// assertions exercise real image content rather than a placeholder URI.
const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	return data
}

// chatImageRuntime wires a chat runtime and its event bus for the attachment
// input tests.
func chatImageRuntime(t *testing.T, model llm.Completer, root string) (*ChatRuntime, *eventbus.EventBus, *chatEventRecorder) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      root,
		Bus:       bus,
		SessionID: "session_20261122_000000_chatimg",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, bus, recordChatEvents(bus)
}

func newChatImageSession(id string, runtime *ChatRuntime, bus *eventbus.EventBus, queenDir string) *Session {
	session := NewSession(id, bus)
	session.Chat = runtime
	session.QueenDir = queenDir
	return session
}

func writeChatAttachment(t *testing.T, queenDir, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(queenDir, "data", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir attachments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatalf("write attachment %s: %v", name, err)
	}
	return "data/attachments/" + name
}

func attachmentRef(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func findContentBlock(blocks []map[string]any, blockType string) map[string]any {
	for _, block := range blocks {
		if block["type"] == blockType {
			return block
		}
	}
	return nil
}

// Parity: framework/server/routes_execution.py::handle_chat
func TestChatRouteDeliversImageDataURI(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)
	model := &attachmentChatLLM{model: "openai/gpt-5.4"}
	runtime, bus, recorder := chatImageRuntime(t, model, t.TempDir())
	queenDir := t.TempDir()
	session := newChatImageSession("session_20261122_000000_imgdata0", runtime, bus, queenDir)
	manager.AddSession(session)

	png := onePixelPNG(t)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	status, body := postChat(t, server.URL, session.ID, map[string]any{
		"message": "what does this screenshot show?",
		"images":  []map[string]any{attachmentRef(dataURL)},
	})
	if status != 200 || body["delivered"] != true || body["status"] != "queen" {
		t.Fatalf("status = %d body = %#v, want delivered queen", status, body)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	imageBlock := findContentBlock(requestContentParts(model.lastRequest(t)), "image_url")
	if imageBlock == nil {
		t.Fatalf("model request carried no image block: %#v", requestContentParts(model.lastRequest(t)))
	}
	if got, _ := imageBlock["image_url"].(map[string]any)["url"].(string); got != dataURL {
		t.Fatalf("image url = %q, want the original data URI", got)
	}

	// The bytes were persisted under the session's data/attachments with
	// owner-only permissions, and the transcript message names the path.
	entries, err := os.ReadDir(filepath.Join(queenDir, "data", "attachments"))
	if err != nil {
		t.Fatalf("read attachments dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "_0.png") {
		t.Fatalf("attachments = %v, want one <ts>_0.png", entries)
	}
	savedPath := filepath.Join(queenDir, "data", "attachments", entries[0].Name())
	info, err := os.Stat(savedPath)
	if err != nil {
		t.Fatalf("stat saved attachment: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode = %o, want 600", info.Mode().Perm())
	}
	savedBytes, err := os.ReadFile(savedPath)
	if err != nil || !bytes.Equal(savedBytes, png) {
		t.Fatalf("saved bytes differ from the request image (err=%v)", err)
	}

	requested := recorder.ofType(eventbus.EventClientInputReceived)
	if len(requested) != 1 {
		t.Fatalf("client_input_received events = %d, want 1", len(requested))
	}
	if requested[0].Data["image_count"] != 1 {
		t.Fatalf("image_count = %#v, want 1", requested[0].Data["image_count"])
	}
	paths, _ := requested[0].Data["image_paths"].([]string)
	if len(paths) != 1 || paths[0] != "data/attachments/"+entries[0].Name() {
		t.Fatalf("image_paths = %#v", requested[0].Data["image_paths"])
	}
	userText := ""
	for _, message := range model.lastRequest(t).Messages {
		if message.Role == "user" {
			userText = message.Content
		}
	}
	for _, want := range []string{
		"[Attachments saved to disk",
		"Image: data/attachments/" + entries[0].Name(),
		"1×1, image/png",
	} {
		if !strings.Contains(userText, want) {
			t.Fatalf("user message missing %q:\n%s", want, userText)
		}
	}
}

// Parity: framework/server/routes_execution.py::handle_chat
func TestChatRouteResolvesHiveAttachmentRef(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)
	model := &attachmentChatLLM{model: "openai/gpt-5.4"}
	runtime, bus, recorder := chatImageRuntime(t, model, t.TempDir())
	queenDir := t.TempDir()
	session := newChatImageSession("session_20261122_000000_imgref00", runtime, bus, queenDir)
	manager.AddSession(session)

	png := onePixelPNG(t)
	rel := writeChatAttachment(t, queenDir, "screenshot.png", png)
	status, body := postChat(t, server.URL, session.ID, map[string]any{
		"message":         "read the uploaded image",
		"display_message": "📷 screenshot",
		"images":          []map[string]any{attachmentRef("hive-attachment://" + rel)},
	})
	if status != 200 || body["delivered"] != true {
		t.Fatalf("status = %d body = %#v", status, body)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	imageBlock := findContentBlock(requestContentParts(model.lastRequest(t)), "image_url")
	if imageBlock == nil {
		t.Fatalf("model request carried no image block: %#v", requestContentParts(model.lastRequest(t)))
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if got, _ := imageBlock["image_url"].(map[string]any)["url"].(string); got != wantURL {
		t.Fatalf("image url = %q, want the file bytes as a data URI", got)
	}

	// The reference did not create a second copy of the file.
	entries, err := os.ReadDir(filepath.Join(queenDir, "data", "attachments"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("attachments = %v (err=%v), want the uploaded file only", entries, err)
	}

	requested := recorder.ofType(eventbus.EventClientInputReceived)
	if len(requested) != 1 {
		t.Fatalf("client_input_received events = %d, want 1", len(requested))
	}
	if requested[0].Data["image_count"] != 1 {
		t.Fatalf("image_count = %#v, want 1", requested[0].Data["image_count"])
	}
	if requested[0].Data["content"] != "📷 screenshot" {
		t.Fatalf("echo content = %#v, want display_message", requested[0].Data["content"])
	}
}

// Parity: framework/server/routes_execution.py::prepare_queen_input
func TestPrepareChatInputRejectsAttachmentRefEscape(t *testing.T) {
	queenDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(queenDir), "outside.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	session := newChatImageSession("session_20261122_000000_imgesc00", nil, eventbus.NewEventBus(), queenDir)

	message, images, paths, err := prepareChatInput(session, "", []map[string]any{
		attachmentRef("hive-attachment://../outside.png"),
	})
	if err != nil {
		t.Fatalf("prepareChatInput: %v", err)
	}
	if message != "" || images != nil || len(paths) != 0 {
		t.Fatalf("traversal ref was not dropped: message=%q images=%#v paths=%v", message, images, paths)
	}

	// The route turns the now-empty input into upstream's 400 rather than
	// delivering a phantom message.
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)
	model := &attachmentChatLLM{model: "openai/gpt-5.4"}
	runtime, bus, _ := chatImageRuntime(t, model, t.TempDir())
	routed := newChatImageSession("session_20261122_000000_imgesc01", runtime, bus, queenDir)
	manager.AddSession(routed)
	status, _ := postChat(t, server.URL, routed.ID, map[string]any{
		"message": "",
		"images":  []map[string]any{attachmentRef("hive-attachment://../outside.png")},
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 for an input with nothing deliverable", status)
	}
	if model.requestCount() != 0 {
		t.Fatalf("model received %d requests, want none", model.requestCount())
	}
}

// Parity: framework/server/routes_execution.py::prepare_queen_input
func TestPrepareChatInputTextCSVAndUnknownBinary(t *testing.T) {
	queenDir := t.TempDir()
	textRel := writeChatAttachment(t, queenDir, "notes.txt", []byte("hello world\nsecond line\n"))
	csvRel := writeChatAttachment(t, queenDir, "data.csv", []byte("name,qty\napple,3\npear,5\n"))
	binaryRel := writeChatAttachment(t, queenDir, "report.docx", []byte{0x50, 0x4b, 0x03, 0x04})
	session := newChatImageSession("session_20261122_000000_imgtext0", nil, eventbus.NewEventBus(), queenDir)
	resolvedQueenDir, err := filepath.EvalSymlinks(queenDir)
	if err != nil {
		t.Fatalf("resolve queen dir: %v", err)
	}

	message, images, paths, err := prepareChatInput(session, "look at these", []map[string]any{
		attachmentRef("hive-attachment://" + textRel),
		attachmentRef("hive-attachment://" + csvRel),
		attachmentRef("hive-attachment://" + binaryRel),
	})
	if err != nil {
		t.Fatalf("prepareChatInput: %v", err)
	}
	if images != nil {
		t.Fatalf("text/CSV/unknown attachments produced model blocks: %#v", images)
	}
	if got, want := len(paths), 3; got != want {
		t.Fatalf("saved paths = %v, want %d entries", paths, want)
	}
	for _, want := range []string{
		"--- Attached file content ---",
		"[Text file: notes.txt]\nhello world\nsecond line\n",
		"[CSV file: data.csv, 2 rows, 2 columns]\nname | qty\n--- | ---\napple | 3\npear | 5",
		"[Attachment saved to disk: data/attachments/report.docx (4 bytes) — no inline preview for '.docx' files; read/convert it from disk with terminal tools]",
		"[Attachments saved to disk (re-read via pdf_read for text, or attach_file to put back into context)]",
		"- Text: data/attachments/notes.txt (3 lines, 24 bytes)",
		"- CSV: data/attachments/data.csv (2 rows, 24 bytes)",
		"  abs: " + filepath.Join(resolvedQueenDir, "data", "attachments", "notes.txt"),
		"<system-reminder>\n",
		"\n</system-reminder>",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("prepared message missing %q:\n%s", want, message)
		}
	}
	// The unknown binary never appears in the listing block: it has a path
	// pointer but no saved-attachment kind upstream.
	if strings.Contains(message, "- Unknown") {
		t.Fatalf("unknown binary leaked into the listing block:\n%s", message)
	}
}

// Parity: framework/server/routes_execution.py::prepare_queen_input
func TestPrepareChatInputThresholds(t *testing.T) {
	queenDir := t.TempDir()
	bigText := bytes.Repeat([]byte("a"), largeTextThresholdBytes+16*1024)
	textRel := writeChatAttachment(t, queenDir, "big.log", bigText)
	pdfRel := writeChatAttachment(t, queenDir, "big.pdf", bytes.Repeat([]byte{0}, largePDFThresholdBytes+1))
	session := newChatImageSession("session_20261122_000000_imgthr00", nil, eventbus.NewEventBus(), queenDir)

	message, images, paths, err := prepareChatInput(session, "", []map[string]any{
		attachmentRef("hive-attachment://" + textRel),
		attachmentRef("hive-attachment://" + pdfRel),
	})
	if err != nil {
		t.Fatalf("prepareChatInput: %v", err)
	}
	if images != nil {
		t.Fatalf("large attachments produced model blocks: %#v", images)
	}
	if len(paths) != 2 {
		t.Fatalf("saved paths = %v, want 2", paths)
	}
	for _, want := range []string{
		"[Text file: big.log — first 8.0 KB of 272.0 KB; read the full file at data/attachments/big.log]\n" + strings.Repeat("a", largeTextHeadChars),
		"- Text: data/attachments/big.log (1 lines, 272.0 KB — LARGE: only the head was inlined; read the full file from disk)",
		"unknown page count, 10.0 MB — LARGE: load partially via pdf_read(file_path='data/attachments/big.pdf', pages='1-10')",
		"NOTE: 2 large attachments were not fully loaded into context",
	} {
		if !strings.Contains(message, want) {
			textLine := ""
			for _, line := range strings.Split(message, "\n") {
				if strings.HasPrefix(line, "- Text:") || strings.HasPrefix(line, "- PDF:") {
					textLine += line + "\n"
				}
			}
			t.Fatalf("prepared message missing %q; listing lines:\n%s", want, textLine)
		}
	}
	// The inlined head is capped at 8 KiB of characters.
	if strings.Count(message, strings.Repeat("a", largeTextHeadChars+1)) > 0 {
		t.Fatal("large text inlined more than the head")
	}
}

// chatTestPDF builds a minimal one-page text-layer PDF.
func chatTestPDF() []byte {
	content := "BT /F1 12 Tf 20 100 Td (Hello PDF) Tj ET"
	return []byte(fmt.Sprintf(`%%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
5 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
trailer
<< /Size 6 /Root 1 0 R >>
%%%%EOF
`, len(content), content))
}

// Parity: framework/server/routes_execution.py::prepare_queen_input
func TestPrepareChatInputSmallPDFFileBlock(t *testing.T) {
	queenDir := t.TempDir()
	pdf := chatTestPDF()
	pdfRel := writeChatAttachment(t, queenDir, "invoice.pdf", pdf)
	session := newChatImageSession("session_20261122_000000_imgpdf00", nil, eventbus.NewEventBus(), queenDir)

	message, images, paths, err := prepareChatInput(session, "summarize it", []map[string]any{
		attachmentRef("hive-attachment://" + pdfRel),
	})
	if err != nil {
		t.Fatalf("prepareChatInput: %v", err)
	}
	if len(paths) != 1 || paths[0] != pdfRel {
		t.Fatalf("saved paths = %v, want [%s]", paths, pdfRel)
	}
	fileBlock := findContentBlock(images, "file")
	if fileBlock == nil {
		t.Fatalf("no file block for a small PDF: %#v", images)
	}
	inner, _ := fileBlock["file"].(map[string]any)
	if inner["filename"] != "invoice.pdf" {
		t.Fatalf("file block filename = %#v", inner["filename"])
	}
	wantData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf)
	if inner["file_data"] != wantData {
		t.Fatal("file block bytes do not match the PDF")
	}
	if !strings.Contains(message, "[PDF page 1]\nHello PDF") {
		t.Fatalf("PDF text was not extracted:\n%s", message)
	}
	if !strings.Contains(message, "- PDF: data/attachments/invoice.pdf (1 page,") {
		t.Fatalf("PDF listing missing:\n%s", message)
	}
}

// Parity: framework/server/routes_execution.py::handle_chat
func TestChatRouteErrorPaths(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)
	model := &attachmentChatLLM{model: "openai/gpt-5.4"}
	runtime, bus, _ := chatImageRuntime(t, model, t.TempDir())

	// Whitespace-only message without images keeps the pre-existing 400.
	session := newChatImageSession("session_20261122_000000_imgerr00", runtime, bus, t.TempDir())
	manager.AddSession(session)
	status, body := postChat(t, server.URL, session.ID, map[string]any{"message": "   "})
	if status != 400 || body["error"] != "message is required" {
		t.Fatalf("blank message: status = %d body = %#v", status, body)
	}

	// A session with no runtime reports 503 after the body is valid.
	noLLM := NewSession("session_20261122_000000_imgerr01", eventbus.NewEventBus())
	noLLM.QueenDir = t.TempDir()
	manager.AddSession(noLLM)
	status, body = postChat(t, server.URL, noLLM.ID, map[string]any{
		"message": "hello",
		"images":  []map[string]any{attachmentRef("data:image/png;base64," + onePixelPNGBase64)},
	})
	if status != 503 {
		t.Fatalf("no-runtime status = %d body = %#v, want 503", status, body)
	}
}

// Parity: framework/agent_loop/agent_loop.py::inject_event
func TestChatRuntimeDeliverClientInputWithImages(t *testing.T) {
	model := &attachmentChatLLM{model: "openai/gpt-5.4"}
	runtime, _, recorder := chatImageRuntime(t, model, t.TempDir())

	blocks := []map[string]any{
		imageContentBlock("data:image/png;base64,AAAA"),
		pdfContentBlock("doc.pdf"),
	}
	if err := runtime.DeliverClientInputWithImages("read these attachments", blocks, "corr-images-1"); err != nil {
		t.Fatalf("DeliverClientInputWithImages: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	got := requestContentParts(model.lastRequest(t))
	types := []string{}
	for _, block := range got {
		if block["type"] != "text" {
			types = append(types, block["type"].(string))
		}
	}
	if strings.Join(types, ",") != "image_url,file" {
		t.Fatalf("model media blocks = %v, want image_url,file", types)
	}
}

// Parity: framework/agent_loop/agent_loop.py::inject_event
func TestChatRuntimeClientInputImagesGateForTextOnlyModel(t *testing.T) {
	model := &attachmentChatLLM{model: "deepseek-v4-pro"}
	runtime, _, recorder := chatImageRuntime(t, model, t.TempDir())

	if err := runtime.DeliverClientInputWithImages("look", []map[string]any{
		imageContentBlock("data:image/png;base64,AAAA"),
	}, ""); err != nil {
		t.Fatalf("DeliverClientInputWithImages: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	for _, block := range requestContentParts(model.lastRequest(t)) {
		if block["type"] == "image_url" {
			t.Fatalf("text-only model received an image block: %#v", block)
		}
	}
	notDelivered := false
	for _, message := range model.lastRequest(t).Messages {
		if strings.Contains(message.Content, "attachment not delivered") {
			notDelivered = true
		}
		for _, block := range message.ContentParts {
			if text, ok := block["text"].(string); ok && strings.Contains(text, "attachment not delivered") {
				notDelivered = true
			}
		}
	}
	if !notDelivered {
		t.Fatalf("model was not told the image was dropped: %#v", requestContentParts(model.lastRequest(t)))
	}
}
