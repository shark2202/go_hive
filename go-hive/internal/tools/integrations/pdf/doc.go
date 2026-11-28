// Package pdf owns the PDF reading family end to end: the single pdf_read
// tool (one action, seven parameters), its action implementation and the
// dependency-free parser stack — classic/stream xref tables, trailer
// dictionaries, FlateDecode / ASCIIHexDecode streams, a content-stream text
// interpreter, ToUnicode CMaps and WinAnsi decoding.
//
// It is the tenth domain subpackage after excel (pilot), devops, ai, social,
// enrichment, cloudflare, infra, webintel and schedchat (see
// docs/integrations-layout.md §5.9):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch3Doc facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch3* identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Scope, stated honestly:
//   - Simple text PDFs (Type1/TrueType fonts, WinAnsi or ToUnicode CMaps) are
//     extracted page by page.
//   - Encrypted PDFs are decrypted with the standard security handler when
//     the caller supplies the password (RC4 40/128, AES-128, AES-256 R5/R6),
//     matching pdfplumber's password argument. A wrong or missing password is
//     reported as pdf_encrypted with a qpdf fallback hint.
//   - Scanned/image-only PDFs are rejected with an explicit needs_vision
//     error that routes the model to attach_file/vision first (OCR only as
//     the non-vision terminal_hint fallback) instead of silently returning an
//     empty body.
//   - Inline images, AcroForm fields, annotations, JavaScript and PostScript
//     functions are ignored.
//
// Upstream reference: tools/src/aden_tools/tools/pdf_read_tool. Action names,
// parameter names/defaults and response shapes are preserved.
//
// Deliberate note: ssrf.go mirrors the root web-scrape file's SSRF guard
// (same deny-list, DNS checks and redirect re-validation) because that file
// belongs to the web-scrape lane; when scrape.go splits, the helper should
// move to integrationkit and both call sites should share it.
package pdf
