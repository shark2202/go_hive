// Package astgrep implements read-only structural code search.
//
// astgrep owns the language registry, pattern compiler, structural matcher,
// workspace path safety, file scanning and result rendering. It deliberately
// does not import internal/server, internal/cli or internal/tools/builtin:
// the model-facing tool definition and registration live in the adapter
// (internal/tools/builtin/ast_grep.go), and the core can be exercised by any
// caller that can supply a workspace root.
//
// # Pattern language
//
// A pattern is source text for one AST node in the target language, after
// metavariable substitution:
//
//   - $NAME captures exactly one AST node.
//   - $_ matches exactly one AST node without binding.
//   - $$$NAME captures zero or more sibling AST nodes.
//   - $$$ matches zero or more sibling AST nodes without binding.
//
// Metavariable names start with an upper-case letter or an underscore and
// continue with upper-case letters, digits or underscores. Reusing a name
// requires identical captured source text at every occurrence. Variadic
// captures consume the longest sibling sequence that still lets the rest of
// the pattern match, then backtrack.
//
// The pattern is parsed with the candidate file's grammar. The effective
// pattern node follows ast-grep's built-in heuristic: descend while a node has
// exactly one child, and stop at the leaf or the innermost node with more
// than one child. A pattern must reduce to one non-wrapper AST node; an
// unparsable pattern, a wrapper with several top-level nodes, a leftover `$`
// token, or a metavariable that is only a string fragment is an
// ErrInvalidPattern.
//
// # Result shape
//
// The display mode is frozen to a plain path header plus hashline-shaped
// anchors without snapshot hashes:
//
//	path/to/file.go:
//	*12:func main() {
//	 13:    work()
//	 14:}
//	  meta: NAME=value
//
// Continuation lines carry a leading space and the actual line number. A
// meta line is present only when a named metavariable was captured. Results
// are grouped by path, sorted by path and byte offset, paged with Request.Skip
// and capped at Config.ResultCap (50 by default). The renderer ends with
// "Result limit reached; narrow path or use skip to page through results."
// when more matches exist, or starts with "No matches found" when none do.
// Parse issues are deduplicated and rendered separately.
//
// # Safety and bounds
//
// Every path is resolved under Config.WorkspaceRoot, `..` is rejected after
// cleaning, and symlink targets are evaluated and must stay inside the root.
// Directory walks skip VCS/cache directories and symlink entries. Files with
// NUL bytes, files above Config.MaxFileBytes, unreadable files and ambiguous
// extensions are never parsed silently as some other language. Directory
// walks filter non-source extensions before parsing; an explicitly named file
// with an unsupported or ambiguous extension is a typed error. Each parse is
// bounded by Config.ParseTimeout, scans stop on context cancellation, and
// only the current tree is retained; trees are released before the next file.
//
// # Supported languages
//
// The default binary links these grammars:
//
//	go, python, typescript, tsx, javascript, rust, json, yaml, markdown, c,
//	cpp, java, csharp
//
// `.h` has no inferred language: pass Lang "cpp" (or "c") explicitly. `.cu`
// and `.cuh` infer as C++. Unsupported languages and extensions are typed
// errors (ErrUnsupportedLanguage), not silent skips.
//
// # Residuals
//
// The frozen design asks for full ast-grep parity where possible. This
// implementation registers these measured differences in package tests:
//
//   - Metavariables must stand for whole AST nodes. String-fragment captures
//     are only supported for JSON string values (pattern `{"key": "$V"}`).
//   - JSON member fragments such as `"key": $V` (unquoted metavariable values)
//     are rejected as invalid patterns; they cannot be parsed by a
//     tree-sitter JSON grammar without inventing a token.
//   - A variadic metavariable cannot be the effective pattern root; it must
//     appear inside a parent node with siblings to consume.
//   - A bare $$$ in a Go or Rust parameter list parses through recovery with a
//     missing type node, so it compiles but cannot match a normal typed
//     parameter and returns no matches. Use concrete typed parameters such as
//     `$A $T` when that structure matters.
//   - Syntax-error recovery is gotreesitter's measured recovery, not byte-
//     exact C tree-sitter parity; malformed files are reported as parse
//     issues instead of claimed matches.
package astgrep
