package eventbus

// Parity: framework/host/event_bus.py

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type EventHandler func(*AgentEvent)

type Subscription struct {
	ID              string
	EventTypes      map[EventType]struct{}
	Handler         EventHandler
	FilterStream    *string
	FilterNode      *string
	FilterExecution *string
	FilterColony    *string
}

type SubscribeOption func(*Subscription)

func WithFilterStream(streamID string) SubscribeOption {
	return func(s *Subscription) { s.FilterStream = &streamID }
}

func WithFilterNode(nodeID string) SubscribeOption {
	return func(s *Subscription) { s.FilterNode = &nodeID }
}

func WithFilterExecution(executionID string) SubscribeOption {
	return func(s *Subscription) { s.FilterExecution = &executionID }
}

func WithFilterColony(colonyID string) SubscribeOption {
	return func(s *Subscription) { s.FilterColony = &colonyID }
}

type EventBusOption func(*eventBusConfig)

type eventBusConfig struct {
	maxHistory            int
	maxConcurrentHandlers int
}

func WithMaxHistory(n int) EventBusOption {
	return func(c *eventBusConfig) { c.maxHistory = n }
}

func WithMaxConcurrentHandlers(n int) EventBusOption {
	return func(c *eventBusConfig) { c.maxConcurrentHandlers = n }
}

type snapshotKey struct {
	streamID    string
	nodeID      any
	executionID any
	iteration   any
	innerTurn   any
	eventType   EventType
}

type EventBus struct {
	mu            sync.Mutex
	subscriptions map[string]*Subscription
	history       []*AgentEvent
	maxHistory    int
	subCounter    int
	seqCounter    int

	sem            chan struct{}
	handlerTimeout time.Duration

	latestLoopState map[string]any

	toolIndexByExecution map[string]int
	toolIndexByUseID     map[string]int

	logMu                     sync.Mutex
	queenLog                  *EventLogFile
	workerLogs                map[string]*EventLogFile
	workerLogResolver         func(streamID string) (path string, ok bool)
	sessionLogIterationOffset int
	sessionLogWriteBroken     bool
	pendingOutputSnapshots    map[snapshotKey]*AgentEvent
	pendingOrder              []snapshotKey
}

func NewEventBus(opts ...EventBusOption) *EventBus {
	cfg := eventBusConfig{maxHistory: 1000, maxConcurrentHandlers: 10}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxConcurrentHandlers < 1 {
		cfg.maxConcurrentHandlers = 1
	}
	return &EventBus{
		subscriptions:          map[string]*Subscription{},
		maxHistory:             cfg.maxHistory,
		sem:                    make(chan struct{}, cfg.maxConcurrentHandlers),
		handlerTimeout:         15 * time.Second,
		toolIndexByExecution:   map[string]int{},
		toolIndexByUseID:       map[string]int{},
		workerLogs:             map[string]*EventLogFile{},
		pendingOutputSnapshots: map[snapshotKey]*AgentEvent{},
	}
}

func (b *EventBus) Subscribe(eventTypes []EventType, handler EventHandler, opts ...SubscribeOption) string {
	sub := &Subscription{
		EventTypes: map[EventType]struct{}{},
		Handler:    handler,
	}
	for _, eventType := range eventTypes {
		sub.EventTypes[eventType] = struct{}{}
	}
	for _, opt := range opts {
		opt(sub)
	}

	b.mu.Lock()
	b.subCounter++
	sub.ID = fmt.Sprintf("sub_%d", b.subCounter)
	b.subscriptions[sub.ID] = sub
	b.mu.Unlock()
	return sub.ID
}

func (b *EventBus) Unsubscribe(subscriptionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subscriptions[subscriptionID]; !ok {
		return false
	}
	delete(b.subscriptions, subscriptionID)
	return true
}

func (b *EventBus) SubscriptionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscriptions)
}

func (b *EventBus) Publish(event *AgentEvent) {
	event.EnsureDefaults()

	b.logMu.Lock()
	offset := b.sessionLogIterationOffset
	b.logMu.Unlock()
	if offset != 0 {
		if v, present := event.Data["iteration"]; present {
			if n, ok := intValue(v); ok {
				newData := make(map[string]any, len(event.Data))
				for k, val := range event.Data {
					newData[k] = val
				}
				newData["iteration"] = n + offset
				event.Data = newData
			}
		}
	}

	b.mu.Lock()
	b.seqCounter++
	event.Seq = b.seqCounter
	b.history = append(b.history, event)
	if len(b.history) > b.maxHistory {
		b.history = b.history[len(b.history)-b.maxHistory:]
	}

	if event.Type == EventLoopStateChanged {
		stream := event.StreamID
		if stream == "" {
			stream = "queen"
		}
		if !strings.HasPrefix(stream, "worker") {
			b.latestLoopState = map[string]any{
				"activity":        event.Data["activity"],
				"park_reason":     event.Data["park_reason"],
				"interrupt_cause": event.Data["interrupt_cause"],
				"questions":       event.Data["questions"],
				"stream_id":       event.StreamID,
				"node_id":         optionalString(event.NodeID),
				"execution_id":    optionalString(event.ExecutionID),
				"at":              event.Timestamp,
				"seq":             event.Seq,
			}
		}
	}

	if event.Type == EventToolCallStarted && event.ExecutionID != nil && *event.ExecutionID != "" {
		execID := *event.ExecutionID
		idx := b.toolIndexByExecution[execID] + 1
		b.toolIndexByExecution[execID] = idx
		event.Data["tool_index"] = idx
		if toolUseID, ok := event.Data["tool_use_id"].(string); ok && toolUseID != "" {
			b.toolIndexByUseID[toolUseID] = idx
		}
	} else if event.Type == EventToolCallCompleted {
		if toolUseID, ok := event.Data["tool_use_id"].(string); ok && toolUseID != "" {
			if idx, ok := b.toolIndexByUseID[toolUseID]; ok {
				event.Data["tool_index"] = idx
			}
		}
	}

	handlers := make([]EventHandler, 0, len(b.subscriptions))
	for _, sub := range b.subscriptions {
		if matches(sub, event) {
			handlers = append(handlers, sub.Handler)
		}
	}
	b.mu.Unlock()

	writeDebugEvent(event)

	b.logMu.Lock()
	if b.queenLog != nil {
		functionalErr := b.writeSessionLogEventLocked(event)
		if functionalErr != nil && !b.sessionLogWriteBroken {
			b.sessionLogWriteBroken = true
			log.Printf("Session event log write raised (subsequent drops silent): %v", functionalErr)
		}
	}
	b.logMu.Unlock()

	if len(handlers) > 0 {
		b.executeHandlers(event, handlers)
	}
}

func matches(sub *Subscription, event *AgentEvent) bool {
	if _, ok := sub.EventTypes[event.Type]; !ok {
		return false
	}
	if !streamFilterMatches(sub.FilterStream, event.StreamID) {
		return false
	}
	if !optionalFilterMatches(sub.FilterNode, event.NodeID) {
		return false
	}
	if !optionalFilterMatches(sub.FilterExecution, event.ExecutionID) {
		return false
	}
	if !optionalFilterMatches(sub.FilterColony, event.ColonyID) {
		return false
	}
	return true
}

func streamFilterMatches(filter *string, value string) bool {
	if filter == nil || *filter == "" {
		return true
	}
	return *filter == value
}

func optionalFilterMatches(filter *string, value *string) bool {
	if filter == nil || *filter == "" {
		return true
	}
	return value != nil && *filter == *value
}

func (b *EventBus) executeHandlers(event *AgentEvent, handlers []EventHandler) {
	var wg sync.WaitGroup
	for _, handler := range handlers {
		handler := handler
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.sem <- struct{}{}
			defer func() { <-b.sem }()

			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if recovered := recover(); recovered != nil {
						log.Printf("Handler error for %s: %v", event.Type, recovered)
					}
				}()
				handler(event)
			}()

			timer := time.NewTimer(b.handlerTimeout)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				log.Printf("EventBus handler exceeded %.0fs on event %s — dropping; fix the handler or the publisher will stall",
					b.handlerTimeout.Seconds(), event.Type)
			}
		}()
	}
	wg.Wait()
}

func (b *EventBus) GetHistory(opts ...HistoryOption) []*AgentEvent {
	cfg := historyConfig{limit: 100}
	for _, opt := range opts {
		opt(&cfg)
	}

	b.mu.Lock()
	events := make([]*AgentEvent, 0, len(b.history))
	for i := len(b.history) - 1; i >= 0; i-- {
		events = append(events, b.history[i])
	}
	b.mu.Unlock()

	if cfg.eventType != nil {
		filtered := events[:0]
		for _, e := range events {
			if e.Type == *cfg.eventType {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	if cfg.streamID != nil && *cfg.streamID != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.StreamID == *cfg.streamID {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	if cfg.executionID != nil && *cfg.executionID != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.ExecutionID != nil && *e.ExecutionID == *cfg.executionID {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	if cfg.limit >= 0 && len(events) > cfg.limit {
		events = events[:cfg.limit]
	}
	return events
}

type historyConfig struct {
	eventType   *EventType
	streamID    *string
	executionID *string
	limit       int
}

type HistoryOption func(*historyConfig)

func WithHistoryType(eventType EventType) HistoryOption {
	return func(c *historyConfig) { c.eventType = &eventType }
}

func WithHistoryStream(streamID string) HistoryOption {
	return func(c *historyConfig) { c.streamID = &streamID }
}

func WithHistoryExecution(executionID string) HistoryOption {
	return func(c *historyConfig) { c.executionID = &executionID }
}

func WithHistoryLimit(limit int) HistoryOption {
	return func(c *historyConfig) { c.limit = limit }
}

func (b *EventBus) GetStats() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	typeCounts := map[string]int{}
	for _, event := range b.history {
		typeCounts[string(event.Type)]++
	}
	return map[string]any{
		"total_events":   len(b.history),
		"subscriptions":  len(b.subscriptions),
		"events_by_type": typeCounts,
	}
}

func (b *EventBus) WaitFor(ctx context.Context, eventType EventType, opts ...SubscribeOption) *AgentEvent {
	received := make(chan *AgentEvent, 1)
	opts = append(opts, func(sub *Subscription) {
		sub.Handler = func(event *AgentEvent) {
			select {
			case received <- event:
			default:
			}
		}
	})
	subID := b.Subscribe([]EventType{eventType}, nil, opts...)
	defer b.Unsubscribe(subID)

	select {
	case event := <-received:
		return event
	case <-ctx.Done():
		return nil
	}
}

func (b *EventBus) SetSessionLog(path string, iterationOffset int) error {
	b.logMu.Lock()
	defer b.logMu.Unlock()

	if b.queenLog != nil {
		b.queenLog.Close()
		b.queenLog = nil
	}
	newLog, err := NewEventLogFile(path)
	if err != nil {
		return err
	}
	b.queenLog = newLog
	b.sessionLogIterationOffset = iterationOffset
	b.sessionLogWriteBroken = false
	return nil
}

func (b *EventBus) SetWorkerLogResolver(resolver func(streamID string) (path string, ok bool)) {
	b.logMu.Lock()
	defer b.logMu.Unlock()
	b.workerLogResolver = resolver
}

func (b *EventBus) workerLogForLocked(streamID string) *EventLogFile {
	if existing, ok := b.workerLogs[streamID]; ok {
		return existing
	}
	if b.workerLogResolver == nil {
		return nil
	}
	path, ok := callResolver(b.workerLogResolver, streamID)
	if !ok || path == "" {
		return nil
	}
	workerLog, err := NewEventLogFile(path)
	if err != nil {
		log.Printf("could not open worker event log %s: %v", path, err)
		return nil
	}
	b.workerLogs[streamID] = workerLog
	return workerLog
}

func callResolver(resolver func(string) (string, bool), streamID string) (path string, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("worker log resolver raised for %s: %v", streamID, recovered)
			path, ok = "", false
		}
	}()
	return resolver(streamID)
}

func (b *EventBus) sinksForLocked(event *AgentEvent) []*EventLogFile {
	var queen []*EventLogFile
	if b.queenLog != nil {
		queen = []*EventLogFile{b.queenLog}
	}
	if b.workerLogResolver == nil {
		return queen
	}
	if !IsWorkerStream(event.StreamID) {
		return queen
	}
	worker := b.workerLogForLocked(event.StreamID)
	if worker == nil {
		return queen
	}
	if IsWorkerLocal(event.StreamID, string(event.Type)) {
		return []*EventLogFile{worker}
	}
	return append([]*EventLogFile{worker}, queen...)
}

func (b *EventBus) closeWorkerLogLocked(streamID string) {
	if workerLog, ok := b.workerLogs[streamID]; ok {
		delete(b.workerLogs, streamID)
		workerLog.Close()
	}
}

func (b *EventBus) CloseSessionLog() {
	b.logMu.Lock()
	defer b.logMu.Unlock()

	b.flushPendingSnapshotsLocked(nil, nil, nil)
	if b.queenLog != nil {
		b.queenLog.Close()
		b.queenLog = nil
	}
	for _, workerLog := range b.workerLogs {
		workerLog.Close()
	}
	b.workerLogs = map[string]*EventLogFile{}
	b.pendingOutputSnapshots = map[snapshotKey]*AgentEvent{}
	b.pendingOrder = nil
	b.sessionLogWriteBroken = false
}

var streamingDeltaTypes = map[EventType]struct{}{
	EventClientOutputDelta: {},
	EventLLMTextDelta:      {},
	EventLLMReasoningDelta: {},
}

func (b *EventBus) writeSessionLogEventLocked(event *AgentEvent) error {
	if b.queenLog == nil {
		return nil
	}

	if _, isDelta := streamingDeltaTypes[event.Type]; isDelta {
		key := snapshotKey{
			streamID:    event.StreamID,
			nodeID:      optionalString(event.NodeID),
			executionID: optionalString(event.ExecutionID),
			iteration:   event.Data["iteration"],
			innerTurn:   innerTurnOf(event.Data),
			eventType:   event.Type,
		}
		if existing, ok := b.pendingOutputSnapshots[key]; ok {
			event = event.CloneWithTimestamp(existing.Timestamp)
		} else {
			b.pendingOrder = append(b.pendingOrder, key)
		}
		b.pendingOutputSnapshots[key] = event
		return nil
	}

	if event.Type == EventClientReasoning {
		key := snapshotKey{
			streamID:    event.StreamID,
			nodeID:      optionalString(event.NodeID),
			executionID: optionalString(event.ExecutionID),
			iteration:   event.Data["iteration"],
			innerTurn:   innerTurnOf(event.Data),
			eventType:   EventLLMReasoningDelta,
		}
		if _, ok := b.pendingOutputSnapshots[key]; ok {
			delete(b.pendingOutputSnapshots, key)
			b.removePendingKeyLocked(key)
		}
	}

	if event.Type == EventLLMTurnComplete {
		b.flushPendingSnapshotsLocked(StrPtr(event.StreamID), event.NodeID, event.ExecutionID)
	}

	// A turn can end without llm_turn_complete: the colony overseer publishes
	// client_output_delta and then execution_completed, never per-call usage
	// (the session does not own its token budget). Flush at the execution
	// boundary so a reload's events.jsonl replay still contains the reply
	// instead of leaving it buffered until the session closes.
	//
	// Parity-exempt: Go-side flush boundary. Upstream's overseer is a queen
	// that publishes llm_turn_complete; this port's overseer emitter drops it.
	if event.Type == EventExecutionCompleted || event.Type == EventExecutionFailed {
		b.flushPendingSnapshotsLocked(StrPtr(event.StreamID), event.NodeID, event.ExecutionID)
	}

	line, err := marshalLine(event)
	if err != nil {
		return err
	}
	for _, sink := range b.sinksForLocked(event) {
		sink.Write(line)
	}

	if event.Type == EventSubagentReport && IsWorkerStream(event.StreamID) {
		b.closeWorkerLogLocked(event.StreamID)
	}
	return nil
}

func (b *EventBus) flushPendingSnapshotsLocked(streamID *string, nodeID *string, executionID *string) {
	if b.queenLog == nil || len(b.pendingOutputSnapshots) == 0 {
		return
	}

	toFlush := make([]snapshotKey, 0, len(b.pendingOutputSnapshots))
	for _, key := range b.pendingOrder {
		event, ok := b.pendingOutputSnapshots[key]
		if !ok {
			continue
		}
		if streamID != nil {
			if key.streamID != *streamID {
				continue
			}
			if !reflect.DeepEqual(key.nodeID, optionalString(nodeID)) {
				continue
			}
			if !reflect.DeepEqual(key.executionID, optionalString(executionID)) {
				continue
			}
		}
		toFlush = append(toFlush, key)
		_ = event
	}

	for _, key := range toFlush {
		event := b.pendingOutputSnapshots[key]
		delete(b.pendingOutputSnapshots, key)
		b.removePendingKeyLocked(key)

		line, err := marshalLine(event)
		if err != nil {
			continue
		}
		for _, sink := range b.sinksForLocked(event) {
			sink.Write(line)
		}
	}
	for _, sink := range b.allLogsLocked() {
		sink.Flush()
	}
}

func (b *EventBus) allLogsLocked() []*EventLogFile {
	logs := make([]*EventLogFile, 0, len(b.workerLogs)+1)
	if b.queenLog != nil {
		logs = append(logs, b.queenLog)
	}
	for _, workerLog := range b.workerLogs {
		logs = append(logs, workerLog)
	}
	return logs
}

func (b *EventBus) removePendingKeyLocked(key snapshotKey) {
	for i, pending := range b.pendingOrder {
		if reflect.DeepEqual(pending, key) {
			b.pendingOrder = append(b.pendingOrder[:i], b.pendingOrder[i+1:]...)
			return
		}
	}
}

func innerTurnOf(data map[string]any) any {
	if v, ok := data["inner_turn"]; ok {
		return v
	}
	return 0
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(n), float64(int(n)) == float64(n)
	case float64:
		return int(n), float64(int(n)) == n
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func marshalLine(event *AgentEvent) (string, error) {
	payload, err := json.Marshal(eventToDiskDict(event))
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

var (
	debugEventsRaw = strings.TrimSpace(os.Getenv("HIVE_DEBUG_EVENTS"))
	debugLogMu     sync.Mutex
	debugLogFile   *os.File
	debugLogReady  bool
)

func debugEventsEnabled() bool {
	lower := strings.ToLower(debugEventsRaw)
	if lower == "1" || lower == "true" || lower == "full" {
		return true
	}
	return debugEventsRaw != "" && lower != "0" && lower != "false"
}

func hiveHomeDir() string {
	if home := strings.TrimSpace(os.Getenv("HIVE_HOME")); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ".hive"
	}
	return filepath.Join(userHome, ".hive")
}

func openDebugEventLog() *os.File {
	if !debugEventsEnabled() {
		return nil
	}
	raw := debugEventsRaw
	var logDir string
	if raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "full") {
		logDir = filepath.Join(hiveHomeDir(), "event_logs")
	} else {
		logDir = raw
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}
	name := time.Now().Format("20060102_150405") + ".jsonl"
	path := filepath.Join(logDir, name)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	log.Printf("Event debug log → %s", path)
	return fh
}

func writeDebugEvent(event *AgentEvent) {
	if !debugEventsEnabled() {
		return
	}
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if !debugLogReady {
		debugLogFile = openDebugEventLog()
		debugLogReady = true
	}
	if debugLogFile == nil {
		return
	}
	payload, err := json.Marshal(event.ToDict())
	if err != nil {
		return
	}
	_, _ = debugLogFile.WriteString(string(payload) + "\n")
	_ = debugLogFile.Sync()
}
