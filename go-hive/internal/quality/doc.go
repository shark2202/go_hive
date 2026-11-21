// Package quality holds the repository's static quality gates.
//
// The gates run as ordinary `go test` cases, so they need no network, no
// third-party analyser and no cgo: go/parser and the standard library are
// enough. The properties enforced here:
//
//   - Layering (imports_test.go): internal/server, internal/tools,
//     internal/agentloop and internal/memory must not import their callers.
//     The direction of a dependency is a design decision that compiles fine
//     either way; only a static check can notice the inversion.
//   - Size (size_test.go): files and functions above their budget must be
//     listed with a reason, and a waiver is a frozen ceiling, not an open
//     door — the file may shrink, never grow. Files under
//     internal/tools/integrations/ (including domain subpackages) carry the
//     stricter 800-line budget; the pre-refactor files above it are pinned in
//     integrationSizeWaivers until phase 2 splits them.
//   - Lane prefixes (lane_prefix_test.go): identifiers named batchN/NLaneM
//     are wave-migration debt. Their count is frozen in
//     lane_prefix_waiver.json and may only shrink; new code must not add any.
//   - Generated markers (generated_test.go): every `*_gen.go` must start with
//     the standard `// Code generated ... DO NOT EDIT.` header.
//   - Package docs (package_doc_test.go): packages listed by `go list ./...`
//     must carry a `// Package <name>` comment; the missing count is frozen in
//     package_doc_waiver.json and may only shrink.
//
// Every gate ships with a self-test that injects a synthetic violation into a
// temporary tree and asserts the checker goes red; exit codes stay PASS=0 /
// FAIL=1, and no gate has a SKIP path that could count as a pass.
package quality
