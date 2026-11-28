// Package routeutil holds the HTTP/JSON helpers shared by the server package
// and its route subpackages: response writing, Python-compatible value
// conversion, JSON body decoding, SSE frames, and the browser bridge's raw
// HTTP/1.0-over-TCP request framing. Route subpackages import this package
// instead of the server assembly package, so dependencies point downward.
package routeutil

// Parity-exempt: Go-side extraction of internal/server's shared HTTP/JSON
// helpers; upstream has no equivalent package boundary.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxJSONBodyBytes is the JSON body cap shared with the server package.
// Upstream aiohttp configures client_max_size=20 MiB for the whole app
// (framework/server/app.py::create_app); a JSON body past it is refused with
// 413 before the handler parses it.
const MaxJSONBodyBytes int64 = 20 << 20 // 20 MiB

// InternalServerError is the generic 500 body message the routes return when
// a handler leaves a failure uncaught upstream.
const InternalServerError = "Internal server error"

// ErrBodyTooLarge reports a request body past MaxJSONBodyBytes. The server
// package aliases this sentinel so errors from its own limited reads and from
// DecodeJSONValue are both recognised by WriteBodyFailure.
var ErrBodyTooLarge = errors.New("request body too large")

// WriteJSON writes status and body as a JSON response, matching the server
// package's historical helper byte-for-byte.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteBodyFailure maps a JSON body read/decode failure to upstream's
// response: a body past the shared cap is 413 (aiohttp's client_max_size
// guard), while every other failure keeps the handler's own status and
// message. Callers that catch JSONDecodeError-like failures pass 400; the
// routes upstream leaves uncaught pass 500 and InternalServerError.
func WriteBodyFailure(w http.ResponseWriter, err error, status int, message string) {
	if errors.Is(err, ErrBodyTooLarge) {
		WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "Request body too large"})
		return
	}
	WriteJSON(w, status, map[string]any{"error": message})
}

// DecodeJSONValue reads at most MaxJSONBodyBytes from r and decodes one JSON
// value with the same json.loads semantics as DecodeJSONBytes.
func DecodeJSONValue(r *http.Request) (any, error) {
	data, err := readLimitedBody(r, MaxJSONBodyBytes)
	if err != nil {
		return nil, err
	}
	return DecodeJSONBytes(data)
}

// DecodeJSONBytes mirrors Python json.loads: one value, no trailing data.
func DecodeJSONBytes(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return decoded, nil
}

// PyTruthy mirrors Python truthiness over decoded JSON values: nil, false,
// zero numbers, empty strings/arrays/objects are falsy; everything else
// (including unknown types) is truthy.
func PyTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// PyIntValue mirrors isinstance(value, int) for a JSON-decoded value.
// Python's bool is a subclass of int, so true counts as 1.
func PyIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return 0, false
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int(typed), true
	}
	return 0, false
}

// PyStr mirrors Python's str() over a JSON-decoded value: nil is "", bools are
// True/False, floats keep a Python-style repr (an integral float gains ".0"),
// and anything else falls back to %v.
func PyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case json.Number:
		return v.String()
	case float64:
		s := strconv.FormatFloat(v, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
			s += ".0"
		}
		return s
	case float32:
		return PyStr(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprintf("%v", value)
}

// PyFloat mirrors float(value) over the values the routes read back out of
// decoded JSON: numbers and bools convert, numeric strings are trimmed and
// parsed, and everything else is rejected.
func PyFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// WriteNamedSSEEvent renders one SSE frame with an explicit event name, as
// SSEResponse.send_event(event=...) does.
func WriteNamedSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
	flusher.Flush()
}

// BridgeProbeTimeout is the short deadline the browser-bridge status probes
// use; a silent local socket must not stall the route.
const BridgeProbeTimeout = 500 * time.Millisecond

// BridgeHTTPRequest speaks the browser bridge's HTTP/1.0-over-TCP contract and
// reads the response to EOF (the bridge closes after replying).
func BridgeHTTPRequest(addr, request string, timeout time.Duration) ([]byte, error) {
	connection, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(connection, request); err != nil {
		return nil, err
	}
	return io.ReadAll(connection)
}

// BridgeResponseBody splits a raw bridge reply at the header terminator and
// returns the body after it.
func BridgeResponseBody(raw []byte) ([]byte, bool) {
	marker := []byte("\r\n\r\n")
	index := bytes.Index(raw, marker)
	if index < 0 {
		return nil, false
	}
	return raw[index+len(marker):], true
}

// readLimitedBody reads at most limit bytes and reports ErrBodyTooLarge rather
// than truncating silently.
func readLimitedBody(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}
