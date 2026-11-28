package cli

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
)

// soulModelFor resolves the model a soul-side call runs on. Upstream's
// soul_reflection.llm_for is the single entry point for the daily reflection,
// "reflect now" and probe answers: a soul configured with model "worker" uses
// the worker_llm slot when that slot is configured, then her queen_llm
// section, then the main slot. A non-empty requestedModel is this port's CLI
// --model override and wins over the configured slots.
//
// Parity: framework/agents/queen/soul_reflection.py::llm_for
func soulModelFor(home hivehome.Home, requestedModel string) func(queenID string, cfg *soulstore.SoulConfig) (llm.Completer, error) {
	if strings.TrimSpace(requestedModel) != "" {
		return func(string, *soulstore.SoulConfig) (llm.Completer, error) {
			return resolveChatProvider(home, requestedModel)
		}
	}
	return soulDailyCompleter(home)
}
