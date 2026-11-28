package cli

// Parity: core/framework/testing/approval_cli.py::interactive_approval

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/testframework"
)

// testApprovalCommands mounts a hidden dev harness for the ported
// framework/testing workflow. Upstream ships the approval flow as library
// functions with no registered CLI entry, so this command is a Go-side mount:
// Hidden keeps it out of every rendered help page, leaving the frozen Python
// CLI surface unchanged.
//
// Exit codes:
//
//	0  review/batch/debug completed without an error result
//	1  storage failure, unreadable batch file, missing test, or an error result
//	2  usage error (missing --root, --debug without --goal, conflicting modes)
func (r *Runner) testApprovalCommands() []*commandSpec {
	return []*commandSpec{{
		Prog: "hive test-approval",
		Help: "Review and approve generated tests (internal dev tool)",
		Description: "Runs the generated-test approval workflow over a test storage directory. " +
			"Defaults to the interactive review; --batch applies a JSON BatchApprovalRequest, " +
			"--debug prints one test's debug report.",
		Dest:   "test_approval",
		Hidden: true,
		Flags: []argSpec{
			{Dest: "root", Long: "root", Kind: kindString, Required: true,
				Help: "Test storage directory (created when missing)"},
			{Dest: "goal", Long: "goal", Kind: kindString,
				Help: "Goal ID (default: every goal in the store)"},
			{Dest: "batch", Long: "batch", Kind: kindString,
				Help: "Apply a JSON BatchApprovalRequest file instead of reviewing"},
			{Dest: "debug", Long: "debug", Kind: kindString,
				Help: "Print the debug report for TEST_ID instead of reviewing"},
		},
		Run: r.cmdTestApproval,
	}}
}

func (r *Runner) cmdTestApproval(args *parsedArgs) int {
	root := args.str("root")
	goal := args.str("goal")
	batchPath := args.str("batch")
	debugID := args.str("debug")

	usage := func(message string) int {
		fmt.Fprintf(r.Stderr, "hive test-approval: error: %s\n", message)
		return 2
	}
	if batchPath != "" && debugID != "" {
		return usage("--batch and --debug are mutually exclusive")
	}
	if debugID != "" && goal == "" {
		return usage("--debug requires --goal")
	}

	storage, err := testframework.NewTestStorage(root)
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: %v\n", err)
		return 1
	}

	if batchPath != "" {
		return r.runTestApprovalBatch(storage, goal, batchPath)
	}
	if debugID != "" {
		return r.runTestApprovalDebug(storage, goal, debugID)
	}
	return r.runTestApprovalReview(storage, goal)
}

func (r *Runner) runTestApprovalBatch(storage *testframework.TestStorage, goal, path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: read batch %s: %v\n", path, err)
		return 1
	}
	var request testframework.BatchApprovalRequest
	if err := json.Unmarshal(data, &request); err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: parse batch %s: %v\n", path, err)
		return 1
	}
	if goal != "" && goal != request.GoalID {
		fmt.Fprintf(r.Stderr, "hive test-approval: error: --goal %q does not match batch goal %q\n", goal, request.GoalID)
		return 2
	}
	result := testframework.BatchApproval(request.GoalID, request.Approvals, storage)
	fmt.Fprintln(r.Stdout, result.Summary())
	for _, row := range result.Results {
		if row.Success || row.Error == nil {
			continue
		}
		fmt.Fprintf(r.Stderr, "%s: %s\n", row.TestID, *row.Error)
	}
	if result.Errors > 0 {
		return 1
	}
	return 0
}

func (r *Runner) runTestApprovalDebug(storage *testframework.TestStorage, goal, testID string) int {
	tool := testframework.NewDebugTool(storage, nil)
	info, err := tool.Analyze(goal, testID, "")
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: %v\n", err)
		return 1
	}
	fmt.Fprintf(r.Stdout, "%s\n", data)
	if info.TestName == "unknown" {
		return 1
	}
	return 0
}

func (r *Runner) runTestApprovalReview(storage *testframework.TestStorage, goal string) int {
	tests, err := pendingTests(storage, goal)
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: %v\n", err)
		return 1
	}
	if len(tests) == 0 {
		fmt.Fprintln(r.Stdout, "No pending tests.")
		return 0
	}
	results, err := testframework.InteractiveApproval(tests, storage, testframework.InteractiveOptions{
		In:  r.Stdin,
		Out: r.Stdout,
	})
	if err != nil {
		fmt.Fprintf(r.Stderr, "test-approval: %v\n", err)
		return 1
	}
	failures := 0
	for _, result := range results {
		if result.Success {
			continue
		}
		failures++
		if result.Error != nil {
			fmt.Fprintf(r.Stderr, "%s: %s\n", result.TestID, *result.Error)
		}
	}
	if failures > 0 {
		return 1
	}
	return 0
}

func pendingTests(storage *testframework.TestStorage, goal string) ([]*testframework.Test, error) {
	if goal != "" {
		return storage.GetPendingTests(goal)
	}
	goals, err := storage.ListAllGoals()
	if err != nil {
		return nil, err
	}
	tests := make([]*testframework.Test, 0)
	for _, candidate := range goals {
		pending, err := storage.GetPendingTests(candidate)
		if err != nil {
			return nil, err
		}
		tests = append(tests, pending...)
	}
	return tests, nil
}
