// Package ai owns the AI-platform integration family end to end: Pinecone
// (9 actions), Weights & Biases (6), Langfuse (6), Hugging Face (9) and Google
// Cloud Vision (9) — 5 tools, 39 actions, with their catalogs, action
// implementations and tests.
//
// It is a domain subpackage after excel and devops (see
// docs/integrations-layout.md §5.2):
//
//   - NewRegistry/Register are the exported entry points the root assembly
//     uses; the historical Install facade stays in the root package and
//     forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch7Lane4* identifiers were renamed to domain names on
//     the way in; the model-facing surface (name/description/JSON schema) is
//     unchanged and is frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{pinecone,wandb,langfuse,huggingface,vision}_tool.
// Action names, parameter defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Credentials resolve from WithCredential (single-token modules) or the
//     upstream environment variables (PINECONE_API_KEY, WANDB_API_KEY,
//     LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY + LANGFUSE_HOST,
//     HUGGINGFACE_TOKEN, GOOGLE_CLOUD_VISION_API_KEY).
//   - Every request runs through Runtime.HTTP and every base URL is injectable
//     with WithBaseURL, so each action is exercisable against an httptest
//     server without real credentials. The Pinecone data plane takes its host
//     per call (index_host); a full http(s) URL there is used verbatim.
//   - HTTP failures mirror the upstream helpers, including their exact
//     401/403/404/429 messages and the 500-character body slices upstream
//     applies to provider errors.
//
// Vision accepts a public URL or a local image path; local paths go through
// Runtime.ResolveSafePath so the tool stays inside the configured roots.
package ai
