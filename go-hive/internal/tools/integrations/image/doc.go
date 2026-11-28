// Package image owns the image_gen_tool family: the image_generate tool, its
// Hive image-proxy request/response handling, the saved-artifact layout, the
// PNG/JPEG re-encode step and the reference-image loader with the SSRF guard.
//
// It follows the domain subpackage layout used by pdf, devops and the P2/P3
// families (see docs/integrations-package-structure.md):
//
//   - NewRegistry builds a standalone image-only registry; Register adds the
//     tool and its implementation to a registry the root assembly already
//     owns (the root batch-3 registry also carries web_search).
//   - The historical InstallBatch3Media facade and the batch3 media
//     dependency/option plumbing stay in the root package and forward here, so
//     the CLI assembly and the cross-tool tests are unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch3Image*/batch3Media* identifiers became domain
//     names on the way in. The model-facing surface (name, description, JSON
//     schema) and the returned payload are unchanged and are frozen by the
//     catalog digest test.
//
// Deliberate duplication: artifacts.go and ssrf.go mirror the root
// batch3_shared.go helpers that web_scrape still uses, because that file
// belongs to the web-scrape lane. When the shared resolver/SSRF guard moves to
// integrationkit, both call sites should converge on it.
package image
