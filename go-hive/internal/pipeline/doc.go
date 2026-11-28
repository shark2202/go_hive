// Package pipeline ports the upstream declarative trigger middleware
// (core/framework/pipeline/).
//
// Production reachability: the package's helper surfaces (BuildAccountsPrompt)
// and startup initialization (NewRunner/InitializeAll, called from
// internal/colony) are reachable from the Go production hosts, but the
// per-request middleware run (Runner.Run) is only reached from
// ColonyRuntime.Trigger, which has no production caller on either side.
// Upstream's production trigger path injects the queen node instead; see
// docs/pipeline.md §7 and reachability_test.go. Status: partial mechanism
// parity, out_of_scope for production trigger reachability.
package pipeline
