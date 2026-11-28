package cli

// Tool Library projection for GET /api/queen/{id}/tools.
//
// The projection code lives in internal/cli/toolcatalog; this file keeps the
// serve wiring (newServeAppDeps) and the exported constructor the server and
// tests call. The catalog comes from the same in-process harness catalogue the
// queen's chat session is built from (session_tools.go): terminal-tools,
// files-tools, chart-tools and hive_tools.

import (
	"github.com/shark2202/go_hive/go-hive/internal/cli/toolcatalog"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// QueenToolLibrary is the process-wide Tool Library snapshot. Its hooks match
// server.AppDeps.QueenToolCatalog / QueenLifecycleTools.
type QueenToolLibrary = toolcatalog.QueenToolLibrary

// newServeAppDeps wires the Tool Library projection into the HTTP app. Called
// once from the serve path; the loader file keeps its frozen line budget.
func newServeAppDeps(home hivehome.Home, deps hiveserver.AppDeps) hiveserver.AppDeps {
	// The live-tools diagnostic and the MCP admission gate both read this
	// process's credential snapshot; install it before any session exists.
	wireMCPGateCredSnapshot(home)
	library := NewQueenToolLibrary(home)
	deps.QueenToolCatalog = library.Catalog
	deps.QueenLifecycleTools = library.Lifecycle
	return deps
}

// NewQueenToolLibrary builds the catalog from the real harness catalogue. The
// home is only used for the integrations' sandbox roots; nothing is executed
// while building.
func NewQueenToolLibrary(home hivehome.Home) *QueenToolLibrary {
	catalogue := newSessionToolCatalogue(home.Root, builtin.New(""), home)
	return toolcatalog.NewQueenToolLibrary(catalogue.registry.GetFullMCPCatalog(), toolContractRawParameters)
}
