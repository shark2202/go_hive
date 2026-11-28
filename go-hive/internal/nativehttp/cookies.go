package nativehttp

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
)

// cookieJar wraps the stdlib RFC 6265 jar with the policy the tool contract
// requires: host-only by default, __Host-/__Secure- prefix validation, a value
// cap and a distinct-cookie cap. It is per Client (one per Hive chat session
// catalogue) and in-memory only; it never reaches disk.
type cookieJar struct {
	mu       sync.Mutex
	inner    *cookiejar.Jar
	cfg      CookieConfig
	seen     map[string]struct{}
	rejected int
}

func newCookieJar(cfg CookieConfig) *cookieJar {
	inner, _ := cookiejar.New(nil) // nil PSL: Domain cookies are rejected below.
	return &cookieJar{inner: inner, cfg: cfg, seen: map[string]struct{}{}}
}

func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if j == nil || !j.cfg.Enabled || len(cookies) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	accepted := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if reason := j.rejectReason(cookie); reason != "" {
			j.rejected++
			continue
		}
		key := cookie.Domain + "\x00" + cookie.Path + "\x00" + cookie.Name
		if _, exists := j.seen[key]; !exists {
			if j.cfg.MaxCookies > 0 && len(j.seen) >= j.cfg.MaxCookies {
				j.rejected++
				continue
			}
			j.seen[key] = struct{}{}
		}
		accepted = append(accepted, cookie)
	}
	if len(accepted) > 0 {
		j.inner.SetCookies(u, accepted)
	}
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	if j == nil || !j.cfg.Enabled {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.inner.Cookies(u)
}

// Clear swaps in a fresh jar (logout). The wrapper's lock serialises the swap
// with concurrent Cookies/SetCookies calls.
func (j *cookieJar) Clear() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	inner, _ := cookiejar.New(nil)
	j.inner = inner
	j.seen = map[string]struct{}{}
	j.rejected = 0
}

func (j *cookieJar) Rejected() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rejected
}

func (j *cookieJar) rejectReason(cookie *http.Cookie) string {
	if cookie == nil {
		return "nil cookie"
	}
	if j.cfg.MaxValueBytes > 0 && len(cookie.Value) > j.cfg.MaxValueBytes {
		return "value too large"
	}
	if strings.HasPrefix(cookie.Name, "__Host-") {
		if !cookie.Secure || cookie.Domain != "" || cookie.Path != "/" {
			return "invalid __Host- prefix"
		}
	}
	if strings.HasPrefix(cookie.Name, "__Secure-") && !cookie.Secure {
		return "invalid __Secure- prefix"
	}
	if j.cfg.HostOnly && cookie.Domain != "" {
		if !domainAllowed(cookie.Domain, j.cfg.AllowedDomains) {
			return "domain cookie rejected"
		}
	}
	return ""
}

func domainAllowed(domain string, allowed []string) bool {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return false
	}
	for _, raw := range allowed {
		pattern := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
		pattern = strings.TrimPrefix(pattern, "*.")
		if pattern == "" {
			continue
		}
		if domain == pattern || strings.HasSuffix(domain, "."+pattern) {
			return true
		}
	}
	return false
}
