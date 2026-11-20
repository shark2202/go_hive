package cli

// Manual soul reflection ("reflect now" from the dashboard). The route hands
// the run to a background goroutine, so this adapter is the whole execution
// path: resolve the current model, read the queen's own session events and
// soul config, and let soulstore.Reflect write the proposal (review) or the
// soul (auto). Nothing here fakes a successful run: a missing configuration,
// a queen without chat sessions, or a failing model all come back as explicit
// errors the route logs.

import (
	"context"
	"fmt"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

// soulReflectTimeout bounds one manual reflection. The daily scheduler may run
// without a deadline for the server's lifetime; a manual click should not hang
// forever if the model stalls.
const soulReflectTimeout = 10 * time.Minute

// newSoulReflectFor adapts the model/edge wiring to the route's Reflect
// contract. The provider is resolved per call, exactly like the soul probe and
// new chat sessions, so a model change in the settings applies without a
// restart.
func newSoulReflectFor(home hivehome.Home, model string) func(string, *soulstore.SoulConfig, bool) error {
	return func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error {
		if cfg == nil {
			return fmt.Errorf("soul: queen %q has no soul configuration", queenID)
		}
		if !bootstrap && len(soulstore.SessionsToRead(home.Root, home.QueensDir(), queenID, cfg)) == 0 {
			// Nothing to read at all. The daily scheduler treats this as
			// "nothing new"; a manual click reports it instead of quietly
			// recording a run that never happened.
			return fmt.Errorf("soul: queen %q has no chat sessions to read", queenID)
		}
		completer, err := resolveChatProvider(home, model)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), soulReflectTimeout)
		defer cancel()
		_, err = soulstore.Reflect(soulstore.ReflectionOptions{
			HomeRoot:  home.Root,
			QueensDir: home.QueensDir(),
			QueenID:   queenID,
			Config:    cfg,
			Completer: completer,
			Bootstrap: bootstrap,
			Context:   ctx,
			MaxTokens: soulDailyMaxTokens(home),
			Log:       nil,
		})
		return err
	}
}
