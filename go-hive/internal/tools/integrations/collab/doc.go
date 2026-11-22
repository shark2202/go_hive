// Package collab owns the collaboration / workspace integration family end to
// end: Jira (10 actions), Confluence (8) and Asana (9) from the batch-5 lane-1
// file, plus Notion (13) and Linear (22) from the batch-4 file — 62 actions in
// total, with their catalogs, implementations and tests.
//
// It was moved wholesale out of internal/tools/integrations (int-split-p3
// lane), following the excel/devops/social pattern:
//
//   - NewAtlassianRegistry and NewNotionRegistry are the two lane entry points
//     the root assembly uses; the historical InstallBatch5Lane1 / InstallBatch4
//     facades stay in the root package and forward here. NewRegistry builds the
//     union of both lanes (Atlassian first) for tests and diagnostics.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch5Lane1*/batch4* identifiers were renamed to domain
//     names on the way in; the model-facing surface (name/description/JSON
//     schema) is unchanged and is frozen by the catalog digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{jira,confluence,asana,notion,linear}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Jira and Confluence derive their API base from JIRA_DOMAIN /
//     CONFLUENCE_DOMAIN (Basic auth with the account email + API token);
//     Asana, Notion and Linear use bearer tokens. WithBaseURL wins in tests.
//   - The upstream `account` alias selector on notion_* resolves through the
//     runtime account view (WithAccountSource) before falling back to the
//     single configured module token.
//   - Linear's GraphQL transport lives in linear_queries.go; the tool keeps the
//     upstream action names and response projections.
package collab
