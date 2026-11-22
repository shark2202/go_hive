// Package data owns the database and data-platform integration family end to
// end: PostgreSQL (9 actions), Redis (12), MongoDB (7) and Supabase (8) from
// the batch-5 lane-2 file, plus Snowflake (4), BigQuery (3), Databricks (8),
// Redshift (6), Azure SQL (5), Power BI (6) and SAP (5) from the batch-6
// lane-4 file — 65 actions in total, with their catalogs, implementations and
// tests.
//
// It was moved wholesale out of internal/tools/integrations (int-split-p3
// lane), following the excel/devops/social pattern:
//
//   - NewDatabasesRegistry and NewPlatformRegistry are the two lane entry
//     points the root assembly uses; the historical InstallBatch5Lane2 /
//     InstallBatch6Lane4 facades stay in the root package and forward here.
//     NewRegistry builds the union of both lanes (databases first) for tests
//     and diagnostics.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch5Lane2*/batch6Lane4* identifiers were renamed to
//     domain names on the way in; the model-facing surface
//     (name/description/JSON schema) is unchanged and is frozen by the catalog
//     digest test.
//
// Upstream reference:
// tools/src/aden_tools/tools/{postgres,redis,mongodb,supabase,snowflake,
// bigquery,databricks,redshift,azure_sql,powerbi,sap}_tool. Action names,
// parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Postgres speaks the wire protocol directly (pgwire.go and friends);
//     Redis likewise (RESP); MongoDB and Supabase talk their HTTP data APIs.
//     Every networked action runs through Runtime.HTTP where one exists, and
//     each one is exercisable against a fake upstream with WithBaseURL.
//   - Snowflake derives its base URL from SNOWFLAKE_ACCOUNT; with WithBaseURL
//     set (tests) the account is not required.
//   - Redshift signs with SigV4 in-process; only long-term IAM access keys are
//     supported (no STS/session-token credentials).
//   - SAP reads SAP_BASE_URL/SAP_USERNAME/SAP_PASSWORD (or WithBaseURL plus
//     env) only; upstream also supports a credential-store adapter.
package data
