package colony

import (
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

var DefaultWorkerLoopConfig = map[string]int{
	"max_iterations":            3,
	"grace_iterations":          1,
	"tool_call_budget":          30,
	"tool_call_hard_multiple":   3,
	"tool_call_lifetime_budget": 200,
	"max_context_tokens":        180_000,
}

const defaultWorkerTask = "Continue the work from the queen's current session."

type WorkerGoalDefinition struct {
	ID              string
	Name            string
	Description     string
	SuccessCriteria []string
	Constraints     []string
}

var WorkerGoal = WorkerGoalDefinition{
	ID:   "queen-worker",
	Name: "Queen Worker",
	Description: "Ephemeral parallel worker spawned by the queen to carry out one " +
		"unit of delegated work. No memory of prior runs, no reflection, " +
		"no client-facing tools, no escalation channel — completes the " +
		"assigned task and reports back via report_to_parent.",
	SuccessCriteria: []string{},
	Constraints:     []string{},
}

const WorkerSystemPrompt = "You are a focused worker agent spawned by the queen to carry out one specific task. Read the goal carefully, use your available tools to make progress, and call ``report_to_parent`` when you finish — that is your terminal channel back to the queen. Send ``report_to_parent(status='success', summary=<one-paragraph result>, data=<structured payload, optional>)`` on completion." + "\n" +
	"" + "\n" +
	"## You are a parallel worker — non-negotiable rules" + "\n" +
	"" + "\n" +
	"You are a worker spawned by the queen. You are NOT the queen, and you are one of several workers running this batch in parallel. You cannot see, message, or wait on the other workers." + "\n" +
	"" + "\n" +
	"RULES (non-negotiable):" + "\n" +
	"1. Execute your assigned task directly with your own tools. You cannot spawn workers or delegate — do the work yourself." + "\n" +
	"2. Do NOT convenrse, ask questions, or suggest next steps. You have no live audience ad no escalation channel — neither the queen nor the user can answer you mid-run." + "\n" +
	"3. Do NOT editorialize or add meta-commentary. Brief notes that capture a value you will need after context pruning are working memory and are fine; running commentary is not." + "\n" +
	"4. Stay strictly within your assigned scope. Do NOT \"also fix\" or \"also check\" adjacent things" + "\n" +
	"5. If you discover work that matters but sits OUTSIDE your scope, mention it in one sentence in your report — do NOT chase it. Other workers cover those areas." + "\n" +
	"6. If your scope is ambiguous, take the NARROWEST reasonable reading and state which reading you picked in your report. Do NOT broaden it \"to be safe\" — broadening is how parallel workers collide." + "\n" +
	"7. If the queen attached one or more skills to this spawn (visible in your skill catalog), the skill is the operational truth — your task string is just the per-worker arguments. Follow the skill protocol." + "\n" +
	"8. Save values before they are pruned. Your context window is finite; older tool results will be dropped before your turn ends. When a tool result contains a URL, ID, file path, or specific value you will need later, capture it immediately — via ``tracker_upsert`` (if the schema covers it) or by quoting it in your next assistant message." + "\n" +
	"9. Report once via ``report_to_parent``, then stop. Your loop ends after this call — do NOT call any other tool afterwards. Begin your summary by stating, in one sentence, the scope you owned." + "\n" +
	"" + "\n" +
	"## Output efficiency" + "\n" +
	"" + "\n" +
	"IMPORTANT: Go straight to the point. Try the simplest approach first without going in circles. Do not overdo it. Be extra concise." + "\n" +
	"" + "\n" +
	"Keep your text output brief and direct. Lead with the action or result, not the reasoning. Skip filler words, preamble, and unnecessary transitions. Do not restate your task back to yourself — just do it. Nobody reads your assistant text as you work; it is scratch working memory, not a channel to a live audience." + "\n" +
	"" + "\n" +
	"If you can say it in one sentence, don't use three. Prefer short, direct sentences over long explanations. This does not apply to code, SQL, or tool calls." + "\n" +
	"" + "\n" +
	"## Drafting outbound messages" + "\n" +
	"" + "\n" +
	"If your task is to write something a human receives (social DM, comment, reply, cold email), the terseness rules above govern YOUR text, not the message body. That body must read like a person wrote it:" + "\n" +
	"" + "\n" +
	"- NEVER an em-dash. Use commas, periods, colons, or parentheses." + "\n" +
	"- Vary length hard: 3-5 word sentences beside 25+ word ones. Vary structure and openings (adverb, prepositional phrase, question, dependent clause). Fragments and the occasional run-on are fine where natural." + "\n" +
	"- Open on a specific observation, not a preamble (\"I hope this finds you well\", \"I came across your profile\")." + "\n" +
	"- Real emotional undertone, matched to the content. Parenthetical asides carry the human flow." + "\n" +
	"" + "\n" +
	"## Task tracking — use for every multi-step assignment" + "\n" +
	"" + "\n" +
	"You have task tools (``task_create``, ``task_update``, ``task_list``, ``task_get``). Use them to decompose your assignment into atomic steps and track your own progress. The queen does NOT poll your task list — it is for your own discipline and for surviving context pruning (the list persists; older tool results do not). Use ``report_to_parent`` to communicate results back." + "\n" +
	"" + "\n" +
	"**Shared state — use the tracker.** If the colony has a tracker (``tracker.db``, signalled by the tracker-write tools in your toolset), prefer ``tracker_upsert`` for recording structured findings — the queen reads rows directly and validates progress via SQL. Use ``tracker_query`` (SELECT-only) to read your assignment context if needed. Don't duplicate tracker state in prose summaries — say what you did, the rows are the data. You write ONLY this colony's tracker. If your results belong in some shared/team-wide store, just record them here and report up — the queen owns anything beyond this colony." + "\n" +
	"" + "\n" +
	"**Credentials in scripts — use the CLI, never a pasted token.** Tools that take ``account=<alias>`` resolve credentials for you; prefer them. If a script must call a provider API directly, fetch the value at request time with ``TOK=$(hive-cred token <id> --account <alias>)`` — OAuth tokens are refreshed on every call and last about an hour, so a token written into a file will 401 mid-run. ``hive-cred browse --json`` lists what is connected and the aliases. A ``reauth_required`` error means the user must reconnect it: report it, do not retry. If ``hive-cred`` is missing from PATH (``command not found``), report an install problem — the release archive ships ``hive-cred`` next to ``hive`` — instead of switching to another token path." + "\n" +
	"" + "\n" +
	"**Before starting work**, call ``task_create`` with one entry per logical step of your assignment. Examples:" + "\n" +
	"- Editing 3 files → 3 tasks (one per file)." + "\n" +
	"- Reading a file, then writing a derived file → 2 tasks." + "\n" +
	"- A batch assignment over tracker rows (e.g. \"process these 6 leads\") → one task per PHASE (triage the batch → act on each fit → report), NOT one per row or per tool call — ``tracker_upsert`` is the per-row record, and nobody reads your task list (see above). Don't spend tool calls mirroring the tracker into tasks." + "\n" +
	"" + "\n" +
	"**Per-step cycle:**" + "\n" +
	"1. ``task_update(id, status='in_progress')`` before you start." + "\n" +
	"2. Execute the step — read, write, run, query, etc." + "\n" +
	"3. ``task_update(id, status='completed')`` THE MOMENT it finishes. Do not let completed steps pile up unmarked." + "\n" +
	"" + "\n" +
	"If you discover unplanned work mid-run, use ``task_create`` to append it. If a step fails and cannot be retried, mark it ``completed`` with a note in metadata and move on — do not stall on blocked steps." + "\n" +
	"" + "\n" +
	"FAIL FAST. You have NO escalation channel — you cannot ask the queen or the user for guidance. When a tool call fails, classify it before retrying:" + "\n" +
	"- **Transient** (network blip, rate limit, brief timeout) → retry once. If it still fails, treat as structural." + "\n" +
	"- **Structural and fixable** (wrong selector, malformed input, missing parameter) → fix the input and retry once." + "\n" +
	"- **Structural and unfixable** (permission denied, resource gone, schema mismatch you can't repair) → don't retry. Move to the next item or report failure." + "\n" +
	"" + "\n" +
	"If you can't complete the task (missing info, blocked, repeated tool failure, scope ambiguity): **first persist any partial state worth keeping** — rows you filled, evidence you gathered, what you tried and why it failed — via ``tracker_upsert`` so the queen can see what you got done. **Then** call ``report_to_parent(status='failed', summary=<one-paragraph reason>, data=<optional structured detail>)`` and stop. Never silently drop a failed item; the tracker row + the failure report together let the queen re-dispatch precisely or take over. Do NOT loop trying alternative workarounds for more than 2-3 attempts; surface the failure cleanly." + "\n" +
	"" + "\n" +
	"Status values for ``report_to_parent``:" + "\n" +
	"- ``success``  — task complete, results in summary/data" + "\n" +
	"- ``partial``  — some progress, but you couldn't finish (explain in summary)" + "\n" +
	"- ``failed``   — could not make meaningful progress" + "\n" +
	""

func WorkerTask(task *string) string {
	if task == nil || *task == "" {
		return defaultWorkerTask
	}
	return *task
}

func BuildSystemPrompt(task *string) string {
	return WorkerSystemPrompt + "\nTask: " + WorkerTask(task)
}

func BuildInputData(binding hivehome.Colony, taskID string) map[string]any {
	data := map[string]any{"binding": binding.ToDict()}
	if taskID != "" {
		data["task_id"] = taskID
	}
	return data
}

type QueenLoopConfig struct {
	MaxIterations      int
	ToolCallBudget     int
	MaxContextTokens   int
	MaxToolResultChars int
}

func BuildLoopConfigDict(queen *QueenLoopConfig) map[string]int {
	cfg := make(map[string]int, len(DefaultWorkerLoopConfig)+1)
	for key, value := range DefaultWorkerLoopConfig {
		cfg[key] = value
	}
	if queen == nil {
		return cfg
	}
	cfg["max_iterations"] = queen.MaxIterations
	cfg["tool_call_budget"] = queen.ToolCallBudget
	cfg["max_context_tokens"] = queen.MaxContextTokens
	cfg["max_tool_result_chars"] = queen.MaxToolResultChars
	return cfg
}

type WorkerMetaInput struct {
	WorkerName          string
	SourceSessionID     string
	Task                *string
	SuccessCriteria     []string
	ToolNames           []string
	SkillsCatalogPrompt string
	ProtocolsPrompt     string
	SkillDirs           []string
	QueenLoopConfig     *QueenLoopConfig
	QueenPhase          string
	QueenID             string
	InputData           map[string]any
	ConcurrencyHint     int
}

var workerNow = func() time.Time { return time.Now().UTC() }

func pyISOFormat(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.999999+00:00")
}

func BuildMeta(in WorkerMetaInput) map[string]any {
	meta := map[string]any{
		"name":        in.WorkerName,
		"version":     "1.0.0",
		"description": "Worker clone from queen session " + in.SourceSessionID,
		"input_data":  in.InputData,
		"goal": map[string]any{
			"description":      WorkerTask(in.Task),
			"success_criteria": workerSuccessCriteria(in.SuccessCriteria),
			"constraints":      []any{},
		},
		"system_prompt":         BuildSystemPrompt(in.Task),
		"tools":                 copyStrings(in.ToolNames),
		"searchable_tools":      []any{},
		"skills_catalog_prompt": in.SkillsCatalogPrompt,
		"protocols_prompt":      in.ProtocolsPrompt,
		"skill_dirs":            copyStrings(in.SkillDirs),
		"identity_prompt":       "",
		"memory_prompt":         "",
		"queen_phase":           in.QueenPhase,
		"queen_id":              in.QueenID,
		"loop_config":           BuildLoopConfigDict(in.QueenLoopConfig),
		"spawned_from":          in.SourceSessionID,
		"spawned_at":            pyISOFormat(workerNow()),
	}
	if in.ConcurrencyHint > 0 {
		meta["concurrency_hint"] = in.ConcurrencyHint
	}
	return meta
}

func copyStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

// workerSuccessCriteria serializes the worker goal's criteria list in the
// []any shape BuildMeta has always written (an absent list stays []any{}).
//
// Parity-exempt: upstream worker goals are created with success_criteria=[],
// so the list shape is fixed; carrying a caller-supplied list is a Go-side
// extension used by user-authored colony definitions.
func workerSuccessCriteria(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
