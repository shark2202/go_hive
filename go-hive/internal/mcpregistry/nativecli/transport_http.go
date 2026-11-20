package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// requestTimeout bounds individual HTTP round trips (not SSE streams).
const requestTimeout = 60 * time.Second

// httpStatusError carries a non-2xx response so callers can print mcp2cli's
// "Error <status>: <body>" line.
type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("Error %d: %s", e.status, e.body)
}

// streamableTransport speaks the MCP streamable HTTP transport: JSON-RPC
// requests are POSTed, and the reply arrives either as a JSON body or as an
// SSE stream on the same response.
type streamableTransport struct {
	url           string
	headers       []Field
	client        *http.Client
	sessionID     string
	protocolValue string
	queue         [][]byte
}

func newStreamableTransport(rawURL string, headers []Field) *streamableTransport {
	return &streamableTransport{
		url:     rawURL,
		headers: headers,
		client:  &http.Client{Transport: http.DefaultTransport},
	}
}

// Send posts one JSON-RPC message and buffers whatever the server answers.
func (t *streamableTransport) Send(message []byte) error {
	context, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(context, http.MethodPost, t.url, bytes.NewReader(message))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	applyHeaders(request.Header, t.headers)
	if t.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.protocolValue != "" {
		request.Header.Set("MCP-Protocol-Version", t.protocolValue)
	}

	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		t.sessionID = sessionID
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return &httpStatusError{status: response.StatusCode, body: strings.TrimSpace(string(body))}
	}

	wantID, hasID := messageID(message)
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 {
			t.queue = append(t.queue, append([]byte{}, trimmed...))
		}
		return nil
	}
	if !hasID {
		// Notifications have no reply to wait for.
		return nil
	}
	return t.readEventStream(response.Body, wantID)
}

// readEventStream consumes SSE events until the response for wantID arrives.
func (t *streamableTransport) readEventStream(body io.Reader, wantID string) error {
	reader := bufio.NewReaderSize(body, 1<<16)
	for {
		event, data, err := readSSEEvent(reader)
		if err != nil {
			if err == io.EOF && data == "" {
				return io.ErrUnexpectedEOF
			}
			if data == "" {
				return err
			}
		}
		if data == "" {
			continue
		}
		t.queue = append(t.queue, []byte(data))
		if id, ok := responseID([]byte(data)); ok && id == wantID {
			return nil
		}
		if event == "endpoint" {
			continue
		}
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
	}
}

// Receive pops the next buffered server message.
func (t *streamableTransport) Receive() ([]byte, error) {
	if len(t.queue) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	message := t.queue[0]
	t.queue = t.queue[1:]
	return message, nil
}

func (t *streamableTransport) Close() error { return nil }

// sseTransport speaks the older HTTP+SSE transport: a GET stream carries
// server messages, and an endpoint event names the POST target.
type sseTransport struct {
	url      string
	headers  []Field
	client   *http.Client
	messages chan []byte
	failures chan error
	ready    chan struct{}
	endpoint string
	cancel   context.CancelFunc
	body     io.Closer
}

func newSSETransport(rawURL string, headers []Field) (*sseTransport, error) {
	context, cancel := context.WithCancel(context.Background())
	transport := &sseTransport{
		url:      rawURL,
		headers:  headers,
		client:   &http.Client{Transport: http.DefaultTransport},
		messages: make(chan []byte, 16),
		failures: make(chan error, 1),
		ready:    make(chan struct{}),
		cancel:   cancel,
	}
	request, err := http.NewRequestWithContext(context, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	applyHeaders(request.Header, transport.headers)
	response, err := transport.client.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		cancel()
		return nil, &httpStatusError{status: response.StatusCode, body: strings.TrimSpace(string(body))}
	}
	transport.body = response.Body
	go transport.pump(response.Body)
	return transport, nil
}

// pump reads the event stream, resolving the endpoint once and forwarding
// every message event.
func (t *sseTransport) pump(body io.Reader) {
	reader := bufio.NewReaderSize(body, 1<<16)
	for {
		event, data, err := readSSEEvent(reader)
		if data != "" {
			switch event {
			case "endpoint":
				if resolved, resolveErr := resolveEndpoint(t.url, data); resolveErr == nil {
					t.endpoint = resolved
					select {
					case <-t.ready:
					default:
						close(t.ready)
					}
				}
			case "message", "":
				select {
				case t.messages <- []byte(data):
				case <-time.After(requestTimeout):
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				select {
				case t.failures <- err:
				default:
				}
			}
			select {
			case <-t.ready:
			default:
				close(t.ready)
			}
			close(t.messages)
			return
		}
	}
}

// Send posts a JSON-RPC message to the endpoint announced by the server.
func (t *sseTransport) Send(message []byte) error {
	select {
	case <-t.ready:
	case <-time.After(requestTimeout):
		return fmt.Errorf("timed out waiting for the SSE endpoint event")
	}
	if t.endpoint == "" {
		return fmt.Errorf("SSE server did not announce an endpoint")
	}
	context, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(context, http.MethodPost, t.endpoint, bytes.NewReader(message))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	applyHeaders(request.Header, t.headers)
	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &httpStatusError{status: response.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return nil
}

// Receive waits for the next message event.
func (t *sseTransport) Receive() ([]byte, error) {
	message, ok := <-t.messages
	if !ok {
		select {
		case err := <-t.failures:
			return nil, err
		default:
			return nil, io.EOF
		}
	}
	return message, nil
}

func (t *sseTransport) Close() error {
	t.cancel()
	if t.body != nil {
		return t.body.Close()
	}
	return nil
}

// resolveEndpoint resolves a relative SSE endpoint against the stream URL.
func resolveEndpoint(base, endpoint string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	return parsed.ResolveReference(target).String(), nil
}

// applyHeaders adds configured auth headers to a request.
func applyHeaders(headers http.Header, values []Field) {
	for _, header := range values {
		headers.Set(header.Key, stringOf(header.Value))
	}
}

// messageID extracts the "id" member of a JSON-RPC message as a string.
func messageID(message []byte) (string, bool) {
	object, ok := decodeRPCObject(message)
	if !ok {
		return "", false
	}
	value, found := object.Lookup("id")
	if !found {
		return "", false
	}
	return stringOf(value), true
}

// responseID extracts the id of a JSON-RPC *response* (a message without a
// method); server requests also carry ids and must not be mistaken for it.
func responseID(message []byte) (string, bool) {
	object, ok := decodeRPCObject(message)
	if !ok {
		return "", false
	}
	if _, hasMethod := object.Lookup("method"); hasMethod {
		return "", false
	}
	return messageID(message)
}

func decodeRPCObject(message []byte) (Ordered, bool) {
	parsed, err := ParseJSON(message)
	if err != nil {
		return nil, false
	}
	object, ok := parsed.(Ordered)
	return object, ok
}

// readSSEEvent reads one server-sent event, returning its name and data.
func readSSEEvent(reader *bufio.Reader) (string, string, error) {
	event := ""
	var data strings.Builder
	seen := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trimmed, "data:") {
				appendSSEData(&data, strings.TrimPrefix(trimmed, "data:"))
				seen = true
			}
			return event, strings.TrimSuffix(data.String(), "\n"), err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if !seen {
				continue
			}
			return event, strings.TrimSuffix(data.String(), "\n"), nil
		}
		if strings.HasPrefix(trimmed, ":") {
			continue
		}
		name, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			event = value
		case "data":
			appendSSEData(&data, value)
			seen = true
		}
	}
}

func appendSSEData(builder *strings.Builder, value string) {
	builder.WriteString(value)
	builder.WriteString("\n")
}
