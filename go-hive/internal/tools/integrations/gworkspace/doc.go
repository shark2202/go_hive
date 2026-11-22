// Package gworkspace owns the Google Workspace integration family end to end:
// Google Sheets (10 actions), Google Docs (11), Gmail (10) and Calendar (8),
// with their catalogs, action implementations and tests.
//
// It was moved wholesale out of internal/tools/integrations (int-split-p3
// lane), following the excel/devops/social pattern:
//
//   - NewRegistry is the exported entry point the root assembly uses; the
//     historical InstallBatch5Lane3 facade stays in the root package and
//     forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch5Lane3* identifiers were renamed to domain names on
//     the way in; the model-facing surface (name/description/JSON schema) is
//     unchanged and is frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{google_sheets,google_docs,gmail,calendar}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Every module authenticates with a Google OAuth2 access token from
//     GOOGLE_ACCESS_TOKEN (or WithCredential) and sends it as a Bearer token.
//     GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET / GOOGLE_REFRESH_TOKEN (plus
//     GOOGLE_ACCESS_TOKEN_EXPIRES_AT) enable the refresh-token path; the token
//     endpoint defaults to oauth2.googleapis.com and is overridable with
//     GOOGLE_TOKEN_URL, like upstream.
//   - The upstream `account` alias selector is accepted as a parameter; the
//     alias resolves through the runtime account view (WithAccountSource) and
//     falls back to the single configured module token, as before.
//   - Google Docs uses two hosts upstream (docs.googleapis.com for documents,
//     www.googleapis.com/drive for comments/export); the drive calls are
//     configured through the google_docs_drive module entry and reuse the
//     google_docs_tool credential.
package gworkspace
