// Package integrations ports the upstream aden_tools integration surface to
// Go as model-facing tools. This file is the structural map of the package;
// the long-form version (ownership table, migration plan, gate numbers) lives
// in go_hive/docs/integrations-package-structure.md.
//
// Layout:
//
//	internal/tools/integrationkit/   shared kernel: Registry, Runtime,
//	                                 Tool/Action/RestSpec, credentials/OAuth,
//	                                 parameter readers, schema builders,
//	                                 bounded HTTP helpers, file helpers.
//	internal/tools/integrations/     assembly root: generated catalog
//	                                 (catalog_gen.go, DO NOT EDIT), per-provider
//	                                 files, New/InstallBatch* entrypoints,
//	                                 kit_aliases.go compatibility seam.
//	internal/tools/integrations/excel/  pilot domain subpackage: catalog +
//	                                 OOXML reader/writer + tests, registering
//	                                 through integrationkit only.
//
// Dependency rules (enforced by internal/quality + review):
//
//   - Domain subpackages import internal/tools/integrationkit and the standard
//     library only (production code). They never import this package or
//     internal/tools, so a subpackage can be moved or tested standalone; test
//     files may import internal/tools to assert against the shared registry.
//   - The root imports subpackages (assembly direction); subpackages never
//     import the root, which keeps the graph acyclic.
//   - New provider code goes into the domain file for its family; new families
//     should be born as subpackages, not as another root file.
//
// Migration status: excel is the pilot (phase 1). The remaining 50+ root
// provider files and their lane-prefixed helpers are phase 2; the lane-prefix
// and 800-line gates in internal/quality ratchet that work down.
//
// Ported modules and parity scope: parameter names, types, defaults, action
// names and source anchors are generated from the upstream Python AST
// (gen_catalog.py). REST actions mirror the upstream method, path,
// authentication and body/query placement. Provider object shaping is passed
// through: upstream tools often project a subset of fields (for example
// Stripe's _format_customer) while this port returns the provider object as
// received. Composite actions (Slack DM/upload/analysis/workflow, GitHub
// repo/email/workflow branches, Stripe subscription status/cancel) are
// hand-written against the same endpoints. These remain the known deviations
// to close before claiming byte-level parity.
package integrations
