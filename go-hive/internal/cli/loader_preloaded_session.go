package cli

// Preloaded `serve --colony` sessions: the loader builds the colony host, the
// session's chat/trigger rung, and registers it with the session manager. The
// assembly lives here rather than in loader.go so the serve entry point stays
// inside the repository's frozen file-size budget.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	hivesession "github.com/aden-hive/hive-go/internal/session"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// loadPreloadedColonySession builds and registers the live session behind one
// `serve --colony <path>` argument: the colony host (overseer included), the
// session's own chat runtime when a provider is configured, and its restored
// trigger runtime. It returns nil when the colony cannot be loaded; the caller
// keeps serving the other arguments.
//
// A preloaded colony used to be a worker-only record: its Chat stayed nil, so
// POST /chat answered 503 and no run_worker could ever be dispatched from the
// session. Attaching the chat runtime here makes the preloaded session a real
// Queen from boot.
func (r *Runner) loadPreloadedColonySession(
	colonyArg string,
	manager *hiveserver.SessionManager,
	liveMu *sync.Mutex,
	live *[]*hiveserver.Session,
	home hivehome.Home,
	model string,
) *hiveserver.Session {
	colonyPath, ok := r.resolveColonyPath(colonyArg)
	if !ok {
		fmt.Fprintf(r.Stdout, "Colony not found: %s\n", colonyArg)
		return nil
	}
	colonyID := filepath.Base(colonyPath)
	sessionID, err := hivesession.NewStore("").GenerateID(time.Now())
	if err != nil {
		fmt.Fprintf(r.Stdout, "Error loading colony %s: %v\n", colonyArg, err)
		return nil
	}
	record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	record.ColonyID = colonyID
	record.WorkerPath = colonyPath
	record.LoadedAt = float64(time.Now().UnixNano()) / 1e9
	// The live runtime behind the session: colony spawn/run/SSE entry.
	// Nil when no provider is configured, in which case the worker and
	// colony routes keep their on-disk fallbacks.
	record.Colony = r.buildColonyHost(colonyPath, colonyID, home, model)
	if built, ok := record.Colony.(*colonyhost.Host); ok {
		// A preloaded colony is live from boot: its overseer starts here
		// rather than waiting for a load_colony round trip.
		queenName := colonyQueenName(home, colonyID)
		if _, startErr := built.StartOverseer(context.Background(), queenOverseerSpec(home, queenName), nil); startErr != nil {
			fmt.Fprintf(r.Stdout, "Warning: colony overseer unavailable for %s: %v\n", colonyID, startErr)
		}
	}
	// Chat/trigger/dispatch runtime for the preloaded session. It must exist
	// before triggers are restored: a restored timer's first fire needs the
	// session's queen executor.
	r.attachPreloadedChatRuntime(record, home, model)
	// Rebuild the colony's triggers.json runtime: enabled timers and
	// webhook routes survive a restart without the user re-activating them.
	if loaded, started := hiveserver.RestoreSessionTriggers(record); loaded > 0 {
		fmt.Fprintf(r.Stdout, "Restored %d trigger(s) (%d running) for %s\n", loaded, started, colonyID)
	}
	manager.AddSession(record)
	liveMu.Lock()
	*live = append(*live, record)
	liveMu.Unlock()
	fmt.Fprintf(r.Stdout, "Loaded colony: %s → session %s\n", colonyID, sessionID)
	return record
}

// attachPreloadedChatRuntime arms a preloaded colony session with the same
// Queen runtime a POST /api/sessions create builds: builtin + integration +
// task + tracker + run_worker tools over a per-session transcript, bound to the
// session's colony so run_worker dispatches and worker reports arrive. Without
// a usable provider the session keeps Chat nil and POST /chat keeps its 503,
// mirroring createSessionWithRuntime's degradation.
func (r *Runner) attachPreloadedChatRuntime(record *hiveserver.Session, home hivehome.Home, model string) {
	if record == nil || record.Colony == nil {
		return
	}
	provider, err := chatProviderFactory(home, model)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", record.ID, err)
		return
	}
	sessionDir := chatSessionDir(home, chatSessionQueenScope(record.QueenName), record.ID)
	toolbox := builtin.New("")
	tools := newSessionToolCatalogue(sessionDir, toolbox, home)
	built, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM:       provider,
		Tools:     tools.Defs(),
		Execute:   tools.Execute,
		Root:      sessionDir,
		Bus:       record.EventBus,
		SessionID: record.ID,
		QueenName: record.QueenName,
		HomeRoot:  home.Root,
		Colony:    record.Colony,
	})
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", record.ID, err)
		return
	}
	record.Chat = built
	record.LLM = provider
	// The preloaded session's queen executor is its chat runtime, so trigger
	// timers and webhooks reach the Queen exactly like a created session.
	record.QueenExecutor = &hiveserver.QueenExecutor{
		NodeRegistry: map[string]hiveserver.QueenNode{"queen": built},
	}
}
