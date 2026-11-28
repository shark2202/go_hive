package nativehttp

import (
	"net/http"
	"net/url"
	"strings"
)

const redacted = "[redacted]"

const (
	maxHeaderCount = 100
	maxHeaderBytes = 64 << 10
)

// RedactURL strips userinfo and replaces query values whose key matches the
// configured redaction list. The result is for display/logs, not replay.
func RedactURL(raw string, keys []string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if redactKey(key, keys) {
			query.Set(key, redacted)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// RedactHeaders copies response headers with sensitive values hidden. Set-Cookie
// values are hidden unless includeCookies, and the copy is bounded.
func RedactHeaders(headers http.Header, includeCookies bool) map[string][]string {
	out := map[string][]string{}
	total := 0
	for name, values := range headers {
		if len(out) >= maxHeaderCount {
			out["x-hive-headers-truncated"] = []string{"true"}
			break
		}
		lower := strings.ToLower(name)
		copied := make([]string, 0, len(values))
		for _, value := range values {
			total += len(name) + len(value)
			if total > maxHeaderBytes {
				out["x-hive-headers-truncated"] = []string{"true"}
				break
			}
			if lower == "set-cookie" && !includeCookies {
				copied = append(copied, redacted)
				continue
			}
			copied = append(copied, value)
		}
		out[lower] = copied
	}
	return out
}

func redactKey(key string, keys []string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, candidate := range keys {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if lower == candidate || strings.Contains(lower, candidate) {
			return true
		}
	}
	return false
}
