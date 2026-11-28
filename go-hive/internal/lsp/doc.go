// Package lsp is a small, read-only Language Server Protocol client for
// go_hive. It speaks JSON-RPC over stdio to language servers the user has
// installed and exposes semantic navigation through the lsp tool adapter in
// internal/tools/builtin.
//
// The package is deliberately independent: it imports only the Go standard
// library, owns its configuration/discovery, transport, client lifecycle,
// document synchronization, UTF-16 position mapping, diagnostics store and
// result rendering, and is usable without a session, HTTP server or tool
// registry. The tool schema, approval metadata and registration live outside
// this package (internal/tools/builtin/lsp.go).
//
// v1 is read-only. The supported actions are status, capabilities,
// diagnostics, definition, type_definition, implementation, references, hover
// and symbols. Rename, code actions, file renames and raw requests are not
// reachable: write actions return a typed unsupported-action error and the
// adapter schema does not contain them.
//
// Configuration (frozen contract, docs/lsp-design.json):
//
//	built-in defaults < $HIVE_HOME/lsp.json
//
// There is no project or session-level config file: the session directory is
// the file tools' sandbox root and is agent-writable, and lsp.json names the
// server command, so reading it would let the model start a process the
// operator disabled. See go_hive/docs/config-roots.html.
//
// JSON only. Server objects merge shallowly: a higher-precedence definition
// overrides the fields it sets and object-valued fields (settings,
// initOptions) replace the whole object instead of deep-merging. Root-marker
// and binary discovery use the workspace root only; there is no parent walk.
// The workspace root is the session directory unless a definition pins an
// absolute rootDir. Server command and args never come from model input.
//
// A server definition may set an optional absolute rootDir. When present it
// becomes that server's workspace root for root markers, command-local bin
// lookup, process cwd, initialize rootUri, document resolution and workspace
// scans. When absent, discovery stays cwd-only. rootDir must be absolute and
// may not contain .. or control characters; a path that is not an existing
// directory leaves the server unrooted with the reason in status. Config
// comes from $HIVE_HOME only, so user-level config can pin each server to its
// project while sessions run from isolated workspace roots.
//
// Position safety: LSP columns are zero-based UTF-16 code units. Every
// conversion goes through TextIndex; Go byte offsets are never used as LSP
// columns.
//
// Parity-exempt: this is a new Go-only capability requested by
// docs/lsp-design.json (R1-R10, A1-A14). Upstream has no lsp tool or LSP
// client, so there is no Python source path to mirror.
package lsp

// Version is reported to language servers in clientInfo.version.
const Version = "0.1.0-readonly"
