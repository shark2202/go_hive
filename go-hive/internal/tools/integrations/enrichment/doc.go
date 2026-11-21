// Package enrichment owns the enrichment/search integration family end to
// end: Apify (6 actions), Apollo.io (7), Lusha (8), SerpAPI (8) and Exa (7),
// with their catalogs, action implementations and tests.
//
// It is the third domain subpackage after excel and devops (see
// docs/integrations-layout.md and docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane5 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical lane-prefixed identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{apify,apollo,lusha,serpapi,exa_search}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Every module resolves its credential from WithCredential or the
//     environment (APIFY_API_TOKEN, APOLLO_API_KEY, LUSHA_API_KEY,
//     SERPAPI_API_KEY, EXA_API_KEY), matching the upstream credential split.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
//   - HTTP failures mirror the upstream helpers, including the exact 401/429
//     messages that upstream special-cases. Statuses upstream does not name
//     (402 among them) fall through to the provider's generic error branch.
//   - Exa retries 429 with the upstream 1s/2s/4s backoff; RetrySleep is a
//     package hook so tests can exercise the retry path without waiting.
package enrichment
