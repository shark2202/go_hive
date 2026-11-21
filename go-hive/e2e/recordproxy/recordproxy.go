// Package recordproxy is a pass-through recording proxy for real-model E2E
// runs: it forwards every request to the configured real upstream and appends
// one JSON record per exchange to a log file. It carries no product code.
//
// The proxy exists so a run against a real model can record what each side
// actually sent (model slot, messages, tool surface) and received, without the
// API key ever entering the configuration under test: the key lives only in
// the proxy process, and request headers are never written to the log.
package recordproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Options configures the proxy.
type Options struct {
	// Upstream is the real API base, e.g. https://token.tokenbroad.com/v1.
	// Request paths are appended verbatim (the harness points api_base at
	// <proxy>/v1 so /v1/chat/completions maps to <upstream>/chat/completions).
	Upstream string
	// APIKey is injected as the upstream Authorization bearer token.
	APIKey string
	// LogPath receives one JSON record per request/response exchange.
	LogPath string
	// Timeout caps one upstream exchange. Zero picks 10 minutes.
	Timeout time.Duration
	// BodyHeadLimit/BodyTailLimit cap the recorded request body: the head
	// carries the prompt/system surface, the tail the most recent messages
	// (where escalation/reply markers land). Zero picks defaults. The elision
	// marker keeps the record self-describing.
	BodyHeadLimit int
	BodyTailLimit int
	// ResponseLimit caps the recorded response body (SSE or JSON).
	ResponseLimit int
}

const (
	defaultBodyHeadLimit = 16 * 1024
	defaultBodyTailLimit = 48 * 1024
	defaultResponseLimit = 128 * 1024
)

// Record is one recorded exchange. Fields are stable for cross-run diffing;
// headers are never recorded.
type Record struct {
	Seq           int      `json:"seq"`
	Time          string   `json:"time"`
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	Model         string   `json:"model"`
	Stream        bool     `json:"stream"`
	Tools         []string `json:"tools"`
	MessageCount  int      `json:"message_count"`
	Roles         []string `json:"roles"`
	Body          string   `json:"body"`
	Status        int      `json:"status"`
	ResponseBody  string   `json:"response_body"`
	Content       string   `json:"content"`
	ToolCalls     []string `json:"tool_calls"`
	FinishReason  string   `json:"finish_reason"`
	ResponseModel string   `json:"response_model"`
	Error         string   `json:"error,omitempty"`
}

// Server is the proxy handler plus its recording state.
type Server struct {
	opts   Options
	client *http.Client

	mu      sync.Mutex
	log     *os.File
	seq     int
	records []Record
}

// New builds the proxy; the caller owns Close.
func New(opts Options) (*Server, error) {
	if strings.TrimSpace(opts.Upstream) == "" {
		return nil, fmt.Errorf("recordproxy: upstream is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.BodyHeadLimit <= 0 {
		opts.BodyHeadLimit = defaultBodyHeadLimit
	}
	if opts.BodyTailLimit <= 0 {
		opts.BodyTailLimit = defaultBodyTailLimit
	}
	if opts.ResponseLimit <= 0 {
		opts.ResponseLimit = defaultResponseLimit
	}
	server := &Server{
		opts: opts,
		client: &http.Client{
			Timeout: opts.Timeout,
			Transport: &http.Transport{
				DisableCompression: true,
			},
		},
	}
	if opts.LogPath != "" {
		file, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("recordproxy: open log: %w", err)
		}
		server.log = file
	}
	return server, nil
}

// Close releases the log file.
func (s *Server) Close() error {
	if s.log == nil {
		return nil
	}
	return s.log.Close()
}

// Requests returns a copy of every recorded exchange so far.
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

// Handler returns the proxy's HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "recordproxy: read body: "+err.Error(), http.StatusBadGateway)
		return
	}
	_ = r.Body.Close()

	// The harness points api_base at <proxy>/v1, so the request path repeats
	// the upstream's own base path (/v1). Strip the overlap instead of
	// doubling it.
	base := strings.TrimSuffix(s.opts.Upstream, "/")
	requestPath := r.URL.Path
	target := base
	if strings.HasPrefix(requestPath, "/v1/") || strings.HasPrefix(requestPath, "/v1") {
		target = base + strings.TrimPrefix(requestPath, "/v1")
	} else {
		target = base + "/" + strings.TrimPrefix(requestPath, "/")
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "recordproxy: build request: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Carry content negotiation only; the key and every other inbound header
	// stay out of the recorded/forwarded set.
	for _, name := range []string{"Content-Type", "Accept"} {
		if value := r.Header.Get(name); value != "" {
			upstreamReq.Header.Set(name, value)
		}
	}
	upstreamReq.Header.Set("Accept-Encoding", "identity")
	if s.opts.APIKey != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+s.opts.APIKey)
	}

	record := Record{
		Method: r.Method,
		Path:   r.URL.Path,
		Time:   time.Now().UTC().Format(time.RFC3339Nano),
		Body:   clampBody(string(body), s.opts.BodyHeadLimit, s.opts.BodyTailLimit),
	}
	record.Model, record.Stream, record.Tools, record.MessageCount, record.Roles = summarizeRequest(body)

	upstreamResp, err := s.client.Do(upstreamReq)
	if err != nil {
		record.Error = err.Error()
		s.append(record)
		http.Error(w, "recordproxy: upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstreamResp.Body.Close()

	record.Status = upstreamResp.StatusCode
	for name, values := range upstreamResp.Header {
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Content-Encoding") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(upstreamResp.StatusCode)
	flusher, _ := w.(http.Flusher)

	var captured bytes.Buffer
	writer := io.Writer(w)
	if flusher != nil {
		writer = &flushWriter{w: w, flusher: flusher}
	}
	if _, err := io.Copy(io.MultiWriter(writer, &captured), upstreamResp.Body); err != nil {
		record.Error = "stream copy: " + err.Error()
	}
	record.ResponseBody = clampBody(captured.String(), s.opts.ResponseLimit, 0)
	record.Content, record.ToolCalls, record.FinishReason, record.ResponseModel = summarizeResponse(captured.Bytes())
	s.append(record)
}

// flushWriter flushes after every chunk so SSE deltas reach the client live.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 {
		f.flusher.Flush()
	}
	return n, err
}

func (s *Server) append(record Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	record.Seq = s.seq
	s.records = append(s.records, record)
	if s.log == nil {
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = s.log.Write(append(encoded, '\n'))
}

// clampBody keeps the head (and optionally the tail) of an oversized body,
// marking the elision so a reader knows the record is truncated.
func clampBody(text string, head, tail int) string {
	if head <= 0 || len(text) <= head {
		return text
	}
	if tail <= 0 {
		return text[:head] + "\n…[trimmed " + fmt.Sprintf("%d", len(text)-head) + " chars]…"
	}
	if len(text) <= head+tail {
		return text
	}
	omitted := len(text) - head - tail
	return text[:head] + "\n…[trimmed " + fmt.Sprintf("%d", omitted) + " chars]…\n" + text[len(text)-tail:]
}

// summarizeRequest extracts the diffable request fields: model, stream flag,
// advertised tool names and the message roles.
func summarizeRequest(body []byte) (model string, stream bool, tools []string, messageCount int, roles []string) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", false, nil, 0, nil
	}
	model, _ = decoded["model"].(string)
	stream, _ = decoded["stream"].(bool)
	if entries, ok := decoded["tools"].([]any); ok {
		for _, entry := range entries {
			tool, _ := entry.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			name, _ := function["name"].(string)
			if name != "" {
				tools = append(tools, name)
			}
		}
	}
	if messages, ok := decoded["messages"].([]any); ok {
		messageCount = len(messages)
		for _, entry := range messages {
			message, _ := entry.(map[string]any)
			role, _ := message["role"].(string)
			roles = append(roles, role)
		}
	}
	return model, stream, tools, messageCount, roles
}

// summarizeResponse extracts content/tool calls from either a plain JSON
// completion or an SSE stream (delta accumulation).
func summarizeResponse(body []byte) (content string, toolCalls []string, finishReason string, responseModel string) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", nil, "", ""
	}
	if trimmed[0] == '{' {
		var decoded map[string]any
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return "", nil, "", ""
		}
		responseModel, _ = decoded["model"].(string)
		choices, _ := decoded["choices"].([]any)
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			finishReason, _ = choice["finish_reason"].(string)
			message, _ := choice["message"].(map[string]any)
			content, _ = message["content"].(string)
			toolCalls = toolCallNames(message["tool_calls"])
		}
		return content, toolCalls, finishReason, responseModel
	}
	// SSE stream: accumulate deltas.
	type callState struct {
		name string
		args strings.Builder
	}
	calls := map[int]*callState{}
	var order []int
	var contentBuilder strings.Builder
	for _, line := range strings.Split(string(trimmed), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			continue
		}
		if value, _ := decoded["model"].(string); value != "" {
			responseModel = value
		}
		choices, _ := decoded["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if value, _ := choice["finish_reason"].(string); value != "" {
			finishReason = value
		}
		delta, _ := choice["delta"].(map[string]any)
		if value, _ := delta["content"].(string); value != "" {
			contentBuilder.WriteString(value)
		}
		if rawCalls, ok := delta["tool_calls"].([]any); ok {
			for _, entry := range rawCalls {
				call, _ := entry.(map[string]any)
				index := 0
				if number, ok := call["index"].(float64); ok {
					index = int(number)
				}
				state, present := calls[index]
				if !present {
					state = &callState{}
					calls[index] = state
					order = append(order, index)
				}
				function, _ := call["function"].(map[string]any)
				if name, _ := function["name"].(string); name != "" {
					state.name = name
				}
				if args, _ := function["arguments"].(string); args != "" {
					state.args.WriteString(args)
				}
			}
		}
	}
	for _, index := range order {
		state := calls[index]
		if state.name == "" {
			continue
		}
		toolCalls = append(toolCalls, state.name+" "+state.args.String())
	}
	return contentBuilder.String(), toolCalls, finishReason, responseModel
}

func toolCallNames(raw any) []string {
	entries, _ := raw.([]any)
	var names []string
	for _, entry := range entries {
		call, _ := entry.(map[string]any)
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		if name != "" {
			names = append(names, strings.TrimSpace(name+" "+arguments))
		}
	}
	return names
}
