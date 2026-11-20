package agentloop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: framework/agent_loop/internals/synthetic_tools.py ask_user_prompt.
const askUserPrompt = `Use this tool when you need to ask the user questions during execution. Reach for it when:

- The task is ambiguous and the user needs to choose an approach
- You need missing information to continue
- You want approval before taking a meaningful action
- A decision has real trade-offs the user should weigh in on
- You want post-task feedback, or to offer saving a skill or updating memory

Usage notes:
- Users will always be able to select "Other" to provide custom text input, so do not include catch-all options like "Other" or "Something else" yourself.
- Each option is a plain string. Do NOT wrap options in ` + "`" + `{"label": "..."}` + "`" + ` or ` + "`" + `{"value": "..."}` + "`" + ` objects — pass the raw choice text directly, e.g. ` + "`" + `"Email"` + "`" + `, not ` + "`" + `{"label": "Email"}` + "`" + `.
- If you recommend a specific option, make that the first option in the list and append " (Recommended)" to the end of its text.
- Call this tool whenever you need the user's response.
- The prompt field must be plain text only.
- Do not include XML, pseudo-tags, or inline option lists inside prompt.
- Omit options only when the question truly requires a free-form response the user must type out, such as describing an idea or pasting an error message.
- Do not repeat the questions in your normal text response. The widget renders them, so keep any surrounding text to a brief intro only.
Example — single question with options:
{"questions": [{"id": "next", "prompt": "What would you like to do?", "options": ["Build a new agent (Recommended)", "Modify existing agent", "Run tests"]}]}

Example — batch:
{"questions": [
  {"id": "scope", "prompt": "What scope?", "options": ["Full", "Partial"]},
  {"id": "format", "prompt": "Output format?", "options": ["PDF", "CSV", "JSON"]},
  {"id": "details", "prompt": "Any special requirements?"}
]}

Example — free-form (queen only):
{"questions": [{"id": "idea", "prompt": "Describe the agent you want to build."}]}
`

var (
	reCloseQuestionTag = regexp.MustCompile(`(?i)\s*</\s*question\s*>\s*`)
	reInlineOptions    = regexp.MustCompile(`(?ims)^\s*_?options\s*[:=]\s*(\[.*?\])\s*$`)
	reTripleNewline    = regexp.MustCompile(`\n{3,}`)

	reRawSetOutputKey   = regexp.MustCompile(`"key"\s*:\s*"(\w+)"`)
	reRawSetOutputValue = regexp.MustCompile(`"value"\s*:\s*"`)
)

// Parity: framework/agent_loop/internals/synthetic_tools.py sanitize_ask_user_inputs.
func SanitizeAskUserInputs(rawQuestion, rawOptions any) (string, []string) {
	_ = rawOptions
	if rawQuestion == nil {
		return "", nil
	}
	q, ok := rawQuestion.(string)
	if !ok {
		q = fmt.Sprint(rawQuestion)
	}

	q = reCloseQuestionTag.ReplaceAllString(q, "\n")

	var recovered []string
	loc := reInlineOptions.FindStringSubmatchIndex(q)
	if loc != nil {
		arrayText := q[loc[2]:loc[3]]
		var parsed any
		if err := json.Unmarshal([]byte(arrayText), &parsed); err == nil {
			if items, ok := parsed.([]any); ok {
				cleaned := make([]string, 0, len(items))
				for _, item := range items {
					text := strings.TrimSpace(fmt.Sprint(item))
					if text != "" {
						cleaned = append(cleaned, text)
					}
				}
				if len(cleaned) >= 1 && len(cleaned) <= 8 {
					recovered = cleaned
					q = q[:loc[0]] + q[loc[1]:]
				}
			}
		}
	}

	q = reTripleNewline.ReplaceAllString(q, "\n\n")
	q = strings.TrimSpace(q)
	return q, recovered
}

// AskUserWaitingContent is what an ask_user call returns to the model: the turn
// parks on the user, so the answer is pending rather than failed. Shared by the
// chat queen and the colony's persistent overseer, which ask in the same
// conversation the session front end renders.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
const AskUserWaitingContent = "Waiting for user input..."

// NormalizeAskUserQuestions coalesces the model's ask_user input into the
// {id, prompt, options?} shape a parked turn records: it applies the same
// self-healing upstream does — recover options the model crammed into the
// prompt, default a missing id, and drop option lists too short to be a real
// choice. A non-empty second return is the actionable error text.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/agent_loop/internals/synthetic_tools.py::sanitize_ask_user_inputs
func NormalizeAskUserQuestions(input map[string]any) ([]map[string]any, string) {
	const missingQuestions = "ERROR: ask_user requires a non-empty 'questions' array. " +
		"Each entry must have {id, prompt, options?}. Example: " +
		`{"questions": [{"id": "q1", "prompt": "What now?", "options": ["A", "B"]}]}`
	raw, ok := input["questions"]
	if !ok || raw == nil {
		return nil, missingQuestions
	}
	var entries []any
	switch typed := raw.(type) {
	case []any:
		entries = typed
	case []map[string]any:
		entries = make([]any, len(typed))
		for i, entry := range typed {
			entries[i] = entry
		}
	default:
		return nil, missingQuestions
	}
	if len(entries) == 0 {
		return nil, missingQuestions
	}

	questions := make([]map[string]any, 0, len(entries))
	for i, entry := range entries {
		question, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := ""
		if rawID, present := question["id"]; present && rawID != nil {
			id = strings.TrimSpace(anyString(rawID, ""))
			if id == "" {
				id = fmt.Sprint(rawID)
			}
		}
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		rawPrompt := question["prompt"]
		if rawPrompt == nil {
			rawPrompt = question["question"]
		}
		prompt, recovered := SanitizeAskUserInputs(rawPrompt, question["options"])
		options := askUserOptionStrings(question["options"])
		if len(options) == 0 {
			options = recovered
		}
		if len(options) < 2 {
			options = nil
		}
		out := map[string]any{"id": id, "prompt": prompt}
		if len(options) > 0 {
			out["options"] = options
		}
		questions = append(questions, out)
	}
	if len(questions) == 0 {
		return nil, "ERROR: no valid question objects in 'questions'. " +
			"Each entry must be an object with 'id' and 'prompt'."
	}
	return questions, ""
}

// askUserOptionStrings flattens the option shapes models emit into plain
// strings (upstream passes plain choices; a model sometimes wraps them).
func askUserOptionStrings(raw any) []string {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := ""
		switch typed := item.(type) {
		case string:
			text = typed
		case map[string]any:
			for _, key := range []string{"label", "value", "text", "name"} {
				if candidate := strings.TrimSpace(anyString(typed[key], "")); candidate != "" {
					text = candidate
					break
				}
			}
		case nil:
		default:
			text = fmt.Sprint(typed)
		}
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_ask_user_tool.
func BuildAskUserTool() llm.ToolDef {
	return llm.ToolDef{
		Name:        "ask_user",
		Description: askUserPrompt,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"minItems":    1,
					"maxItems":    8,
					"description": "List of questions to present to the user.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{
								"type":        "string",
								"description": "Short identifier for this question (used in the response).",
							},
							"prompt": map[string]any{
								"type":        "string",
								"description": "The question text shown to the user.",
							},
							"options": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
								"description": "2-3 predefined choices as plain strings " +
									`(e.g. ["Yes", "No", "Maybe"]). Do NOT ` +
									`wrap items in {"label": "..."} or ` +
									`{"value": "..."} objects — pass the raw ` +
									"choice text directly. The UI appends an " +
									"'Other' free-text input automatically, " +
									"so don't include catch-all options. " +
									"Omit only when the user must type a free-form answer.",
								"minItems": 2,
								"maxItems": 3,
							},
						},
						"required": []string{"id", "prompt"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_collect_result_tool.
func BuildCollectResultTool() llm.ToolDef {
	return llm.ToolDef{
		Name: "collect_result",
		Description: "Fetch the result of a tool that is running in the background — one " +
			`that returned {"status":"started","handle":"bg_…"}. Pass that handle. ` +
			"It waits until the work is done and returns the real result. If a new " +
			`message for you arrives first it returns {"status":"pending"} at once, ` +
			"so you can answer it; call this again to go back to waiting. This " +
			"is how you retrieve image_generate and slow terminal_exec results. " +
			"If the result contains auto_backgrounded=true and job_id, the " +
			"command is still a terminal job: use terminal_job_logs(job_id, " +
			"wait_until_exit=true) for its final output/status, or " +
			"terminal_job_manage to stop it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"handle": map[string]any{
					"type":        "string",
					"description": `The handle from the backgrounded call (e.g. "bg_1").`,
				},
				"wait_seconds": map[string]any{
					"type":        "integer",
					"description": "Stop waiting after this many seconds. Leave it out to wait until the work is done.",
					"minimum":     0,
				},
			},
			"required": []string{"handle"},
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_suggest_colony_tool.
func BuildSuggestColonyTool() llm.ToolDef {
	return llm.ToolDef{
		Name: "suggest_colony",
		Description: "Propose forking this session into a colony. Call this when " +
			"the current work needs parallelization, scheduling, " +
			"background/recurring execution, or otherwise should outlive " +
			"this chat. The frontend opens a 'Create Colony' popup " +
			"pre-filled with this colony_id and the current queen " +
			"auto-selected; the user reviews and confirms. On accept, " +
			"this chat's conversation is compacted into the new colony's " +
			"queen seed and this session locks — so make sure you've " +
			"left the conversation in a self-contained state. On dismiss " +
			"the chat continues normally. Do NOT call this for one-shot " +
			"work the user wants finished in THIS chat — do that work " +
			"yourself with your independent toolkit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"colony_id": map[string]any{
					"type": "string",
					"description": "Lowercase alphanumeric+underscore slug for the " +
						"proposed colony (e.g. 'morning_hn_digest', " +
						"'fintech_competitor_research'). Pre-fills the " +
						"popup's name field; the user can edit it before " +
						"confirming.",
				},
				"reason": map[string]any{
					"type": "string",
					"description": "Optional one-sentence rationale shown in the " +
						"popup — why parallel/persistent execution helps " +
						"here. Be specific (e.g. '23 competitor profiles " +
						"to research in parallel' rather than 'this is a " +
						"lot of work').",
				},
			},
			"required": []string{"colony_id"},
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_set_output_tool.
func BuildSetOutputTool(outputKeys []string) *llm.ToolDef {
	if len(outputKeys) == 0 {
		return nil
	}
	keys := append([]string(nil), outputKeys...)
	enumValues := make([]any, len(keys))
	for i, key := range keys {
		enumValues[i] = key
	}
	return &llm.ToolDef{
		Name: "set_output",
		Description: "Set an output value for this node. Call once per output key. " +
			"Use this for brief notes, counts, status, and file references — " +
			"NOT for large data payloads. When a tool result was saved to a " +
			"data file, pass the filename as the value " +
			"(e.g. 'google_sheets_get_values_1.txt') so the next phase can " +
			"load the full data. Values exceeding ~2000 characters are " +
			"auto-saved to data files. " +
			"Valid keys: " + pyStringListRepr(keys),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key": map[string]any{
					"type":        "string",
					"description": "Output key. Must be one of: " + pyStringListRepr(keys),
					"enum":        enumValues,
				},
				"value": map[string]any{
					"type":        "string",
					"description": "The output value — a brief note, count, status, or data filename reference.",
				},
			},
			"required": []string{"key", "value"},
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_escalate_tool.
//
// The escalate tool belongs to the legacy primary worker stream ("worker", no
// colon). Upstream does not offer it to parallel workers (stream_id="worker:<id>");
// this port spawns only the parallel shape, so colony.workerTools keeps it off
// the surface and the dispatch gate refuses it there. The builder stays for the
// legacy path (and the dispatch it feeds) so the port does not drop upstream
// behaviour that a future legacy worker entry point would need.
func BuildEscalateTool() llm.ToolDef {
	return llm.ToolDef{
		Name: EscalateToolName,
		Description: "Escalate to the queen when requesting user input, " +
			"blocked by errors, missing " +
			"credentials, or ambiguous constraints that require supervisor " +
			"guidance. Include a concise reason and optional context. " +
			"The node will pause until the queen injects guidance.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Short reason for escalation (e.g. 'Tool repeatedly failing').",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Optional diagnostic details for the queen.",
				},
			},
			"required": []string{"reason"},
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py build_report_to_parent_tool.
func BuildReportToParentTool(reportSchema map[string]any) llm.ToolDef {
	var dataParam map[string]any
	required := []string{"status", "summary"}
	if len(reportSchema) > 0 {
		dataParam = copyMap(reportSchema)
		if _, ok := dataParam["type"]; !ok {
			dataParam["type"] = "object"
		}
		dataParam["description"] = "REQUIRED structured receipt — must conform to this schema " +
			"(required fields present, enum values exact). The overseer relies " +
			"on it; do not omit it or return a free-form object."
		required = []string{"status", "summary", "data"}
	} else {
		dataParam = map[string]any{
			"type":        "object",
			"description": "Optional structured payload (rows fetched, IDs processed, files written, etc.) that the overseer can merge into its final summary.",
		}
	}
	return llm.ToolDef{
		Name: "report_to_parent",
		Description: "Send a structured report back to the parent overseer and " +
			"terminate. Call this when you have finished your task " +
			"(success, partial, or failed) or cannot make further " +
			"progress. Your loop ends after this call -- do not call any " +
			"other tool afterwards. The overseer reads the summary + " +
			"data fields and aggregates them into a user-facing response.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []any{"success", "partial", "failed"},
					"description": "Overall outcome. 'success' = task complete. 'partial' = some progress but incomplete. 'failed' = could not make progress.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "One-paragraph narrative for the overseer. What you did, what you found, and any notable issues.",
				},
				"data": dataParam,
			},
			"required": required,
		},
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py handle_report_to_parent.
func HandleReportToParent(toolInput map[string]any) ToolResult {
	status := strings.ToLower(strings.TrimSpace(anyString(toolInput["status"], "success")))
	switch status {
	case "success", "partial", "failed":
	default:
		status = "success"
	}
	summary := strings.TrimSpace(anyString(toolInput["summary"], ""))
	if summary == "" {
		summary = fmt.Sprintf("(worker returned %s with no summary)", status)
	}
	data, ok := toolInput["data"].(map[string]any)
	if !ok {
		if raw := toolInput["data"]; raw != nil {
			data = map[string]any{"value": raw}
		} else {
			data = map[string]any{}
		}
	}
	toolInput["_normalised"] = map[string]any{
		"status":  status,
		"summary": summary,
		"data":    data,
	}
	return ToolResult{
		Content: fmt.Sprintf("Report delivered to overseer (status=%s). This worker will terminate now.", status),
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py handle_set_output.
func HandleSetOutput(toolInput map[string]any, outputKeys []string) ToolResult {
	key := anyString(toolInput["key"], "")
	value := anyString(toolInput["value"], "")
	validKeys := outputKeys
	if validKeys == nil {
		validKeys = []string{}
	}

	if key == "" {
		if raw, hasRaw := toolInput["_raw"].(string); hasRaw {
			if match := reRawSetOutputKey.FindStringSubmatch(raw); match != nil {
				key = match[1]
			}
			if match := reRawSetOutputValue.FindStringSubmatchIndex(raw); match != nil {
				value = raw[match[1]:]
				for _, suffix := range []string{"\"}\n", "\"}", "\""} {
					if strings.HasSuffix(value, suffix) {
						value = value[:len(value)-len(suffix)]
						break
					}
				}
			}
			if key != "" {
				toolInput["key"] = key
				toolInput["value"] = value
			}
		}
	}

	for _, valid := range validKeys {
		if key == valid {
			return ToolResult{
				Content: fmt.Sprintf("Output '%s' set successfully.", key),
			}
		}
	}
	return ToolResult{
		Content: fmt.Sprintf("Invalid output key '%s'. Valid keys: %s", key, pyStringListRepr(validKeys)),
		IsError: true,
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py SYNTHETIC_PHASE_TOOL_BUILDERS.
var SyntheticPhaseToolBuilders = map[string]func() llm.ToolDef{
	"suggest_colony": BuildSuggestColonyTool,
}

func anyString(value any, fallback string) string {
	if str, ok := value.(string); ok {
		return str
	}
	return fallback
}

func pyStringListRepr(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
