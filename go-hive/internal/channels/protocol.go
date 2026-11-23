package channels

// The process stdio protocol: one UTF-8 JSON object per line, JSON-RPC 2.0
// shaped (request/response by id, plus notifications). A plugin writes
// protocol frames to stdout only; its logs belong on stderr.
//
// The wire contract is a Go-only implementation of upstream's in-process
// ChannelHost object graph. Method names, params and the QueenOutput kinds
// keep upstream's vocabulary; the transport is documented here.
//
// Parity-exempt: stdio JSON-lines transport; upstream is an in-process Python
// object (ChannelHost / QueenOutput), not a wire protocol.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProtocolVersion is the handshake protocol name in hello.
const ProtocolVersion = "hive-channel/1"

// Frame and image limits. A frame is one newline-terminated JSON object.
// Images arrive base64 in a send request; the decoded limit matches the bytes
// upstream's channel image path accepts after _MAX_IMAGE_BYTES (2 MiB) with
// headroom for chat-app photos, and the frame limit leaves room for several
// images in one send.
const (
	DefaultMaxFrameBytes = 16 << 20
	MaxImageBytes        = 4 << 20
	MaxImagesPerSend     = 8
	MaxImagesTotalBytes  = 8 << 20
	DefaultQueueSize     = 256
)

// JSON-RPC error codes. ChannelError carries a kind in data.kind so a plugin
// can distinguish "no such queen" from a locked session without string
// matching.
const (
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeChannelError   = -32000
)

// ErrorKind is the data.kind of a JSON-RPC error returned for a failed plugin
// request.
type ErrorKind string

const (
	KindNoQueen          ErrorKind = "no_queen"
	KindNoSession        ErrorKind = "no_session"
	KindSessionLocked    ErrorKind = "session_locked"
	KindQueenUnavailable ErrorKind = "queen_unavailable"
	KindInvalidParams    ErrorKind = "invalid_params"
	KindInternal         ErrorKind = "internal"
)

// ChannelError is the backend's refusal, mapped onto a JSON-RPC error whose
// data.kind names the condition. Messages follow upstream's wording where
// upstream raises ChannelError.
//
// Parity: framework/server/channels.py::ChannelError
type ChannelError struct {
	Kind    ErrorKind
	Message string
	Err     error
	// Code optionally overrides the JSON-RPC error code (method not found,
	// invalid params). Zero means CodeChannelError.
	Code int
}

func (e *ChannelError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *ChannelError) Unwrap() error { return e.Err }

// Errorf builds a ChannelError with a formatted message.
func Errorf(kind ErrorKind, format string, args ...any) *ChannelError {
	return &ChannelError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// InvalidParamsError is a JSON-RPC -32602 refusal for a malformed plugin
// request.
func InvalidParamsError(format string, args ...any) *ChannelError {
	return &ChannelError{
		Kind:    KindInvalidParams,
		Code:    CodeInvalidParams,
		Message: fmt.Sprintf(format, args...),
	}
}

// MethodNotFoundError answers a plugin request for a method the host does not
// serve.
func MethodNotFoundError(method string) *ChannelError {
	return &ChannelError{
		Kind:    KindInvalidParams,
		Code:    CodeMethodNotFound,
		Message: fmt.Sprintf("method %q is not supported by the Hive channel host", method),
	}
}

// AsChannelError resolves any error to a ChannelError, defaulting to the
// internal kind so an adapter bug still reaches the plugin as a typed error.
func AsChannelError(err error) *ChannelError {
	if err == nil {
		return nil
	}
	var channelErr *ChannelError
	if errors.As(err, &channelErr) {
		return channelErr
	}
	return &ChannelError{Kind: KindInternal, Message: err.Error(), Err: err}
}

// OutputKind is one QueenOutput kind. idle carries no fields beyond its kind.
//
// Parity: framework/server/channels.py::QueenOutput (kind)
type OutputKind string

const (
	OutputMessage  OutputKind = "message"
	OutputQuestion OutputKind = "question"
	OutputError    OutputKind = "error"
	OutputFiles    OutputKind = "files"
	OutputIdle     OutputKind = "idle"
)

// Output is what the queen said in a subscribed session. Files are absolute
// paths inside the session's data/attachments; the plugin runs on the same
// machine, so it reads them directly (upstream's QueenOutput.files are Paths).
//
// Parity: framework/server/channels.py::QueenOutput
type Output struct {
	SessionID string
	Kind      OutputKind
	Text      string
	Questions []map[string]any
	Files     []string
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// envelope is one decoded frame. Exactly one of method / result / error is
// expected; id is raw so a string id round-trips byte for byte.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (e *envelope) hasID() bool {
	return len(e.ID) > 0 && !bytes.Equal(bytes.TrimSpace(e.ID), []byte("null"))
}

func (e *envelope) isResponse() bool {
	return e.Method == "" && (len(e.Result) > 0 || e.Error != nil)
}

type requestFrame struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notificationFrame struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type successFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

// EncodeFrame marshals one value as a single newline-terminated JSON line.
// json.Marshal escapes embedded newlines, so a handler text can never split a
// frame.
func EncodeFrame(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// decodeEnvelope parses one frame and enforces the JSON-RPC 2.0 marker. A
// plugin line that is not a protocol object is a protocol violation.
func decodeEnvelope(line []byte) (*envelope, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, errors.New("empty frame")
	}
	var frame envelope
	if err := json.Unmarshal(trimmed, &frame); err != nil {
		return nil, fmt.Errorf("malformed JSON frame: %w", err)
	}
	if frame.JSONRPC != "2.0" {
		return nil, fmt.Errorf("frame jsonrpc = %q, want \"2.0\"", frame.JSONRPC)
	}
	if frame.Method == "" && !frame.isResponse() {
		return nil, errors.New("frame has neither method nor response")
	}
	return &frame, nil
}

func marshalRequest(id, method string, params any) ([]byte, error) {
	return EncodeFrame(requestFrame{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func marshalNotification(method string, params any) ([]byte, error) {
	return EncodeFrame(notificationFrame{JSONRPC: "2.0", Method: method, Params: params})
}

func marshalSuccess(id json.RawMessage, result any) ([]byte, error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return EncodeFrame(successFrame{JSONRPC: "2.0", ID: id, Result: result})
}

func marshalError(id json.RawMessage, code int, message string, kind ErrorKind) ([]byte, error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	data := map[string]any{}
	if kind != "" {
		data["kind"] = string(kind)
	}
	return EncodeFrame(errorFrame{JSONRPC: "2.0", ID: id, Error: rpcError{Code: code, Message: message, Data: data}})
}

// helloParams is the first host→plugin request.
//
// Parity: framework/server/channels.py::ChannelHost (name, config, state_dir)
type helloParams struct {
	Protocol string         `json:"protocol"`
	Name     string         `json:"name"`
	Config   map[string]any `json:"config"`
	StateDir string         `json:"state_dir"`
}

// welcomeResult is the plugin's hello response. Unknown capability strings are
// kept for forward compatibility; the host does not gate on them.
type welcomeResult struct {
	PluginVersion string   `json:"plugin_version"`
	Capabilities  []string `json:"capabilities"`
}

type openSessionParams struct {
	QueenID   string `json:"queen_id"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

type openSessionResult struct {
	SessionID string `json:"session_id"`
}

type sendParams struct {
	SessionID     string   `json:"session_id"`
	Text          string   `json:"text"`
	Images        []string `json:"images,omitempty"`
	DisplayText   string   `json:"display_text,omitempty"`
	CorrelationID string   `json:"correlation_id,omitempty"`
}

type sendResult struct {
	SessionID string `json:"session_id"`
}

type subscribeParams struct {
	SessionID string `json:"session_id"`
}

type subscribeResult struct {
	SubscriptionID string `json:"subscription_id"`
}

type unsubscribeParams struct {
	SubscriptionID string `json:"subscription_id"`
}

type setTitleParams struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

type judgeParams struct {
	Question string            `json:"question"`
	State    map[string]any    `json:"state"`
	Criteria map[string]string `json:"criteria,omitempty"`
}

type judgeResult struct {
	Probability *float64 `json:"probability"`
}

// outputParams is the host→plugin notification carrying one QueenOutput. seq
// starts at 1 per subscription; a gap means the host dropped frames under
// backpressure (see the subscription queue policy).
//
// Parity: framework/server/channels.py::QueenOutput
type outputParams struct {
	SubscriptionID string           `json:"subscription_id"`
	SessionID      string           `json:"session_id"`
	Kind           OutputKind       `json:"kind"`
	Text           string           `json:"text"`
	Questions      []map[string]any `json:"questions,omitempty"`
	Files          []string         `json:"files,omitempty"`
	Seq            int64            `json:"seq"`
}

// decodeSendImages decodes base64 image payloads and enforces the documented
// limits. An image larger than MaxImageBytes or a request with more than
// MaxImagesPerSend images is invalid params, not a silently dropped
// attachment.
//
// Parity-exempt: base64 image transport; upstream passes bytes in-process.
func decodeSendImages(images []string) ([][]byte, error) {
	if len(images) > MaxImagesPerSend {
		return nil, Errorf(KindInvalidParams, "send carries %d images; at most %d are allowed", len(images), MaxImagesPerSend)
	}
	out := make([][]byte, 0, len(images))
	total := 0
	for index, encoded := range images {
		decoded, err := decodeBase64Image(encoded)
		if err != nil {
			return nil, Errorf(KindInvalidParams, "images[%d]: %v", index, err)
		}
		if decoded == nil {
			continue
		}
		total += len(decoded)
		if total > MaxImagesTotalBytes {
			return nil, Errorf(KindInvalidParams,
				"images total %d bytes; at most %d are allowed per send", total, MaxImagesTotalBytes)
		}
		out = append(out, decoded)
	}
	return out, nil
}

func decodeBase64Image(encoded string) ([]byte, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, nil
	}
	// A data: URI is tolerated so plugins can forward camera payloads as they
	// received them.
	payload := encoded
	if strings.HasPrefix(payload, "data:") {
		if comma := strings.IndexByte(payload, ','); comma >= 0 {
			payload = payload[comma+1:]
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		// Some clients omit padding; tolerate the raw alphabet when padded
		// decoding fails.
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %w", err)
	}
	if len(decoded) > MaxImageBytes {
		return nil, fmt.Errorf("decoded image is %d bytes; the limit is %d", len(decoded), MaxImageBytes)
	}
	return decoded, nil
}
