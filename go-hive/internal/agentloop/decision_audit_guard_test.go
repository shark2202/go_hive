package agentloop

// Decision-ledger writer guard.
//
// The tracker port is complete, but the decision audit chain is not: upstream
// never calls decide()/record_outcome() from the agent loop. Its only
// non-literal reference is the NodeProtocol docstring
// (core/framework/orchestrator/node.py:619-647), and the graph host that owns
// the run lifecycle is unported (contracts/module-inventory.json marks
// orchestrator/orchestrator.py out_of_scope). The Go colony seam installs a
// StreamDecisionTracker on AgentContext.Runtime, but the loop never reads it.
//
// These tests pin that boundary twice: a source scan rejects a
// ctx.Runtime.<writer> call site, and a real turn proves no ledger is written.
// A loop-side producer therefore cannot land without the parity claim
// (docs/migration-known-gaps.md §2 and the tracker package note) being
// revisited.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// decisionWriterMethods are the ledger mutations a producer at the loop
// boundary would call through AgentContext.Runtime.
var decisionWriterMethods = map[string]bool{
	"StartRun":         true,
	"EndRun":           true,
	"Decide":           true,
	"RecordOutcome":    true,
	"ReportProblem":    true,
	"DecideAndExecute": true,
	"QuickDecision":    true,
	"SetNode":          true,
}

// Parity-exempt: Go audit-chain guard. Upstream has no test asserting the
// absence of a writer, because the graph host where a node would record is not
// part of this port.
func TestAgentLoopHasNoDecisionWriterCallSite(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	var hits []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !decisionWriterMethods[sel.Sel.Name] {
				return true
			}
			// Match the documented boundary: <ctx>.Runtime.<writer>(...).
			// RuntimeLogger and other fields are unrelated.
			if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Runtime" {
				hits = append(hits, fmt.Sprintf("%s: %s", fset.Position(call.Pos()), sel.Sel.Name))
			}
			return true
		})
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("agent loop calls the decision ledger at %v; the audit chain is documented as "+
			"partial/graph-host out_of_scope (docs/migration-known-gaps.md §2). If this is a real "+
			"producer, update that section and the tracker package-parity note instead of claiming "+
			"full alignment — and make sure it records genuine decisions, not synthetic no-op events",
			hits)
	}
}

// Parity-exempt: Go audit-chain guard. Upstream has no test asserting the
// absence of a writer, because the graph host where a node would record is not
// part of this port.
func TestAgentLoopWritesNoDecisionLedger(t *testing.T) {
	ledgerRoot := t.TempDir()
	ledger, err := tracker.NewStreamLedger(ledgerRoot)
	if err != nil {
		t.Fatalf("NewStreamLedger: %v", err)
	}
	stream, err := ledger.NewStream("worker:guard")
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("unexpected tool call %q", req.Name)
		return ToolResult{}, nil
	})
	ctx := midturnAgentContext(completer)
	// The production colony seam: a real stream-scoped ledger on the context.
	ctx.Runtime = stream

	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if calls := completer.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want 1 (the turn must actually run)", calls)
	}

	runs, err := filepath.Glob(filepath.Join(ledgerRoot, "runs", "*.json"))
	if err != nil {
		t.Fatalf("glob runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("agent loop wrote decision runs %v with no decide()/record_outcome() call site; "+
			"if a real producer was added, update docs/migration-known-gaps.md §2 and the tracker "+
			"package-parity note from partial/out_of_scope instead of claiming full alignment — and "+
			"make sure the writer is a genuine boundary, not a synthetic no-op event", runs)
	}
	if active := stream.GetActiveExecutions(); len(active) != 0 {
		t.Fatalf("agent loop opened decision executions %v without recording a decision", active)
	}
}
