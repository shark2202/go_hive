package agentloop

// Cold-resume cursor state owned by the agent loop.
//
// A Go loop restores recent_responses inside ExecuteTurn, but the event-loop
// iteration counter, the node identity, the output accumulator snapshot, the
// cumulative tool-call count and the turn-level doom fingerprint window need
// the same treatment as their Python counterparts: they are checkpointed onto
// the conversation cursor and rehydrated before a resumed turn, so
// LoopConfig.MaxIterations and LoopConfig.ToolCallLifetimeBudget stay true caps
// across process restarts, the resumed run reports the node upstream wrote, and
// a cold mid-turn resume keeps the repetition history it had.
//
// Every write is a read-modify-write over the cursor. The message persist
// bumps next_seq and the chat runtime stamps pending_input; a plain
// ReadCursor/WriteCursor pair would carry the older snapshot back and drop
// whichever field the other writer just set.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
// Parity: framework/agent_loop/agent_loop.py::_write_cursor

import (
	"encoding/json"
	"strconv"
)

const (
	toolCallsUsedCursorKey          = "tool_calls_used"
	recentToolFingerprintsCursorKey = "recent_tool_fingerprints"
	// iterationCursorKey is upstream's 0-based event-loop iteration index. The
	// stored value is the last iteration the process reached; restore resumes
	// at iteration+1, exactly like cursor_persistence.restore.
	iterationCursorKey = "iteration"
	// nodeIDCursorKey is ctx.agent_id — the node the checkpoint belongs to, so
	// a host resuming a different node does not silently adopt this run.
	nodeIDCursorKey = "node_id"
	// outputsCursorKey is OutputAccumulator.to_dict(): the declared-key
	// set_output values, as one JSON object.
	outputsCursorKey = "outputs"
)

// mutateAgentLoopCursor applies fn to the cursor as one atomic
// read-modify-write when the store supports the CursorMutator seam. A store
// without it (test doubles) keeps the historical read/write pair.
func mutateAgentLoopCursor(store ConversationStore, fn func(cursor map[string]any)) error {
	if store == nil {
		return nil
	}
	if mutator, ok := store.(CursorMutator); ok {
		return mutator.MutateCursor(fn)
	}
	cursor, err := store.ReadCursor()
	if err != nil {
		return err
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	fn(cursor)
	return store.WriteCursor(cursor)
}

// RestoreCursorState rehydrates the loop's cold-resume state from the
// conversation cursor. It replaces (never adds to) the in-memory values, so it
// is meant for a freshly constructed loop; calling it again would rewind a
// loop that already ran.
//
// Defaults follow upstream's tolerant restore: a cursor without "iteration"
// restores 0 (so the next iteration is 1), a missing or malformed "outputs"
// restores an empty accumulator, and a missing "node_id" restores "". None of
// them fails the resume.
//
// A window restored non-empty is kept through the next turn boundary: the
// first turn after a cold start is the interrupted turn's continuation, and
// upstream resumes it with its recent_tool_fingerprints intact. Later turns
// clear the window as usual.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
// Parity: framework/agent_loop/agent_loop.py::execute (restored counters)
func (l *AgentLoop) RestoreCursorState(conv *NodeConversation) error {
	if l == nil || conv == nil || conv.Store() == nil {
		return nil
	}
	cursor, err := conv.Store().ReadCursor()
	if err != nil {
		return err
	}
	var used int
	var fingerprints [][]ToolFingerprint
	iteration := 0
	iterationKnown := false
	nodeID := ""
	outputs := map[string]any{}
	if cursor != nil {
		used = cursorInt(cursor[toolCallsUsedCursorKey])
		fingerprints = cursorToolFingerprints(cursor[recentToolFingerprintsCursorKey])
		// start_iteration = cursor.get("iteration", 0) + 1. The offset only
		// applies when the checkpoint carried the field: a legacy cursor has no
		// information about consumed iterations (see cursorStartIteration).
		if parsed, ok := cursorIterationValue(cursor[iterationCursorKey]); ok {
			iteration = parsed
			iterationKnown = true
		}
		nodeID = cursorNodeID(cursor[nodeIDCursorKey])
		outputs = cursorOutputs(cursor[outputsCursorKey])
	}
	l.stateMu.Lock()
	l.toolCallsUsed = used
	l.cursorIteration = iteration
	l.cursorIterationKnown = iterationKnown
	l.restoredNodeID = nodeID
	l.restoredOutputs = outputs
	l.resumeDoomWindow = len(fingerprints) > 0
	l.stateMu.Unlock()
	if l.DoomWindow == nil {
		l.DoomWindow = NewDoomLoopWindow()
	}
	l.DoomWindow.Restore(fingerprints)
	return nil
}

// CursorIteration returns the 0-based iteration index of the last checkpointed
// event-loop iteration (0 when the cursor carried none). A restored loop with
// a known index starts the next turn at the restored value+1.
func (l *AgentLoop) CursorIteration() int {
	if l == nil {
		return 0
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.cursorIteration
}

// cursorStartIteration is the first inner-turn index for the next turn:
// upstream's start_iteration (cursor iteration + 1) when the checkpoint carried
// the field, and 0 for a loop that has not checkpointed one yet. A legacy
// cursor without "iteration" carries no consumed-iteration information, so the
// port treats it as a fresh counter instead of shifting every published
// iteration by one.
func (l *AgentLoop) cursorStartIteration() int {
	if l == nil {
		return 0
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if !l.cursorIterationKnown {
		return 0
	}
	return l.cursorIteration + 1
}

// RestoredNodeID returns the node id the cursor checkpoint carried ("" when
// the field was absent, matching upstream's default).
func (l *AgentLoop) RestoredNodeID() string {
	if l == nil {
		return ""
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.restoredNodeID
}

// RestoredOutputs returns a copy of the output accumulator snapshot restored
// from the cursor. An empty map means the cursor carried no outputs.
func (l *AgentLoop) RestoredOutputs() map[string]any {
	if l == nil {
		return map[string]any{}
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	out := make(map[string]any, len(l.restoredOutputs))
	for key, value := range l.restoredOutputs {
		out[key] = value
	}
	return out
}

// PersistCursorState checkpoints the loop-owned fields it can derive without an
// agent context: the cumulative tool-call counter and the fingerprint window.
// Callers inside a turn should use PersistCursorStateFor so the iteration,
// node id and outputs are checkpointed too.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (l *AgentLoop) PersistCursorState(conv *NodeConversation) {
	l.persistCursorState(conv, nil)
}

// PersistCursorStateFor writes the complete upstream checkpoint: iteration,
// node_id, outputs, tool_calls_used and recent_tool_fingerprints. It never
// disturbs the cursor fields other writers own (next_seq, recent_responses,
// pending_input and the park records). It always writes
// recent_tool_fingerprints even when the window is empty, matching Python's
// write_cursor, so a cleared window is persisted as [] rather than left stale.
//
// outputs is the conversation-derived accumulator (the port's source of truth
// for set_output), with any restored value the transcript can no longer
// reconstruct filled back in.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (l *AgentLoop) PersistCursorStateFor(conv *NodeConversation, agentCtx *AgentContext) {
	l.persistCursorState(conv, agentCtx)
}

func (l *AgentLoop) persistCursorState(conv *NodeConversation, agentCtx *AgentContext) {
	if l == nil || conv == nil || conv.Store() == nil {
		return
	}
	l.stateMu.Lock()
	used := l.toolCallsUsed
	iteration := l.cursorIteration
	iterationKnown := l.cursorIterationKnown
	l.stateMu.Unlock()
	fingerprints := l.doomWindowSnapshot()
	var nodeID string
	var outputs map[string]any
	if agentCtx != nil {
		nodeID = cursorAgentNodeID(agentCtx)
		outputs = l.cursorOutputsSnapshot(conv, agentCtx)
	}
	_ = mutateAgentLoopCursor(conv.Store(), func(cursor map[string]any) {
		if iterationKnown {
			cursor[iterationCursorKey] = iteration
		}
		if agentCtx != nil {
			cursor[nodeIDCursorKey] = nodeID
			cursor[outputsCursorKey] = outputs
		}
		cursor[toolCallsUsedCursorKey] = used
		cursor[recentToolFingerprintsCursorKey] = encodeToolFingerprints(fingerprints)
	})
}

// persistCursorIteration flushes the iteration index at an iteration boundary,
// before the next model call. Upstream writes its cursor after every iteration
// (the 6g checkpoint), so a crash between iterations resumes at the next one
// instead of replaying the completed work.
func (l *AgentLoop) persistCursorIteration(conv *NodeConversation, agentCtx *AgentContext) {
	if l == nil || conv == nil || conv.Store() == nil || agentCtx == nil {
		return
	}
	l.stateMu.Lock()
	iteration := l.cursorIteration
	iterationKnown := l.cursorIterationKnown
	l.stateMu.Unlock()
	if !iterationKnown {
		return
	}
	nodeID := cursorAgentNodeID(agentCtx)
	_ = mutateAgentLoopCursor(conv.Store(), func(cursor map[string]any) {
		cursor[iterationCursorKey] = iteration
		cursor[nodeIDCursorKey] = nodeID
	})
}

// cursorOutputsSnapshot is OutputAccumulator.to_dict() for the checkpoint: the
// conversation's successful set_output values, with the transcript's declared-key
// call values filling in a key no call ever set successfully, and the restored
// snapshot filling in a key the transcript can no longer reconstruct. A
// successful value wins over a later rejected call, and the conversation wins
// for a key it still carries, so a resumed run never rewinds an output a later
// set_output overwrote.
func (l *AgentLoop) cursorOutputsSnapshot(conv *NodeConversation, agentCtx *AgentContext) map[string]any {
	outputs := map[string]any{}
	if conv != nil && agentCtx != nil {
		keys := agentCtx.AgentSpec.OutputKeys
		outputs = conv.OutputAccumulator(keys)
		// The live loop rejects set_output before dispatch (upstream removed
		// the tool), so its result is an error and the success-only view above
		// never sees the key. The call's arguments are still the record of
		// what the model set, so the checkpoint keeps them for resume.
		for key, value := range conv.OutputCallValues(keys) {
			if _, present := outputs[key]; !present {
				outputs[key] = value
			}
		}
	}
	l.stateMu.Lock()
	restored := l.restoredOutputs
	l.stateMu.Unlock()
	for key, value := range restored {
		if _, present := outputs[key]; !present {
			outputs[key] = value
		}
	}
	return outputs
}

// persistToolCallsUsed checkpoints the cumulative counter at the point a batch
// admitted calls. Upstream increments before dispatch and writes its cursor on
// every checkpoint, so a crash mid-batch cannot refill the budget.
func (l *AgentLoop) persistToolCallsUsed(conv *NodeConversation) {
	if l == nil || conv == nil || conv.Store() == nil {
		return
	}
	l.stateMu.Lock()
	used := l.toolCallsUsed
	l.stateMu.Unlock()
	_ = mutateAgentLoopCursor(conv.Store(), func(cursor map[string]any) {
		cursor[toolCallsUsedCursorKey] = used
	})
}

// persistDoomWindow checkpoints the turn-level fingerprint window.
func (l *AgentLoop) persistDoomWindow(conv *NodeConversation) {
	if l == nil || conv == nil || conv.Store() == nil {
		return
	}
	fingerprints := l.doomWindowSnapshot()
	_ = mutateAgentLoopCursor(conv.Store(), func(cursor map[string]any) {
		cursor[recentToolFingerprintsCursorKey] = encodeToolFingerprints(fingerprints)
	})
}

// clearDoomWindow drops the in-memory window and persists the empty state, so
// a reset (text-only turn, escalated/parked doom loop, non-guarded batch) is
// what a restarted loop restores instead of the stale repetition history.
func (l *AgentLoop) clearDoomWindow(conv *NodeConversation) {
	if l == nil || l.DoomWindow == nil {
		return
	}
	l.DoomWindow.Clear()
	l.persistDoomWindow(conv)
}

func (l *AgentLoop) doomWindowSnapshot() [][]ToolFingerprint {
	if l == nil || l.DoomWindow == nil {
		return nil
	}
	return l.DoomWindow.Snapshot()
}

// takeResumeDoomWindow reports whether the next turn boundary must keep the
// cursor-restored window, and consumes that one-shot signal.
func (l *AgentLoop) takeResumeDoomWindow() bool {
	if l == nil {
		return false
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	restored := l.resumeDoomWindow
	l.resumeDoomWindow = false
	return restored
}

// encodeToolFingerprints renders the window in upstream's JSON shape: a list
// of turns, each a list of [tool_name, canonical_args_json] pairs. An empty
// window encodes as [] (not null), like Python's cursor writes.
func encodeToolFingerprints(turns [][]ToolFingerprint) []any {
	out := make([]any, 0, len(turns))
	for _, turn := range turns {
		pairs := make([]any, 0, len(turn))
		for _, item := range turn {
			pairs = append(pairs, []any{item.Name, item.Arguments})
		}
		out = append(out, pairs)
	}
	return out
}

// cursorToolFingerprints decodes the JSON-decoded cursor value. The file store
// decodes arrays as []any; an in-process cursor may hold typed values. A
// missing or malformed value restores an empty window rather than failing the
// turn, matching upstream's tolerate-and-continue restore.
func cursorToolFingerprints(raw any) [][]ToolFingerprint {
	switch turns := raw.(type) {
	case [][]ToolFingerprint:
		return cloneToolFingerprintTurns(turns)
	case []any:
		out := make([][]ToolFingerprint, 0, len(turns))
		for _, turnRaw := range turns {
			turn, ok := cursorFingerprintTurn(turnRaw)
			if !ok {
				return nil
			}
			out = append(out, turn)
		}
		return out
	default:
		return nil
	}
}

func cursorFingerprintTurn(raw any) ([]ToolFingerprint, bool) {
	switch pairs := raw.(type) {
	case []ToolFingerprint:
		return append([]ToolFingerprint(nil), pairs...), true
	case []any:
		turn := make([]ToolFingerprint, 0, len(pairs))
		for _, pairRaw := range pairs {
			item, ok := cursorFingerprintPair(pairRaw)
			if !ok {
				return nil, false
			}
			turn = append(turn, item)
		}
		return turn, true
	default:
		return nil, false
	}
}

func cursorFingerprintPair(raw any) (ToolFingerprint, bool) {
	switch pair := raw.(type) {
	case []any:
		if len(pair) != 2 {
			return ToolFingerprint{}, false
		}
		name, nameOK := pair[0].(string)
		args, argsOK := pair[1].(string)
		if !nameOK || !argsOK {
			return ToolFingerprint{}, false
		}
		return ToolFingerprint{Name: name, Arguments: args}, true
	case []string:
		if len(pair) != 2 {
			return ToolFingerprint{}, false
		}
		return ToolFingerprint{Name: pair[0], Arguments: pair[1]}, true
	case [2]string:
		return ToolFingerprint{Name: pair[0], Arguments: pair[1]}, true
	default:
		return ToolFingerprint{}, false
	}
}

func cloneToolFingerprintTurns(turns [][]ToolFingerprint) [][]ToolFingerprint {
	if turns == nil {
		return nil
	}
	out := make([][]ToolFingerprint, 0, len(turns))
	for _, turn := range turns {
		out = append(out, append([]ToolFingerprint(nil), turn...))
	}
	return out
}

// cursorInt reads a JSON cursor number. encoding/json decodes numbers as
// float64; an in-process writer may store int, int64, json.Number, or a
// numeric string.
func cursorInt(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		if err != nil {
			return 0
		}
		return parsed
	case string:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// cursorIterationValue reads the checkpoint's iteration index and reports
// whether the cursor actually carried a usable value. A cursor written before
// the field existed, or one with a malformed value, reports false so the loop
// starts from a fresh counter instead of inventing consumed iterations.
func cursorIterationValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(value)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// cursorNodeID reads the checkpoint's node id. A cursor written by an older
// process may carry a non-string (or no) value; upstream treats that as the
// empty default instead of failing the resume.
func cursorNodeID(raw any) string {
	value, _ := raw.(string)
	return value
}

// cursorAgentNodeID resolves the node id upstream stores as ctx.agent_id. A
// context without AgentID falls back to its spec id, matching the loop's other
// event stamps.
func cursorAgentNodeID(agentCtx *AgentContext) string {
	if agentCtx == nil {
		return ""
	}
	if agentCtx.AgentID != "" {
		return agentCtx.AgentID
	}
	return agentCtx.AgentSpec.ID
}

// cursorOutputs decodes the checkpoint's "outputs" object into the accumulator
// snapshot. The file store hands back map[string]any; an in-process writer may
// store map[string]string, and a legacy double-encoded writer a JSON string.
// Anything else restores an empty accumulator (never nil, so the next
// checkpoint writes {} instead of null).
func cursorOutputs(raw any) map[string]any {
	switch values := raw.(type) {
	case map[string]any:
		out := make(map[string]any, len(values))
		for key, value := range values {
			out[key] = value
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(values))
		for key, value := range values {
			out[key] = value
		}
		return out
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(values), &decoded); err == nil && decoded != nil {
			return decoded
		}
		return map[string]any{}
	default:
		return map[string]any{}
	}
}
