package cli

// Session history search: `search_messages` and `search_timeline`.
//
// The queen can already read her active context; these two tools reach every
// past session in the same queen's directory, which is the upstream contract
// (docs/key_concepts/queen.md: "every past session is searchable with
// search_messages"). The search itself is memory.SearchMessages /
// memory.SearchTimeline — the ported engines the audit found with zero
// production callers. This file is only the tool surface: schemas, parameter
// coercion, and the scope the search may read.
//
// Scope is resolved from the session directory this catalogue was built for
// (…/queens/<queen>/sessions/<id>), never from model input: the model chooses
// a pattern, not a filesystem. A `session` filter is accepted only as a bare
// session id, and every read is enumerated from the queen's own sessions/
// directory, so a crafted value cannot walk out of it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools"
)

// SearchMessagesToolName / SearchTimelineToolName are the upstream tool names
// (tools/src/memory_tools/tool.py, timeline.py).
const (
	SearchMessagesToolName = "search_messages"
	SearchTimelineToolName = "search_timeline"
)

// searchToolDefs returns both definitions in catalogue order. search_timeline
// is advertised only when the memory_timeline feature is on, matching the
// upstream flag gate.
func searchToolDefs(home hivehome.Home) []llm.ToolDef {
	defs := []llm.ToolDef{searchMessagesToolDef()}
	if memoryTimelineAvailable(home) {
		defs = append(defs, searchTimelineToolDef())
	}
	return defs
}

// memoryTimelineAvailable mirrors the upstream `memory_timeline` feature flag.
// The switch lives in internal/server (config_features.go), which this package
// already imports for senders and the scanner gate.
func memoryTimelineAvailable(home hivehome.Home) bool {
	return hiveserver.MemoryTimelineEnabled(home)
}

// Parity: tools/src/memory_tools/tool.py::search_messages
func searchMessagesToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: SearchMessagesToolName,
		Description: "Recall what was said in any past session — yours and the user's. Your active context " +
			"only holds the current session, so reach for this whenever the user says \"remember…\", " +
			"\"last time\", \"what did we decide\", or refers to a prior conversation, decision, fact, " +
			"file or link you no longer have in front of you.\n\n" +
			"Searchable content: the user's typed text, your own prose replies, and tool result bodies " +
			"(including large results that spilled to disk). Tool names and arguments are not searchable.\n\n" +
			"Pattern is a Rust-style regex (RE2 subset): plain text matches plain text, and inline flags " +
			"work (\"(?i)stock\"). Lookaround and backreferences are rejected.\n\n" +
			"Returns {matches: [{session, session_started_at, turn: [{role, content, is_hit?}]}], " +
			"total_matches}. Invalid input returns {error, message} instead of raising.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type": "string",
					"description": "Rust-style regex (RE2 subset). Plain text matches plain text — pass a " +
						"name or keyword as-is ('AAPL', 'deadline'). Inline flags: '(?i)' case-insensitive, " +
						"'(?m)' line anchors, '(?s)' dot matches newlines.",
				},
				"session": map[string]any{
					"type": "string",
					"description": "Optional. Restrict to one session id (e.g. " +
						"'session_20260501_145636_72fa023d'). Omit to search all of your past sessions.",
				},
				"role": map[string]any{
					"type":        "string",
					"enum":        []string{"user", "assistant", "tool", "all"},
					"description": "Who said it: 'user' (typed messages), 'assistant' (your prose), 'tool' (tool result bodies), or 'all' (default).",
				},
				"match_source": map[string]any{
					"type":        "string",
					"enum":        []string{"events", "data", "all"},
					"description": "'events' searches in-chat text, 'data' searches spilled tool-result bodies, 'all' (default) searches both.",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only sessions started on or after this point are searched.",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only sessions started before this point are searched (exclusive).",
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Maximum matches to return (default 20). total_matches still reports every hit.",
				},
				"context": map[string]any{
					"type":        "string",
					"enum":        []string{"none", "narrow", "turn"},
					"description": "How much surrounding conversation each match carries: 'none', 'narrow' (the hit only), or 'turn' (default: the exchange around it).",
				},
				"turn_char_cap": map[string]any{
					"type":        "integer",
					"description": "Character budget for one match's turn window (default 4000).",
				},
				"per_message_char_cap": map[string]any{
					"type":        "integer",
					"description": "Character budget for each message inside a turn window (default 800).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

// Parity: tools/src/memory_tools/timeline.py::search_timeline
func searchTimelineToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: SearchTimelineToolName,
		Description: "Search the dated timeline of events, facts and plans the user has mentioned across past " +
			"conversations. Each item is one occurrence, dated to when it happened or held (relative dates " +
			"like \"last week\" are already resolved), with said_on for when the user mentioned it and the " +
			"session to open with search_messages for full context. Results are oldest first.\n\n" +
			"Use it for when / how many / how much / what changed / what is current questions. Use " +
			"search_messages for exact wording or anything the user did not state as a fact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex over the timeline text (same RE2 subset as search_messages).",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only items that happened on or after this point are returned.",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only items that happened before this point are returned (exclusive).",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"event", "fact", "plan", "all"},
					"description": "Kind of occurrence: 'event', 'fact', 'plan', or 'all' (default).",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum items to return (default 60, capped at 300).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

// executeSearchTool runs one history-search call. handled is false for any
// other tool name so the caller can keep its dispatch order.
func (c *sessionToolCatalogue) executeSearchTool(name string, input map[string]any) (content string, isError bool, handled bool) {
	if name != SearchMessagesToolName && name != SearchTimelineToolName {
		return "", false, false
	}
	ref, toolErr := c.searchScope()
	if toolErr != nil {
		return tools.PythonJSONDumps(toolErr.ToMap()), true, true
	}
	params := searchParams(input)
	if !searchSessionFilterValid(params.session) {
		return tools.PythonJSONDumps(map[string]any{
			"error":   "filter_invalid",
			"message": "session=" + params.session + " is not a session id; pass the bare id from a previous result",
		}), true, true
	}
	if name == SearchMessagesToolName {
		result, toolErr := memory.SearchMessages(c.home.Root, ref, memory.SearchMessagesOptions{
			Pattern:           params.pattern,
			Session:           params.session,
			Role:              params.role,
			MatchSource:       params.matchSource,
			Since:             params.since,
			Until:             params.until,
			MaxMatches:        params.maxMatches,
			Context:           params.context,
			TurnCharCap:       params.turnCharCap,
			PerMessageCharCap: params.perMessageCharCap,
		})
		if toolErr != nil {
			return tools.PythonJSONDumps(toolErr.ToMap()), true, true
		}
		return tools.PythonJSONDumps(map[string]any{
			"matches":       result.Matches,
			"total_matches": result.TotalMatches,
		}), false, true
	}
	if !memoryTimelineAvailable(c.home) {
		return tools.PythonJSONDumps(map[string]any{
			"error":   "feature_disabled",
			"message": "the memory_timeline feature is off; turn it on in Developer settings to search the dated timeline",
		}), true, true
	}
	result, toolErr := memory.SearchTimeline(c.home.Root, ref, memory.SearchTimelineOptions{
		Pattern:    params.pattern,
		Since:      params.since,
		Until:      params.until,
		Kind:       params.kind,
		MaxResults: params.maxResults,
	})
	if toolErr != nil {
		return tools.PythonJSONDumps(toolErr.ToMap()), true, true
	}
	return tools.PythonJSONDumps(map[string]any{
		"items":                  result.Items,
		"total":                  result.Total,
		"truncated":              result.Truncated,
		"sessions_with_timeline": result.SessionsWithTimeline,
	}), false, true
}

type searchToolParams struct {
	pattern           string
	session           string
	role              string
	matchSource       string
	since             string
	until             string
	maxMatches        int
	context           string
	turnCharCap       int
	perMessageCharCap int
	kind              string
	maxResults        int
}

func searchParams(input map[string]any) searchToolParams {
	return searchToolParams{
		pattern:           searchStringParam(input, "pattern"),
		session:           searchStringParam(input, "session"),
		role:              searchStringParam(input, "role"),
		matchSource:       searchStringParam(input, "match_source"),
		since:             searchStringParam(input, "since"),
		until:             searchStringParam(input, "until"),
		maxMatches:        searchIntParam(input, "max_matches"),
		context:           searchStringParam(input, "context"),
		turnCharCap:       searchIntParam(input, "turn_char_cap"),
		perMessageCharCap: searchIntParam(input, "per_message_char_cap"),
		kind:              searchStringParam(input, "kind"),
		maxResults:        searchIntParam(input, "max_results"),
	}
}

func searchStringParam(input map[string]any, key string) string {
	raw, ok := input[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func searchIntParam(input map[string]any, key string) int {
	switch raw := input[key].(type) {
	case int:
		return raw
	case int64:
		return int(raw)
	case float64:
		return int(raw)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

// searchScope resolves the queen this session belongs to and refuses to search
// when her directory is not on disk. A catalogue built for a root outside the
// queen tree (a bare temp dir in tests, a colony path) gets scope_not_found
// rather than a silently empty answer.
func (c *sessionToolCatalogue) searchScope() (memory.ScopeRef, *memory.ToolError) {
	queen := queenScopeFromSessionRoot(c.home, c.root)
	ref := memory.ScopeRef{Scope: memory.SearchScopeQueens, Owner: queen}
	if c.home.Root == "" {
		return ref, &memory.ToolError{
			Code:    "scope_unbound",
			Message: "no Hive home is attached to this session, so past sessions cannot be reached",
		}
	}
	dir := memory.SearchOwnerDir(c.home.Root, ref.Scope, ref.Owner)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ref, &memory.ToolError{
			Code:      "scope_not_found",
			Message:   "queen '" + queen + "' not found on disk",
			Available: memory.SearchListOwners(c.home.Root, ref.Scope),
		}
	}
	return ref, nil
}

// searchSessionFilterValid accepts only a bare session directory name. The
// engines enumerate sessions from the queen's own sessions/ directory, so this
// is defence in depth: a value carrying a separator or ".." is rejected here
// instead of being handed to the filter.
func searchSessionFilterValid(session string) bool {
	if session == "" {
		return true
	}
	if strings.ContainsAny(session, `/\`) || strings.Contains(session, "..") {
		return false
	}
	return filepath.Base(session) == session && session != "." && session != ".."
}
