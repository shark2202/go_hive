package cli

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

// installRuntimeLogBuffer tees the process logger into the ring behind
// GET /api/runtime-logs while keeping stderr output.
func installRuntimeLogBuffer() {
	log.SetOutput(io.MultiWriter(os.Stderr, hiveserver.DefaultRuntimeLogBuffer()))
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
