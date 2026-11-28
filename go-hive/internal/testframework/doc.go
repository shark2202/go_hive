// Package testframework ports the upstream goal-based testing framework
// (core/framework/testing): generated test cases with approval state, test
// results, a file-backed test/result store with indexes, failure
// categorization, debug reports, and the interactive/batch approval flow.
//
// The package is internal and dev-facing. Nothing here registers a model
// tool; the only production caller is the hidden `hive test-approval` dev
// harness (internal/cli/test_approval.go), which keeps the frozen CLI
// surface unchanged while making the ported workflow reachable.
//
// Upstream modules and their Go files:
//
//	test_case.py     -> test_case.go
//	test_result.py   -> test_result.go
//	test_storage.py  -> test_storage.go
//	categorizer.py   -> categorizer.go
//	debug_tool.py    -> debug_tool.go
//	approval_types.py-> approval_types.go
//	approval_cli.py  -> approval_cli.go
package testframework
