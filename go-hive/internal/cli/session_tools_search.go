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

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
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
		Name:        SearchMessagesToolName,
		Description: "Recall what was said in any past session — yours and the user's.\n\nThis is your long-term memory. Your active context only holds\nthe current session (and even that gets compacted as it grows).\nEverything the user told you, every answer you gave, every\ntool result you saw across every previous session lives in\nthis index. Reach for this tool whenever you can't see\nsomething in your active context but suspect it happened.\n\nCALL THIS when the user says any of:\n    • \"remember…\" / \"do you remember…\"\n    • \"last time\" / \"before\" / \"earlier\" / \"previously\"\n    • \"what did I ask about…\" / \"what did we decide…\"\n    • \"the X I mentioned\" / \"that thing we worked on\"\n    • any reference to a prior conversation, decision,\n      ticker, name, link, file, or fact you don't currently\n      have in front of you\n\nSearchable content (the only things this matches):\n    • user text the user typed\n    • your own prose responses\n    • tool result bodies (including full bodies of large\n      tool results that spilled to disk)\n\nPattern is a Rust-style regex (RE2 subset). For a name or\nticker, plain text is enough: ``\"AAPL\"``, ``\"(?i)stock\"``,\n``\"deadline\"``. See per-argument descriptions for inline\nflags and other options.\n\nReturns: dict with ``matches[]`` and ``total_matches``. Each\nmatch has ``session``, ``session_started_at`` and ``turn[]``\nof ``{role, content, is_hit?}`` entries. On invalid input or\nan unbound scope, returns ``{\"error\": \"...\", ...}`` instead\nof raising.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"description": "Rust-style regex (RE2 subset). Plain text matches plain text — for a name, ticker, or keyword you can pass it as-is (e.g. 'AAPL', 'deadline'). Use inline flags for case-insensitive match ('(?i)stock'), line-bound ^/$ ('(?m)'), or '.' matching newlines ('(?s)'). Lookaround and backreferences are not supported.",
					"type":        "string",
				},
				"session": map[string]any{
					"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
					"default":     nil,
					"description": "Optional. Restrict to one session id (e.g. 'session_20260501_145636_72fa023d'). Use to drill into a specific past conversation; omit to search across all sessions.",
				},
				"role": map[string]any{
					"default":     "all",
					"description": "Filter by who said it: 'user' (the user's typed messages), 'assistant' (your own prose), 'tool' (tool result bodies), or 'all' (default).",
					"enum":        []string{"user", "assistant", "tool", "all"},
					"type":        "string",
				},
				"match_source": map[string]any{
					"default":     "all",
					"description": "'events' searches in-chat message text. 'data' searches the spilled bodies of large tool results. 'all' (default) searches both in parallel.",
					"enum":        []string{"events", "data", "all"},
					"type":        "string",
				},
				"since": map[string]any{
					"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
					"default":     nil,
					"description": "ISO date or datetime. Only sessions started on or after this point are searched. Use to scope to recent memory.",
				},
				"until": map[string]any{
					"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
					"default":     nil,
					"description": "ISO date or datetime. Only sessions started before this point are searched (exclusive).",
				},
				"max_matches": map[string]any{
					"default":     20,
					"description": "Cap on returned hits. Default 20. Bump up if you suspect more relevant past context exists.",
					"type":        "integer",
				},
				"context": map[string]any{
					"default":     "turn",
					"description": "How much context to return per hit. 'turn' (default) returns the full conversational turn around the match — best for understanding what was happening. 'narrow' returns only the matched message. 'none' returns coordinates only.",
					"enum":        []string{"none", "narrow", "turn"},
					"type":        "string",
				},
				"turn_char_cap": map[string]any{
					"default":     4000,
					"description": "Total character budget per returned turn window. Default 4000.",
					"type":        "integer",
				},
				"per_message_char_cap": map[string]any{
					"default":     800,
					"description": "Character budget per individual message inside a turn — long messages get middle-truncated. Default 800.",
					"type":        "integer",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

// Parity: tools/src/memory_tools/timeline.py::search_timeline
func searchTimelineToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        SearchTimelineToolName,
		Description: "Search the dated timeline of events, facts and plans the user has mentioned across past conversations.\n\nEach item is one occurrence, dated to when it happened or held (relative dates like \"last week\" are\nalready resolved), with \"said_on\" for when the user mentioned it and the session/turn to open with\nsearch_messages for the full context. Results are oldest first.\n\nUse it for when / how many / how much / what changed / what is current questions: count distinct\nitems in the asked window by their \"when\", take the latest fact for current state, and compare dates for\nbefore/after. Use search_messages for exact wording or anything the user didn't state as a fact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"description": "Regex matched case-insensitively against each item's text and its alias phrasings, e.g. 'wedding', 'bak(e|ing)|cake|bread', 'museum'. Use '.' to list everything in a date range.",
					"type":        "string",
				},
				"since": map[string]any{
					"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
					"default":     nil,
					"description": "ISO date. Only items that happened (or held) on or after this date.",
				},
				"until": map[string]any{
					"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
					"default":     nil,
					"description": "ISO date. Only items that happened (or held) before this date (exclusive).",
				},
				"kind": map[string]any{
					"default":     "all",
					"description": "'event' (happened), 'fact' (a state or preference as of a date), 'plan' (intended), or 'all' (default).",
					"enum":        []string{"event", "fact", "plan", "all"},
					"type":        "string",
				},
				"max_results": map[string]any{
					"default":     60,
					"description": "Cap on returned items. Default 60.",
					"type":        "integer",
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
