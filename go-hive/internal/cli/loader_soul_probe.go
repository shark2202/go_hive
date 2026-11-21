package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hivelogging "github.com/aden-hive/hive-go/internal/logging"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

// installRuntimeLogBuffer routes the standard logger to stderr, the in-process
// ring the /api/runtime-logs route reads, and — unless HIVE_NO_FILE_LOG is set
// — the rotating $HIVE_HOME/logs/hive-server.log sink, mirroring upstream's
// root-logger file handler.
func installRuntimeLogBuffer(home hivehome.Home) {
	writers := []io.Writer{os.Stderr, hiveserver.DefaultRuntimeLogBuffer()}
	if file, enabled, err := hivelogging.OpenServerLog(home.Root); err != nil {
		// A broken log path must never take the server down (upstream's
		// `except Exception: pass`).
		fmt.Fprintf(os.Stderr, "Warning: file log unavailable: %v\n", err)
	} else if enabled {
		writers = append(writers, file)
	}
	log.SetOutput(io.MultiWriter(writers...))
}

// newSoulProbeAnswerFor resolves the configured model and adapts it to the
// soul probe contract (one system prompt + one question -> text). The route
// calls it per request, so a model change in configuration applies without a
// restart.
func newSoulProbeAnswerFor(home hivehome.Home, model string) func(string, *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error) {
	return func(queenID string, cfg *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error) {
		provider, err := resolveChatProvider(home, model)
		if err != nil {
			return nil, err
		}
		return func(system, question string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			completion, err := provider.Complete(ctx, []map[string]any{
				{"role": "system", "content": system},
				{"role": "user", "content": question},
			}, llm.CompleteOptions{})
			if err != nil {
				return "", err
			}
			return completion.Content, nil
		}, nil
	}
}
