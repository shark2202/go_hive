package server

// Parity: framework/server/routes_execution.py::_compact_queen_conversation_in_place

// In-place queen conversation compaction: the colony fork copies the source DM
// session tree and then collapses the inherited transcript into a single
// summary message, tagged with inherited_from so the frontend can recognise it.
// The out-of-place variant used by compact-and-fork lives in
// routes_execution.go; this one rewrites the conversation it is given.

import (
	"context"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// CompactQueenConversationInPlace replaces queenDir/conversations with a single
// seq-0 user message holding the LLM summary, tagged inherited_from when set.
//
// The summary part is written before the stale parts are removed, so an
// interrupted run leaves a readable store (upstream writes the summary first
// for the same reason). nil, nil means "nothing to compact": no completer, no
// conversations dir, or no messages on disk.
//
// A cancelled or expired ctx aborts before the rewrite. A compaction transport
// that ignores ctx can finish after the wall-clock cap fired; dropping its
// result here keeps the copied raw transcript authoritative, which is what the
// fork's timeout path promises.
func CompactQueenConversationInPlace(ctx context.Context, queenDir string, completer llm.Completer, maxContextTokens int, inheritedFrom string) (*compactionSummary, error) {
	if completer == nil {
		return nil, nil
	}
	convsDir := filepath.Join(queenDir, "conversations")
	if !entryIsDir(convsDir) {
		return nil, nil
	}
	messages, err := readCompactionMessages(convsDir)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}

	summary, err := agentloop.LlmCompact(ctx, completer, messages, agentloop.LlmCompactOptions{
		MaxContextTokens:     maxContextTokens,
		PreserveUserMessages: true,
	})
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	store := agentloop.NewFileConversationStore(convsDir)
	defer store.Close()

	summaryMsg := agentloop.Message{Seq: 0, Role: agentloop.RoleUser, Content: summary}
	if inheritedFrom != "" {
		summaryMsg.InheritedFrom = &inheritedFrom
	}
	if err := store.WritePart(0, summaryMsg.ToStorageDict()); err != nil {
		return nil, err
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		return nil, err
	}
	if err := store.WriteMeta(map[string]any{
		"system_prompt":      "",
		"max_context_tokens": maxContextTokens,
	}); err != nil {
		return nil, err
	}
	removeStaleConversationParts(convsDir)
	return &compactionSummary{Messages: len(messages), Chars: utf8.RuneCountInString(summary), Text: summary}, nil
}

// removeStaleConversationParts drops every part but the seq-0 summary and the
// partials dir, matching _compact_queen_conversation_in_place's cleanup step.
// Best effort: a leftover part is redundant rather than corrupt, because the
// cursor the store now serves starts after the summary.
func removeStaleConversationParts(convsDir string) {
	partsDir := filepath.Join(convsDir, "parts")
	if entries, err := os.ReadDir(partsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == "0000000000.json" {
				continue
			}
			if filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			_ = os.Remove(filepath.Join(partsDir, entry.Name()))
		}
	}
	_ = os.RemoveAll(filepath.Join(convsDir, "partials"))
}
