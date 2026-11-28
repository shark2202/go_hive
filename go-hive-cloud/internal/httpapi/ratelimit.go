package httpapi

// Inbox write rate limiting.
//
// Rate limiting is a private-deploy operational control, not part of the
// observed cloud contract: contracts/observed-contract.json records no
// 429/Retry-After behaviour, and the reference clients do not expect it. The
// server therefore defaults to disabled (limit <= 0) and only answers 429 when
// an operator opts in with --inbox-rate-limit.

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// inboxLimiter is a per-team fixed-window limiter. A window opens on the
// team's first write and admits at most limit writes.
type inboxLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	buckets map[string]inboxWindow
}

type inboxWindow struct {
	start time.Time
	count int
}

func newInboxLimiter(limit int, window time.Duration, now func() time.Time) *inboxLimiter {
	if window <= 0 {
		window = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &inboxLimiter{
		limit:   limit,
		window:  window,
		now:     now,
		buckets: map[string]inboxWindow{},
	}
}

func (l *inboxLimiter) enabled() bool { return l != nil && l.limit > 0 }

// allow consumes one write slot for teamID. When the request is rejected,
// retryAfter is the time left in the current window.
func (l *inboxLimiter) allow(teamID string) (bool, time.Duration) {
	if !l.enabled() {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket := l.buckets[teamID]
	if bucket.start.IsZero() || now.Sub(bucket.start) >= l.window {
		bucket = inboxWindow{start: now}
	}
	if bucket.count >= l.limit {
		retryAfter := l.window - now.Sub(bucket.start)
		l.buckets[teamID] = bucket
		return false, retryAfter
	}
	bucket.count++
	l.buckets[teamID] = bucket

	// Keep the map from growing without bound when many teams are seen.
	if len(l.buckets) > 1024 {
		for id, item := range l.buckets {
			if now.Sub(item.start) >= l.window {
				delete(l.buckets, id)
			}
		}
	}
	return true, 0
}

// allowInboxWrite applies the optional limiter to one inbox write. It writes
// the 429 response itself and reports whether the handler may continue.
func (s *Server) allowInboxWrite(w http.ResponseWriter, reqAuth *RequestAuth) bool {
	ok, retryAfter := s.inboxLimiter.allow(reqAuth.TeamID)
	if ok {
		return true
	}
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "inbox rate limit exceeded"})
	return false
}
