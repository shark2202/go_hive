package pdf

// Model-facing fallback guidance for pdf_read. The scanned branch must send
// the agent to attach_file/vision before OCR, mirroring upstream
// pdf_read_tool.py:399-403 ("no OCR or image rendering needed"), while the
// encrypted/open-failure branches keep their qpdf/pdfinfo probes. Wording is
// pinned here so a later refactor cannot quietly restore OCR-first guidance.
//
// This is guidance parity, not parser parity: the Go extractor is not
// pdfplumber, so byte-level extraction equivalence remains a separate
// evidence gap tracked outside this test.

import (
	"strings"
	"testing"
)

// Parity: tools/src/aden_tools/tools/pdf_read_tool/pdf_read_tool.py::_terminal_next_step
func TestPDFNextStepScannedRoutesToAttachFileFirst(t *testing.T) {
	step := pdfNextStep("pdf_scanned_no_text", "/tmp/scan.pdf")
	if !strings.Contains(step, "attach_file") {
		t.Fatalf("next_step = %q, want attach_file", step)
	}
	if !strings.Contains(step, "no OCR") {
		t.Fatalf("next_step = %q, want 'no OCR'", step)
	}
	attachIndex, ocrIndex := strings.Index(step, "attach_file"), strings.Index(step, "OCR")
	if ocrIndex < 0 || attachIndex > ocrIndex {
		t.Fatalf("next_step must put attach_file/vision before OCR: %q", step)
	}
	if !strings.Contains(step, "terminal_hint") {
		t.Fatalf("next_step = %q, want the terminal_hint fallback named", step)
	}
}

// Parity: tools/src/aden_tools/tools/pdf_read_tool/pdf_read_tool.py::_terminal_next_step
func TestPDFNextStepKeepsEncryptedAndOpenProbes(t *testing.T) {
	encrypted := pdfNextStep("pdf_encrypted", "/tmp/locked.pdf")
	if !strings.Contains(encrypted, "qpdf") || !strings.Contains(encrypted, "/tmp/locked.pdf") {
		t.Fatalf("encrypted next_step = %q", encrypted)
	}
	openFailed := pdfNextStep("pdf_open_failed", "/tmp/broken.pdf")
	if !strings.Contains(openFailed, "pdfinfo") || !strings.Contains(openFailed, "/tmp/broken.pdf") {
		t.Fatalf("open-failure next_step = %q", openFailed)
	}
}
