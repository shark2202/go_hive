// Package commerce owns the commerce / finance integration family end to
// end: Shopify (10 actions), QuickBooks (9), Plaid (7) and Razorpay (7) — 29
// actions in total, with their catalogs, implementations and tests.
//
// It was moved wholesale out of internal/tools/integrations (int-split-p3
// lane), following the excel/devops/social pattern:
//
//   - NewRegistry is the exported entry point the root assembly uses; the
//     historical InstallBatch6Lane3 facade stays in the root package and
//     forwards here, so the CLI assembly is unchanged.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch6Lane3* identifiers were renamed to domain names on
//     the way in; the model-facing surface (name/description/JSON schema) is
//     unchanged and is frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{shopify,quickbooks,plaid,razorpay}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Shopify and QuickBooks derive their base URL from the credential itself
//     (SHOPIFY_ACCESS_TOKEN = token|store; QUICKBOOKS_* = token|realm), so
//     their ModuleConfig BaseURL stays empty until WithBaseURL (tests) or the
//     credential fills it in.
//   - Plaid and Razorpay read their paired key|secret credential from the
//     environment (PLAID_* / RAZORPAY_*); WithCredential covers tests.
package commerce
