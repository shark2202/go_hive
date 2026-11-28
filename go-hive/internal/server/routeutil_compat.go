package server

// Parity-exempt: Go-side extraction of the shared HTTP/JSON helpers; the
// server package keeps these thin aliases so its ~800 call sites do not churn
// while route subpackages import internal/server/routeutil directly.

import (
	"net/http"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/server/routeutil"
)

const internalServerError = routeutil.InternalServerError

// browserProbeTimeout stays identical to routeutil's bridge probe deadline so
// the queen lifecycle /profiles probe and the routesapp status probe time out
// the same way.
const browserProbeTimeout = routeutil.BridgeProbeTimeout

// errBodyTooLarge stays identical to routeutil's sentinel so WriteBodyFailure
// recognises oversized bodies produced by this package's readLimitedBody too.
var errBodyTooLarge = routeutil.ErrBodyTooLarge

func writeJSON(w http.ResponseWriter, status int, body any) {
	routeutil.WriteJSON(w, status, body)
}

func writeJSONBodyFailure(w http.ResponseWriter, err error, status int, message string) {
	routeutil.WriteBodyFailure(w, err, status, message)
}

func decodeJSONValue(r *http.Request) (any, error) {
	return routeutil.DecodeJSONValue(r)
}

func decodeJSONBytes(data []byte) (any, error) {
	return routeutil.DecodeJSONBytes(data)
}

func pyTruthy(value any) bool {
	return routeutil.PyTruthy(value)
}

func pythonIntValue(value any) (int, bool) {
	return routeutil.PyIntValue(value)
}

func pyStr(value any) string {
	return routeutil.PyStr(value)
}

func toFloat(value any) (float64, bool) {
	return routeutil.PyFloat(value)
}

func writeNamedSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload map[string]any) {
	routeutil.WriteNamedSSEEvent(w, flusher, eventType, payload)
}

func bridgeHTTPRequest(addr, request string, timeout time.Duration) ([]byte, error) {
	return routeutil.BridgeHTTPRequest(addr, request, timeout)
}

func bridgeResponseBody(raw []byte) ([]byte, bool) {
	return routeutil.BridgeResponseBody(raw)
}
