// Package cloudflare owns the Cloudflare integration family end to end: one
// `cloudflare` tool with 54 actions covering zones, DNS, analytics, security,
// cache, Workers/R2/Pages and account administration, plus its catalogs,
// implementations and the frozen-surface digest test.
//
// It is the sixth domain subpackage after excel, devops, ai, social and
// enrichment (see docs/integrations-layout.md §5.5):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane2 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical lane-prefixed identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/cloudflare_tool/cloudflare_tool.py. Action names,
// parameter names/defaults and response shapes are preserved, with one
// documented exception: upstream's `action` argument of
// cloudflare_create_firewall_rule is exposed as `rule_action`, because the Go
// port reserves `action` for the tool's action selector that every batch-7
// tool dispatches through.
//
// Design:
//   - Each action is a row in the actionCatalog: method, path template,
//     parameters, defaults, query/body field mapping and response wrapper.
//     Rows live in catalog_<face>.go and are concatenated in upstream order,
//     because that order fixes the tool's action enum. Actions that need
//     several upstream calls or conditional bodies carry a Run override;
//     everything else is executed by the shared table-driven dispatcher.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server through WithBaseURL.
//   - HTTP failures mirror the upstream _make_request helper, including the
//     exact 401/403/404/429 messages, the Retry-After hint, the 204 success
//     payload and the 500-rune provider error truncation.
//   - zone_id keeps the upstream 32-hex validation; domain checks mirror
//     _validate_domain.
//
// Deliberate limits:
//   - One CLOUDFLARE_API_TOKEN credential is resolved through ModuleConfig;
//     WithCredential covers tests, and a missing token returns the upstream
//     error value instead of making an unauthenticated call.
//   - Shaping stays a pass-through projection of the upstream fields; actions
//     that upstream returns unshaped are returned as received.
package cloudflare
