package hive

import (
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/playbook"
)

// PlaybookRun is one recorded playbook execution.
//
// Parity: framework/host/playbook/runner.py::PlaybookRun
type PlaybookRun = colony.PlaybookRun

// RunPlaybookTool is the run_playbook tool definition a queen can be given.
//
// Parity: framework/tools/playbook_tools.py::run_playbook
func RunPlaybookTool() Tool {
	return toolFromLLM(colony.RunPlaybookToolDef())
}

// RunWorkerTool is the run_worker tool definition a queen can be given.
//
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
func RunWorkerTool() Tool {
	return toolFromLLM(colony.RunWorkerToolDef())
}

// PlaybookControlTools are the status/stop tools paired with run_playbook.
//
// Parity: framework/tools/playbook_tools.py
func PlaybookControlTools() []Tool {
	defs := colony.PlaybookControlToolDefs()
	out := make([]Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, toolFromLLM(def))
	}
	return out
}

// SavedPlaybooks lists the playbook names saved under a directory.
//
// Parity: framework/tools/playbook_tools.py::_saved_playbook_names
func SavedPlaybooks(dir string) []string {
	return playbook.SavedPlaybookNames(dir)
}

// SanitizePlaybookName normalises a playbook name for storage.
//
// Parity: framework/tools/playbook_tools.py::_sanitize_name
func SanitizePlaybookName(name string) string {
	return playbook.SanitizeName(name)
}

// ComputeHardTimeout derives the hard worker timeout from a soft timeout.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_compute_hard_timeout
func ComputeHardTimeout(soft time.Duration) time.Duration {
	return colony.ComputeHardTimeout(soft)
}

// ListPlaybookRuns reads the recorded playbook runs from a colony database,
// newest first. limit <= 0 means every row.
//
// Parity: framework/host/playbook/runner.py::PlaybookRun
func ListPlaybookRuns(dbPath string, limit int) ([]PlaybookRun, error) {
	return colony.ListPlaybookRuns(dbPath, limit)
}
