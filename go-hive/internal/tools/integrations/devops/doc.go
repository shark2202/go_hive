// Package devops owns the DevOps hosting integration family end to end:
// GitLab (9 actions), AWS S3 (8), Vercel (7) and Docker Hub (7), with their
// catalogs, action implementations and tests.
//
// It is the second domain subpackage after excel (see
// docs/integrations-layout.md and docs/integrations-package-structure.md):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical InstallBatch7Lane1 facade stays in the root package
//     and forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch7Lane1* identifiers were dropped on the way in; the
//     model-facing surface (name/description/JSON schema) is unchanged and is
//     frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{gitlab,aws_s3,vercel,docker_hub}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - GitLab, Vercel and Docker Hub resolve a single bearer/PAT credential
//     through ModuleConfig (GITLAB_TOKEN, VERCEL_TOKEN, DOCKER_HUB_TOKEN), so
//     WithCredential covers tests. GITLAB_URL optionally relocates a
//     self-hosted instance; WithBaseURL wins over it.
//   - AWS S3 follows the upstream environment-only contract
//     (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION) and signs every
//     request with SigV4 in-process, like _sign_request/_s3_request.
//   - Every networked action runs through Runtime.HTTP, so each one is
//     exercisable against an httptest server with WithBaseURL. s3_generate_
//     presigned_url is the one exception: upstream it only signs a URL and
//     never calls the API, and that is preserved here.
//   - HTTP failures mirror the upstream per-module helpers, including the
//     exact 401/403/404/429 payloads (and the fact that GitLab's POST path
//     does not special-case 404/429 while GET/PUT do). One ordering nuance:
//     required-argument checks run before credential resolution here, while
//     upstream resolves the token first; the returned error is the same class
//     and no request is made either way.
//   - `assignee_ids` parsing: upstream raises ValueError on a non-numeric
//     entry; here it becomes an {"error": ...} value, because tool failures
//     are values in this port.
package devops
