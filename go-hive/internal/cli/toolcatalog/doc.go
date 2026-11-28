// Package toolcatalog owns the tool catalogue pieces that do not need the
// CLI runner: the per-session task tool set, the history-search tool surface,
// the flat tool-surface projection (eager/lazy tiers) and the Tool Library
// projection.
//
// It is purpose-built for internal/cli and must not import internal/cli: the
// composition root keeps the sessionToolCatalogue type, the execute dispatch
// and the serve wiring, and calls into this package with thin adapters. Every
// tool name, definition, order and result shape stays byte-identical to the
// pre-split implementation; this package only moves code.
package toolcatalog
