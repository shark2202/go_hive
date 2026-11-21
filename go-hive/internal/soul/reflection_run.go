package soul

// Parity: framework/agents/queen/soul_reflection.py
//
// When the reflection runs: past the configured hour, once a day per queen, at
// most a few attempts, an hour apart. The server's scheduler calls RunDue on a
// tick and at startup, so a day the process was down for is caught up on the
// next boot; nothing here touches the conversation's own path.

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// ReflectionDue reports whether today's reflection should run now: past the
// hour, not yet done, not retried too soon or too often, and (in review mode)
// with no proposal still waiting for its decision.
//
// Parity: framework/agents/queen/soul_reflection.py::due
func ReflectionDue(queensDir, queenID string, cfg *SoulConfig, now time.Time) bool {
	if cfg == nil {
		return false
	}
	if now.Hour() < cfg.Hour {
		return false
	}
	if cfg.Mode == "review" && PendingProposal(queensDir, queenID) != nil {
		// The last one is still waiting; the chats are read once it is decided.
		return false
	}
	state := LoadReflectionState(queensDir, queenID)
	today := now.Format("2006-01-02")
	if lastRun, _ := state["last_run"].(string); lastRun == today {
		return false
	}
	attempts, _ := state["attempts"].(map[string]any)
	if day, _ := attempts["day"].(string); day == today {
		count, _ := AsInt(attempts["count"])
		if count >= reflectionAttemptsPerDay {
			return false
		}
		at, _ := attempts["at"].(string)
		if when, err := parseReflectionTime(at); err == nil {
			if now.Sub(when) < reflectionRetryAfter {
				return false
			}
		}
	}
	return true
}

// reflectionNoteAttempt counts one attempt for the day, so a failing
// reflection is retried an hour later and stops after three.
//
// Parity: framework/agents/queen/soul_reflection.py::_note_attempt
func reflectionNoteAttempt(queensDir, queenID string, now time.Time) error {
	state := LoadReflectionState(queensDir, queenID)
	today := now.Format("2006-01-02")
	attempts, _ := state["attempts"].(map[string]any)
	if attempts == nil {
		attempts = map[string]any{}
	}
	count := 1
	if day, _ := attempts["day"].(string); day == today {
		previous, _ := AsInt(attempts["count"])
		count = previous + 1
	}
	attempts["day"] = today
	attempts["count"] = count
	attempts["at"] = now.Format("2006-01-02T15:04:05-07:00")
	state["attempts"] = attempts
	return SaveReflectionState(queensDir, queenID, state)
}

// reflectionRecordFailure keeps a failed attempt visible in state.json without
// touching last_run: the day is still due, so the retry ladder applies. A
// failure is never recorded as a successful run.
func reflectionRecordFailure(queensDir, queenID string, now time.Time, cause error) {
	state := LoadReflectionState(queensDir, queenID)
	state["last_error"] = cause.Error()
	state["last_error_at"] = now.Format("2006-01-02T15:04:05-07:00")
	if err := SaveReflectionState(queensDir, queenID, state); err != nil {
		log.Printf("soul: could not record %s's reflection failure: %v", queenID, err)
	}
}

// parseReflectionTime reads the ISO timestamps state.json carries: attempts
// are written with an offset, hand-edited files may omit it.
func parseReflectionTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02T15:04:05", value)
}

// DailyRunnerOptions is the server's nightly reflection loop.
type DailyRunnerOptions struct {
	// HomeRoot holds configuration.json, memories/ and any relative source
	// file path.
	HomeRoot string
	// QueensDir is <HomeRoot>/queens in the server, but tests may point it
	// anywhere.
	QueensDir string
	// Config returns the hive configuration; nil reads configuration.json
	// under HomeRoot.
	Config func() map[string]any
	// BuildCompleter resolves the model one queen's reflection runs on. A
	// failure is retried like any other failed attempt.
	BuildCompleter func(queenID string, cfg *SoulConfig) (llm.Completer, error)
	// ProfileSource reads each queen's profile for the identity her
	// reflection runs under; forwarded to Reflect. nil means "no profile":
	// her id, with no identity block.
	ProfileSource QueenProfileSource
	// Log receives the runner's lines; nil means log.Printf.
	Log func(format string, args ...any)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MaxTokens is the answer budget per model call.
	MaxTokens int
}

// DailyRunner runs the reflections that are due. Its passes are sequential and
// never overlap: a slow model delays the next tick instead of stacking runs.
type DailyRunner struct {
	options DailyRunnerOptions
	mu      sync.Mutex
}

// NewDailyRunner builds a runner with the defaults filled in.
func NewDailyRunner(options DailyRunnerOptions) *DailyRunner {
	return &DailyRunner{options: options}
}

func (r *DailyRunner) logf(format string, args ...any) {
	if r.options.Log != nil {
		r.options.Log(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (r *DailyRunner) now() time.Time {
	if r.options.Now != nil {
		return r.options.Now()
	}
	return time.Now()
}

func (r *DailyRunner) config() map[string]any {
	if r.options.Config != nil {
		return r.options.Config()
	}
	if r.options.HomeRoot == "" {
		return map[string]any{}
	}
	value, _ := readJSONFile(filepath.Join(r.options.HomeRoot, "configuration.json")).(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

// RunDue reflects every queen that is due now and returns their ids. A failed
// reflection is logged, recorded in state.json, and left due for its retry;
// it is not reported as done.
//
// Parity: framework/agents/queen/soul_reflection.py::run_due
func (r *DailyRunner) RunDue(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	moment := now
	if moment.IsZero() {
		moment = r.now()
	}
	if r.options.HomeRoot == "" && r.options.Config == nil {
		return nil
	}
	done := make([]string, 0)
	config := r.config()
	for _, queenID := range SoulQueens(config) {
		cfg := GetSoulConfig(config, queenID)
		if cfg == nil || !ReflectionDue(r.options.QueensDir, queenID, cfg, moment) {
			continue
		}
		if err := reflectionNoteAttempt(r.options.QueensDir, queenID, moment); err != nil {
			r.logf("soul: could not record %s's reflection attempt: %v", queenID, err)
		}
		if r.options.BuildCompleter == nil {
			reflectionRecordFailure(r.options.QueensDir, queenID, moment, fmt.Errorf("no model configured"))
			r.logf("soul: %s's reflection has no model; it is retried later today", queenID)
			continue
		}
		completer, err := r.options.BuildCompleter(queenID, cfg)
		if err != nil {
			reflectionRecordFailure(r.options.QueensDir, queenID, moment, err)
			r.logf("soul: %s's reflection has no model: %v", queenID, err)
			continue
		}
		_, err = Reflect(ReflectionOptions{
			HomeRoot:      r.options.HomeRoot,
			QueensDir:     r.options.QueensDir,
			QueenID:       queenID,
			Config:        cfg,
			Completer:     completer,
			ProfileSource: r.options.ProfileSource,
			Now:           moment,
			Context:       context.Background(),
			MaxTokens:     r.options.MaxTokens,
			Log:           r.options.Log,
		})
		if err != nil {
			reflectionRecordFailure(r.options.QueensDir, queenID, moment, err)
			r.logf("soul: %s's reflection failed; it is retried later today: %v", queenID, err)
			continue
		}
		done = append(done, queenID)
	}
	return done
}

// Run checks for due reflections for the server's lifetime: one pass at
// startup (so a day the process was down for is caught up), then one every
// every. It returns when stop closes.
//
// Parity: framework/agents/queen/soul_reflection.py::scheduler
func (r *DailyRunner) Run(stop <-chan struct{}, every, firstAfter time.Duration) {
	if every <= 0 {
		every = 10 * time.Minute
	}
	if firstAfter > 0 {
		timer := time.NewTimer(firstAfter)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	r.safePass()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.safePass()
		}
	}
}

func (r *DailyRunner) safePass() {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logf("soul: scheduler tick failed: %v", recovered)
		}
	}()
	done := r.RunDue(time.Time{})
	if len(done) > 0 {
		r.logf("soul: reflected for %s", strings.Join(done, ", "))
	}
}
