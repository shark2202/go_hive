// Package soulgw is the deterministic OpenAI-compatible gateway used by the
// soul reflection tests (unit and end-to-end). It is test infrastructure: the
// production runtime only ever sees it as a configured api_base.
//
// The gateway plays the reflection tool loop the way a cooperating model
// would: the first call for a conversation answers with add_item, quoting a
// line copied from the prompt it just received (so the runtime's verbatim
// quote check can pass); the follow-up call — the one carrying a role="tool"
// message — answers with finish. Non-reflection requests (for example a chat
// session's own turn) get a plain reply. Every request is recorded so a test
// can assert what the runtime actually sent (system prompt, tools, chat text).
package soulgw

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kind classifies a recorded request.
type Kind string

const (
	KindReflection Kind = "reflection"
	KindChat       Kind = "chat"
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
	// Model is the model name echoed in responses.
	Model string
	// Section is the soul section add_item targets.
	Section string
	// ItemText is the text add_item writes.
	ItemText string
	// Delay, when positive, pauses the first reflection call (the one without
	// a tool result), so a test can observe a reflection while it is in flight.
	Delay time.Duration
	// FailureStatus, when non-zero, answers every reflection call with that
	// HTTP status instead of a tool call (model-failure tests).
	FailureStatus int
}

// Server is an http.Handler plus its request recorder.
type Server struct {
	opts Options

	mu      sync.Mutex
	seq     int
	records []Record
}

// New returns a gateway server with the defaults filled in.
func New(opts Options) *Server {
	if opts.Model == "" {
		opts.Model = "soul-e2e-model"
	}
	if opts.Section == "" {
		opts.Section = "now"
	}
	if opts.ItemText == "" {
		opts.ItemText = "I want to keep painting."
	}
	return &Server{opts: opts}
}

// Handler returns the HTTP handler the runtime talks to.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// Requests returns a copy of everything recorded so far.
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

// ReflectionRequests returns the recorded reflection requests.
func (s *Server) ReflectionRequests() []Record {
	out := []Record{}
	for _, record := range s.Requests() {
		if record.Kind == KindReflection {
			out = append(out, record)
		}
	}
	return out
}

func (s *Server) recordRequest(kind Kind, path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.records = append(s.records, Record{Seq: s.seq, Kind: kind, Path: path, Body: body})
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind := KindChat
	if requestCarriesTool(payload, "add_item") {
		kind = KindReflection
	}
	s.recordRequest(kind, r.URL.Path, string(raw))
	if kind != KindReflection {
		writePlain(w, s.opts.Model, "ok")
		return
	}
	if s.opts.FailureStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.opts.FailureStatus)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "soulgw: requested model failure"},
		})
		return
	}
	_, user, hasToolResult := reflectionMessages(payload)
	if !hasToolResult && s.opts.Delay > 0 {
		time.Sleep(s.opts.Delay)
	}
	if hasToolResult {
		writeToolCall(w, s.opts.Model, "finish", map[string]any{
			"summary": "Noted what the chats showed.",
		})
		return
	}
	quote := pickQuote(user)
	if quote == "" {
		writeToolCall(w, s.opts.Model, "finish", map[string]any{
			"summary": "Nothing to note.",
		})
		return
	}
	writeToolCall(w, s.opts.Model, "add_item", map[string]any{
		"section":  s.opts.Section,
		"text":     s.opts.ItemText,
		"evidence": []any{quote},
		"reason":   "It came up in the chats.",
	})
}

func requestCarriesTool(payload map[string]any, name string) bool {
	tools, _ := payload["tools"].([]any)
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] == name {
			return true
		}
	}
	return false
}

// reflectionMessages returns the system prompt, the user prompt and whether the
// conversation already carries a tool result (i.e. this is a follow-up turn).
func reflectionMessages(payload map[string]any) (string, string, bool) {
	system, user := "", ""
	toolResult := false
	messages, _ := payload["messages"].([]any)
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		switch role {
		case "system":
			if system == "" {
				system = content
			}
		case "user":
			user = content
		case "tool":
			toolResult = true
		}
	}
	return system, user, toolResult
}

// pickQuote copies the longest chat line out of the reflection prompt's
// conversation section. Quoting what the prompt carried (rather than a fixture
// string) keeps the answer valid no matter how the runtime clips the material.
func pickQuote(user string) string {
	if index := strings.Index(user, "## Your chats since your last reflection"); index >= 0 {
		user = user[index:]
	}
	best := ""
	for _, line := range strings.Split(user, "\n") {
		label := strings.Index(line, "] ")
		if label < 0 {
			continue
		}
		rest := line[label+2:]
		separator := strings.Index(rest, ": ")
		if separator <= 0 {
			continue
		}
		text := strings.TrimSpace(rest[separator+2:])
		if utf8.RuneCountInString(text) < 4 || strings.HasPrefix(text, "(") {
			continue
		}
		if utf8.RuneCountInString(text) > utf8.RuneCountInString(best) {
			best = text
		}
	}
	if utf8.RuneCountInString(best) > 120 {
		best = string([]rune(best)[:120])
	}
	return best
}

func writePlain(w http.ResponseWriter, model, content string) {
	writeCompletion(w, model, map[string]any{
		"role":    "assistant",
		"content": content,
	}, "stop")
}

func writeToolCall(w http.ResponseWriter, model, name string, args map[string]any) {
	encoded, err := json.Marshal(args)
	if err != nil {
		http.Error(w, "encode arguments", http.StatusInternalServerError)
		return
	}
	writeCompletion(w, model, map[string]any{
		"role":    "assistant",
		"content": nil,
		"tool_calls": []any{map[string]any{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(encoded),
			},
		}},
	}, "tool_calls")
}

func writeCompletion(w http.ResponseWriter, model string, message map[string]any, finishReason string) {
	payload := map[string]any{
		"id":      "chatcmpl-soulgw",
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
