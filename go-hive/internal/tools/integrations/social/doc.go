// Package social owns the social-media integration family end to end:
// YouTube (8 actions), Reddit (7), Twitter/X (7), Twilio (7) and Pushover (7),
// with their catalogs, action implementations and tests.
//
// It is the third domain subpackage after excel and devops (see
// docs/integrations-layout.md and docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane7 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch-7 lane-7 identifiers were dropped on the way in;
//     the model-facing surface (name/description/JSON schema) is unchanged and
//     is frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{youtube,reddit,twitter,twilio,pushover}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - YouTube authenticates with an API key query parameter, Twitter with a
//     bearer token, Twilio with Basic auth over form-encoded bodies, and
//     Pushover with its app token as a form/query field.
//   - Reddit follows the OAuth2 client-credentials grant. WithCredential can
//     inject an already-acquired access token (tests, external token caches);
//     otherwise the token is fetched from REDDIT_TOKEN_URL (default
//     https://www.reddit.com/api/v1/access_token) using REDDIT_CLIENT_ID and
//     REDDIT_CLIENT_SECRET.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
package social
