// Package stallgw is the deterministic OpenAI-compatible gateway used by the
// stall-check end-to-end test. It is test infrastructure: the production
// runtime only ever sees it as a configured api_base.
//
// One process plays both models the stall check needs:
//
//   - the queen's chat model (POST /v1/chat/completions): the first turn answers
//     with the announcement and no tool call — the "talks about work and stops"
//     reply the check exists for; later turns (the reminder turn included)
//     answer differently. Session-title requests are answered with a fixed
//     title. Streaming requests are answered with OpenAI chat.completion
//     chunks, because the Go provider does stream chat turns.
//   - the judge slot (POST /judge/systemone, the TypeSafe “noul“ protocol):
//     every question is answered with the configured probability.
//
// Every request is recorded with its raw body so a test can assert what the
// runtime actually sent.
package stallgw

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Kind classifies a recorded request.
type Kind string

const (
	KindQueen Kind = "queen"
	KindTitle Kind = "title"
	KindJudge Kind = "judge"
)

// Record is one request the gateway saw.
type Record struct {
	Seq  int    `json:"seq"`
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	Auth string `json:"auth,omitempty"`
	Body string `json:"body"`
}

// Options configures a Server.
type Options struct {
	// Model is the model name echoed in completions.
	Model string
	// Announcement is the first queen turn's reply: work promised, no tool call.
	Announcement string
	// LaterReply answers every queen turn after the first.
	LaterReply string
	// Title answers session-title requests.
	Title string
	// StallProbability is the judge's noul answer: 1 = stalled, 0 = fine.
	StallProbability float64
	// ToolCallName, when set, makes the first queen turn call this tool with
	// empty arguments instead of answering with the announcement: the turn
	// that did work. Queen turns after the tool result answer ToolCallReply.
	ToolCallName string
	// ToolCallReply answers the queen turn that follows the tool result.
	ToolCallReply string
	// LogPath, when set, appends one JSON record per request to that file.
	LogPath string
	// ReplyWhen, when set, restricts the scripted queen turns to requests
	// whose body carries the marker (the run's user message): other queen
	// calls — session-start turns, for one — answer FillerReply and leave
	// the script untouched.
	ReplyWhen string
	// FillerReply answers queen calls that do not carry ReplyWhen.
	FillerReply string
}

// Server is an http.Handler plus its request recorder.
type Server struct {
	opts Options

	mu         sync.Mutex
	seq        int
	queenTurns int
	records    []Record
}

// New returns a gateway server with the defaults filled in.
func New(opts Options) *Server {
	if opts.Model == "" {
		opts.Model = "stall-e2e-model"
	}
	if opts.Announcement == "" {
		opts.Announcement = "On it — drawing the fox now."
	}
	if opts.LaterReply == "" {
		opts.LaterReply = "Done."
	}
	if opts.Title == "" {
		opts.Title = "Fox drawing"
	}
	if opts.FillerReply == "" {
		opts.FillerReply = "（后台）收到。"
	}
	return &Server{opts: opts}
}

// Handler returns the HTTP handler the runtime talks to.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("POST /chat/completions", s.chat)
	mux.HandleFunc("POST /judge/systemone", s.judge)
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

func (s *Server) record(kind Kind, r *http.Request, body string) {
	s.mu.Lock()
	s.seq++
	record := Record{Seq: s.seq, Kind: kind, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body}
	s.records = append(s.records, record)
	logPath := s.opts.LogPath
	s.mu.Unlock()
	if logPath != "" {
		if encoded, err := json.Marshal(record); err == nil {
			if file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = file.Write(append(encoded, '\n'))
				_ = file.Close()
			}
		}
	}
}

func readPayload(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return "", nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", nil, false
	}
	return string(raw), payload, true
}

// chat answers the queen's model calls. The first turn is the scripted stall:
// an announcement with no tool call. Everything after it (including the turn a
// reminder wakes) answers LaterReply, so a test can tell the two apart.
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	raw, payload, ok := readPayload(w, r)
	if !ok {
		return
	}
	system := systemPrompt(payload)
	kind := KindQueen
	content := ""
	toolCall := ""
	s.mu.Lock()
	if strings.Contains(system, "You name chat conversations") {
		kind = KindTitle
		content = s.opts.Title
	} else if s.opts.ReplyWhen != "" && !strings.Contains(raw, s.opts.ReplyWhen) {
		content = s.opts.FillerReply
	} else {
		s.queenTurns++
		switch {
		case s.queenTurns == 1 && s.opts.ToolCallName != "":
			toolCall = s.opts.ToolCallName
		case s.queenTurns == 1:
			content = s.opts.Announcement
		case s.queenTurns == 2 && s.opts.ToolCallName != "":
			content = s.opts.ToolCallReply
		default:
			content = s.opts.LaterReply
		}
	}
	s.mu.Unlock()
	s.record(kind, r, raw)

	if stream, _ := payload["stream"].(bool); stream {
		includeUsage := false
		if options, ok := payload["stream_options"].(map[string]any); ok {
			includeUsage, _ = options["include_usage"].(bool)
		}
		if toolCall != "" {
			s.streamToolCall(w, toolCall, includeUsage)
			return
		}
		s.streamCompletion(w, content, includeUsage)
		return
	}
	if toolCall != "" {
		writeCompletion(w, s.opts.Model, map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":       "call_stallgw_tool",
				"type":     "function",
				"function": map[string]any{"name": toolCall, "arguments": "{}"},
			}},
		}, "tool_calls")
		return
	}
	writeCompletion(w, s.opts.Model, map[string]any{
		"role":    "assistant",
		"content": content,
	}, "stop")
}

// judge answers the TypeSafe “/systemone“ noul question with the configured
// probability, in the shape judge.py expects: answers.answer.noul.
func (s *Server) judge(w http.ResponseWriter, r *http.Request) {
	raw, _, ok := readPayload(w, r)
	if !ok {
		return
	}
	s.record(KindJudge, r, raw)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"answers": map[string]any{
			"answer": map[string]any{"noul": s.opts.StallProbability},
		},
	})
}

// systemPrompt returns the first system message's content, if any.
func systemPrompt(payload map[string]any) string {
	messages, _ := payload["messages"].([]any)
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role == "system" {
			content, _ := message["content"].(string)
			return content
		}
	}
	return ""
}

func writeCompletion(w http.ResponseWriter, model string, message map[string]any, finishReason string) {
	payload := map[string]any{
		"id":      "chatcmpl-stallgw",
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

// streamCompletion answers a `stream: true` request with OpenAI
// chat.completion chunks, the shape a real OpenAI-compatible endpoint returns.
func (s *Server) streamCompletion(w http.ResponseWriter, content string, includeUsage bool) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	created := time.Now().Unix()
	id := "chatcmpl-stallgw-stream"
	send := func(payload map[string]any) {
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) {
		send(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   s.opts.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		})
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	chunk(map[string]any{"content": content}, nil)
	chunk(map[string]any{}, "stop")
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

// streamToolCall answers a `stream: true` request with the OpenAI
// tool_calls delta shape: the scripted turn that did work.
func (s *Server) streamToolCall(w http.ResponseWriter, name string, includeUsage bool) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	created := time.Now().Unix()
	id := "chatcmpl-stallgw-tool"
	send := func(payload map[string]any) {
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) {
		send(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   s.opts.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		})
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index": 0,
		"id":    "call_stallgw_tool",
		"type":  "function",
		"function": map[string]any{
			"name":      name,
			"arguments": "",
		},
	}}}, nil)
	chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index":    0,
		"function": map[string]any{"arguments": "{}"},
	}}}, nil)
	chunk(map[string]any{}, "tool_calls")
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
