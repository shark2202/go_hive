// Package crm owns the customer-facing integration family end to end: the
// batch-6 lane-1 CRM tools (Salesforce 10 actions, Pipedrive 14, Zoho CRM 7,
// Attio 16), the batch-6 lane-2 support desk (Intercom 12, Zendesk 9,
// Freshdesk 25, Greenhouse 9) and the batch-5 lane-4 pair (HubSpot 15,
// Obsidian 6) — 109 actions in total, with their catalogs, implementations and
// tests.
//
// It was moved wholesale out of internal/tools/integrations (int-split-p3
// lane), following the excel/devops/social pattern:
//
//   - NewCRMRegistry, NewSupportRegistry and NewHubSpotObsidianRegistry are
//     the three lane entry points the root assembly uses; the historical
//     InstallBatch6Lane1 / InstallBatch6Lane2 / InstallBatch5Lane4 facades
//     stay in the root package and forward here. NewRegistry builds the union
//     of all three lanes for tests and diagnostics.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch6Lane1*/batch6Lane2*/batch5Lane4* identifiers were
//     renamed to domain names on the way in; the model-facing surface
//     (name/description/JSON schema) is unchanged and is frozen by the catalog
//     digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{salesforce,pipedrive,zoho_crm,attio,intercom,
// zendesk,freshdesk,greenhouse,hubspot,obsidian}_tool. Action names, parameter
// names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - One credential per module (WithCredential / WithBaseURL in tests);
//     hubspot and zoho_crm additionally accept the upstream OAuth2 refresh
//     variables (HUBSPOT_* / ZOHO_*), resolved in-process.
//   - Freshdesk and Zendesk derive their host from the account credentials;
//     their BaseURL stays empty until WithBaseURL (tests) or the credential
//     fills it in.
//   - Greenhouse and Attio keep the upstream single-token contract.
package crm
