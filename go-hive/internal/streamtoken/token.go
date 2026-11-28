// Package streamtoken keeps the runtime's own managed `hive` streamToken
// fresh without a desktop client. It decodes the token's JWT exp claim,
// schedules a refresh a bounded time before expiry, POSTs the current token to
// the cloud refresh endpoint, and writes the rotated token back to the
// credential store under the same id the rest of the runtime reads.
//
// The package owns no HTTP server surface: internal/server starts it from the
// serve startup hook and internal/llm calls RefreshNow when the hive proxy
// rejects a token mid-call.
//
// Parity: core/framework/server/streamtoken_refresh.py
package streamtoken

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Policy bounds when the loop refreshes and how long it may sleep between
// checks.
type Policy struct {
	// RefreshBeforeSeconds is how much life a token must have left before the
	// loop stops treating it as fresh. 2h mirrors the desktop client's
	// STREAM_REFRESH_THRESHOLD_MS so both refresh sources agree on "close to
	// expiry".
	RefreshBeforeSeconds int
	// MinSleepSeconds is the shortest gap between ticks. It keeps a
	// past-due/unparseable token from spinning the loop.
	MinSleepSeconds int
	// MaxSleepSeconds is the longest gap between ticks, so a freshly minted
	// 24h token still gets re-checked hourly.
	MaxSleepSeconds int
}

// DefaultPolicy mirrors REFRESH_BEFORE_SECONDS / MIN_SLEEP_SECONDS /
// MAX_SLEEP_SECONDS.
//
// Parity: core/framework/server/streamtoken_refresh.py::REFRESH_BEFORE_SECONDS,
// ::MIN_SLEEP_SECONDS, ::MAX_SLEEP_SECONDS
var DefaultPolicy = Policy{
	RefreshBeforeSeconds: 7200,
	MinSleepSeconds:      60,
	MaxSleepSeconds:      3600,
}

// SleepSeconds returns how long to sleep before the next refresh check.
//
//   - a token with no readable exp sleeps the minimum, so a corrupted or
//     not-yet-pushed credential is re-checked quickly;
//   - a token at or inside the refresh threshold sleeps the minimum and is
//     refreshed on the next tick;
//   - otherwise the sleep is the exact gap to (exp - threshold), clamped to
//     [min, max].
//
// Parity: core/framework/server/streamtoken_refresh.py::compute_sleep_seconds
func (p Policy) SleepSeconds(expUnix *int64, nowUnix float64) int {
	if expUnix == nil {
		return p.MinSleepSeconds
	}
	secondsUntilRefresh := float64(*expUnix) - float64(p.RefreshBeforeSeconds) - nowUnix
	if secondsUntilRefresh <= float64(p.MinSleepSeconds) {
		return p.MinSleepSeconds
	}
	if secondsUntilRefresh > float64(p.MaxSleepSeconds) {
		return p.MaxSleepSeconds
	}
	return int(secondsUntilRefresh)
}

// NeedsRefresh reports whether a token is close enough to expiry (or
// unreadable) that a refresh attempt is due. The loop uses it after its sleep
// to notice that someone else — typically the desktop pushing a new token —
// already rotated the credential.
//
// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
// (current_exp - now > REFRESH_BEFORE_SECONDS guard)
func (p Policy) NeedsRefresh(expUnix *int64, nowUnix float64) bool {
	if expUnix == nil {
		return true
	}
	return float64(*expUnix)-nowUnix <= float64(p.RefreshBeforeSeconds)
}

// DecodeJWTExp reads the exp claim from a JWT without verifying the signature.
// It returns the unix-timestamp exp and true, or 0 and false when the token is
// malformed or carries no numeric exp. The value only decides *when* to
// refresh: the signature is validated server-side at refresh time and by the
// hive proxy on every LLM call.
//
// Parity: core/framework/server/streamtoken_refresh.py::decode_jwt_exp
func DecodeJWTExp(token string) (int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	// JWT payloads are unpadded base64url; tolerate stray padding either way.
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	// UseNumber keeps a large exp from being rounded through float64 before it
	// is compared against the clock.
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil {
		return 0, false
	}
	raw, ok := claims["exp"]
	if !ok {
		return 0, false
	}
	number, ok := raw.(json.Number)
	if !ok {
		return 0, false
	}
	if exp, err := number.Int64(); err == nil {
		return exp, true
	}
	// A fractional exp is truncated toward zero, like int(exp) upstream.
	asFloat, err := number.Float64()
	if err != nil {
		return 0, false
	}
	return int64(asFloat), true
}
