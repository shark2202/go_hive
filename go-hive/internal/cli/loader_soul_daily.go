package cli

import (
	"log"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
)

// soulDailyInterval is how often the server checks for queens whose daily
// reflection is due. Upstream's scheduler ticks every ten minutes; the check
// itself is cheap (state + configuration only) until a reflection is due.
const soulDailyInterval = 10 * time.Minute

// soulDailyFirstDelay is how long the server settles before the first due
// check. Upstream's scheduler(first_after_s=120.0) sleeps two minutes before
// its first tick, so a restart does not reflect on top of a booting server.
const soulDailyFirstDelay = 2 * time.Minute

// startSoulDailyReflection runs the ported daily soul reflection for the
// server's lifetime: one pass after soulDailyFirstDelay (so a day the process
// was down for is caught up), then one every soulDailyInterval. It returns the
// stop function the serve loop defers.
//
// The scheduler never touches a live conversation: it reads the sessions'
// events.jsonl, writes a proposal (review mode) or the soul (auto mode) under
// queens/<id>/soul, and logs every failure to the runtime log buffer. A queen
// without a configured model keeps her attempt recorded and is retried an hour
// later, at most three times a day; nothing is reported as a successful run.
func (r *Runner) startSoulDailyReflection(home hivehome.Home) func() {
	stop := make(chan struct{})
	runner := soulstore.NewDailyRunner(soulstore.DailyRunnerOptions{
		HomeRoot:  home.Root,
		QueensDir: home.QueensDir(),
		Config: func() map[string]any {
			return hiveserver.HiveConfig(home)
		},
		BuildCompleter: soulDailyCompleter(home),
		ProfileSource:  soulProfileSource{},
		Log:            log.Printf,
		MaxTokens:      soulDailyMaxTokens(home),
	})
	go runner.Run(stop, soulDailyInterval, soulDailyFirstDelay)
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// soulDailyCompleter resolves the model one queen's daily reflection runs on.
// Upstream's llm_for prefers the worker_llm slot when her soul config says
// "worker" and that slot is configured, then her own queen_llm section, then
// the main model; this port keeps that order, so a worker-configured home no
// longer reflects on the chat provider.
//
// Parity: framework/agents/queen/soul_reflection.py::llm_for
func soulDailyCompleter(home hivehome.Home) func(queenID string, cfg *soulstore.SoulConfig) (llm.Completer, error) {
	return func(queenID string, cfg *soulstore.SoulConfig) (llm.Completer, error) {
		if cfg != nil && cfg.Model == "worker" {
			worker, err := workerChatProvider(home)
			if err != nil {
				return nil, err
			}
			if worker != nil {
				return worker, nil
			}
		}
		// QueenLLMSection or the main slot, exactly upstream's
		// queen_models.build(queen_id) or manager.build_llm().
		return queenAwareChatProvider(home, "", queenID)
	}
}

// soulDailyMaxTokens mirrors upstream's max(llm.max_tokens, get_aux_max_tokens()).
func soulDailyMaxTokens(home hivehome.Home) int {
	section, _ := hiveserver.HiveConfig(home)["llm"].(map[string]any)
	configured, _ := soulstore.AsInt(section["max_tokens"])
	if configured > llm.DefaultAuxMaxTokens {
		return configured
	}
	return llm.DefaultAuxMaxTokens
}
