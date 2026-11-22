package cli

// Session start turns: what runs right after a session registers.
//
// Parity: framework/server/session_manager.py::_start_queen — a fresh queen
// session starts its persistent loop immediately: with an initial prompt the
// loop's first iteration answers it; without one it still runs one real model
// turn before any user message and parks on turn_done. Both paths live here so
// the assembly entry point (loader.go) stays within its frozen size budget.

import (
	"strings"

	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// startSessionTurns delivers the create-session initial prompt, or starts the
// opening iteration when this is a fresh segment with no prompt.
//
// The synchronous initial prompt keeps its existing behaviour: it is the
// loop's first user turn, so the opening iteration must not run on top of it.
// A resumed session, a colony-bound session (whose overseer owns the turns) or
// a session whose start block was not written this time gets neither.
func startSessionTurns(record *hiveserver.Session, runtime *hiveserver.ChatRuntime, initialPrompt *string) error {
	if runtime == nil || record == nil {
		return nil
	}
	if initialPrompt != nil && strings.TrimSpace(*initialPrompt) != "" {
		return runtime.Deliver(*initialPrompt)
	}
	if !record.QueenLoopFresh || record.Colony != nil || record.ColonyID != "" || record.QueenResumeFrom != "" {
		return nil
	}
	runtime.StartOpeningTurn()
	return nil
}
