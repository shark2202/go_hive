package agentloop

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

type LlmCompactOptions struct {
	Spec                 *AgentSpec
	Accumulator          map[string]any
	CharLimit            *int
	MaxDepth             int
	MaxContextTokens     int
	PreserveUserMessages bool
}

// Parity: framework/agent_loop/internals/compaction.py llm_compact.
func LlmCompact(ctx context.Context, completer llm.Completer, messages []Message, opts LlmCompactOptions) (string, error) {
	return llmCompactDepth(ctx, completer, messages, opts.Accumulator, 0, opts)
}

func llmCompactDepth(ctx context.Context, completer llm.Completer, messages []Message, accumulator map[string]any, depth int, opts LlmCompactOptions) (string, error) {
	maxDepth := opts.MaxDepth
	if maxDepth == 0 {
		maxDepth = LLMCompactMaxDepth
	}
	if depth > maxDepth {
		return "", fmt.Errorf("LLM compaction recursion limit (%d)", maxDepth)
	}
	maxContextTokens := opts.MaxContextTokens
	if maxContextTokens == 0 {
		maxContextTokens = 128_000
	}
	charLimit := LLMCompactCharLimit(maxContextTokens)
	if opts.CharLimit != nil {
		charLimit = *opts.CharLimit
	}

	if depth == 0 {
		messages = StripImagesFromMessages(messages)
	}

	formatted := FormatMessagesForSummary(messages)

	var summary string
	if len(formatted) > charLimit && len(messages) > 1 {
		var err error
		summary, err = llmCompactSplit(ctx, completer, messages, accumulator, depth, opts, charLimit, maxContextTokens)
		if err != nil {
			return "", err
		}
	} else {
		prompt := BuildLlmCompactionPrompt(opts.Spec, accumulator, formatted, maxContextTokens, opts.PreserveUserMessages)
		systemMsg := llmCompactSystemMessage(opts.PreserveUserMessages)

		var summaryBudget int
		if opts.PreserveUserMessages {
			summaryBudget = maxInt(1024, maxContextTokens/2)
		} else {
			summaryBudget = minInt(LLMCompactSummaryMaxTokens, maxInt(1024, maxContextTokens/8))
		}
		targetTokens := CompactionTargetChars(len(formatted), maxContextTokens) / 4
		summaryBudget = minInt(summaryBudget, maxInt(1024, targetTokens*2))

		response, err := completer.AComplete(ctx, llm.CompletionRequest{
			Messages:  []llm.ChatMessage{{Role: "user", Content: prompt}},
			System:    systemMsg,
			MaxTokens: summaryBudget,
		})
		if err != nil {
			if IsContextTooLargeError(err) && len(messages) > 1 {
				summary, err = llmCompactSplit(ctx, completer, messages, accumulator, depth, opts, charLimit, maxContextTokens)
				if err != nil {
					return "", err
				}
			} else {
				return "", err
			}
		} else {
			summary = response.Content
		}
	}

	if depth == 0 {
		toolHistory := ExtractToolCallHistory(messages, 30)
		if toolHistory != "" && !strings.Contains(summary, "TOOLS ALREADY CALLED") {
			summary += "\n\n" + toolHistory
		}
	}
	return summary, nil
}

func llmCompactSystemMessage(preserveUserMessages bool) string {
	if preserveUserMessages {
		return "You are a conversation compactor for an AI agent. " +
			"Write a detailed summary that allows the agent to " +
			"continue its work. CRITICAL: reproduce every user " +
			"message verbatim and in full inside the 'User Messages' " +
			"section — do not paraphrase, truncate, or merge them. " +
			"Assistant turns and tool results may be summarised, but " +
			"user input is sacred."
	}
	return "You are a conversation compactor for an AI agent. " +
		"Write a detailed summary that allows the agent to " +
		"continue its work. Preserve user-stated rules, " +
		"constraints, and account/identity preferences verbatim."
}

// Parity: compaction.py _llm_compact_split. The left half summarises without
func llmCompactSplit(ctx context.Context, completer llm.Completer, messages []Message, accumulator map[string]any, depth int, opts LlmCompactOptions, charLimit int, maxContextTokens int) (string, error) {
	mid := len(messages) / 2
	if mid < 1 {
		mid = 1
	}
	leftOpts := opts
	leftOpts.CharLimit = &charLimit
	leftOpts.MaxContextTokens = maxContextTokens
	rightOpts := leftOpts

	type splitResult struct {
		index   int
		summary string
		err     error
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan splitResult, 2)
	go func() {
		summary, err := llmCompactDepth(childCtx, completer, messages[:mid], nil, depth+1, leftOpts)
		results <- splitResult{index: 0, summary: summary, err: err}
	}()
	go func() {
		summary, err := llmCompactDepth(childCtx, completer, messages[mid:], accumulator, depth+1, rightOpts)
		results <- splitResult{index: 1, summary: summary, err: err}
	}()

	var halves [2]splitResult
	for received := 0; received < 2; received++ {
		result := <-results
		halves[result.index] = result
		if result.err != nil {
			cancel()
			<-results
			return "", result.err
		}
	}
	return halves[0].summary + "\n\n" + halves[1].summary, nil
}

// Parity: compaction.py build_llm_compaction_prompt.
func BuildLlmCompactionPrompt(spec *AgentSpec, accumulator map[string]any, formattedMessages string, maxContextTokens int, preserveUserMessages bool) string {
	ctxLines := make([]string, 0, 5)
	if spec != nil {
		ctxLines = append(ctxLines, fmt.Sprintf("NODE: %s (id=%s)", spec.Name, spec.ID))
		if spec.Description != "" {
			ctxLines = append(ctxLines, "PURPOSE: "+spec.Description)
		}
		if spec.SuccessCriteria != nil && *spec.SuccessCriteria != "" {
			ctxLines = append(ctxLines, "SUCCESS CRITERIA: "+*spec.SuccessCriteria)
		}
	}

	if accumulator != nil {
		done, todo := accumulatorSections(spec, accumulator)
		if len(done) > 0 {
			ctxLines = append(ctxLines, "OUTPUTS ALREADY SET:\n"+strings.Join(done, "\n"))
		}
		if len(todo) > 0 {
			ctxLines = append(ctxLines, "OUTPUTS STILL NEEDED: "+strings.Join(todo, ", "))
		}
	} else if spec != nil && len(spec.OutputKeys) > 0 {
		ctxLines = append(ctxLines, "OUTPUTS STILL NEEDED: "+strings.Join(spec.OutputKeys, ", "))
	}

	targetChars := CompactionTargetChars(len(formattedMessages), maxContextTokens)
	targetTokens := targetChars / 4
	nodeCtx := strings.Join(ctxLines, "\n")

	userMessagesSection := "6. **User Messages** — Preserve ALL user-stated rules, constraints, identity preferences, and account details verbatim.\n"
	if preserveUserMessages {
		userMessagesSection = "6. **User Messages** — Reproduce EVERY user message verbatim and " +
			"in full, in chronological order, each on its own line prefixed " +
			"with the message index (e.g. \"[U1] ...\"). Do NOT paraphrase, " +
			"summarise, merge, or omit any user message. Preserve markdown, " +
			"code fences, whitespace, and punctuation exactly as the user " +
			"wrote them.\n"
	}

	return "You are compacting an AI agent's conversation history. " +
		"The agent is still working and needs to continue.\n\n" +
		"AGENT CONTEXT:\n" + nodeCtx + "\n\n" +
		"CONVERSATION MESSAGES:\n" + formattedMessages + "\n\n" +
		"INSTRUCTIONS:\n" +
		fmt.Sprintf("Write a summary of approximately %d characters ", targetChars) +
		fmt.Sprintf("(~%d tokens).\n\n", targetTokens) +
		"Organise the summary into these sections (omit empty ones):\n\n" +
		"1. **Primary Request and Intent** — What the user originally asked " +
		"for and the high-level goal the agent is working toward.\n" +
		"2. **Key Technical Concepts** — Important domain-specific terms, " +
		"patterns, or architectural decisions established in the conversation.\n" +
		"3. **Files and Code Sections** — Specific files read/written/edited " +
		"with brief descriptions of changes. Include short code snippets only " +
		"when they capture critical logic.\n" +
		"4. **Errors and Fixes** — Problems encountered and how they were " +
		"resolved. Include root causes so the agent doesn't repeat them.\n" +
		"5. **Problem Solving Efforts** — Approaches tried, dead ends hit, " +
		"and reasoning behind the current strategy.\n" +
		userMessagesSection +
		"7. **Pending Tasks** — Work remaining, outputs still needed, and " +
		"any blockers.\n" +
		"8. **Current Work** — The most recent action taken and the immediate " +
		"next step the agent should perform. This section is the most important " +
		"for seamless resumption.\n\n" +
		"Additional rules:\n" +
		"- Be detailed enough that the agent can resume without re-doing work.\n" +
		"- Preserve key decisions made and results obtained.\n" +
		"- When in doubt, keep information rather than discard it.\n"
}

func accumulatorSections(spec *AgentSpec, accumulator map[string]any) (done []string, todo []string) {
	keys := make([]string, 0, len(accumulator))
	seen := make(map[string]struct{}, len(accumulator))
	if spec != nil {
		for _, key := range spec.OutputKeys {
			if _, present := accumulator[key]; present {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
		}
	}
	extras := make([]string, 0, len(accumulator))
	for key := range accumulator {
		if _, ok := seen[key]; !ok {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	keys = append(keys, extras...)

	for _, key := range keys {
		value := accumulator[key]
		if value == nil {
			todo = append(todo, key)
			continue
		}
		done = append(done, fmt.Sprintf("  %s: %s", key, truncateRunes(fmt.Sprint(value), 150)))
	}
	return done, todo
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
