package browserbridge

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ratelimit.go ports core/framework/rate_limiter.py's SocialRateLimiter plus
// the navigate-time profile-view hook from tools/navigation.py. The Go module
// has no SQLite dependency, so records persist as JSON at RateLimitPath.

type limitKey struct {
	platform string
	action   string
}

type limitDef struct {
	hourlyDefault int
	hourlyMax     int
	dailyDefault  int
	dailyMax      int
	weeklyDefault int
	weeklyMax     int
	minDelayS     float64
}

var socialLimits = map[limitKey]limitDef{
	{"linkedin", "profile_view"}: {25, 100, 150, 400, 0, 0, 5},
	{"linkedin", "invite"}:       {10, 25, 50, 125, 200, 500, 30},
	{"linkedin", "message"}:      {20, 80, 60, 200, 0, 0, 15},
	{"linkedin", "comment"}:      {8, 30, 50, 150, 0, 0, 20},
	{"x", "reply"}:               {10, 30, 50, 150, 0, 0, 30},
	{"x", "dm"}:                  {20, 80, 60, 200, 0, 0, 20},
	{"x", "post"}:                {5, 25, 30, 125, 0, 0, 60},
	{"instagram", "dm"}:          {16, 40, 80, 150, 300, 500, 45},
}

type rateRecord struct {
	Platform    string  `json:"platform"`
	AccountID   string  `json:"account_id"`
	ActionType  string  `json:"action_type"`
	TargetID    string  `json:"target_id,omitempty"`
	PerformedAt float64 `json:"performed_at"`
}

// SocialRateLimiter is the process-safe, JSON-backed limiter.
type SocialRateLimiter struct {
	mu        sync.Mutex
	path      string
	overrides map[string]int
	records   []rateRecord
}

// NewSocialRateLimiter loads persisted records and configuration overrides.
// Missing files are fine; limits still apply from the built-in defaults.
func NewSocialRateLimiter(path, configPath string) *SocialRateLimiter {
	l := &SocialRateLimiter{path: path, overrides: loadRateLimitOverrides(configPath)}
	if path == "" {
		return l
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	var payload struct {
		Records []rateRecord `json:"records"`
	}
	if json.Unmarshal(data, &payload) == nil {
		l.records = payload.Records
	}
	return l
}

func loadRateLimitOverrides(path string) map[string]int {
	out := map[string]int{}
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return out
	}
	raw, ok := payload["rate_limits"].(map[string]any)
	if !ok {
		return out
	}
	for key, val := range raw {
		switch n := val.(type) {
		case float64:
			out[key] = int(n)
		case string:
			if i, err := strconv.Atoi(n); err == nil {
				out[key] = i
			}
		}
	}
	return out
}

func (l *SocialRateLimiter) limitFor(platform, action, window string) int {
	def, ok := socialLimits[limitKey{platform, action}]
	if !ok {
		return 0
	}
	var val, ceiling int
	switch window {
	case "hourly":
		val, ceiling = def.hourlyDefault, def.hourlyMax
	case "daily":
		val, ceiling = def.dailyDefault, def.dailyMax
	case "weekly":
		val, ceiling = def.weeklyDefault, def.weeklyMax
	}
	if val == 0 {
		return 0
	}
	envName := "HIVE_RATE_" + strings.ToUpper(platform) + "_" + strings.ToUpper(action) + "_" + strings.ToUpper(window)
	if raw := os.Getenv(envName); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			val = n
		}
	} else if n, ok := l.overrides[platform+"."+action+"."+window]; ok {
		val = n
	}
	if val < 1 {
		val = 1
	}
	if ceiling > 0 && val > ceiling {
		val = ceiling
	}
	return val
}

func (l *SocialRateLimiter) minDelay(platform, action string) float64 {
	if def, ok := socialLimits[limitKey{platform, action}]; ok {
		return def.minDelayS
	}
	return 0
}

// Check mirrors upstream check(): min-delay first, then hourly → daily →
// weekly. halt_campaign is true for daily/weekly caps, false for pacing.
func (l *SocialRateLimiter) Check(platform, accountID, action string) RateLimitDecision {
	platform = strings.ToLower(platform)
	action = strings.ToLower(action)
	now := float64(time.Now().UnixNano()) / 1e9
	l.mu.Lock()
	defer l.mu.Unlock()

	if delay := l.minDelay(platform, action); delay > 0 {
		var last float64
		found := false
		for i := len(l.records) - 1; i >= 0; i-- {
			r := l.records[i]
			if r.Platform == platform && r.AccountID == accountID && r.ActionType == action {
				last = r.PerformedAt
				found = true
				break
			}
		}
		if found && now-last < delay {
			return RateLimitDecision{
				Allowed: false, Reason: "too_fast",
				RetryAfterSeconds: round1(delay - (now - last)),
				MinDelaySeconds:   delay,
				HaltCampaign:      false,
			}
		}
	}

	decision := RateLimitDecision{Allowed: true}
	windows := []struct {
		name string
		secs float64
		halt bool
	}{{"hourly", 3600, false}, {"daily", 86400, true}, {"weekly", 604800, true}}
	for _, w := range windows {
		limit := l.limitFor(platform, action, w.name)
		if limit == 0 {
			continue
		}
		count, oldest := l.countSince(platform, accountID, action, now-w.secs)
		countCopy, limitCopy := count, limit
		switch w.name {
		case "hourly":
			decision.HourlyCount, decision.HourlyLimit = &countCopy, &limitCopy
		case "daily":
			decision.DailyCount, decision.DailyLimit = &countCopy, &limitCopy
		case "weekly":
			decision.WeeklyCount, decision.WeeklyLimit = &countCopy, &limitCopy
		}
		if count >= limit {
			retry := w.secs
			if oldest > 0 {
				retry = (oldest + w.secs) - now
			}
			if retry < 0 {
				retry = 0
			}
			decision.Allowed = false
			decision.Reason = w.name + "_limit_reached"
			decision.RetryAfterSeconds = round1(retry)
			decision.HaltCampaign = w.halt
			if w.name == "hourly" {
				decision.HourlyCount, decision.HourlyLimit = &countCopy, &limitCopy
			}
			return decision
		}
	}
	return decision
}

func (l *SocialRateLimiter) countSince(platform, accountID, action string, since float64) (int, float64) {
	count := 0
	oldest := 0.0
	for _, r := range l.records {
		if r.Platform != platform || r.AccountID != accountID || r.ActionType != action || r.PerformedAt < since {
			continue
		}
		count++
		if oldest == 0 || r.PerformedAt < oldest {
			oldest = r.PerformedAt
		}
	}
	return count, oldest
}

// Record appends one successfully completed action and persists the store.
func (l *SocialRateLimiter) Record(platform, accountID, action, targetID string) {
	platform = strings.ToLower(platform)
	action = strings.ToLower(action)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	l.records = append(l.records, rateRecord{
		Platform: platform, AccountID: accountID, ActionType: action,
		TargetID: targetID, PerformedAt: now,
	})
	cutoff := now - 7*86400
	keep := l.records[:0]
	for _, r := range l.records {
		if r.PerformedAt >= cutoff {
			keep = append(keep, r)
		}
	}
	l.records = keep
	l.saveLocked()
}

func (l *SocialRateLimiter) saveLocked() {
	if l.path == "" {
		return
	}
	data, err := json.Marshal(map[string]any{"records": l.records})
	if err != nil {
		return
	}
	_ = os.WriteFile(l.path, data, 0o600)
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// ── Navigate-time profile-view hook ───────────────────────────────────────

const rateLimitGuidance = "STOP. This rate limit is configured by the user and must be respected. " +
	"Do NOT retry, loop, or attempt to circumvent it. " +
	"You MUST use set_trigger to schedule a trigger that resumes this work " +
	"after retry_after_seconds has elapsed. " +
	"If running a campaign with remaining targets, calculate the required " +
	"pacing from the observed limits (e.g. hourly_limit=10 with 50 targets " +
	"→ set_trigger every ~6 minutes per target, or batch 10 then set_trigger " +
	"for retry_after_seconds) and create the trigger NOW before ending this turn. " +
	"Exceeding these limits risks getting the user's account banned."

type rateLimitPlan struct {
	platform  string
	accountID string
	blocked   map[string]any
}

var instagramNonProfile = map[string]struct{}{
	"explore": {}, "direct": {}, "reels": {}, "stories": {}, "accounts": {},
	"p": {}, "reel": {}, "tv": {}, "about": {}, "legal": {}, "developer": {}, "static": {},
}

func socialProfilePlatform(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	path := u.EscapedPath()
	switch host {
	case "linkedin.com":
		if strings.HasPrefix(path, "/in/") && len(strings.Trim(path[len("/in/"):], "/")) > 0 {
			return "linkedin"
		}
	case "instagram.com":
		rest := strings.Trim(path, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && parts[0] != "" {
			if _, bad := instagramNonProfile[strings.ToLower(parts[0])]; !bad {
				return "instagram"
			}
		}
	}
	return ""
}

// The identity probes are the browser-side half of upstream rate_limit_hook's
// IDENTITY_JS. They run on the current tab and never navigate.
var identityJS = map[string]string{
	"linkedin": `(function () {
  var candidates = document.querySelectorAll('a[href*="/in/"]');
  for (var i = 0; i < candidates.length; i++) {
    var a = candidates[i];
    if (a.closest('nav, header, .global-nav, .feed-identity-module, .scaffold-layout__aside, [data-test-global-nav], #global-nav')) {
      var match = (a.href || '').match(/\/in\/([^\/\?#]+)/);
      if (match) return { status: 'ok', account_id: 'https://www.linkedin.com/in/' + match[1] + '/' };
    }
  }
  if (candidates.length === 0) return { status: 'no_profile_link' };
  var first = (candidates[0].href || '').match(/\/in\/([^\/\?#]+)/);
  if (first) return { status: 'ok', account_id: 'https://www.linkedin.com/in/' + first[1] + '/' };
  return { status: 'no_profile_link' };
})();`,
	"instagram": `(function () {
  var profileLink = document.querySelector('a[href$="/accounts/edit/"]');
  if (profileLink) {
    var parts = location.pathname.split('/').filter(Boolean);
    if (parts.length === 1) return { status: 'ok', account_id: '@' + parts[0] };
  }
  var moreMenu = document.querySelector('[aria-label="Settings"]') || document.querySelector('[aria-label="More"]');
  if (moreMenu) {
    var navLinks = document.querySelectorAll('nav a[href^="/"]');
    for (var j = 0; j < navLinks.length; j++) {
      var href = navLinks[j].getAttribute('href') || '';
      var m = href.match(/^\/([a-zA-Z0-9._]+)\/$/);
      if (m && m[1] !== 'explore' && m[1] !== 'direct' && m[1] !== 'reels' && m[1] !== 'accounts') {
        return { status: 'ok', account_id: '@' + m[1] };
      }
    }
  }
  return { status: 'no_handle' };
})();`,
}

func (b *Bridge) rateLimitsActive() bool {
	if b.rateLimiter == nil {
		return false
	}
	_, nop := b.rateLimiter.(nopRateLimiter)
	return !nop
}

// planProfileView detects the acting account and checks the profile_view
// limit for social profile URLs. nil means "not a social profile".
func (b *Bridge) planProfileView(ctx context.Context, conn *Connection, tabID int, rawURL string) *rateLimitPlan {
	if !b.rateLimitsActive() {
		return nil
	}
	platform := socialProfilePlatform(rawURL)
	if platform == "" {
		return nil
	}
	accountID := "unknown"
	if js := identityJS[platform]; js != "" {
		if v, err := b.evalJS(ctx, conn, tabID, js, 5*time.Second); err == nil {
			if m, ok := v.(map[string]any); ok && m["status"] == "ok" {
				if id, ok := m["account_id"].(string); ok && id != "" {
					accountID = id
				}
			}
		}
	}
	decision := b.rateLimiter.Check(platform, accountID, "profile_view")
	if decision.Allowed {
		return &rateLimitPlan{platform: platform, accountID: accountID}
	}
	return &rateLimitPlan{platform: platform, accountID: accountID, blocked: rateLimitResult(platform, accountID, decision)}
}

func rateLimitResult(platform, accountID string, d RateLimitDecision) map[string]any {
	out := map[string]any{
		"ok":                  false,
		"error":               "rate_limited",
		"action_type":         "profile_view",
		"platform":            platform,
		"account_id":          accountID,
		"halt_campaign":       d.HaltCampaign,
		"reason":              d.Reason,
		"retry_after_seconds": d.RetryAfterSeconds,
		"guidance":            rateLimitGuidance,
	}
	if d.MinDelaySeconds > 0 {
		out["min_delay_seconds"] = d.MinDelaySeconds
	}
	addCount := func(name string, count, limit *int) {
		if count != nil {
			out[name+"_count"] = *count
		}
		if limit != nil {
			out[name+"_limit"] = *limit
		}
	}
	addCount("hourly", d.HourlyCount, d.HourlyLimit)
	addCount("daily", d.DailyCount, d.DailyLimit)
	addCount("weekly", d.WeeklyCount, d.WeeklyLimit)
	return out
}

func (p *rateLimitPlan) record(limiter RateLimiter) {
	if p == nil || p.platform == "" {
		return
	}
	limiter.Record(p.platform, p.accountID, "profile_view", "")
}
