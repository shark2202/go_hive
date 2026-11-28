// Parity: core/framework/testing/approval_cli.py::interactive_approval
// Parity: core/framework/testing/approval_cli.py::batch_approval
package testframework

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// InteractiveOptions supplies the interactive review's streams and seams.
// Upstream reads input()/prints directly; the Go port takes them explicitly so
// the flow is testable. Zero values mean os.Stdin, os.Stdout, and the system
// editor.
type InteractiveOptions struct {
	In         io.Reader
	Out        io.Writer
	OnProgress func(current, total int)
	// Edit opens the code for editing. nil uses $EDITOR (vim/nano/vi/notepad
	// fallbacks). On editor failure the original code is returned.
	Edit func(code string) (string, error)
}

// InteractiveApproval reviews tests one by one, prompting for a/r/e/s and
// persisting every decision except skip. It returns one result per test.
func InteractiveApproval(tests []*Test, storage *TestStorage, opts InteractiveOptions) ([]ApprovalResult, error) {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Edit == nil {
		opts.Edit = defaultEditCode
	}
	lines := &lineReader{reader: bufio.NewReader(opts.In)}
	results := make([]ApprovalResult, 0, len(tests))
	total := len(tests)
	for index, test := range tests {
		if opts.OnProgress != nil {
			opts.OnProgress(index+1, total)
		}
		if err := displayTest(opts.Out, test, index+1, total); err != nil {
			return results, err
		}
		action, err := promptAction(lines, opts.Out)
		if err != nil {
			return results, err
		}
		results = append(results, processAction(test, action, storage, opts, lines))
		fmt.Fprintln(opts.Out)
	}
	return results, nil
}

// BatchApproval applies programmatic approval requests for one goal. Invalid
// requests and missing tests are counted as errors without aborting the batch.
func BatchApproval(goalID string, requests []ApprovalRequest, storage *TestStorage) BatchApprovalResult {
	results := make([]ApprovalResult, 0, len(requests))
	counts := map[string]int{"approved": 0, "modified": 0, "rejected": 0, "skipped": 0, "errors": 0}
	for index := range requests {
		request := requests[index]
		if valid, message := request.ValidateAction(); !valid {
			results = append(results, ApprovalError(request.TestID, request.Action, message))
			counts["errors"]++
			continue
		}
		test, err := storage.LoadTest(goalID, request.TestID)
		if err != nil {
			results = append(results, ApprovalError(request.TestID, request.Action, err.Error()))
			counts["errors"]++
			continue
		}
		if test == nil {
			results = append(results, ApprovalError(
				request.TestID, request.Action, fmt.Sprintf("Test %s not found", request.TestID)))
			counts["errors"]++
			continue
		}
		if !validApprovalAction(request.Action) {
			results = append(results, ApprovalError(
				request.TestID, request.Action, fmt.Sprintf("Unknown action: %s", request.Action)))
			counts["errors"]++
			continue
		}
		switch request.Action {
		case ApprovalActionApprove:
			test.Approve(request.ApprovedBy)
			counts["approved"]++
		case ApprovalActionModify:
			code := test.TestCode
			if request.ModifiedCode != nil {
				code = *request.ModifiedCode
			}
			test.Modify(code, request.ApprovedBy)
			counts["modified"]++
		case ApprovalActionReject:
			reason := "No reason provided"
			if request.Reason != nil {
				reason = *request.Reason
			}
			test.Reject(reason)
			counts["rejected"]++
		case ApprovalActionSkip:
			counts["skipped"]++
		}
		if request.Action != ApprovalActionSkip {
			if err := storage.UpdateTest(test); err != nil {
				results = append(results, ApprovalError(request.TestID, request.Action, err.Error()))
				counts["errors"]++
				continue
			}
		}
		// Upstream formats `f"Test {action}d successfully"`, so modify/reject
		// read "modifyd"/"rejectd"; the message is preserved verbatim.
		results = append(results, ApprovalSuccess(
			request.TestID, request.Action, fmt.Sprintf("Test %sd successfully", request.Action)))
	}
	return BatchApprovalResult{
		GoalID:   goalID,
		Total:    len(requests),
		Approved: counts["approved"],
		Modified: counts["modified"],
		Rejected: counts["rejected"],
		Skipped:  counts["skipped"],
		Errors:   counts["errors"],
		Results:  results,
	}
}

type lineReader struct {
	reader *bufio.Reader
}

// readLine mirrors Python input(): the newline is stripped and surrounding
// whitespace is trimmed. EOF with a partial line returns that line; the next
// call returns io.EOF.
func (l *lineReader) readLine() (string, error) {
	line, err := l.reader.ReadString('\n')
	if err != nil {
		if err == io.EOF && line != "" {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func displayTest(out io.Writer, test *Test, index, total int) error {
	separator := strings.Repeat("=", 60)
	if _, err := fmt.Fprintf(out, "\n%s\n", separator); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "[%d/%d] %s\n", index, total, test.TestName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Type: %s\n", test.TestType); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Criteria: %s\n", test.ParentCriteriaID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Confidence: %.0f%%\n", test.LLMConfidence*100); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%s\n", separator); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "\nDescription: %s\n", test.Description); err != nil {
		return err
	}
	if len(test.Input) > 0 {
		rendered, err := json.MarshalIndent(test.Input, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "\nInput:\n%s\n", rendered); err != nil {
			return err
		}
	}
	if len(test.ExpectedOutput) > 0 {
		rendered, err := json.MarshalIndent(test.ExpectedOutput, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "\nExpected Output:\n%s\n", rendered); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "\nTest Code:\n%s\n%s\n%s\n", strings.Repeat("-", 40), test.TestCode, strings.Repeat("-", 40)); err != nil {
		return err
	}
	_, err := fmt.Fprint(out, "\n[a]pprove  [r]eject  [e]dit  [s]kip\n")
	return err
}

func promptAction(lines *lineReader, out io.Writer) (ApprovalAction, error) {
	for {
		if _, err := fmt.Fprint(out, "Your choice: "); err != nil {
			return "", err
		}
		choice, err := lines.readLine()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(choice) {
		case "a":
			return ApprovalActionApprove, nil
		case "r":
			return ApprovalActionReject, nil
		case "e":
			return ApprovalActionModify, nil
		case "s":
			return ApprovalActionSkip, nil
		default:
			if _, err := fmt.Fprintln(out, "Invalid choice. Please enter a, r, e, or s."); err != nil {
				return "", err
			}
		}
	}
}

func processAction(test *Test, action ApprovalAction, storage *TestStorage, opts InteractiveOptions, lines *lineReader) ApprovalResult {
	switch action {
	case ApprovalActionApprove:
		test.Approve("")
		if err := storage.UpdateTest(test); err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		fmt.Fprintln(opts.Out, "✓ Approved")
		return ApprovalSuccess(test.ID, action, "Approved")

	case ApprovalActionReject:
		if _, err := fmt.Fprint(opts.Out, "Rejection reason: "); err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		reason, err := lines.readLine()
		if err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		if reason == "" {
			reason = "No reason provided"
		}
		test.Reject(reason)
		if err := storage.UpdateTest(test); err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		fmt.Fprintf(opts.Out, "✗ Rejected: %s\n", reason)
		return ApprovalSuccess(test.ID, action, "Rejected: "+reason)

	case ApprovalActionModify:
		edited, err := opts.Edit(test.TestCode)
		if err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		if edited != test.TestCode {
			test.Modify(edited, "")
			if err := storage.UpdateTest(test); err != nil {
				return ApprovalError(test.ID, action, err.Error())
			}
			fmt.Fprintln(opts.Out, "✓ Modified and approved")
			return ApprovalSuccess(test.ID, action, "Modified and approved")
		}
		// No changes made: upstream treats this as a plain approval.
		test.Approve("")
		if err := storage.UpdateTest(test); err != nil {
			return ApprovalError(test.ID, action, err.Error())
		}
		fmt.Fprintln(opts.Out, "✓ Approved (no modifications)")
		return ApprovalSuccess(test.ID, ApprovalActionApprove, "No modifications made")

	case ApprovalActionSkip:
		fmt.Fprintln(opts.Out, "⏭ Skipped (remains pending)")
		return ApprovalSuccess(test.ID, action, "Skipped")

	default:
		return ApprovalError(test.ID, action, fmt.Sprintf("Unknown action: %s", action))
	}
}

// defaultEditCode writes the code to a temp .py file, opens $EDITOR (or a
// fallback), and returns the edited content. Editor failures keep the
// original code, matching upstream's check=True/FileNotFoundError handling.
func defaultEditCode(code string) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	if _, err := exec.LookPath(editor); err != nil {
		for _, fallback := range []string{"nano", "vi", "notepad"} {
			if _, fallbackErr := exec.LookPath(fallback); fallbackErr == nil {
				editor = fallback
				break
			}
		}
	}
	if _, err := exec.LookPath(editor); err != nil {
		fmt.Printf("Editor '%s' not found, keeping original code\n", editor)
		return code, nil
	}
	file, err := os.CreateTemp("", "*.py")
	if err != nil {
		return code, err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(code); err != nil {
		file.Close()
		return code, err
	}
	if err := file.Close(); err != nil {
		return code, err
	}
	command := exec.Command(editor, path)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fmt.Println("Editor failed, keeping original code")
		return code, nil
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return code, err
	}
	return string(edited), nil
}
