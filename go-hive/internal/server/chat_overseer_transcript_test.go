package server

// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// (the session conversation records the worker/overseer reply)
// Parity-exempt: Go-side front-end recorder. The overseer is a separate
// process in this port; its final text is appended to the chat transcript
// here so a restarted session rebuilds the reply it saw live.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
)

// readSessionEventLog parses the session's events.jsonl, the file the
// dashboard replays a reload from.
func readSessionEventLog(root string) ([]map[string]any, error) {
	file, err := os.Open(filepath.Join(root, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	events := []map[string]any{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func overseerTranscriptColony(t *testing.T) (*colony.ColonyRuntime, *eventbus.EventBus) {
	t.Helper()
	colonyBus := eventbus.NewEventBus()
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         &fakeChatLLM{},
		EventBus:    colonyBus,
		StreamID:    "colony_overseer_transcript",
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime, colonyBus
}

func transcriptAssistantTexts(parts []map[string]any) []string {
	out := []string{}
	for _, part := range parts {
		if role, _ := part["role"].(string); role != agentloop.RoleAssistant {
			continue
		}
		content, _ := part["content"].(string)
		out = append(out, content)
	}
	return out
}

// TestOverseerReplyRecordedOnceInChatTranscript proves a turn's several
// client_output_delta frames become one assistant transcript row containing
// only the final snapshot, and a repeated execution_completed cannot append
// a second copy.
// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// (the session conversation records the worker/overseer reply)
func TestOverseerReplyRecordedOnceInChatTranscript(t *testing.T) {
	colonyRuntime, colonyBus := overseerTranscriptColony(t)
	root := t.TempDir()
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      root,
		SessionID: "s-overseer-transcript",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	const executionID = "overseer-turn-1"
	colonyBus.EmitExecutionStarted(overseerStreamID, executionID, nil, nil, nil)
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"checking the north field", "checking the north field", eventbus.StrPtr(executionID), eventbus.IntPtr(1), 0)
	// Text before a tool call is a preamble: the recorder must drop it rather
	// than treat it as the turn's final answer.
	colonyBus.EmitToolCallStarted(overseerStreamID, overseerStreamID, "call-1", "read_field", map[string]any{"field": "north"}, eventbus.StrPtr(executionID))
	colonyBus.EmitToolCallCompleted(overseerStreamID, overseerStreamID, "call-1", "read_field", "clear", false, eventbus.StrPtr(executionID))
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"the north field is clear", "the north field is clear", eventbus.StrPtr(executionID), eventbus.IntPtr(2), 0)
	colonyBus.EmitExecutionCompleted(overseerStreamID, executionID, nil, nil, nil)
	// A duplicate terminal event (two bus consumers seeing the same turn)
	// must not append a second assistant row.
	colonyBus.EmitExecutionCompleted(overseerStreamID, executionID, nil, nil, nil)

	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		if err != nil {
			return false
		}
		texts := transcriptAssistantTexts(parts)
		return len(texts) == 1 && strings.Contains(texts[0], "the north field is clear")
	}) {
		parts, _ := chatRuntime.Transcript()
		t.Fatalf("assistant transcript rows = %#v, want one final overseer reply", transcriptAssistantTexts(parts))
	}
	if err := chatRuntime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	messages := chatRuntime.conv.Messages()
	var assistant []agentloop.Message
	for _, message := range messages {
		if message.Role == agentloop.RoleAssistant {
			assistant = append(assistant, message)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("assistant messages = %d (%#v), want exactly one per turn", len(assistant), assistant)
	}
	if !strings.HasPrefix(assistant[0].Content, overseerTranscriptPrefix) ||
		!strings.Contains(assistant[0].Content, "the north field is clear") {
		t.Fatalf("recorded assistant message = %q, want the overseer-marked final text", assistant[0].Content)
	}
	if strings.Contains(assistant[0].Content, "checking the north field") {
		t.Fatalf("recorded assistant message = %q, want only the turn's final text", assistant[0].Content)
	}
}

// TestOverseerReplyVisibleAfterRestart proves the recorded reply is rebuilt
// from the session transcript by a fresh runtime, and that events.jsonl
// replay carries the same reply on the queen stream for the UI history.
// Parity-exempt: Go-side restart/transcript-rebuild check. Upstream's session
// store is the conversation itself; this port front-ends a colony overseer
// whose reply must still be readable after a process restart.
func TestOverseerReplyVisibleAfterRestart(t *testing.T) {
	colonyRuntime, colonyBus := overseerTranscriptColony(t)
	root := t.TempDir()
	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      root,
		SessionID: "s-overseer-restart-transcript",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	first.BindColony(colonyHostStub{runtime: colonyRuntime})

	const executionID = "overseer-turn-restart"
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"the ledger is reconciled", "the ledger is reconciled", eventbus.StrPtr(executionID), eventbus.IntPtr(1), 0)
	colonyBus.EmitExecutionCompleted(overseerStreamID, executionID, nil, nil, nil)
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := first.Transcript()
		if err != nil || len(transcriptAssistantTexts(parts)) != 1 {
			return false
		}
		// events.jsonl is the reload history: the bridge's mirrored copy
		// must be flushed there while the session is still live, on the
		// queen stream, still naming the overseer as its origin.
		events, err := readSessionEventLog(root)
		if err != nil {
			return false
		}
		matches := 0
		for _, event := range events {
			if eventType, _ := event["type"].(string); eventType != string(eventbus.EventClientOutputDelta) {
				continue
			}
			if stream, _ := event["stream_id"].(string); stream != chatStreamID {
				continue
			}
			data, _ := event["data"].(map[string]any)
			snapshot, _ := data["snapshot"].(string)
			relayed, _ := data["relayed_from"].(string)
			if strings.Contains(snapshot, "the ledger is reconciled") && relayed == overseerStreamID {
				matches++
			}
		}
		return matches == 1
	}) {
		parts, _ := first.Transcript()
		t.Fatalf("live transcript rows = %#v / events.jsonl lacks the mirrored reply", transcriptAssistantTexts(parts))
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A fresh runtime over the same root rebuilds the conversation from disk.
	restarted, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      root,
		SessionID: "s-overseer-restart-transcript",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime (restart): %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	parts, err := restarted.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	texts := transcriptAssistantTexts(parts)
	if len(texts) != 1 || !strings.Contains(texts[0], "the ledger is reconciled") {
		t.Fatalf("rebuilt transcript assistant rows = %#v, want the overseer reply", texts)
	}
}

// Parity-exempt: Go-side id-reuse guard. A reloaded colony restarts its
// execution counter, so the same execution id can name a new turn; a fresh
// execution_started must re-arm it instead of suppressing the new reply.
func TestOverseerTranscriptRearmsReusedExecutionID(t *testing.T) {
	colonyRuntime, colonyBus := overseerTranscriptColony(t)
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      t.TempDir(),
		SessionID: "s-overseer-reused-id",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	const executionID = "overseer:colony_reload_turn_1"
	recordTurn := func(reply string) {
		colonyBus.EmitExecutionStarted(overseerStreamID, executionID, nil, nil, nil)
		colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
			reply, reply, eventbus.StrPtr(executionID), eventbus.IntPtr(1), 0)
		colonyBus.EmitExecutionCompleted(overseerStreamID, executionID, nil, nil, nil)
	}
	recordTurn("first colony run")
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		return err == nil && len(transcriptAssistantTexts(parts)) == 1
	}) {
		t.Fatal("the first run's reply was not recorded")
	}
	// Same id, new turn: the started event re-arms it.
	recordTurn("second colony run")
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		return err == nil && len(transcriptAssistantTexts(parts)) == 2
	}) {
		parts, _ := chatRuntime.Transcript()
		t.Fatalf("assistant rows = %#v, want both runs recorded", transcriptAssistantTexts(parts))
	}
}

// TestOverseerTranscriptIgnoresIncrementsAndTerminalReport proves the
// recorder only fires on a completed turn's final text and that the
// overseer's own terminal report keeps its existing "[External event]"
// record instead of being duplicated as an assistant reply.
// Parity-exempt: Go-side dedupe check. Upstream has no separate overseer
// transcript path; this pins the port's one-row-per-turn and report
// non-duplication contract.
func TestOverseerTranscriptIgnoresIncrementsAndTerminalReport(t *testing.T) {
	colonyRuntime, colonyBus := overseerTranscriptColony(t)
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      t.TempDir(),
		SessionID: "s-overseer-no-dup",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	// A delta alone is an increment, not a record.
	const executionID = "overseer-turn-dedupe"
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"working", "working", eventbus.StrPtr(executionID), eventbus.IntPtr(1), 0)
	time.Sleep(50 * time.Millisecond)
	parts, _ := chatRuntime.Transcript()
	if texts := transcriptAssistantTexts(parts); len(texts) != 0 {
		t.Fatalf("incremental frames wrote assistant rows: %#v", texts)
	}

	// A failed turn never produces a final assistant record.
	colonyBus.EmitExecutionFailed(overseerStreamID, executionID, "provider error", nil, nil)
	time.Sleep(50 * time.Millisecond)
	parts, _ = chatRuntime.Transcript()
	if texts := transcriptAssistantTexts(parts); len(texts) != 0 {
		t.Fatalf("failed turn wrote assistant rows: %#v", texts)
	}

	// A turn that ends on a tool boundary has no final answer: its preamble
	// must not be recorded.
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"let me check", "let me check", eventbus.StrPtr("overseer-tool-only"), eventbus.IntPtr(1), 0)
	colonyBus.EmitToolCallStarted(overseerStreamID, overseerStreamID, "call-9", "read_field", nil, eventbus.StrPtr("overseer-tool-only"))
	colonyBus.EmitToolCallCompleted(overseerStreamID, overseerStreamID, "call-9", "read_field", "ok", false, eventbus.StrPtr("overseer-tool-only"))
	colonyBus.EmitExecutionCompleted(overseerStreamID, "overseer-tool-only", nil, nil, nil)
	time.Sleep(50 * time.Millisecond)
	parts, _ = chatRuntime.Transcript()
	if texts := transcriptAssistantTexts(parts); len(texts) != 0 {
		t.Fatalf("tool-boundary turn wrote assistant rows: %#v", texts)
	}

	// A completed turn records once; its terminal report is separate.
	colonyBus.EmitClientOutputDelta(overseerStreamID, overseerStreamID,
		"done and verified", "done and verified", eventbus.StrPtr(executionID), eventbus.IntPtr(2), 0)
	colonyBus.EmitExecutionCompleted(overseerStreamID, executionID, nil, nil, nil)
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		return err == nil && len(transcriptAssistantTexts(parts)) == 1
	}) {
		t.Fatal("completed overseer turn was not recorded")
	}

	// The report event itself carries no assistant text; observing it must
	// not let the recorder write a second copy.
	report := eventbus.NewAgentEvent(eventbus.EventSubagentReport, overseerStreamID)
	report.Data = map[string]any{
		"worker_id": "overseer:colony_overseer_transcript",
		"status":    "stopped",
		"summary":   "Overseer stopped with the colony.",
	}
	if text, ok := chatRuntime.overseerTranscript.observe(report); ok || text != "" {
		t.Fatalf("terminal report produced assistant text %q, want none", text)
	}
	if err := chatRuntime.recordOverseerTerminal("Overseer stopped with the colony."); err != nil {
		t.Fatalf("recordOverseerTerminal: %v", err)
	}
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		if err != nil {
			return false
		}
		found := false
		for _, part := range parts {
			content, _ := part["content"].(string)
			if strings.Contains(content, "[External event]") && strings.Contains(content, "Overseer stopped with the colony.") {
				found = true
			}
		}
		return found
	}) {
		t.Fatal("the overseer's terminal report was not archived as an external turn")
	}
	parts, _ = chatRuntime.Transcript()
	if texts := transcriptAssistantTexts(parts); len(texts) != 1 {
		t.Fatalf("assistant rows after terminal report = %#v, want the single turn reply", texts)
	}
}
