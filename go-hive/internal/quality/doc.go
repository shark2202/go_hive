// Package quality holds the repository's static quality gates.
//
// The gates run as ordinary `go test` cases, so they need no network, no
// third-party analyser and no cgo: go/parser and the standard library are
// enough. Two properties are enforced here:
//
//   - Layering (imports_test.go): internal/server, internal/tools,
//     internal/agentloop and internal/memory must not import their callers.
//     The direction of a dependency is a design decision that compiles fine
//     either way; only a static check can notice the inversion.
//   - Size (size_test.go): files and functions above their budget must be
//     listed with a reason, and a waiver is a frozen ceiling, not an open
//     door — the file may shrink, never grow.
package quality
