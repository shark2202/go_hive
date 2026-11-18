package sentinel

// Shared runtime helpers for the sentinel package: an injectable clock and the
// small Python-compatibility shims (string/truthiness/rounding) the ported
// modules rely on.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Clock is the injectable time source for every sentinel module. Production
// code uses RealClock; tests drive a manual clock so thresholds, rate limits
// and token lifetimes are deterministic.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall-clock implementation of Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewUUID4Hex returns a random RFC-4122 v4 UUID with the dashes removed, the
// Python `uuid.uuid4().hex` shape used for escalation and runtime ids.
func NewUUID4Hex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a panic here is
		// preferable to minting a predictable identity token.
		panic("sentinel: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// PyStr mirrors Python's str() for the JSON scalar types the config files can
// hold. It matters for allowlists, which accept numbers and coerce them to
// strings exactly like the reference implementation.
func PyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case float64:
		if math.Trunc(typed) == typed && math.Abs(typed) < 1e15 {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		return PyStr(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

// PyStrOr returns PyStr(value), or fallback when value is absent (nil).
func PyStrOr(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	return PyStr(value)
}

// PyTruthy mirrors Python truthiness for decoded JSON values.
func PyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case json.Number:
		f, err := typed.Float64()
		return err == nil && f != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// ToFloat converts a decoded JSON value to a float, returning ok=false when it
// is absent or not numeric (Python would raise and the caller falls back).
func ToFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		f, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// ToInt converts a decoded JSON value to an int, returning ok=false when it is
// absent or not numeric.
func ToInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case json.Number:
		n, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// TruncateRunes cuts text to at most n runes, matching Python slicing which
// counts characters rather than bytes.
func TruncateRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}
