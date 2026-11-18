package eventbus

// Parity: framework/host/event_bus.py (Snapshot helpers)

import "time"

const stalenessWindow = 300 * time.Second

type snapshotState struct {
	history         []*AgentEvent
	seq             int
	latestLoopState map[string]any
}

func (b *EventBus) snapshotStateLocked() snapshotState {
	history := make([]*AgentEvent, len(b.history))
	copy(history, b.history)
	var cell map[string]any
	if b.latestLoopState != nil {
		cell = make(map[string]any, len(b.latestLoopState))
		for k, v := range b.latestLoopState {
			cell[k] = v
		}
	}
	return snapshotState{history: history, seq: b.seqCounter, latestLoopState: cell}
}

type openToolEntry struct {
	id          string
	name        any
	startedAt   time.Time
	executionID any
	seq         int
}

func (e openToolEntry) asMap() map[string]any {
	return map[string]any{
		"id":           e.id,
		"name":         e.name,
		"started_at":   isoTimestamp(e.startedAt),
		"execution_id": e.executionID,
		"seq":          e.seq,
	}
}

func ComputeSessionSnapshot(bus *EventBus) map[string]any {
	bus.mu.Lock()
	state := bus.snapshotStateLocked()
	bus.mu.Unlock()

	history := state.history

	openTools := map[string]openToolEntry{}
	openOrder := []string{}
	var currentExecutionID any
	var lastEventTS time.Time
	var hasLastEvent bool
	var lastQueenEventTS time.Time
	var hasLastQueenEvent bool
	var lastUnresolvedReqSeq any
	var pendingQuestions any

	removeOpenTool := func(toolUseID string) {
		if _, ok := openTools[toolUseID]; !ok {
			return
		}
		delete(openTools, toolUseID)
		for i, id := range openOrder {
			if id == toolUseID {
				openOrder = append(openOrder[:i], openOrder[i+1:]...)
				break
			}
		}
	}

	for _, ev := range history {
		lastEventTS = ev.Timestamp
		hasLastEvent = true

		stream := ev.StreamID
		if stream == "" {
			stream = "queen"
		}
		if len(stream) >= 6 && stream[:6] == "worker" {
			continue
		}
		lastQueenEventTS = ev.Timestamp
		hasLastQueenEvent = true

		data := ev.Data
		switch ev.Type {
		case EventExecutionStarted:
			currentExecutionID = optionalString(ev.ExecutionID)
		case EventExecutionCompleted, EventExecutionFailed:
			currentExecutionID = nil
			openTools = map[string]openToolEntry{}
			openOrder = nil
		case EventToolCallStarted:
			if toolUseID, ok := data["tool_use_id"].(string); ok && toolUseID != "" {
				if _, exists := openTools[toolUseID]; !exists {
					openOrder = append(openOrder, toolUseID)
				}
				openTools[toolUseID] = openToolEntry{
					id:          toolUseID,
					name:        data["tool_name"],
					startedAt:   ev.Timestamp,
					executionID: optionalString(ev.ExecutionID),
					seq:         ev.Seq,
				}
			}
		case EventToolCallCompleted:
			if toolUseID, ok := data["tool_use_id"].(string); ok && toolUseID != "" {
				removeOpenTool(toolUseID)
			}
		case EventClientInputRequested:
			lastUnresolvedReqSeq = ev.Seq
			if qs, ok := data["questions"].([]map[string]any); ok {
				pendingQuestions = qs
			} else if qs, ok := data["questions"].([]any); ok {
				pendingQuestions = qs
			} else {
				pendingQuestions = nil
			}
		case EventClientInputReceived:
			lastUnresolvedReqSeq = nil
			pendingQuestions = nil
		}
	}

	var activity any
	var parkReason any
	var interruptCause any
	if state.latestLoopState != nil {
		activity = state.latestLoopState["activity"]
		parkReason = state.latestLoopState["park_reason"]
		interruptCause = state.latestLoopState["interrupt_cause"]
		switch qs := state.latestLoopState["questions"].(type) {
		case []map[string]any:
			pendingQuestions = qs
		case []any:
			pendingQuestions = qs
		}
	}

	activityStr, _ := activity.(string)
	isExecuting := activityStr == "executing"
	awaitingInput := activityStr == "awaiting_user"
	interrupted := activityStr == "interrupted"

	if !isExecuting {
		openTools = map[string]openToolEntry{}
		openOrder = nil
	}

	if isExecuting && hasLastQueenEvent {
		age := time.Since(lastQueenEventTS)
		if age > stalenessWindow {
			isExecuting = false
			interrupted = true
			activity = "interrupted"
			interruptCause = "stale"
			currentExecutionID = nil
			openTools = map[string]openToolEntry{}
			openOrder = nil
		}
	}

	var queenBusyReason any
	switch {
	case interrupted:
		queenBusyReason = "interrupted"
	case awaitingInput:
		queenBusyReason = "awaiting_input"
	case isExecuting:
		if len(openTools) > 0 {
			queenBusyReason = "tool"
		} else {
			queenBusyReason = "llm"
		}
	}

	currentTools := make([]map[string]any, 0, len(openOrder))
	for _, id := range openOrder {
		if tool, ok := openTools[id]; ok {
			currentTools = append(currentTools, tool.asMap())
		}
	}

	var interruptCauseOut any
	if interrupted {
		interruptCauseOut = interruptCause
	}
	var parkReasonOut any
	if awaitingInput || interrupted {
		parkReasonOut = parkReason
	}
	var pendingQuestionsOut any
	if awaitingInput {
		pendingQuestionsOut = pendingQuestions
	}
	var lastEventAt any
	if hasLastEvent {
		lastEventAt = isoTimestamp(lastEventTS)
	}
	var lastQueenEventAt any
	if hasLastQueenEvent {
		lastQueenEventAt = isoTimestamp(lastQueenEventTS)
	}

	return map[string]any{
		"snapshot_seq":         state.seq,
		"activity":             activity,
		"is_executing":         isExecuting,
		"awaiting_input":       awaitingInput,
		"interrupted":          interrupted,
		"interrupt_cause":      interruptCauseOut,
		"current_execution_id": currentExecutionID,
		"current_tool_calls":   currentTools,
		"queen_busy_reason":    queenBusyReason,
		"park_reason":          parkReasonOut,
		"pending_questions":    pendingQuestionsOut,
		"last_event_seq":       lastUnresolvedReqSeq,
		"last_event_at":        lastEventAt,
		"last_queen_event_at":  lastQueenEventAt,
	}
}

func CollectResolvedRequestSeqs(bus *EventBus) map[int]struct{} {
	bus.mu.Lock()
	history := make([]*AgentEvent, len(bus.history))
	copy(history, bus.history)
	bus.mu.Unlock()

	resolved := map[int]struct{}{}
	unresolved := map[int]struct{}{}
	for _, ev := range history {
		switch ev.Type {
		case EventClientInputRequested:
			unresolved[ev.Seq] = struct{}{}
		case EventClientInputReceived:
			for seq := range unresolved {
				resolved[seq] = struct{}{}
			}
			unresolved = map[int]struct{}{}
		}
	}
	return resolved
}
