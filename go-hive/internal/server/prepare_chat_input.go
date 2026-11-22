package server

// Parity: framework/server/routes_execution.py::prepare_queen_input

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // registers the gif decoder for the best-effort dimension probe
	_ "image/jpeg" // registers the jpeg decoder for the best-effort dimension probe
	_ "image/png"  // registers the png decoder for the best-effort dimension probe
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/attachments"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/pdf"
)

// Chat-attachment thresholds. LARGE_PDF_THRESHOLD_BYTES lives in
// routes_session_attachment.go (largePDFThresholdBytes) — the upload endpoint
// documents the same number.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (thresholds)
const (
	pdfExtractMaxPages      = 200
	largeTextThresholdBytes = 256 * 1024
	largeTextHeadChars      = 8 * 1024
)

// chatExtToMIME maps allowed attachment extensions to MIME types. Text-shaped
// extensions come from the shared attach_file allowlist; `.svg` is excluded
// because the composer sends SVGs as data:image/* URIs and their bytes cannot
// go into an image block.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (_EXT_TO_MIME)
var chatExtToMIME = func() map[string]string {
	exts := map[string]string{
		".pdf":  "application/pdf",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".webp": "image/webp",
		".gif":  "image/gif",
		".csv":  "text/csv",
	}
	for ext, mime := range attachments.TextExtToMIME {
		if ext == ".svg" {
			continue
		}
		exts[ext] = mime
	}
	return exts
}()

// chatTextMIMEs are the MIMEs that take the inline-text branch. text/csv is
// handled by its own table-formatting branch first; image/svg+xml is excluded
// with .svg above.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (_TEXT_MIMES)
var chatTextMIMEs = func() map[string]struct{} {
	mimes := map[string]struct{}{}
	for _, mime := range attachments.TextExtToMIME {
		if mime == "text/csv" || mime == "image/svg+xml" {
			continue
		}
		mimes[mime] = struct{}{}
	}
	return mimes
}()

// chatMIMEToExt is the reverse lookup data-URI attachments are saved with.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (_MIME_TO_EXT)
var chatMIMEToExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// savedChatAttachment is one saved attachment's entry in the [Attachments]
// block: kind, path relative to the session dir, absolute path and the meta
// string.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input
// (saved_attachment_info)
type savedChatAttachment struct {
	kind    string
	relPath string
	absPath string
	meta    string
}

// chatAttachmentInput is one attachment's resolved source, shared by the
// per-MIME handlers: the data bytes (when already in memory), the size, and
// where the file lives on disk.
type chatAttachmentInput struct {
	idx          int
	raw          map[string]any
	mime         string
	rawBytes     []byte
	size         int
	relPath      string
	resolvedPath string
	isRef        bool
}

// chatAttachmentStore writes data-URI attachments under
// <queen_dir>/data/attachments through an os.Root anchored on the resolved
// session directory, so a symlinked data/ or attachments/ cannot redirect the
// write outside the session tree.
type chatAttachmentStore struct {
	root     *os.Root
	queenDir string
}

func openChatAttachmentStore(queenDir string) (*chatAttachmentStore, error) {
	absolute, err := filepath.Abs(queenDir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	const attachmentsRel = "data/attachments"
	if err := root.MkdirAll(attachmentsRel, 0o755); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &chatAttachmentStore{root: root, queenDir: absolute}, nil
}

func (s *chatAttachmentStore) close() {
	if s != nil && s.root != nil {
		_ = s.root.Close()
	}
}

// save writes one attachment with owner-only permissions. Upstream overwrites
// a same-named file (write_bytes); the timestamped name makes that a no-op
// collision case rather than a data-loss one.
func (s *chatAttachmentStore) save(rel string, data []byte) error {
	file, err := s.root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (s *chatAttachmentStore) absPath(rel string) string {
	return filepath.Join(s.queenDir, filepath.FromSlash(rel))
}

// resolveChatAttachmentRef resolves a hive-attachment:// relative path inside
// the session's queen dir. It refuses anything that resolves outside the
// directory (path traversal, symlink escape) or is not a regular file, the
// same guard upstream applies with Path.resolve().relative_to(queen_dir).
//
// Parity: framework/server/routes_execution.py::prepare_queen_input
// (hive-attachment:// path-traversal guard)
func resolveChatAttachmentRef(queenDir, rel string) (string, os.FileInfo, bool) {
	absolute, err := filepath.Abs(queenDir)
	if err != nil {
		return "", nil, false
	}
	resolvedDir := absolute
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		resolvedDir = resolved
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(filepath.Join(absolute, filepath.FromSlash(rel))))
	if err != nil {
		return "", nil, false
	}
	if !pathWithin(target, resolvedDir) {
		log.Printf("chat attachments: rejecting attachment ref outside queen_dir: hive-attachment://%s", rel)
		return "", nil, false
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	return target, info, true
}

// chatAttachmentCollector accumulates the outputs of one prepare pass: the
// saved paths, the listing entries, the extracted text parts and the content
// blocks the queen should receive.
type chatAttachmentCollector struct {
	store           *chatAttachmentStore
	ts              int64
	savedImagePaths []string
	savedInfo       []savedChatAttachment
	textParts       []string
	imagesOnly      []map[string]any
}

// prepareChatInput is the Go port of upstream prepare_queen_input: it persists
// a user message's attachments into the session, extracts the text the model
// should see inline, and returns the message (with the extracted text and the
// saved-attachments listing appended), the content blocks the queen should
// receive, and the saved files' paths relative to the session directory.
//
// display_message intentionally never reaches here: it only affects the UI
// echo the route publishes.
func prepareChatInput(session *Session, message string, imageContent []map[string]any) (string, []map[string]any, []string, error) {
	if len(imageContent) == 0 || session == nil || strings.TrimSpace(session.QueenDir) == "" {
		return message, imageContent, nil, nil
	}
	store, err := openChatAttachmentStore(session.QueenDir)
	if err != nil {
		return "", nil, nil, err
	}
	defer store.close()

	collector := &chatAttachmentCollector{store: store, ts: time.Now().UnixMilli()}
	for idx, img := range imageContent {
		if err := collector.process(idx, img); err != nil {
			return "", nil, nil, err
		}
	}
	message = collector.assembleMessage(message)
	if len(collector.imagesOnly) == 0 {
		return message, nil, collector.savedImagePaths, nil
	}
	return message, collector.imagesOnly, collector.savedImagePaths, nil
}

// process resolves one attachment's source and dispatches it to the per-MIME
// handler. Remote URLs pass through untouched, as upstream keeps them.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input
// (hive-attachment vs data: vs passthrough branch)
func (c *chatAttachmentCollector) process(idx int, img map[string]any) error {
	url := chatImageURL(img)
	input := chatAttachmentInput{idx: idx, raw: img}
	switch {
	case strings.HasPrefix(url, "hive-attachment://"):
		// The file is already on disk (multipart upload saved it). Skips the
		// body-size cost of re-sending the bytes inline.
		refRel := strings.TrimLeft(strings.TrimPrefix(url, "hive-attachment://"), "/")
		target, info, ok := resolveChatAttachmentRef(c.store.queenDir, refRel)
		if !ok {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(target))
		mime := chatExtToMIME[ext]
		if mime == "" {
			// Unknown/binary type (e.g. .docx, .xlsx, archives) — no inline
			// block exists for it, but the file IS on disk and the agent has
			// terminal tools. Surface it instead of silently dropping the
			// user's attachment.
			c.savedImagePaths = append(c.savedImagePaths, refRel)
			c.textParts = append(c.textParts, fmt.Sprintf(
				"[Attachment saved to disk: %s (%s) — no inline preview for '%s' files; read/convert it from disk with terminal tools]",
				refRel, formatAttachmentSize(int(info.Size())), extOrUnknown(ext)))
			return nil
		}
		input.mime = mime
		input.size = int(info.Size())
		input.relPath = refRel
		input.resolvedPath = target
		input.isRef = true
		c.savedImagePaths = append(c.savedImagePaths, refRel)
	case strings.HasPrefix(url, "data:"):
		header, encoded, ok := strings.Cut(url, ",")
		if !ok {
			log.Printf("chat attachments: failed to decode attachment %d", idx)
			return nil
		}
		mime := "image/jpeg"
		if colon := strings.IndexByte(header, ':'); colon >= 0 {
			mime = header[colon+1:]
			if semi := strings.IndexByte(mime, ';'); semi >= 0 {
				mime = mime[:semi]
			}
		}
		decoded, err := decodeChatDataURI(encoded)
		if err != nil {
			log.Printf("chat attachments: failed to decode attachment %d", idx)
			return nil
		}
		input.mime = mime
		input.rawBytes = decoded
		input.size = len(decoded)
	default:
		c.imagesOnly = append(c.imagesOnly, img)
		return nil
	}

	_, isTextMIME := chatTextMIMEs[input.mime]
	switch {
	case input.mime == "application/pdf":
		return c.handlePDF(input)
	case input.mime == "text/csv":
		return c.handleCSV(input)
	case isTextMIME || strings.HasPrefix(input.mime, "text/"):
		return c.handleText(input)
	default:
		return c.handleImage(input)
	}
}

// handlePDF emits the provider-neutral OpenAI `file` block for a small PDF
// (plus extracted text), or keeps a large PDF path-only.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (PDF branch)
func (c *chatAttachmentCollector) handlePDF(input chatAttachmentInput) error {
	pdfFilename := ""
	relPath, resolvedPath, rawBytes := input.relPath, input.resolvedPath, input.rawBytes
	if !input.isRef {
		pdfFilename = fmt.Sprintf("%d_%d.pdf", c.ts, input.idx)
		relPath = "data/attachments/" + pdfFilename
		if err := c.store.save(relPath, rawBytes); err != nil {
			return err
		}
		c.savedImagePaths = append(c.savedImagePaths, relPath)
		resolvedPath = c.store.absPath(relPath)
	} else {
		pdfFilename = filepath.Base(resolvedPath)
	}
	if rawBytes == nil {
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			log.Printf("chat attachments: PDF read failed (%v): %s", err, resolvedPath)
			return nil
		}
		rawBytes = data
	}

	isLarge := input.size > largePDFThresholdBytes
	maxPages := pdfExtractMaxPages
	if isLarge {
		// Large PDFs stay path-only: count pages, extract nothing.
		maxPages = 0
	}
	pdfPageCount, pdfParts := pdf.ExtractTextPages(rawBytes, maxPages)
	c.textParts = append(c.textParts, pdfParts...)
	if !isLarge {
		encoded := base64.StdEncoding.EncodeToString(rawBytes)
		c.imagesOnly = append(c.imagesOnly, map[string]any{
			"type": "file",
			"file": map[string]any{
				"file_data": "data:application/pdf;base64," + encoded,
				"filename":  pdfFilename,
			},
		})
		if pdfPageCount > pdfExtractMaxPages {
			c.textParts = append(c.textParts, fmt.Sprintf(
				"[PDF text extraction stopped at page %d of %d — read the rest via pdf_read]",
				pdfExtractMaxPages, pdfPageCount))
		}
	}
	var meta string
	if isLarge {
		pagesLabel := "unknown page count"
		if pdfPageCount > 0 {
			pagesLabel = fmt.Sprintf("%d pages", pdfPageCount)
		}
		suggestTo := 10
		if pdfPageCount > 0 {
			suggestTo = min(50, pdfPageCount)
		}
		meta = fmt.Sprintf("%s, %s — LARGE: load partially via pdf_read(file_path='%s', pages='1-%d')",
			pagesLabel, formatAttachmentSize(input.size), relPath, suggestTo)
	} else if pdfPageCount > 0 {
		plural := "s"
		if pdfPageCount == 1 {
			plural = ""
		}
		meta = fmt.Sprintf("%d page%s, %s", pdfPageCount, plural, formatAttachmentSize(input.size))
	} else {
		meta = formatAttachmentSize(input.size)
	}
	c.savedInfo = append(c.savedInfo, savedChatAttachment{"PDF", relPath, resolvedPath, meta})
	return nil
}

// handleCSV feeds the first 200 rows through the shared table formatter; a CSV
// never becomes a model content block.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (CSV branch)
func (c *chatAttachmentCollector) handleCSV(input chatAttachmentInput) error {
	csvFilename := ""
	relPath, resolvedPath := input.relPath, input.resolvedPath
	if !input.isRef {
		csvFilename = fmt.Sprintf("%d_%d.csv", c.ts, input.idx)
		relPath = "data/attachments/" + csvFilename
		if err := c.store.save(relPath, input.rawBytes); err != nil {
			return err
		}
		c.savedImagePaths = append(c.savedImagePaths, relPath)
		resolvedPath = c.store.absPath(relPath)
	} else {
		csvFilename = filepath.Base(resolvedPath)
	}
	reader, closer, ok := chatAttachmentReader(input.rawBytes, resolvedPath)
	csvText := ""
	rowCount := 0
	if ok {
		csvText, rowCount = csvPreviewCounted(reader, csvFilename)
		if closer != nil {
			_ = closer.Close()
		}
	}
	if csvText != "" {
		c.textParts = append(c.textParts, csvText)
	}
	meta := formatAttachmentSize(input.size)
	if rowCount > 0 {
		meta = fmt.Sprintf("%d rows, %s", rowCount, formatAttachmentSize(input.size))
	}
	c.savedInfo = append(c.savedInfo, savedChatAttachment{"CSV", relPath, resolvedPath, meta})
	return nil
}

// handleText inlines a text-shaped attachment, truncating to the head above
// the size threshold.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (text branch)
func (c *chatAttachmentCollector) handleText(input chatAttachmentInput) error {
	textFilename := ""
	relPath, resolvedPath := input.relPath, input.resolvedPath
	if !input.isRef {
		textFilename = fmt.Sprintf("%d_%d.txt", c.ts, input.idx)
		relPath = "data/attachments/" + textFilename
		if err := c.store.save(relPath, input.rawBytes); err != nil {
			return err
		}
		c.savedImagePaths = append(c.savedImagePaths, relPath)
		resolvedPath = c.store.absPath(relPath)
	} else {
		textFilename = filepath.Base(resolvedPath)
	}
	isLarge := input.size > largeTextThresholdBytes
	text, lineCount, ok := readChatTextAttachment(input.rawBytes, resolvedPath, isLarge)
	if !ok {
		return nil
	}
	if isLarge {
		c.textParts = append(c.textParts, fmt.Sprintf(
			"[Text file: %s — first %s of %s; read the full file at %s]\n%s",
			textFilename, formatAttachmentSize(largeTextHeadChars),
			formatAttachmentSize(input.size), relPath, truncateChatHead(text)))
	} else {
		c.textParts = append(c.textParts, fmt.Sprintf("[Text file: %s]\n%s", textFilename, text))
	}
	meta := fmt.Sprintf("%d lines, %s", lineCount, formatAttachmentSize(input.size))
	if isLarge {
		meta += " — LARGE: only the head was inlined; read the full file from disk"
	}
	c.savedInfo = append(c.savedInfo, savedChatAttachment{"Text", relPath, resolvedPath, meta})
	return nil
}

// handleImage inlines a regular image: the original block for data URIs, or a
// rebuilt data-URI block for a hive-attachment ref. The frontend caps images
// at 10 MB, so there is no context-blow risk.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (image branch)
func (c *chatAttachmentCollector) handleImage(input chatAttachmentInput) error {
	relPath, resolvedPath, rawBytes := input.relPath, input.resolvedPath, input.rawBytes
	if input.isRef && rawBytes == nil {
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			log.Printf("chat attachments: image read failed (%v): %s", err, resolvedPath)
			return nil
		}
		rawBytes = data
	}
	if !input.isRef {
		extChoice := chatMIMEToExt[input.mime]
		if extChoice == "" {
			extChoice = ".bin"
		}
		filename := fmt.Sprintf("%d_%d%s", c.ts, input.idx, extChoice)
		relPath = "data/attachments/" + filename
		if err := c.store.save(relPath, rawBytes); err != nil {
			return err
		}
		c.savedImagePaths = append(c.savedImagePaths, relPath)
		resolvedPath = c.store.absPath(relPath)
		c.imagesOnly = append(c.imagesOnly, input.raw)
	} else {
		// File already on disk; build a data-URI image_url block so
		// downstream filters see a normal image attachment instead of the
		// hive-attachment scheme.
		encoded := base64.StdEncoding.EncodeToString(rawBytes)
		c.imagesOnly = append(c.imagesOnly, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": fmt.Sprintf("data:%s;base64,%s", input.mime, encoded)},
		})
	}
	meta := fmt.Sprintf("%s%s, %d bytes", chatImageDimensions(rawBytes), input.mime, len(rawBytes))
	c.savedInfo = append(c.savedInfo, savedChatAttachment{"Image", relPath, resolvedPath, meta})
	return nil
}

// assembleMessage prepends the extracted attachment text and appends the
// [Attachments] listing block, exactly as upstream shapes the user message.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (message assembly)
func (c *chatAttachmentCollector) assembleMessage(message string) string {
	if len(c.textParts) > 0 {
		attachedText := strings.Join(c.textParts, "\n\n")
		if message != "" {
			message = message + "\n\n--- Attached file content ---\n" + attachedText
		} else {
			message = "--- Attached file content ---\n" + attachedText
		}
	}
	if len(c.savedInfo) == 0 {
		return message
	}
	// The structured listing teaches the agent the on-disk paths so it can
	// re-read or re-attach them later. Two paths per file: relative
	// (portable reasoning) + absolute (for path-taking tools).
	lines := []string{"[Attachments saved to disk (re-read via pdf_read for text, or attach_file to put back into context)]"}
	largeCount := 0
	for _, info := range c.savedInfo {
		lines = append(lines, fmt.Sprintf("- %s: %s (%s)", info.kind, info.relPath, info.meta))
		lines = append(lines, "  abs: "+info.absPath)
		if strings.Contains(info.meta, "LARGE:") {
			largeCount++
		}
	}
	if largeCount > 0 {
		verb := "attachments were"
		if largeCount == 1 {
			verb = "attachment was"
		}
		lines = append(lines, fmt.Sprintf(
			"NOTE: %d large %s not fully loaded into context — use pdf_read (PDFs) or terminal tools (text files) to read the rest selectively.",
			largeCount, verb))
	}
	block := "<system-reminder>\n" + strings.Join(lines, "\n") + "\n</system-reminder>"
	if message != "" {
		return message + "\n\n" + block
	}
	return block
}

// chatImageURL mirrors upstream img.get("image_url", {}).get("url", "").
func chatImageURL(img map[string]any) string {
	inner, _ := img["image_url"].(map[string]any)
	url, _ := inner["url"].(string)
	return url
}

// decodeChatDataURI decodes the payload after the base64 marker. Encoders
// sometimes omit the padding; accept both spellings.
func decodeChatDataURI(encoded string) ([]byte, error) {
	if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(encoded)
}

// chatAttachmentReader returns a reader over the attachment bytes already in
// memory, or opens the on-disk file for streaming reads.
func chatAttachmentReader(rawBytes []byte, path string) (io.Reader, io.Closer, bool) {
	if rawBytes != nil {
		return bytes.NewReader(rawBytes), nil, true
	}
	file, err := os.Open(path)
	if err != nil {
		log.Printf("chat attachments: text attachment read failed (%v): %s", err, path)
		return nil, nil, false
	}
	return file, file, true
}

// readChatTextAttachment returns the attachment's text and line count. A large
// file streams: only the inlined head is decoded, and the line count streams
// in chunks so the whole file never lands in memory.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input
// (_read_text_attachment)
func readChatTextAttachment(rawBytes []byte, path string, isLarge bool) (string, int, bool) {
	if rawBytes != nil {
		text := string(rawBytes)
		if text == "" {
			return "", 0, true
		}
		return text, strings.Count(text, "\n") + 1, true
	}
	if isLarge {
		file, err := os.Open(path)
		if err != nil {
			log.Printf("chat attachments: text attachment read failed (%v): %s", err, path)
			return "", 0, false
		}
		defer file.Close()
		// ×4: worst-case UTF-8 bytes per char, so the decoded head always
		// covers largeTextHeadChars.
		head := make([]byte, largeTextHeadChars*4)
		read, err := io.ReadFull(file, head)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			log.Printf("chat attachments: text attachment read failed (%v): %s", err, path)
			return "", 0, false
		}
		head = head[:read]
		newlines := bytes.Count(head, []byte("\n"))
		buffer := make([]byte, 1024*1024)
		for {
			chunk, err := file.Read(buffer)
			newlines += bytes.Count(buffer[:chunk], []byte("\n"))
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Printf("chat attachments: text attachment read failed (%v): %s", err, path)
				return "", 0, false
			}
		}
		return string(head), newlines + 1, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("chat attachments: text attachment read failed (%v): %s", err, path)
		return "", 0, false
	}
	text := string(data)
	if text == "" {
		return "", 0, true
	}
	return text, strings.Count(text, "\n") + 1, true
}

// truncateChatHead keeps at most largeTextHeadChars characters of the decoded
// head, as upstream's text[:LARGE_TEXT_HEAD_CHARS] does.
func truncateChatHead(text string) string {
	runes := []rune(text)
	if len(runes) <= largeTextHeadChars {
		return text
	}
	return string(runes[:largeTextHeadChars])
}

// formatAttachmentSize is upstream's _fmt_size.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (_fmt_size)
func formatAttachmentSize(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func extOrUnknown(ext string) string {
	if ext == "" {
		return "unknown"
	}
	return ext
}

// chatImageDimensions is the best-effort "W×H, " prefix upstream gets from
// PIL. Formats the stdlib cannot sniff degrade to "".
func chatImageDimensions(rawBytes []byte) string {
	config, _, err := image.DecodeConfig(bytes.NewReader(rawBytes))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d×%d, ", config.Width, config.Height)
}
