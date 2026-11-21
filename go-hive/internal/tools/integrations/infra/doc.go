// Package infra owns the infrastructure automation integration family end to
// end: Terraform Cloud (5 actions), PagerDuty (8), Prometheus (2), Kafka (6),
// n8n (6) and Tines (5) — 32 actions total — with their catalogs, action
// implementations and shared request helpers.
//
// It is the sixth domain subpackage after excel, devops, ai, social and
// enrichment (see docs/integrations-layout.md and
// docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane3 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical lane-3 wave identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{terraform,pagerduty,prometheus,kafka,n8n,tines}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits, stated per module:
//   - kafka: REST Proxy v3 only. The upstream module also speaks only REST
//     Proxy, so no native broker protocol, no SASL/TLS listener config, no
//     record consumption/streaming and no Schema Registry or ACL management
//     are reproduced. Produce is one fire-and-forget record per call.
//   - terraform: Terraform Cloud / Enterprise API v2 only; run apply/discard/
//     cancel are not exposed because upstream does not expose them either.
//   - prometheus: no retries and no query-length rewriting; an out-of-range
//     timeout falls back to the upstream 5s default.
//   - n8n: read + activate/deactivate only; upstream has no workflow
//     create/update/delete action, so neither does this port.
//   - tines: read-only, matching upstream.
//   - pagerduty: no retries; the write-API "From" header comes from
//     PAGERDUTY_FROM_EMAIL and is omitted when unset, like upstream.
//
// Requests run on Runtime.HTTP so each action is exercisable against an
// httptest server. Credentials resolve from WithCredential, the module's
// environment variables, or WithGetenv. Modules that need more than one
// configuration field (terraform base URL, kafka REST URL + cluster id,
// n8n base URL, tines domain, prometheus base URL / basic auth) read the
// extra fields from the environment; WithBaseURL overrides the endpoint for
// tests.
package infra
