package colony

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// overseerLoopConfig is the persistent overseer's loop profile. The overseer
// is the colony's queen, so it runs upstream's colony-queen loop config
// (selected when effective_phase == "colony"): an effectively unbounded
// 999_999-iteration loop with the 50/500 tool-call envelope. HIVE_MAX_ITER
// caps it for single-reply deployments, exactly as it caps the DM queen.
//
// Parity: core/framework/agents/queen/agent.py::queen_colony_loop_config
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop (HIVE_MAX_ITER)
func overseerLoopConfig(storagePath string) agentloop.LoopConfig {
	config := agentloop.ColonyQueenLoopConfig()
	if cap, ok := colonyEnvPositiveInt("HIVE_MAX_ITER"); ok {
		config.MaxIterations = cap
	}
	// Upstream builds the queen loop with spillover_dir=queen_dir/data; the
	// colony storage directory is the overseer's equivalent home, so an
	// oversized tool result spills there instead of staying in context.
	if storagePath != "" {
		config.SpilloverDir = filepath.Join(storagePath, "data")
	}
	return config
}

// colonyEnvPositiveInt reads a positive integer environment override. It
// mirrors the server's envPositiveInt: unset, empty, non-numeric and
// non-positive values all report absent.
func colonyEnvPositiveInt(name string) (int, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}
