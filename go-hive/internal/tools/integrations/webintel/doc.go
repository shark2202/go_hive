// Package webintel owns the web-intelligence and location integration family
// end to end: SimilarWeb (29 actions), News (7) and Google Maps (6), with
// their catalogs, action implementations and tests.
//
// It is the sixth domain subpackage after excel, devops, ai, social and
// enrichment (see docs/integrations-layout.md §5.5 and
// docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane6 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch7Lane6* identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{similarweb_tool,news_tool,google_maps_tool}.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - SimilarWeb and Google Maps credentials are single API keys; the news
//     module keeps per-provider keys because either provider may be absent.
//   - Every request goes through Runtime.HTTP so each action is exercisable
//     against an httptest server; base URLs honour WithBaseURL.
//   - Google Maps geo/place URLs are the classic Web Service endpoints, not
//     the newer Places API (New); upstream uses the same ones.
//   - Successful SimilarWeb bodies are returned unchanged, exactly like the
//     upstream `response.json()`; News keeps the two-provider NewsData →
//     Finlight fallback with 429 backoff; Maps keeps the upstream split
//     between transport/HTTP failures and API-level status values.
package webintel
