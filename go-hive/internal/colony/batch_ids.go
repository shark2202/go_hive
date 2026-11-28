package colony

// Batch coordinate minting for parallel worker dispatch.
//
// Spawn and resume batches use distinct prefixes upstream, and both are
// "<prefix>_<UTC compact timestamp>_<8 hex chars>" — a sortable, collision-
// resistant id the queen-side report formatter can correlate back to the
// dispatch that produced it.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
// ("rpw_%Y%m%dT%H%M%SZ_" + uuid4().hex[:8])
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
// ("rsw_%Y%m%dT%H%M%SZ_" + uuid4().hex[:8])

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	spawnBatchPrefix  = "rpw_"
	resumeBatchPrefix = "rsw_"
)

// newWorkerBatchID mints the batch id a fresh fan-out is stamped with.
func newWorkerBatchID() string { return newBatchID(spawnBatchPrefix) }

// newResumeBatchID mints the batch id a resume pass is stamped with. It is
// deliberately distinct from the spawn prefix so a resumed run is not
// mistaken for a fresh dispatch.
func newResumeBatchID() string { return newBatchID(resumeBatchPrefix) }

func newBatchID(prefix string) string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		// A failed entropy read must not block the dispatch: fall back to the
		// nanosecond clock for the suffix (still 8 hex chars, still unique per
		// process run).
		value := uint64(time.Now().UnixNano())
		for i := 0; i < len(suffix); i++ {
			suffix[i] = byte(value >> (8 * i))
		}
	}
	return prefix + time.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(suffix)
}
