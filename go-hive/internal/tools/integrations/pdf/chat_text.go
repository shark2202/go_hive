package pdf

// Chat-attachment text extraction: the subset of pdfplumber the server's
// prepare-chat-input step needs (page count plus per-page text, bounded to a
// page cap). It rides the same parser as pdf_read rather than adding a second
// PDF stack.
//
// Parity: core/framework/server/routes_execution.py::prepare_queen_input
// (_inspect_pdf: page count + page_text extraction).

import (
	"fmt"
	"strings"
)

// ExtractTextPages reports the document's page count and returns the text of
// its first maxPages pages, each prefixed with "[PDF page N]" exactly as the
// upstream chat handler formats it. Pages whose text is empty are skipped.
// A document the parser cannot open reports a zero page count and no text;
// callers treat that as "unknown", the way pdfplumber failures degrade
// upstream. Pass maxPages <= 0 to count pages without extracting text (the
// large-PDF path).
func ExtractTextPages(data []byte, maxPages int) (int, []string) {
	doc, err := pdfOpen(data)
	if err != nil {
		return 0, nil
	}
	pages, err := doc.pages()
	if err != nil {
		return 0, nil
	}
	if maxPages > len(pages) {
		maxPages = len(pages)
	}
	parts := make([]string, 0, maxPages)
	for index := 0; index < maxPages; index++ {
		text, err := doc.pageText(pages[index])
		if err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[PDF page %d]\n%s", index+1, text))
	}
	return len(pages), parts
}
