package longmemeval

// Seeding a question's history as finished queen sessions.
//
// Parity: tests/e2e/longmemeval.py::seed_haystack
// Parity: tests/e2e/synthetic.py::past_session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	hivesession "github.com/shark2202/go_hive/go-hive/internal/session"
)

const turnSpacing = 2 * time.Minute

// SeedResult reports what a seed wrote.
type SeedResult struct {
	// Mapping is LongMemEval session id -> Hive session id, so a result can
	// say whether the evidence sessions were among what the queen retrieved.
	Mapping map[string]string
	// Sessions is the number of session directories written.
	Sessions int
	// Skipped counts haystack sessions that carried no content and were left
	// out, exactly as upstream skips them.
	Skipped int
}

// SeedHaystack writes the question's history as finished queen sessions under
// homeRoot/queens/<queenID>/sessions/, dated with the benchmark's session
// dates. It is the same events.jsonl layout the runtime writes, which is what
// search_messages reads.
func SeedHaystack(homeRoot, queenID string, item Item) (SeedResult, error) {
	if strings.TrimSpace(homeRoot) == "" {
		return SeedResult{}, fmt.Errorf("seed %s: empty hive home", item.QuestionID)
	}
	if !hivesession.ValidSegment(queenID) || queenID == "" {
		return SeedResult{}, fmt.Errorf("seed %s: invalid queen id %q", item.QuestionID, queenID)
	}
	if err := ValidateItem(&item); err != nil {
		return SeedResult{}, err
	}

	result := SeedResult{Mapping: map[string]string{}}
	for i := range item.HaystackSessionIDs {
		lmeID := item.HaystackSessionIDs[i]
		startedAt, err := ParseDate(item.HaystackDates[i])
		if err != nil {
			return SeedResult{}, fmt.Errorf("seed %s: session %s: %w", item.QuestionID, lmeID, err)
		}
		turns := make([]Turn, 0, len(item.HaystackSessions[i]))
		for _, turn := range item.HaystackSessions[i] {
			if strings.TrimSpace(turn.Content) == "" {
				continue
			}
			turns = append(turns, turn)
		}
		if len(turns) == 0 {
			result.Skipped++
			continue
		}
		sessionID, err := seedSession(homeRoot, queenID, lmeID, startedAt, turns)
		if err != nil {
			return SeedResult{}, err
		}
		result.Mapping[lmeID] = sessionID
		result.Sessions++
	}
	return result, nil
}

func seedSession(homeRoot, queenID, lmeID string, startedAt time.Time, turns []Turn) (string, error) {
	sessionID := seedSessionID(lmeID, startedAt)
	sessionDir := filepath.Join(homeRoot, "queens", queenID, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("seed session %s: %w", sessionID, err)
	}

	events := make([]map[string]any, 0, len(turns))
	t := startedAt
	for iteration, turn := range turns {
		t = t.Add(turnSpacing)
		stream := "queen"
		var eventType string
		var data map[string]any
		switch turn.Role {
		case "user":
			eventType = "client_input_received"
			data = map[string]any{"content": turn.Content, "image_count": 0}
		case "worker_tool":
			stream = "worker:" + sessionID
			eventType = "tool_call_completed"
			data = map[string]any{
				"tool_use_id": fmt.Sprintf("call_%d", iteration),
				"tool_name":   "terminal_exec",
				"result":      turn.Content,
				"is_error":    false,
			}
		default:
			eventType = "client_output_delta"
			data = map[string]any{
				"content":    turn.Content,
				"snapshot":   turn.Content,
				"iteration":  iteration,
				"inner_turn": 0,
			}
		}
		events = append(events, map[string]any{
			"type":           eventType,
			"stream_id":      stream,
			"node_id":        "queen",
			"execution_id":   sessionID,
			"data":           data,
			"timestamp":      t.Format(time.RFC3339),
			"correlation_id": nil,
			"colony_id":      nil,
			"seq":            len(events) + 1,
		})
	}

	var builder strings.Builder
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return "", err
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(builder.String()), 0o644); err != nil {
		return "", fmt.Errorf("seed session %s: %w", sessionID, err)
	}
	meta := map[string]any{"phase": "independent", "queen_id": queenID}
	if err := writeJSONFile(filepath.Join(sessionDir, "meta.json"), meta); err != nil {
		return "", fmt.Errorf("seed session %s: %w", sessionID, err)
	}
	return sessionID, nil
}

// seedSessionID derives a session id of the runtime's own shape
// (session_YYYYMMDD_HHMMSS_xxxxxxxx). Upstream uses uuid4; deriving the suffix
// from the LongMemEval session id keeps a fixture run byte-reproducible.
// Session ids never enter scoring.
func seedSessionID(lmeID string, startedAt time.Time) string {
	sum := sha256.Sum256([]byte(lmeID))
	return "session_" + startedAt.Format("20060102_150405") + "_" + hex.EncodeToString(sum[:])[:8]
}
