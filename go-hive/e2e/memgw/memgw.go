// Package memgw is the deterministic OpenAI-compatible gateway used by the
// memory end-to-end runs (Go and Python). It is test infrastructure: the
// production runtime only ever sees it as a configured api_base.
//
// The gateway keys its answer on the request's system prompt:
//   - the timeline extraction prompt returns dated items derived from the
//     session dates the prompt carries (so relative dates resolve against a
//     date the caller had to supply);
//   - the recall selector prompt returns a JSON selection, or 500 when the
//     query carries FAILRECALL (the "LLM down must not block" case);
//   - reflection prompts answer plain text, ending their tool loop;
//   - everything else is a chat reply. A user turn containing
//     `CALL <tool> {json}` asks for that tool call once; the follow-up turn
//     (which carries a role=tool message) echoes the tool result.
//
// Every request is recorded so a run can diff the exact prompts the Go and
// Python implementations send.
package memgw

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind classifies a recorded request.
type Kind string

const (
	KindTimeline   Kind = "timeline"
	KindRecall     Kind = "recall"
	KindReflection Kind = "reflection"
	KindChat       Kind = "chat"
	KindOther      Kind = "other"
)

// Record is one request the gateway saw.
type Record struct {
	Seq  int    `json:"seq"`
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	Body string `json:"body"`
}

// Options configures a Server.
type Options struct {
	// LogPath, when set, appends one JSON record per request to this file.
	LogPath string
	// Model is the model name echoed in responses.
	Model string
}

// Server is an http.Handler plus its request recorder.
type Server struct {
	opts Options

	mu      sync.Mutex
	seq     int
	record  []Record
	reps    int
	logFile *os.File
}

// New returns a gateway server. Close releases the log file.
func New(opts Options) (*Server, error) {
	if opts.Model == "" {
		opts.Model = "memory-e2e-model"
	}
	s := &Server{opts: opts}
	if opts.LogPath != "" {
		file, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		s.logFile = file
	}
	return s, nil
}

// Requests returns a copy of everything recorded so far.
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.record...)
}

// RequestsOf returns the records of one kind.
func (s *Server) RequestsOf(kind Kind) []Record {
	out := []Record{}
	for _, record := range s.Requests() {
		if record.Kind == kind {
			out = append(out, record)
		}
	}
	return out
}

// Close releases the log file.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile != nil {
		err := s.logFile.Close()
		s.logFile = nil
		return err
	}
	return nil
}

func (s *Server) recordRequest(kind Kind, path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	record := Record{Seq: s.seq, Kind: kind, Path: path, Body: body}
	s.record = append(s.record, record)
	if s.logFile != nil {
		if encoded, err := json.Marshal(record); err == nil {
			_, _ = s.logFile.Write(append(encoded, '\n'))
		}
	}
}

func (s *Server) nextReply() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reps++
	return s.reps
}

// Handler serves /v1/chat/completions and /v1/models.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []any{map[string]any{
				"id": s.opts.Model, "object": "model", "owned_by": "memory-e2e",
			}},
		})
	})
	chat := func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		body := string(raw)
		var req chatRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
		system := req.systemText()
		kind := classify(system)
		s.recordRequest(kind, r.URL.Path, body)
		switch kind {
		case KindTimeline:
			writeJSON(w, http.StatusOK, s.completion(timelineAnswer(req)))
			return
		case KindRecall:
			if strings.Contains(req.lastUserText(), "FAILRECALL") {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]any{"message": "memory-e2e: recall selector unavailable"},
				})
				return
			}
			writeJSON(w, http.StatusOK, s.completion(recallAnswer(req.lastUserText())))
			return
		case KindReflection:
			writeJSON(w, http.StatusOK, s.completion("NO_OP"))
			return
		}
		content, toolCalls := s.chatAnswer(req)
		if req.Stream {
			includeUsage := false
			if value, ok := req.StreamOptions["include_usage"].(bool); ok {
				includeUsage = value
			}
			s.streamCompletion(w, content, toolCalls, includeUsage)
			return
		}
		writeJSON(w, http.StatusOK, s.completionWithTools(content, toolCalls))
	}
	mux.HandleFunc("/v1/chat/completions", chat)
	mux.HandleFunc("/chat/completions", chat)
	return mux
}

func classify(system string) Kind {
	switch {
	case strings.Contains(system, "You build a dated timeline"):
		return KindTimeline
	case strings.Contains(system, "You are selecting memories"):
		return KindRecall
	case strings.Contains(system, "reflection agent"):
		return KindReflection
	case strings.Contains(system, "conversation title"):
		return KindOther
	}
	return KindChat
}

// --- request/response shapes -------------------------------------------

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []any           `json:"tool_calls"`
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions map[string]any `json:"stream_options"`
}

func (r chatRequest) messageText(message chatMessage) string {
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		return text
	}
	var parts []map[string]any
	if err := json.Unmarshal(message.Content, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if value, ok := part["text"].(string); ok {
				b.WriteString(value)
			}
		}
		return b.String()
	}
	return ""
}

func (r chatRequest) systemText() string {
	parts := []string{}
	for _, message := range r.Messages {
		if message.Role == "system" {
			parts = append(parts, r.messageText(message))
		}
	}
	return strings.Join(parts, "\n")
}

func (r chatRequest) lastUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.messageText(r.Messages[i])
		}
	}
	return ""
}

func (r chatRequest) lastToolText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "tool" {
			return r.messageText(r.Messages[i])
		}
	}
	return ""
}

type responseMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls []any  `json:"tool_calls,omitempty"`
}

type responseChoice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type chatResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []responseChoice `json:"choices"`
	Usage   map[string]any   `json:"usage"`
}

func (s *Server) completion(content string) chatResponse {
	return s.completionWithTools(content, nil)
}

func (s *Server) completionWithTools(content string, toolCalls []any) chatResponse {
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	return chatResponse{
		ID:      fmt.Sprintf("chatcmpl-memgw-%d", s.nextReply()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   s.opts.Model,
		Choices: []responseChoice{{
			Index:        0,
			Message:      responseMessage{Role: "assistant", Content: content, ToolCalls: toolCalls},
			FinishReason: finish,
		}},
		Usage: map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// streamCompletion answers a `stream: true` request with OpenAI chat.completion
// chunks. The Go provider tolerates a plain JSON answer to a stream request,
// but litellm (the Python implementation's transport) treats it as an empty
// stream and retries, so a shared gateway must speak SSE.
func (s *Server) streamCompletion(w http.ResponseWriter, content string, toolCalls []any, includeUsage bool) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	id := fmt.Sprintf("chatcmpl-memgw-%d", s.nextReply())
	created := time.Now().Unix()
	send := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   s.opts.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send(map[string]any{"role": "assistant"}, nil)
	if len(toolCalls) > 0 {
		indexed := make([]any, 0, len(toolCalls))
		for index, call := range toolCalls {
			entry, _ := call.(map[string]any)
			entry["index"] = index
			indexed = append(indexed, entry)
		}
		send(map[string]any{"tool_calls": indexed}, nil)
		send(map[string]any{}, "tool_calls")
	} else {
		runes := []rune(content)
		const chunk = 48
		for start := 0; start < len(runes); start += chunk {
			end := start + chunk
			if end > len(runes) {
				end = len(runes)
			}
			send(map[string]any{"content": string(runes[start:end])}, nil)
		}
		send(map[string]any{}, "stop")
	}
	if includeUsage {
		usage := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": s.opts.Model,
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		encoded, _ := json.Marshal(usage)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// --- timeline answers ---------------------------------------------------

var sessionLineRe = regexp.MustCompile(`(?m)^Session (S\d+), (\d{4}-\d{2}-\d{2}) \([^)]+\)`)

type timelineItem struct {
	Session string   `json:"session"`
	Turn    int      `json:"turn"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Start   string   `json:"start"`
	End     string   `json:"end"`
	Aliases []string `json:"aliases,omitempty"`
}

type timelineAnswerBody struct {
	Items []timelineItem `json:"items"`
}

func timelineAnswer(req chatRequest) string {
	prompt := req.lastUserText()
	out := timelineAnswerBody{Items: []timelineItem{}}
	for _, match := range sessionLineRe.FindAllStringSubmatch(prompt, -1) {
		label := match[1]
		started, err := time.Parse("2006-01-02", match[2])
		if err != nil {
			continue
		}
		yesterday := started.AddDate(0, 0, -1).Format("2006-01-02")
		twoWeeks := started.AddDate(0, 0, -14).Format("2006-01-02")
		twoWeeksEarlier := started.AddDate(0, 0, -15).Format("2006-01-02")
		out.Items = append(out.Items,
			// "yesterday" resolved against the session date carried by the prompt.
			timelineItem{
				Session: label, Turn: 1, Kind: "event",
				Text:  "用户在前一天参加了同事的婚礼，随礼 800 元",
				Start: yesterday, End: yesterday,
				Aliases: []string{"婚礼", "喜宴", "随礼", "北京", "第五条应被截断"},
			},
			// Deliberately reversed range: the reader must swap start/end.
			timelineItem{
				Session: label, Turn: 2, Kind: "plan",
				Text:  "用户计划去看牙医",
				Start: twoWeeks, End: twoWeeksEarlier,
			},
			// Unknown kind, out-of-range turn, unparsable date: normalized.
			timelineItem{
				Session: label, Turn: 999, Kind: "reminder",
				Text:  "用户养了一只橘猫叫团子",
				Start: "not-a-date", End: "",
			},
			// Dropped: unknown session label.
			timelineItem{Session: "S99", Turn: 1, Kind: "event", Text: "不应写入"},
			// Dropped: empty text.
			timelineItem{Session: label, Turn: 1, Kind: "event", Text: "   "},
		)
	}
	encoded, _ := json.Marshal(out)
	return string(encoded)
}

// --- recall selector answers -------------------------------------------

var memoryLineRe = regexp.MustCompile(`(?m)^\[([a-z]+)\] ([^:\n]+): (.*)$`)

func recallAnswer(query string) string {
	selected := []string{}
	for _, match := range memoryLineRe.FindAllStringSubmatch(query, -1) {
		name := strings.TrimSpace(match[2])
		if strings.HasSuffix(name, ".md") && !strings.Contains(name, "profile") {
			selected = append(selected, name)
		}
		if len(selected) == 2 {
			break
		}
	}
	if len(selected) == 0 {
		selected = []string{}
	}
	encoded, _ := json.Marshal(map[string]any{"selected_memories": selected})
	return string(encoded)
}

// --- chat answers -------------------------------------------------------

var (
	// A client turn asks for a tool call with a whole-line marker, e.g.
	// "CALL search_timeline {"pattern":"婚礼"}", optionally stamped with a
	// `[2026-11-20 18:00 CST] ` prefix. Matched line by line and validated
	// as JSON so a reminder that merely quotes such a message (the excerpt
	// block prefixes it with "user: ") never triggers a call.
	callRe = regexp.MustCompile(`^(?:\[\d{4}-\d{2}-\d{2}[^\]]*\]\s*)?CALL\s+([A-Za-z_]+)\s+(\{.*\})$`)
)

func findCall(text string) (string, string, bool) {
	for _, line := range strings.Split(text, "\n") {
		match := callRe.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		arguments := strings.TrimSpace(match[2])
		if json.Valid([]byte(arguments)) {
			return match[1], arguments, true
		}
	}
	return "", "", false
}

func (s *Server) chatAnswer(req chatRequest) (string, []any) {
	// Only the continuation request of a tool call carries a tool message as
	// its last turn. A later user turn keeps the earlier tool result in
	// history and must still be free to ask for a new call.
	if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
		return "TOOL_RESULT_ECHO: " + truncate(req.lastToolText(), 900), nil
	}
	last := req.lastUserText()
	if name, arguments, ok := findCall(last); ok {
		call := map[string]any{
			"id":   fmt.Sprintf("call_memgw_%d", s.nextReply()),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": arguments,
			},
		}
		return "", []any{call}
	}
	reply := fmt.Sprintf("ASSISTANT_REPLY_%d_OK", s.nextReply())
	if compact := strings.Join(strings.Fields(last), " "); compact != "" {
		reply += " (echo: " + truncate(compact, 120) + ")"
	}
	return reply, nil
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// SortRecords orders records by sequence (already appended in order; helper
// for readers that merge several gateway log files).
func SortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
}
