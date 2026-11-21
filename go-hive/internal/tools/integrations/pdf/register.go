package pdf

// Registry construction and shared constants. NewRegistry is what the root
// package's thin facade (integrations/pdf_install.go) forwards to; Register is
// the assembly hook for a registry the caller already owns — the root batch-3
// registry also carries web_scrape, which stays in the root package until the
// web-scrape lane splits.

import (
	"errors"
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

const (
	defaultMaxPages   = 100
	hardMaxPages      = 1000
	defaultMaxBytesMB = 25.0
	maxFiles          = 10
	visionThreshold   = 200 // chars; mirrors upstream _VISION_TEXT_THRESHOLD
	maxDecodedStream  = 64 << 20
)

// terminalHint is the upstream terminal-tool cheatsheet, kept so the
// Go port yields the same actionable fallback as the Python tool.
const terminalHint = "pdftotext '<path>' - | head -200        text extraction (whole file)\n" +
	"  pdfinfo '<path>'                        page count + metadata\n" +
	"  pdfimages -list '<path>'                image inventory (scanned docs)\n" +
	"  qpdf --decrypt '<path>' /tmp/decrypted.pdf  decrypt first when encrypted"

var (
	errPDFEncrypted = errors.New("pdf: encrypted")
	errPDFInvalid   = errors.New("pdf: invalid document")
)

// NewRegistry builds a registry containing only the pdf_read tool, sharing the
// Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    integrationkit.NewHTTPClient(nil),
		Now:     time.Now,
		Modules: map[string]*ModuleConfig{},
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	return Register(integrationkit.NewRegistry(runtime))
}

// Register adds the pdf_read catalog and its action implementation to a
// registry the caller owns.
func Register(registry *Registry) *Registry {
	registry.AddTool(pdfReadTool)
	registry.AddImpl("pdf_read", "pdf_read", pdfReadAction)
	return registry
}
