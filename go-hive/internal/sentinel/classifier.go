package sentinel

// Nudge-vs-escalate classifier for a parked colony queen.
//
// The queen often pauses before its goal is done — a progress paragraph ending
// in "shall we continue?", or an ask_user that asks the same. ParkReason alone
// cannot tell a spurious permission-seek from a genuine blocker (both arrive
// as TURN_DONE / ASK_USER), so a cheap one-shot LLM decides, grounded in the
// goal, open tasks, the queen's last message, pending questions, the user's
// most recent steer, and recent tool errors. Hard blockers are decided before
// this is called; the classifier only sees the ambiguous middle.
//
// Parity: core/framework/sentinel/classifier.py

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	VerdictContinue   = "continue"
	VerdictNeedsHuman = "needs_human"
	VerdictDone       = "done"
)

// ParkContext is everything the classifier (and the escalation message) needs.
type ParkContext struct {
	ParkReason string
	Goal       *string
	OpenTasks  []string
	// LastAssistantText is the queen's last message.
	LastAssistantText string
	// RecentUserText is the user's most recent message (steer/instruction).
	// Empty when the last turn was the queen's.
	RecentUserText string
	// PendingQuestions are the explicit questions the queen asked.
	PendingQuestions []map[string]any
	RecentErrors     []string
	// RunningWorkers is the live worker snapshot at park time. A queen idling
	// while its fan-out is still running is waiting, not stalled.
	RunningWorkers []map[string]any
	// HardBlocker is a precomputed deterministic signal; when true the source
	// escalates without calling the classifier.
	HardBlocker bool
}

// GoalText returns the goal or "" when unset.
func (p ParkContext) GoalText() string {
	if p.Goal == nil {
		return ""
	}
	return *p.Goal
}

// ClassifierVerdict is the classifier's decision.
type ClassifierVerdict struct {
	Verdict string
	Reason  string
	// Errored is true when the verdict is the conservative fallback after a
	// classifier failure or with no LLM at all. Callers that would act on
	// continue should skip rather than poke a parked queen on a glitch.
	Errored bool
}

// NeedsHuman reports whether the verdict escalates.
func (v ClassifierVerdict) NeedsHuman() bool { return v.Verdict == VerdictNeedsHuman }

// IsDone reports whether the verdict is a completion report.
func (v ClassifierVerdict) IsDone() bool { return v.Verdict == VerdictDone }

const classifierSystemPrompt = `You are the supervisor of an autonomous agent (the 'queen') that is working toward a goal with a task list. The queen has paused. Decide whether it should keep going on its own, or whether a human is genuinely needed.

Answer 'continue' when the pause is just the queen seeking permission or reporting progress while work clearly remains (e.g. 'shall I continue?', 'should I proceed to the next batch?'). The queen has standing authority to finish its goal without asking.
Answer 'needs_human' ONLY for a genuine blocker the queen cannot resolve alone: expired/invalid login or credentials, a crashed or unavailable tool/browser, missing information only the human has, or a real decision with materially different outcomes. When unsure and work can still proceed, prefer 'continue'.
Answer 'done' when the queen has clearly COMPLETED its goal — all work finished and a final result or summary delivered, with nothing left to do (e.g. a closing report, a final scoreboard, 'all done'). Judge completion from the goal, the absence of remaining work, and the queen's last message — never from an empty task list alone (a queen can stall before it ever records tasks).
BLOCKED-REMAINDER — when the goal is substantially complete but a remainder is blocked by something the queen cannot resolve on its own (an external limit it has hit — rate/credit/quota/InMail/monthly limit — billing, exhausted capacity, or a dependency outside its control), do NOT answer 'continue': continuing is futile because the work cannot make progress. Answer 'done' if the queen has delivered a completion/closing report on what it finished, or 'needs_human' if the human must act on the blocker. 'continue' is ONLY for work that can actually progress now.
EXCEPTION — user intent overrides standing authority: if the user's most recent message asked the queen to stop, pause, hold, wait for them, or hand control back, answer 'needs_human' even if work remains. (A message that merely redirects the work — 'stop using X, do Y instead' — is NOT a request to halt; keep going.)
If workers from an earlier fan-out are still running, an idle queen is normally just waiting for their reports — that is healthy autonomous progress, not a blocker: answer 'continue'.

Respond ONLY with JSON: {"verdict": "continue" | "needs_human" | "done", "reason": "<one short sentence>"}`

// FormatElapsed renders a compact human-readable duration: 45s, 12m, 2h3m.
func FormatElapsed(seconds float64) string {
	total := int(seconds)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	minutes := total / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	minutes = minutes % 60
	if minutes != 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	return fmt.Sprintf("%dh", hours)
}

// FormatRunningWorkers renders running workers as
// "- <id> [<status>, <elapsed>]: <task>" lines, shared by the classifier
// prompt and the nudge body. Elapsed is omitted when absent. Empty when there
// are no workers.
func FormatRunningWorkers(workers []map[string]any, limit int) string {
	if len(workers) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 8
	}
	lines := make([]string, 0, len(workers)+1)
	shown := workers
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, worker := range shown {
		id := "?"
		if value, ok := worker["worker_id"]; ok {
			id = PyStr(value)
		}
		status := "?"
		if value, ok := worker["status"]; ok {
			status = PyStr(value)
		}
		head := status
		if elapsed, ok := ToFloat(worker["elapsed_seconds"]); ok && elapsed > 0 {
			head = status + ", " + FormatElapsed(elapsed)
		}
		task := ""
		if value, ok := worker["task"]; ok {
			task = strings.TrimSpace(PyStr(value))
		}
		if task != "" {
			lines = append(lines, fmt.Sprintf("- %s [%s]: %s", id, head, task))
		} else {
			lines = append(lines, fmt.Sprintf("- %s [%s]", id, head))
		}
	}
	if len(workers) > limit {
		lines = append(lines, fmt.Sprintf("- ... and %d more", len(workers)-limit))
	}
	return strings.Join(lines, "\n")
}

// BuildClassifierPrompt assembles the user prompt sent to the classifier.
func BuildClassifierPrompt(ctx ParkContext) string {
	openTasks := make([]string, 0, len(ctx.OpenTasks))
	for _, task := range ctx.OpenTasks {
		if len(openTasks) == 10 {
			break
		}
		openTasks = append(openTasks, "- "+task)
	}
	tasks := strings.Join(openTasks, "\n")
	if tasks == "" {
		tasks = "(none recorded)"
	}

	questions := ""
	if len(ctx.PendingQuestions) > 0 {
		parts := make([]string, 0, len(ctx.PendingQuestions))
		for index, question := range ctx.PendingQuestions {
			if index == 5 {
				break
			}
			value, ok := question["prompt"]
			if !ok {
				value = question
			}
			parts = append(parts, PyStr(value))
		}
		questions = "\nExplicit question(s) the queen asked: " + strings.Join(parts, "; ")
	}

	errors := ""
	if len(ctx.RecentErrors) > 0 {
		lines := make([]string, 0, len(ctx.RecentErrors))
		for index, item := range ctx.RecentErrors {
			if index == 5 {
				break
			}
			lines = append(lines, "- "+item)
		}
		errors = "\nRecent tool errors:\n" + strings.Join(lines, "\n")
	}

	workers := ""
	if len(ctx.RunningWorkers) > 0 {
		workers = fmt.Sprintf("\nWorkers still running (%d):\n", len(ctx.RunningWorkers)) +
			FormatRunningWorkers(ctx.RunningWorkers, 8)
	}

	userMessage := ""
	if strings.TrimSpace(ctx.RecentUserText) != "" {
		userMessage = "\nUser's most recent message:\n\"\"\"\n" + TruncateRunes(ctx.RecentUserText, 1500) + "\n\"\"\""
	}

	return fmt.Sprintf("Goal: %s\n", goalOrPlaceholder(ctx)) +
		fmt.Sprintf("Open tasks still to do:\n%s\n", tasks) +
		fmt.Sprintf("Park reason: %s\n", ctx.ParkReason) +
		fmt.Sprintf("Queen's last message:\n\"\"\"\n%s\n\"\"\"", TruncateRunes(ctx.LastAssistantText, 1500)) +
		userMessage + questions + errors + workers + "\n\n" +
		"Should the queen continue on its own, or does it need a human?"
}

func goalOrPlaceholder(ctx ParkContext) string {
	if goal := ctx.GoalText(); goal != "" {
		return goal
	}
	return "(no goal recorded)"
}

// logClassifyDecision emits the full classifier input + verdict as one
// structured line — the canonical record for debugging sentinel behaviour.
func logClassifyDecision(ctx ParkContext, verdict ClassifierVerdict) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelLogf("sentinel: classify_decision log failed: %v", recovered)
		}
	}()
	payload := map[string]any{
		"verdict":             verdict.Verdict,
		"reason":              verdict.Reason,
		"park_reason":         ctx.ParkReason,
		"goal":                ctx.Goal,
		"open_tasks":          ctx.OpenTasks,
		"last_assistant_text": TruncateRunes(ctx.LastAssistantText, 1500),
		"recent_user_text":    TruncateRunes(ctx.RecentUserText, 500),
		"pending_questions":   ctx.PendingQuestions,
		"recent_errors":       firstStrings(ctx.RecentErrors, 5),
		"running_workers":     len(ctx.RunningWorkers),
		"hard_blocker":        ctx.HardBlocker,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		sentinelLogf("sentinel: classify_decision marshal failed: %v", err)
		return
	}
	sentinelLogf("[sentinel] classify_decision %s", encoded)
}

func firstStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

// ClassifyPark classifies an ambiguous park. On any failure it defaults to
// continue (keep the colony moving; do not raise a false alarm), flagged
// errored so the caller can decline to nudge.
func ClassifyPark(ctx context.Context, park ParkContext, completer llm.Completer) ClassifierVerdict {
	if completer == nil {
		verdict := ClassifierVerdict{Verdict: VerdictContinue, Reason: "no llm available", Errored: true}
		logClassifyDecision(park, verdict)
		return verdict
	}
	response, err := completer.AComplete(ctx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: BuildClassifierPrompt(park)}},
		System:    classifierSystemPrompt,
		MaxTokens: llm.AuxMaxTokens(),
		JSONMode:  true,
	})
	if err != nil {
		sentinelLogf("sentinel: classifier failed; defaulting to continue: %v", err)
		verdict := ClassifierVerdict{Verdict: VerdictContinue, Reason: "classifier error", Errored: true}
		logClassifyDecision(park, verdict)
		return verdict
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Content)), &decoded); err != nil {
		sentinelLogf("sentinel: classifier returned invalid JSON; defaulting to continue: %v", err)
		verdict := ClassifierVerdict{Verdict: VerdictContinue, Reason: "classifier error", Errored: true}
		logClassifyDecision(park, verdict)
		return verdict
	}
	verdictText := strings.ToLower(strings.TrimSpace(PyStr(decoded["verdict"])))
	verdict := ClassifierVerdict{Verdict: VerdictContinue, Reason: PyStr(decoded["reason"])}
	switch verdictText {
	case VerdictNeedsHuman:
		verdict.Verdict = VerdictNeedsHuman
	case VerdictDone:
		verdict.Verdict = VerdictDone
	}
	logClassifyDecision(park, verdict)
	return verdict
}
