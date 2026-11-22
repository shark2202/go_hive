package server

// Per-turn tool surface for a chat session: the projection of the session
// catalogue for one turn, its context readout, and the executor context that
// carries the bound colony down to run_worker.
//
// Split from chat_runtime.go to keep that file inside the repo's 1500-line
// budget (internal/quality TestFileSizeBudget).

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
)

// toolExecuteContext carries the bound colony down to the catalogue's
// run_worker executor. The catalogue is built before the binding exists, so
// the runtime is resolved per call rather than captured at construction.
func (r *ChatRuntime) toolExecuteContext() context.Context {
	// Workers belong to the colony, not to this chat turn: drop the runtime's
	// cancellation so a session teardown does not kill in-flight work the
	// colony still owns. The colony's own Stop() remains the lifecycle owner.
	ctx := context.WithoutCancel(r.ctx)
	if runtime := r.boundColonyRuntime(); runtime != nil {
		return colony.WithRunWorkerSpawner(ctx, runtime)
	}
	return ctx
}
