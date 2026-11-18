// Package integrations ports the upstream aden_tools integration surface to
// Go as model-facing tools.
//
// Ported modules (10 / 165 actions):
//
//	account_info 1  credential-store view (AccountSource)
//	airtable   9   declarative REST, fake-server tested
//	csv        4   local filesystem (csv_sql excluded: DuckDB dependency)
//	github    19   declarative REST + 3 composite actions
//	risk_score 1   pure Go port of the weighted grading tables
//	slack     51   declarative Web API + 5 composite actions
//	stripe    54   declarative REST/form + 2 composite actions
//	telegram  13   declarative Bot API
//	time       1   local clock + IANA zones
//	trello    12   declarative REST with query credentials
//
// Parity scope: parameter names, types, defaults, action names and source
// anchors are generated from the upstream Python AST (gen_catalog.py). REST
// actions mirror the upstream method, path, authentication, and body/query
// placement. Provider object shaping is passed through: upstream tools often
// project a subset of fields (for example Stripe's _format_customer) while
// this port returns the provider object as received. Composite actions
// (Slack DM/upload/analysis/workflow, GitHub repo/email/workflow branches,
// Stripe subscription status/cancel) are hand-written against the same
// endpoints. These are the known deviations to close before claiming
// byte-level parity.
package integrations
