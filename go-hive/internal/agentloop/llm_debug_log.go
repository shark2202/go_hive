package agentloop

// Parity: framework/tracker/llm_debug_logger.py
//
// Opt-in local LLM-turn log: with HIVE_ENABLE_LLM_LOGS=1/true, every model call
// appends JSONL records to $HIVE_HOME/llm_logs/<ts>.jsonl for offline replay:
//
//   - session_header  (first turn of an execution_id, and again whenever the
//     system prompt or tool schema changes) — carries the large fields once;
//   - turn            one per model call — outputs plus a content-addressed
//     message delta (message_hashes = full ordered sequence, new_messages =
//     hash -> body for messages not yet emitted for this execution_id).
//
// The gate is upstream's `.strip().lower() in ("1", "true")`: only those two
// spellings enable the log; any other value is a silent no-op. Every failure is
// swallowed — logging must never break the agent.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/pyenv"
)

const llmDebugHashLength = 16

var llmDebugState struct {
	mu           sync.Mutex
	file         *os.File
	opened       bool
	headerHashes map[string]string
	seenMessages map[string]map[string]struct{}
}

// envOptIn is the package-local spelling of upstream's
// `.strip().lower() in ("1", "true")` gate.
func envOptIn(name string) bool { return pyenv.Is1OrTrue(name) }

// llmLogsEnabled mirrors upstream's opt-in gate.
func llmLogsEnabled() bool { return envOptIn("HIVE_ENABLE_LLM_LOGS") }

// LogLLMTurn writes the header (when stale) and one turn record. It never
// returns an error: callers are the hot path of a turn.
func LogLLMTurn(nodeID, streamID, executionID string, iteration int, systemPrompt string, messages []map[string]any, assistantText string, toolCalls, toolResults []map[string]any, tokenCounts map[string]any, tools []any) {
	if !llmLogsEnabled() {
		return
	}
	if strings.TrimSpace(executionID) == "" {
		// Upstream always has an execution id; without one the delta state has
		// no key, so there is nothing meaningful to log.
		return
	}
	file := llmDebugFile()
	if file == nil {
		return
	}
	timestamp := time.Now().Format(time.RFC3339)

	llmDebugState.mu.Lock()
	defer llmDebugState.mu.Unlock()

	serializedTools := serializeTools(tools)
	headerHash := contentHash(map[string]any{"system_prompt": systemPrompt, "tools": serializedTools})
	if llmDebugState.headerHashes[executionID] != headerHash {
		writeJSONLine(file, map[string]any{
			"_kind":         "session_header",
			"timestamp":     timestamp,
			"execution_id":  executionID,
			"node_id":       nodeID,
			"stream_id":     streamID,
			"header_hash":   headerHash,
			"system_prompt": systemPrompt,
			"tools":         serializedTools,
		})
		llmDebugState.headerHashes[executionID] = headerHash
	}

	seen := llmDebugState.seenMessages[executionID]
	if seen == nil {
		seen = map[string]struct{}{}
		llmDebugState.seenMessages[executionID] = seen
	}
	messageHashes := make([]string, 0, len(messages))
	newMessages := map[string]any{}
	for _, message := range messages {
		hash := contentHash(message)
		messageHashes = append(messageHashes, hash)
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		newMessages[hash] = message
	}

	writeJSONLine(file, map[string]any{
		"_kind":          "turn",
		"timestamp":      timestamp,
		"execution_id":   executionID,
		"node_id":        nodeID,
		"stream_id":      streamID,
		"iteration":      iteration,
		"header_hash":    headerHash,
		"message_hashes": messageHashes,
		"new_messages":   newMessages,
		"assistant_text": assistantText,
		"tool_calls":     nonNilSlice(toolCalls),
		"tool_results":   nonNilSlice(toolResults),
		"token_counts":   tokenCounts,
	})
}

// llmDebugFile lazily opens this process's JSONL file.
func llmDebugFile() *os.File {
	llmDebugState.mu.Lock()
	defer llmDebugState.mu.Unlock()
	if llmDebugState.opened {
		return llmDebugState.file
	}
	llmDebugState.opened = true
	home, err := hivehome.Resolve()
	if err != nil {
		return nil
	}
	dir := home.LLMLogsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(dir, time.Now().Format("20060102_150405")+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	llmDebugState.file = file
	if llmDebugState.headerHashes == nil {
		llmDebugState.headerHashes = map[string]string{}
	}
	if llmDebugState.seenMessages == nil {
		llmDebugState.seenMessages = map[string]map[string]struct{}{}
	}
	return file
}

// resetLLMDebugLogForTest closes the sink and clears the delta state.
func resetLLMDebugLogForTest() {
	llmDebugState.mu.Lock()
	file := llmDebugState.file
	llmDebugState.file = nil
	llmDebugState.opened = false
	llmDebugState.headerHashes = nil
	llmDebugState.seenMessages = nil
	llmDebugState.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}

func writeJSONLine(file *os.File, record map[string]any) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = file.Write(append(encoded, '\n'))
	_ = file.Sync()
}

// serializeTools reduces tool defs to the schema fields shown to the model.
func serializeTools(tools []any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		switch typed := tool.(type) {
		case map[string]any:
			out = append(out, map[string]any{
				"name":        stringValue(typed["name"]),
				"description": stringValue(typed["description"]),
				"parameters":  typed["parameters"],
			})
		case interface{ ToolSchema() map[string]any }:
			out = append(out, typed.ToolSchema())
		default:
			if encoded, err := json.Marshal(tool); err == nil {
				var decoded map[string]any
				if json.Unmarshal(encoded, &decoded) == nil {
					out = append(out, map[string]any{
						"name":        stringValue(decoded["name"]),
						"description": stringValue(decoded["description"]),
						"parameters":  decoded["parameters"],
					})
					continue
				}
			}
			out = append(out, map[string]any{"name": "", "description": "", "parameters": nil})
		}
	}
	return out
}

// contentHash mirrors upstream: canonical JSON (sorted keys, non-ASCII kept),
// sha256, first 16 hex chars.
func contentHash(payload any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte{}
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:llmDebugHashLength]
}

func nonNilSlice(value []map[string]any) []map[string]any {
	if value == nil {
		return []map[string]any{}
	}
	return value
}

// logLLMTurn adapts the loop's live turn state to LogLLMTurn.
func (l *AgentLoop) logLLMTurn(agentCtx *AgentContext, conv *NodeConversation, iteration int, messages []map[string]any, completion llm.Completion, callDicts []map[string]any, records []ToolCallRecord, tools []llm.ToolDef) {
	if !llmLogsEnabled() || agentCtx == nil {
		return
	}
	executionID := agentCtx.ExecutionID
	if strings.TrimSpace(executionID) == "" {
		executionID = agentCtx.AgentID
	}
	toolResults := make([]map[string]any, 0, len(records))
	for _, record := range records {
		toolResults = append(toolResults, map[string]any{
			"tool_use_id": record.ToolUseID,
			"tool_name":   record.ToolName,
			"content":     record.Content,
			"is_error":    record.IsError,
		})
	}
	tokenCounts := map[string]any{
		"input_tokens":          completion.InputTokens,
		"output_tokens":         completion.OutputTokens,
		"cached_tokens":         completion.CachedTokens,
		"cache_creation_tokens": completion.CacheCreationTokens,
	}
	serializedTools := make([]any, 0, len(tools))
	for _, tool := range tools {
		serializedTools = append(serializedTools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	systemPrompt := ""
	if conv != nil {
		systemPrompt = conv.SystemPrompt()
	}
	LogLLMTurn(agentCtx.AgentID, agentCtx.StreamID, executionID, iteration,
		systemPrompt, messages, completion.Content, callDicts, toolResults, tokenCounts, serializedTools)
}
