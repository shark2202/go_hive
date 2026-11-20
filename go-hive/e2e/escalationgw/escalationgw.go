// Package escalationgw is the deterministic OpenAI-compatible gateway used by
// the worker-escalation end-to-end test.
//
// It is test infrastructure: the production runtime only ever sees it as a
// configured api_base. One process plays every model in the chain:
//
//   - the chat queen (tools include list_worker_questions): first turn asks
//     run_worker; the [WORKER_ESCALATION] turn lists the question; the turn
//     after that answers it with reply_to_worker; anything else is plain text.
//   - a colony worker (tools include report_to_parent): its first three turns
//     repeat one identical task_list call, tripping the deterministic doom-loop
//     detector (the one worker escalation producer upstream keeps). After the
//     trip it keeps alternating distinct calls, waiting for the queen's reply
//     to be recorded, so the manual escalate tool is never needed; the turn
//     that carries [QUEEN_REPLY] reports success.
//   - everything else (title generation, overseer, memory reflection): plain
//     text so those background turns end immediately.
//
// Requests are recorded with their raw bodies so a test can assert what the
// runtime actually sent.
package escalationgw

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kind classifies a recorded request.
type Kind string

const (
	KindQueen    Kind = "queen"
	KindOverseer Kind = "overseer"
	KindWorker   Kind = "worker"
	KindOther    Kind = "other"
)

const (
	// DefaultWorkerTask is the task the scripted queen dispatches.
	DefaultWorkerTask = "query the ledger"
	// DefaultDoomLoopReason is what the deterministic detector reports when it
	// escalates the worker's repeated call.
	DefaultDoomLoopReason = "Tool doom loop detected"
	// DefaultQueenGuidance is the reply the queen sends back.
	DefaultQueenGuidance = "Use the staging database."
	// DefaultWorkerReport is the worker's resumed terminal report.
	DefaultWorkerReport = "Queried the ledger with the staging database."
)

// Record is one request the gateway saw.
type Record struct {
	Seq       int      `json:"seq"`
	Kind      Kind     `json:"kind"`
	Path      string   `json:"path"`
	Model     string   `json:"model"`
	Tools     []string `json:"tools"`
	Response  string   `json:"response"`
	ToolCalls []string `json:"tool_calls,omitempty"`
	// Message is the assistant message this gateway answered with (JSON), so a
	// test can assert the exact tool arguments the model produced.
	Message string `json:"message,omitempty"`
	Body    string `json:"body"`
}

// Options configures a Server.
type Options struct {
	Model              string
	WorkerTask         string
	QueenGuidance      string
	WorkerReport       string
	WorkerReportStatus string
	QueenFinalReply    string
	// WorkerRepeatCount is how many identical calls trip the deterministic
	// doom-loop detector. 0 selects the production threshold (3).
	WorkerRepeatCount int
}

// Server is an http.Handler plus its request recorder.
type Server struct {
	opts Options

	mu      sync.Mutex
	seq     int
	records []Record
}

// New returns a gateway with defaults filled in.
func New(opts Options) *Server {
	if opts.Model == "" {
		opts.Model = "escalation-e2e-model"
	}
	if opts.WorkerTask == "" {
		opts.WorkerTask = DefaultWorkerTask
	}
	if opts.WorkerRepeatCount <= 0 {
		opts.WorkerRepeatCount = 3
	}
	if opts.QueenGuidance == "" {
		opts.QueenGuidance = DefaultQueenGuidance
	}
	if opts.WorkerReport == "" {
		opts.WorkerReport = DefaultWorkerReport
	}
	if opts.WorkerReportStatus == "" {
		opts.WorkerReportStatus = "success"
	}
	if opts.QueenFinalReply == "" {
		opts.QueenFinalReply = "The worker has its answer and will report back."
	}
	return &Server{opts: opts}
}

// Handler returns the HTTP handler the runtime talks to.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("POST /chat/completions", s.chat)
	return mux
}

// Requests returns a copy of everything recorded so far.
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

// RequestsOfKind returns the recorded requests of one kind.
func (s *Server) RequestsOfKind(kind Kind) []Record {
	out := []Record{}
	for _, record := range s.Requests() {
		if record.Kind == kind {
			out = append(out, record)
		}
	}
	return out
}

type toolCall struct {
	ID        string
	Name      string
	Arguments string
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	tools := toolNames(payload)
	kind := classify(tools)
	content, calls := s.decide(kind, payload)

	responseLabel := content
	if len(calls) > 0 {
		names := make([]string, 0, len(calls))
		for _, call := range calls {
			names = append(names, call.Name)
		}
		responseLabel = "tool_calls:" + strings.Join(names, ",")
	}
	s.record(Record{
		Kind:      kind,
		Path:      r.URL.Path,
		Model:     stringValue(payload["model"]),
		Tools:     tools,
		Response:  responseLabel,
		ToolCalls: toolCallNames(calls),
		Message:   assistantMessageJSON(content, calls),
		Body:      string(raw),
	})

	if stream, _ := payload["stream"].(bool); stream {
		includeUsage := false
		if options, ok := payload["stream_options"].(map[string]any); ok {
			includeUsage, _ = options["include_usage"].(bool)
		}
		s.streamCompletion(w, content, calls, includeUsage)
		return
	}
	s.writeCompletion(w, content, calls)
}

func (s *Server) record(record Record) {
	s.mu.Lock()
	s.seq++
	record.Seq = s.seq
	s.records = append(s.records, record)
	s.mu.Unlock()
}

// decide returns the scripted answer for one request.
func (s *Server) decide(kind Kind, payload map[string]any) (string, []toolCall) {
	text := conversationText(payload)
	switch kind {
	case KindWorker:
		if strings.Contains(text, "[QUEEN_REPLY]") {
			arguments, _ := json.Marshal(map[string]any{
				"status":  s.opts.WorkerReportStatus,
				"summary": s.opts.WorkerReport,
				"data":    map[string]any{"resumed": true},
			})
			return "", []toolCall{{ID: "call_report", Name: "report_to_parent", Arguments: string(arguments)}}
		}
		// The first WorkerRepeatCount turns re-issue one identical call; the
		// deterministic detector escalates on the last of them. After the trip
		// the worker alternates distinct arguments so the streak resets and the
		// deterministic park cannot fire before the queen answers.
		repeats := strings.Count(text, "tool_call:task_list\n")
		if repeats < s.opts.WorkerRepeatCount {
			return "", []toolCall{{
				// Unique per call: the conversation store keys tool results by
				// id, so reusing one id would make the repeats invisible.
				ID:        fmt.Sprintf("call_repeat_%d", repeats+1),
				Name:      "task_list",
				Arguments: `{}`,
			}}
		}
		s.waitForQueenReply()
		status := "pending"
		if repeats%2 == 0 {
			status = "done"
		}
		arguments, _ := json.Marshal(map[string]any{"status": status})
		return "", []toolCall{{
			ID:        fmt.Sprintf("call_after_trip_%d", repeats+1),
			Name:      "task_list",
			Arguments: string(arguments),
		}}
	case KindQueen:
		if strings.Contains(text, "[WORKER_ESCALATION]") {
			requestID := escalationRequestID(text)
			switch {
			case strings.Contains(text, "tool_call:reply_to_worker"):
				return s.opts.QueenFinalReply, nil
			case strings.Contains(text, "tool_call:list_worker_questions"):
				if requestID == "" {
					return "The escalation carried no request_id; relaying by hand.", nil
				}
				arguments, _ := json.Marshal(map[string]any{
					"request_id": requestID,
					"reply":      s.opts.QueenGuidance,
				})
				return "", []toolCall{{ID: "call_reply", Name: "reply_to_worker", Arguments: string(arguments)}}
			default:
				return "", []toolCall{{ID: "call_list", Name: "list_worker_questions", Arguments: "{}"}}
			}
		}
		if strings.Contains(text, "[WORKER_REPORT]") {
			return "Noted the worker's report.", nil
		}
		return s.opts.QueenFinalReply, nil
	case KindOverseer:
		// The colony's persistent overseer owns the session's client turns: its
		// first turn dispatches the worker the chain needs.
		if strings.Contains(text, "[WORKER_REPORT]") || strings.Contains(text, "tool_call:run_worker") {
			return "Dispatched the worker.", nil
		}
		arguments, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"task": s.opts.WorkerTask,
				"goal": "Query the ledger",
			}},
			// The scripted worker trips the doom loop inside this budget and
			// then keeps working while the queen answers; the production
			// default (3 + 1 grace) is too tight for that chain.
			"max_iterations": 60,
		})
		return "", []toolCall{{ID: "call_run_worker", Name: "run_worker", Arguments: string(arguments)}}
	}
	return "ok", nil
}

var requestIDPattern = regexp.MustCompile(`request_id:\s*([0-9a-f]{8,})`)

// waitForQueenReply blocks a post-trip worker call until the scripted queen has
// answered it (its reply_to_worker call is recorded). The worker is then still
// alive when the guidance lands; without the gate the worker's fast scripted
// turns could exhaust its iteration budget before the queen's turn completes.
// Bounded: a broken chain must fail the test's assertions, not hang the gateway.
func (s *Server) waitForQueenReply() {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s.queenReplied() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Server) queenReplied() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.Kind != KindQueen {
			continue
		}
		for _, name := range record.ToolCalls {
			if name == "reply_to_worker" {
				return true
			}
		}
	}
	return false
}

func escalationRequestID(text string) string {
	match := requestIDPattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// classify names the model behind a request from its advertised tools: only the
// chat queen carries the lifecycle inbox tools, only the colony worker carries
// report_to_parent, and the persistent overseer carries run_worker without the
// session's lifecycle surface. Everything else is "other".
func classify(tools []string) Kind {
	has := func(name string) bool {
		for _, tool := range tools {
			if tool == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("list_worker_questions"):
		return KindQueen
	case has("report_to_parent"):
		return KindWorker
	case has("run_worker"):
		return KindOverseer
	}
	return KindOther
}

// conversationText renders every message in the request as
// "role:content", with assistant tool calls marked as "tool_call:<name>" so the
// script can tell which step of the chain it is answering.
func conversationText(payload map[string]any) string {
	messages, _ := payload["messages"].([]any)
	var builder strings.Builder
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		role := stringValue(message["role"])
		content, _ := message["content"].(string)
		builder.WriteString(role)
		builder.WriteString(":")
		builder.WriteString(content)
		builder.WriteString("\n")
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, raw := range calls {
				call, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				function, _ := call["function"].(map[string]any)
				builder.WriteString("tool_call:")
				builder.WriteString(stringValue(function["name"]))
				builder.WriteString("\n")
			}
		}
	}
	return builder.String()
}

func toolNames(payload map[string]any) []string {
	raw, _ := payload["tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, entry := range raw {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if function, ok := tool["function"].(map[string]any); ok {
			names = append(names, stringValue(function["name"]))
			continue
		}
		names = append(names, stringValue(tool["name"]))
	}
	return names
}

func toolCallNames(calls []toolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	return names
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// assistantMessage builds the assistant message for one answer.
func assistantMessage(content string, calls []toolCall) map[string]any {
	message := map[string]any{"role": "assistant", "content": content}
	if len(calls) == 0 {
		return message
	}
	envelope := make([]any, 0, len(calls))
	for _, call := range calls {
		envelope = append(envelope, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	message["tool_calls"] = envelope
	return message
}

func assistantMessageJSON(content string, calls []toolCall) string {
	encoded, err := json.Marshal(assistantMessage(content, calls))
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (s *Server) writeCompletion(w http.ResponseWriter, content string, calls []toolCall) {
	message := assistantMessage(content, calls)
	finish := "stop"
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	payload := map[string]any{
		"id":      "chatcmpl-escalationgw",
		"object":  "chat.completion",
		"created": 0,
		"model":   s.opts.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// streamCompletion answers a `stream: true` request with OpenAI
// chat.completion chunks, tool calls included.
func (s *Server) streamCompletion(w http.ResponseWriter, content string, calls []toolCall, includeUsage bool) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	created := time.Now().Unix()
	id := "chatcmpl-escalationgw-stream"
	send := func(payload map[string]any) {
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	delta := func(change map[string]any, finish any) {
		send(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   s.opts.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         change,
				"finish_reason": finish,
			}},
		})
	}
	delta(map[string]any{"role": "assistant"}, nil)
	if content != "" {
		delta(map[string]any{"content": content}, nil)
	}
	for index, call := range calls {
		delta(map[string]any{"tool_calls": []any{map[string]any{
			"index": index,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		}}}, nil)
	}
	finish := "stop"
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	delta(map[string]any{}, finish)
	if includeUsage {
		send(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   s.opts.Model,
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
