package cli

import (
	"log"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

// soulDailyInterval is how often the server checks for queens whose daily
// reflection is due. Upstream's scheduler ticks every ten minutes; the check
// itself is cheap (state + configuration only) until a reflection is due.
const soulDailyInterval = 10 * time.Minute

// startSoulDailyReflection runs the ported daily soul reflection for the
// server's lifetime: one pass right away (so a day the process was down for is
// caught up) and one every soulDailyInterval. It returns the stop function the
// serve loop defers.
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
		// framework.agents.queen.soul_reflection.llm_for asks the session
		// manager for a worker model first. This port has no process-wide
		// worker LLM (colony hosts build their own), so both "worker" and
		// "queen" resolve to the configured model.
		BuildCompleter: func(queenID string, cfg *soulstore.SoulConfig) (llm.Completer, error) {
			return resolveChatProvider(home, "")
		},
		ProfileSource: soulProfileSource{},
		Log:           log.Printf,
		MaxTokens:     soulDailyMaxTokens(home),
	})
	go runner.Run(stop, soulDailyInterval, 0)
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
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
