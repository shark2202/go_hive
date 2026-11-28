package agentloop

// Response n-gram stall detection inside the turn loop.
//
// Python's EventLoopNode carries two independent stall guards in
// core/framework/agent_loop/internals/stall_detector.py:
//
//  1. ngram_similarity / is_stalled: consecutive assistant responses whose
//     character-bigram Jaccard similarity is at or above the configured
//     threshold stall the node. agent_loop.py appends each turn's assistant
//     text to recent_responses, keeps the last stall_detection_threshold (3)
//     entries, and on a hit publishes node_stalled with reason "Consecutive
//     similar responses detected", logs a CONTINUE step ("Stall detected
//     before judge evaluation") and fails the turn with "Node stalled: N
//     similar responses (85+ threshold)". This file ports that guard.
//  2. fingerprint_tool_calls / is_tool_doom_loop: exact per-turn tool-call
//     fingerprints, ported separately in doom_loop.go. That port's detector
//     warns then parks where Python escalates or parks at the same
//     threshold, so it remains a partial port of the second guard.
//
// The judge-based server/stall_check.go is a different mechanism: a turn-end
// judge reminder that can be disabled and never fails the turn inside the
// loop. It does not substitute for this guard.
//
// Parity: framework/agent_loop/internals/stall_detector.py::ngram_similarity
// Parity: framework/agent_loop/internals/stall_detector.py::is_stalled
// Parity: framework/agent_loop/agent_loop.py::execute (6f stall detection)

import (
	"fmt"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
)

const (
	// DefaultStallDetectionThreshold is the response window size: three
	// consecutive similar assistant responses stall the turn.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.stall_detection_threshold
	DefaultStallDetectionThreshold = 3

	// DefaultStallSimilarityThreshold is the minimum character-bigram Jaccard
	// similarity for two consecutive responses to count as "similar".
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.stall_similarity_threshold
	DefaultStallSimilarityThreshold = 0.85

	// ResponseStallReason is the node_stalled event's reason. Upstream's text
	// is exact; the dashboard filters on it.
	//
	// Parity: framework/agent_loop/internals/event_publishing.py::publish_stalled
	ResponseStallReason = "Consecutive similar responses detected"

	// ResponseStallStepFeedback is the runtime-log step's verdict feedback.
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f stall step)
	ResponseStallStepFeedback = "Stall detected before judge evaluation"

	// ResponseStallNodeError is the runtime-log node completion error. The
	// turn's own error is the longer "Node stalled: N similar responses…".
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f node log)
	ResponseStallNodeError = "Node stalled"

	recentResponsesCursorKey = "recent_responses"
	responseStallCounter     = "response_stall"
)

// ngramDefault is upstream's default n for the similarity call site.
const ngramDefault = 2

// NgramSimilarity is the Jaccard similarity of the two strings' character
// n-gram sets: |A∩B| / |A∪B|, in [0,1], with 1.0 for an exact match.
//
// Both strings are lowercased first and sliced by rune (Python indexes str by
// code point), so the sets are the same for ASCII and other single-code-point
// text. A whitespace-only string yields an empty set, as upstream's
// `if s.strip()` guard does, and an empty set on either side gives 0.0.
//
// Parity: framework/agent_loop/internals/stall_detector.py::ngram_similarity
func NgramSimilarity(s1, s2 string, n int) float64 {
	if n <= 0 || s1 == "" || s2 == "" {
		return 0
	}
	ngrams1 := ngramSet(s1, n)
	ngrams2 := ngramSet(s2, n)
	if len(ngrams1) == 0 || len(ngrams2) == 0 {
		return 0
	}
	intersection := 0
	for gram := range ngrams1 {
		if _, ok := ngrams2[gram]; ok {
			intersection++
		}
	}
	union := len(ngrams1) + len(ngrams2) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// ngramSet is upstream's inner _ngrams: the distinct character n-grams of the
// lowercased string. A whitespace-only string produces no grams.
func ngramSet(s string, n int) map[string]struct{} {
	grams := make(map[string]struct{})
	lowered := strings.ToLower(s)
	if strings.TrimSpace(lowered) == "" {
		return grams
	}
	runes := []rune(lowered)
	for i := 0; i+n <= len(runes); i++ {
		gram := string(runes[i : i+n])
		// Python's _ngrams filters each slice with `if s[i:i+n].strip()`;
		// all-whitespace grams (not just an all-whitespace response) are
		// excluded, while grams merely containing spaces stay in the set.
		if strings.TrimSpace(gram) == "" {
			continue
		}
		grams[gram] = struct{}{}
	}
	return grams
}

// IsResponseStalled reports whether the whole window of recent responses is
// mutually similar: at least threshold entries, a non-empty first entry, and
// every consecutive pair at or above similarityThreshold. One dissimilar pair
// resets the signal, which is what keeps "attempt 1" / "attempt 2" apart.
//
// A threshold <= 0 falls back to DefaultStallDetectionThreshold; the loop's
// config accessor resolves that before calling, and the guard keeps an
// accidental zero from panicking like upstream's index of an empty list.
//
// Parity: framework/agent_loop/internals/stall_detector.py::is_stalled
func IsResponseStalled(recentResponses []string, threshold int, similarityThreshold float64) bool {
	if threshold <= 0 {
		threshold = DefaultStallDetectionThreshold
	}
	if len(recentResponses) < threshold {
		return false
	}
	if recentResponses[0] == "" {
		return false
	}
	for i := 1; i < len(recentResponses); i++ {
		if NgramSimilarity(recentResponses[i], recentResponses[i-1], ngramDefault) < similarityThreshold {
			return false
		}
	}
	return true
}

// ResponseStalledError is the turn-loop failure the n-gram guard raises. The
// text mirrors upstream's AgentResult.error exactly:
// "Node stalled: 3 similar responses (85+ threshold)".
type ResponseStalledError struct {
	Threshold           int
	SimilarityThreshold float64
}

// NodeStalledSink publishes a node_stalled reason to the host's event bus.
type NodeStalledSink func(reason string)

func (e *ResponseStalledError) Error() string {
	threshold := DefaultStallDetectionThreshold
	similarity := DefaultStallSimilarityThreshold
	if e != nil {
		if e.Threshold > 0 {
			threshold = e.Threshold
		}
		if e.SimilarityThreshold > 0 {
			similarity = e.SimilarityThreshold
		}
	}
	// Upstream formats `similarity_threshold * 100` with `:.0f`; Go's %.0f
	// rounds the same way for the 0.85 default (85) and for configured values.
	return fmt.Sprintf("Node stalled: %d similar responses (%.0f+ threshold)", threshold, similarity*100)
}

// stallDetectionThreshold resolves the window size, with zero (an unset field
// on a partially-built config) falling back to the upstream default.
func (l *AgentLoop) stallDetectionThreshold() int {
	if l != nil && l.Config.StallDetectionThreshold > 0 {
		return l.Config.StallDetectionThreshold
	}
	return DefaultStallDetectionThreshold
}

// stallSimilarityThreshold resolves the similarity floor, with zero falling
// back to the upstream default.
func (l *AgentLoop) stallSimilarityThreshold() float64 {
	if l != nil && l.Config.StallSimilarityThreshold > 0 {
		return l.Config.StallSimilarityThreshold
	}
	return DefaultStallSimilarityThreshold
}

// responseStallEndsTurn applies the guard's observable effects: the node_stalled
// publication, the CONTINUE runtime step and stalled node completion, and the
// failed turn carrying ResponseStalledError.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f stall detection)
func (l *AgentLoop) responseStallEndsTurn(agentCtx *AgentContext, completion llm.Completion, inner int, iterStart time.Time, result *TurnResult) (TurnResult, error) {
	l.bumpBy(responseStallCounter, 1)
	if l.NodeStalledSink != nil {
		l.NodeStalledSink(ResponseStallReason)
	}
	// Upstream logs the stall before the judge: a CONTINUE step with the
	// iteration's text and tokens, then a failed node with exit_status
	// "stalled" and the short "Node stalled" error.
	l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
		StepIndex:       inner,
		LLMText:         completion.Content,
		InputTokens:     completion.InputTokens,
		OutputTokens:    completion.OutputTokens,
		LatencyMS:       stepLatencyMS(iterStart),
		Verdict:         "CONTINUE",
		VerdictFeedback: ResponseStallStepFeedback,
	})
	nodeError := ResponseStallNodeError
	l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
		Success:    false,
		Error:      &nodeError,
		TotalSteps: inner + 1,
		ExitStatus: "stalled",
	})
	result.Success = false
	result.FinalText = completion.Content
	result.InnerTurns = inner + 1
	return *result, &ResponseStalledError{
		Threshold:           l.stallDetectionThreshold(),
		SimilarityThreshold: l.stallSimilarityThreshold(),
	}
}

// restoreRecentResponses reads the persisted stall window. Upstream restores
// it from the cursor's "recent_responses" key on resume; a missing store, key,
// or malformed value restores an empty window instead of failing the turn.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
func (l *AgentLoop) restoreRecentResponses(conv *NodeConversation) []string {
	if conv == nil || conv.Store() == nil {
		return nil
	}
	cursor, err := conv.Store().ReadCursor()
	if err != nil || cursor == nil {
		return nil
	}
	return cursorRecentResponses(cursor)
}

// cursorRecentResponses decodes the JSON-decoded cursor value. The file store
// reads arrays as []any; a cursor built in-process may hold []string.
func cursorRecentResponses(cursor map[string]any) []string {
	raw, ok := cursor[recentResponsesCursorKey]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		responses := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			responses = append(responses, text)
		}
		return responses
	default:
		return nil
	}
}

// persistRecentResponses writes the stall window under the upstream cursor key.
// Best-effort like the rest of the loop's cursor writes: a broken store must
// not fail a turn. An empty window is written as [] (not null), matching
// Python's recent_responses.clear() followed by write_cursor.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (l *AgentLoop) persistRecentResponses(conv *NodeConversation, responses []string) {
	if conv == nil || conv.Store() == nil {
		return
	}
	store := conv.Store()
	value := append([]string{}, responses...)
	if mutator, ok := store.(CursorMutator); ok {
		_ = mutator.MutateCursor(func(cursor map[string]any) {
			cursor[recentResponsesCursorKey] = value
		})
		return
	}
	cursor, err := store.ReadCursor()
	if err != nil {
		return
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	cursor[recentResponsesCursorKey] = value
	_ = store.WriteCursor(cursor)
}

// clearRecentResponses empties both the in-memory window and its cursor entry.
// Upstream clears recent_responses on the paths that reset the turn's stall
// state (the tool doom-loop escalation and park paths, and the queen-input
// resume); the caller owns the slice, so the cleared window is returned.
func (l *AgentLoop) clearRecentResponses(conv *NodeConversation, responses []string) []string {
	if len(responses) == 0 {
		return responses
	}
	l.persistRecentResponses(conv, nil)
	return responses[:0]
}
