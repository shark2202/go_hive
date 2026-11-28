package streamtoken

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// makeJWT builds a three-part JWT-shaped token without signing it; only the
// unsigned exp decode is under test.
func makeJWT(claims map[string]any) string {
	encode := func(value any) string {
		payload, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(payload)
	}
	return encode(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + encode(claims) + ".fake-signature"
}

// Parity: core/tests/test_streamtoken_refresh.py::TestDecodeJwtExp::test_returns_exp_from_valid_token
func TestDecodeJWTExpReadsExp(t *testing.T) {
	token := makeJWT(map[string]any{"sub": "u@e.com", "exp": 1900000000})
	exp, ok := DecodeJWTExp(token)
	if !ok || exp != 1900000000 {
		t.Fatalf("DecodeJWTExp = (%d, %v), want (1900000000, true)", exp, ok)
	}

	fractional := makeJWT(map[string]any{"exp": 1900000000.9})
	if exp, ok := DecodeJWTExp(fractional); !ok || exp != 1900000000 {
		t.Fatalf("fractional exp = (%d, %v), want (1900000000, true)", exp, ok)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestDecodeJwtExp::test_returns_none_on_malformed_token
func TestDecodeJWTExpRejectsMalformed(t *testing.T) {
	for _, token := range []string{"not.a.jwt", "totally bogus", "", "a.b.c", "a..c"} {
		if exp, ok := DecodeJWTExp(token); ok {
			t.Fatalf("DecodeJWTExp(%q) = (%d, true), want false", token, exp)
		}
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestDecodeJwtExp::test_returns_none_when_exp_missing
func TestDecodeJWTExpRequiresNumericExp(t *testing.T) {
	if exp, ok := DecodeJWTExp(makeJWT(map[string]any{"sub": "u@e.com"})); ok {
		t.Fatalf("missing exp = (%d, true), want false", exp)
	}
	if exp, ok := DecodeJWTExp(makeJWT(map[string]any{"exp": "tomorrow"})); ok {
		t.Fatalf("string exp = (%d, true), want false", exp)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestComputeSleepSeconds::test_exp_none_sleeps_min
func TestSleepSecondsUnreadableTokenSleepsMin(t *testing.T) {
	if got := DefaultPolicy.SleepSeconds(nil, 1000); got != DefaultPolicy.MinSleepSeconds {
		t.Fatalf("SleepSeconds(nil) = %d, want %d", got, DefaultPolicy.MinSleepSeconds)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestComputeSleepSeconds::test_within_threshold_fires_immediately
func TestSleepSecondsWithinThresholdFiresImmediately(t *testing.T) {
	now := 1000.0
	exp := int64(now + 3600)
	if got := DefaultPolicy.SleepSeconds(&exp, now); got != DefaultPolicy.MinSleepSeconds {
		t.Fatalf("SleepSeconds(within threshold) = %d, want %d", got, DefaultPolicy.MinSleepSeconds)
	}
	expired := int64(now - 7200)
	if got := DefaultPolicy.SleepSeconds(&expired, now); got != DefaultPolicy.MinSleepSeconds {
		t.Fatalf("SleepSeconds(expired) = %d, want %d", got, DefaultPolicy.MinSleepSeconds)
	}
	if !DefaultPolicy.NeedsRefresh(&expired, now) {
		t.Fatal("an expired token must need a refresh")
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestComputeSleepSeconds::test_far_from_expiry_clamps_to_max
func TestSleepSecondsFarFromExpiryClampsToMax(t *testing.T) {
	now := 1000.0
	exp := int64(now + 10*3600)
	if got := DefaultPolicy.SleepSeconds(&exp, now); got != DefaultPolicy.MaxSleepSeconds {
		t.Fatalf("SleepSeconds(far) = %d, want %d", got, DefaultPolicy.MaxSleepSeconds)
	}
	if DefaultPolicy.NeedsRefresh(&exp, now) {
		t.Fatal("a 10h token is not due for refresh")
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestComputeSleepSeconds::test_mid_range_returns_exact_gap
func TestSleepSecondsMidRangeReturnsExactGap(t *testing.T) {
	now := 1000.0
	exp := int64(now + 3000)
	policy := Policy{RefreshBeforeSeconds: 500, MinSleepSeconds: 60, MaxSleepSeconds: 3600}
	if got := policy.SleepSeconds(&exp, now); got != 2500 {
		t.Fatalf("SleepSeconds(mid) = %d, want 2500", got)
	}
}
