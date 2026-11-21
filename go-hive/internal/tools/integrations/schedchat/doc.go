// Package schedchat owns the scheduling/chat integration family end to end:
// Brevo (9 actions), Cal.com (9), Calendly (8), Discord (7) and Mattermost
// (7) — 40 actions total, with their catalogs, action implementations and
// tests.
//
// It is the sixth domain subpackage after excel, devops, ai, social and
// enrichment (see docs/integrations-layout.md and
// docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane8 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical lane-prefixed identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{brevo,calcom,calendly,discord,mattermost}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Credentials resolve from WithCredential, the module's environment variables,
// or WithGetenv:
//
//	brevo       BREVO_API_KEY      (header: api-key)
//	calcom      CALCOM_API_KEY     (query: apiKey)
//	calendly    CALENDLY_PAT       (Authorization: Bearer)
//	discord     DISCORD_BOT_TOKEN  (Authorization: Bot)
//	mattermost  MATTERMOST_ACCESS_TOKEN + MATTERMOST_URL
//	                               (Authorization: Bearer; base gets /api/v4)
//
// Response shaping is per module, not generic: brevo and calendly keep the
// upstream field projections, calcom returns the provider JSON unchanged, and
// discord/mattermost wrap the raw resource in their upstream envelope.
//
// Deliberate limits, stated per module:
//   - Every networked action runs through Runtime.HTTP, so each one is
//     exercisable against an httptest server with WithBaseURL.
//   - discord/mattermost: upstream takes an `account` alias for multi-account
//     credential stores. This port resolves the alias through the runtime's
//     account view and falls back to the single module credential.
//   - discord/mattermost: a 429 is retried twice using the provider's
//     retry_after (capped at 60s). RetrySleep is a package seam so tests never
//     stall; after the third 429 the classified rate-limit error is returned.
//   - mattermost: the base URL is MATTERMOST_URL with /api/v4 appended when it
//     is not already present, exactly like the upstream client constructor.
//   - calcom: the API key travels as a query parameter because the upstream
//     client does not send an Authorization header.
package schedchat
