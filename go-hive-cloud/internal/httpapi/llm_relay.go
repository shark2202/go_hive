// HTTP plumbing for the LLM proxy: request-body cap, header forwarding and
// SSE relay. Anthropic-specific header handling lives here so llm.go stays
// focused on routing and status/error mapping.
package httpapi

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errRequestBodyTooLarge = errors.New("llm: request body too large")

// cappedBody enforces the proxy's request-body ceiling while streaming the
// body to the upstream (no full buffering).
type cappedBody struct {
	rc    io.ReadCloser
	limit int64
	read  int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.read += int64(n)
	if c.read > c.limit {
		return n, errRequestBodyTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }

// capRequestBody rejects a declared over-limit body before dialing and enforces
// the same ceiling for chunked bodies.
func capRequestBody(r *http.Request, limit int64) (io.ReadCloser, error) {
	if r.ContentLength > limit {
		return nil, errRequestBodyTooLarge
	}
	return &cappedBody{rc: r.Body, limit: limit}, nil
}

// forwardMessagesHeaders sends only the Messages protocol headers a client may
// set. The client's Authorization/x-api-key are deliberately dropped and
// replaced with the configured upstream credential; hop-by-hop and unrelated
// headers never reach the upstream.
func forwardMessagesHeaders(dst, src http.Header, version, authStyle, key string) {
	dst.Set("Content-Type", headerOrDefault(src.Get("Content-Type"), "application/json"))
	if accept := src.Get("Accept"); accept != "" {
		dst.Set("Accept", accept)
	}
	dst.Set("Anthropic-Version", headerOrDefault(src.Get("Anthropic-Version"), version))
	if beta := src.Get("Anthropic-Beta"); beta != "" {
		dst.Set("Anthropic-Beta", beta)
	}
	dst.Set("User-Agent", "hive-cloud")
	setUpstreamCredential(dst, authStyle, key)
}

// forwardImagesHeaders mirrors the observed image client: JSON body plus Bearer
// HIVE_API_KEY. Only Content-Type/Accept are relayed from the caller.
func forwardImagesHeaders(dst, src http.Header, key string) {
	dst.Set("Content-Type", headerOrDefault(src.Get("Content-Type"), "application/json"))
	if accept := src.Get("Accept"); accept != "" {
		dst.Set("Accept", accept)
	}
	dst.Set("User-Agent", "hive-cloud")
	if key != "" {
		dst.Set("Authorization", "Bearer "+key)
	}
}

func setUpstreamCredential(dst http.Header, authStyle, key string) {
	if key == "" {
		return
	}
	if authStyle == authStyleBearer {
		dst.Set("Authorization", "Bearer "+key)
		return
	}
	dst.Set("X-Api-Key", key)
}

func headerOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// copyUpstreamHeaders relays upstream response headers except hop-by-hop and
// framing headers (Go recomputes framing). This preserves Anthropic's
// rate-limit/request-id headers and upstream error content types.
func copyUpstreamHeaders(dst, src http.Header) {
	blocked := connectionHeaderSet(src)
	for key, values := range src {
		if isHopByHopHeader(key) || blocked[strings.ToLower(key)] || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func connectionHeaderSet(header http.Header) map[string]bool {
	out := map[string]bool{}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
				out[token] = true
			}
		}
	}
	return out
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// relaySSE copies an SSE body byte-for-byte and flushes after every line, so a
// client sees each event (and keepalive comment) as soon as the upstream writes
// it. It intentionally does not parse event names: the proxy forwards the
// upstream's whole event vocabulary, including events this module does not know.
func relaySSE(w http.ResponseWriter, body io.Reader) error {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if _, writeErr := io.WriteString(w, line); writeErr != nil {
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
