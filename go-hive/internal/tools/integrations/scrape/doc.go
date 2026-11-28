// Package scrape owns the web-scrape domain end to end: the web_scrape catalog
// definition, the action pipeline (pre-flight fetch, SSRF/robots checks,
// headless-browser render), the dependency-free HTML tokenizer/extractor and
// the go-rod browser pool.
//
// It is the web-scrape domain subpackage (see
// docs/integrations-package-structure.md). The root package keeps the
// historical InstallBatch3Doc facade and joins this package's catalog with
// pdf_read, so the CLI assembly and tool order are unchanged.
//
// The package imports internal/tools/integrationkit, the go-rod browser stack
// that the renderer moved with, and the standard library; it never imports the
// integrations root or internal/tools. The model-facing surface
// (name/description/schema) is frozen by digest_test.go.
//
// Upstream reference: tools/src/aden_tools/tools/web_scrape_tool.
// Action names, parameter names/defaults and response shapes are preserved.
package scrape
