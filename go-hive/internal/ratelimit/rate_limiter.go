package ratelimit

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type limitEntry struct {
	hourlyDefault, hourlyMax *int
	dailyDefault, dailyMax   *int
	weeklyDefault, weeklyMax *int
	minDelayS                float64
}

func intp(v int) *int { return &v }

var limitOrder = [][2]string{
	{"linkedin", "profile_view"},
	{"linkedin", "invite"},
	{"linkedin", "message"},
	{"linkedin", "comment"},
	{"x", "reply"},
	{"x", "dm"},
	{"x", "post"},
	{"instagram", "dm"},
}

var limits = map[[2]string]limitEntry{
	{"linkedin", "profile_view"}: {
		hourlyDefault: intp(25), hourlyMax: intp(100),
		dailyDefault: intp(150), dailyMax: intp(400),
		minDelayS: 5,
	},
	{"linkedin", "invite"}: {
		hourlyDefault: intp(10), hourlyMax: intp(25),
		dailyDefault: intp(50), dailyMax: intp(125),
		weeklyDefault: intp(200), weeklyMax: intp(500),
		minDelayS: 30,
	},
	{"linkedin", "message"}: {
		hourlyDefault: intp(20), hourlyMax: intp(80),
		dailyDefault: intp(60), dailyMax: intp(200),
		minDelayS: 15,
	},
	{"linkedin", "comment"}: {
		hourlyDefault: intp(8), hourlyMax: intp(30),
		dailyDefault: intp(50), dailyMax: intp(150),
		minDelayS: 20,
	},
	{"x", "reply"}: {
		hourlyDefault: intp(10), hourlyMax: intp(30),
		dailyDefault: intp(50), dailyMax: intp(150),
		minDelayS: 30,
	},
	{"x", "dm"}: {
		hourlyDefault: intp(20), hourlyMax: intp(80),
		dailyDefault: intp(60), dailyMax: intp(200),
		minDelayS: 20,
	},
	{"x", "post"}: {
		hourlyDefault: intp(5), hourlyMax: intp(25),
		dailyDefault: intp(30), dailyMax: intp(125),
		minDelayS: 60,
	},
	{"instagram", "dm"}: {
		hourlyDefault: intp(16), hourlyMax: intp(40),
		dailyDefault: intp(80), dailyMax: intp(150),
		weeklyDefault: intp(300), weeklyMax: intp(500),
		minDelayS: 45,
	},
}

const schema = `CREATE TABLE IF NOT EXISTS actions (
    id           INTEGER PRIMARY KEY,
    platform     TEXT    NOT NULL,
    account_id   TEXT    NOT NULL,
    action_type  TEXT    NOT NULL,
    target_id    TEXT,
    performed_at REAL    NOT NULL,
    session_id   TEXT
);
CREATE INDEX IF NOT EXISTS idx_actions_lookup
    ON actions(platform, account_id, action_type, performed_at);
`

type ConfigOverridesFunc func() map[string]any

var configOverrides ConfigOverridesFunc = readHiveConfigOverrides

func SetConfigOverrides(fn ConfigOverridesFunc) {
	if fn == nil {
		configOverrides = readHiveConfigOverrides
		return
	}
	configOverrides = fn
}

func readHiveConfigOverrides() map[string]any {
	home, err := hivehome.Resolve()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	section, ok := cfg["rate_limits"].(map[string]any)
	if !ok {
		return nil
	}
	return section
}

func clamp(value int, ceiling *int) int {
	if ceiling != nil && value > *ceiling {
		return *ceiling
	}
	return value
}

func forWindow(entry limitEntry, window string) (defaultVal, ceiling *int) {
	switch window {
	case "hourly":
		return entry.hourlyDefault, entry.hourlyMax
	case "daily":
		return entry.dailyDefault, entry.dailyMax
	case "weekly":
		return entry.weeklyDefault, entry.weeklyMax
	}
	return nil, nil
}

func pyInt(s string) (int, bool) {
	body := strings.TrimSpace(s)
	if body == "" {
		return 0, false
	}
	sign := ""
	if body[0] == '+' || body[0] == '-' {
		sign = body[:1]
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	var cleaned strings.Builder
	cleaned.Grow(len(body))
	prevDigit := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c >= '0' && c <= '9' {
			cleaned.WriteByte(c)
			prevDigit = true
			continue
		}
		if c == '_' && prevDigit && i != len(body)-1 {
			prevDigit = false
			continue
		}
		return 0, false
	}
	if !prevDigit {
		return 0, false
	}
	n, err := strconv.Atoi(sign + cleaned.String())
	if err != nil {
		return 0, false
	}
	return n, true
}

func coerceInt(value any) (int, bool) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return int(math.Trunc(v)), true
		}
	case json.Number:
		if n, err := strconv.Atoi(v.String()); err == nil {
			return n, true
		}
		if f, err := v.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int(math.Trunc(f)), true
		}
	case string:
		return pyInt(v)
	}
	return 0, false
}

func CoerceInt(value any) (int, bool) { return coerceInt(value) }

func LimitEntryExists(platform, action string) bool {
	_, ok := limits[[2]string{platform, action}]
	return ok
}

func LimitCeiling(platform, action, window string) (int, bool) {
	entry, ok := limits[[2]string{platform, action}]
	if !ok {
		return 0, false
	}
	_, ceiling := forWindow(entry, window)
	if ceiling == nil {
		return 0, false
	}
	return *ceiling, true
}

func resolveLimit(platform, action, window string) *int {
	return resolveLimitFrom(configOverrides, platform, action, window)
}

func resolveLimitFrom(source ConfigOverridesFunc, platform, action, window string) *int {
	key := [2]string{strings.ToLower(platform), strings.ToLower(action)}
	entry, ok := limits[key]
	if !ok {
		return nil
	}
	defaultVal, ceiling := forWindow(entry, window)
	if defaultVal == nil {
		return nil
	}

	envName := fmt.Sprintf("HIVE_RATE_%s_%s_%s",
		strings.ToUpper(platform), strings.ToUpper(action), strings.ToUpper(window))
	if envVal, present := os.LookupEnv(envName); present {
		if parsed, ok := pyInt(envVal); ok {
			v := clamp(parsed, ceiling)
			return &v
		}
	}

	cfgKey := strings.ToLower(platform) + "." + strings.ToLower(action) + "." + window
	if source != nil {
		if cfg := source(); cfg != nil {
			if raw, present := cfg[cfgKey]; present {
				if parsed, ok := coerceInt(raw); ok {
					if parsed < 1 {
						parsed = 1
					}
					return &parsed
				}
			}
		}
	}
	return defaultVal
}

func minDelay(platform, action string) float64 {
	key := [2]string{strings.ToLower(platform), strings.ToLower(action)}
	if entry, ok := limits[key]; ok {
		return entry.minDelayS
	}
	return 0
}

func GetAllLimits() []map[string]any {
	return getAllLimits(configOverrides)
}

func GetAllLimitsWithOverrides(overrides map[string]any) []map[string]any {
	return getAllLimits(func() map[string]any { return overrides })
}

func getAllLimits(source ConfigOverridesFunc) []map[string]any {
	out := make([]map[string]any, 0, len(limitOrder))
	for _, key := range limitOrder {
		entry := limits[key]
		row := map[string]any{
			"platform":    key[0],
			"action_type": key[1],
			"min_delay_s": entry.minDelayS,
		}
		for _, window := range []string{"hourly", "daily", "weekly"} {
			defaultVal, ceiling := forWindow(entry, window)
			if defaultVal == nil {
				continue
			}
			if effective := resolveLimitFrom(source, key[0], key[1], window); effective != nil {
				row[window] = *effective
			}
			row[window+"_max"] = ceiling
			row[window+"_default"] = defaultVal
		}
		out = append(out, row)
	}
	return out
}

type CheckResult struct {
	Allowed           bool     `json:"allowed"`
	Reason            string   `json:"reason,omitempty"`
	RetryAfterSeconds *float64 `json:"retry_after_seconds,omitempty"`
	MinDelaySeconds   *float64 `json:"min_delay_seconds,omitempty"`
	HaltCampaign      bool     `json:"halt_campaign,omitempty"`
	HourlyCount       *int     `json:"hourly_count"`
	HourlyLimit       *int     `json:"hourly_limit"`
	DailyCount        *int     `json:"daily_count"`
	DailyLimit        *int     `json:"daily_limit"`
	WeeklyCount       *int     `json:"weekly_count"`
	WeeklyLimit       *int     `json:"weekly_limit"`
}

type SocialRateLimiter struct {
	DBPath string

	now func() float64
}

func DefaultDBPath() (string, error) {
	home, err := hivehome.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Root, "social_rate_limits.db"), nil
}

func NewSocialRateLimiter(dbPath string) (*SocialRateLimiter, error) {
	if dbPath == "" {
		var err error
		dbPath, err = DefaultDBPath()
		if err != nil {
			return nil, err
		}
	}
	expanded, err := expandUser(dbPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(expanded), 0o755); err != nil {
		return nil, err
	}
	limiter := &SocialRateLimiter{DBPath: expanded, now: unixSeconds}
	if err := limiter.ensureSchema(); err != nil {
		return nil, err
	}
	return limiter, nil
}

func unixSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func expandUser(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func (s *SocialRateLimiter) connect() (*sql.DB, error) {
	db, err := sql.Open("sqlite", s.DBPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func (s *SocialRateLimiter) ensureSchema() error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("ratelimit: install schema: %w", err)
	}
	return nil
}

func round1(value float64) float64 { return math.Round(value*10) / 10 }

type windowCheck struct {
	name string
	secs float64
	halt bool
}

var windows = []windowCheck{
	{name: "hourly", secs: 3600, halt: false},
	{name: "daily", secs: 86400, halt: true},
	{name: "weekly", secs: 604800, halt: true},
}

func (s *SocialRateLimiter) Check(platform, accountID, actionType string) (CheckResult, error) {
	platform = strings.ToLower(platform)
	actionType = strings.ToLower(actionType)
	now := s.now()

	db, err := s.connect()
	if err != nil {
		return CheckResult{}, err
	}
	defer db.Close()

	delay := minDelay(platform, actionType)
	if delay > 0 {
		var performedAt float64
		err := db.QueryRow(
			"SELECT performed_at FROM actions WHERE platform = ? AND account_id = ? AND action_type = ? ORDER BY performed_at DESC LIMIT 1",
			platform, accountID, actionType,
		).Scan(&performedAt)
		switch {
		case err == nil:
			if elapsed := now - performedAt; elapsed < delay {
				retry := round1(delay - elapsed)
				return CheckResult{
					Allowed:           false,
					Reason:            "too_fast",
					RetryAfterSeconds: &retry,
					MinDelaySeconds:   &delay,
					HaltCampaign:      false,
				}, nil
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return CheckResult{}, err
		}
	}

	var hourlyCount, hourlyLimit *int
	var dailyCount, dailyLimit *int
	var weeklyCount, weeklyLimit *int

	for _, window := range windows {
		limit := resolveLimit(platform, actionType, window.name)
		if limit == nil {
			continue
		}
		windowStart := now - window.secs
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM actions WHERE platform = ? AND account_id = ? AND action_type = ? AND performed_at >= ?",
			platform, accountID, actionType, windowStart,
		).Scan(&count); err != nil {
			return CheckResult{}, err
		}
		countVal := count
		limitVal := *limit

		switch window.name {
		case "hourly":
			hourlyCount, hourlyLimit = &countVal, &limitVal
		case "daily":
			dailyCount, dailyLimit = &countVal, &limitVal
		default:
			weeklyCount, weeklyLimit = &countVal, &limitVal
		}

		if count < limitVal {
			continue
		}

		var oldest float64
		err := db.QueryRow(
			"SELECT performed_at FROM actions WHERE platform = ? AND account_id = ? AND action_type = ? AND performed_at >= ? ORDER BY performed_at ASC LIMIT 1",
			platform, accountID, actionType, windowStart,
		).Scan(&oldest)
		var retry float64
		if errors.Is(err, sql.ErrNoRows) {
			retry = window.secs
		} else if err != nil {
			return CheckResult{}, err
		} else {
			retry = round1((oldest + window.secs) - now)
		}
		if retry < 0 {
			retry = 0
		}
		return CheckResult{
			Allowed:           false,
			Reason:            window.name + "_limit_reached",
			RetryAfterSeconds: &retry,
			HaltCampaign:      window.halt,
			HourlyCount:       hourlyCount,
			HourlyLimit:       hourlyLimit,
			DailyCount:        dailyCount,
			DailyLimit:        dailyLimit,
			WeeklyCount:       weeklyCount,
			WeeklyLimit:       weeklyLimit,
		}, nil
	}

	return CheckResult{
		Allowed:     true,
		HourlyCount: hourlyCount,
		HourlyLimit: hourlyLimit,
		DailyCount:  dailyCount,
		DailyLimit:  dailyLimit,
		WeeklyCount: weeklyCount,
		WeeklyLimit: weeklyLimit,
	}, nil
}

func (s *SocialRateLimiter) Record(platform, accountID, actionType string, targetID, sessionID *string) error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(
		"INSERT INTO actions (platform, account_id, action_type, target_id, performed_at, session_id) VALUES (?, ?, ?, ?, ?, ?)",
		strings.ToLower(platform), accountID, strings.ToLower(actionType), targetID, s.now(), sessionID,
	)
	return err
}

func (s *SocialRateLimiter) GetDailyCounts(platform, accountID string) (map[string]int, error) {
	return s.counts(strings.ToLower(platform), accountID, s.now()-86400)
}

func (s *SocialRateLimiter) GetWeeklyCounts(platform, accountID string) (map[string]int, error) {
	return s.counts(strings.ToLower(platform), accountID, s.now()-604800)
}

func (s *SocialRateLimiter) counts(platform, accountID string, since float64) (map[string]int, error) {
	db, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(
		"SELECT action_type, COUNT(*) FROM actions WHERE platform = ? AND account_id = ? AND performed_at >= ? GROUP BY action_type",
		platform, accountID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var actionType string
		var count int
		if err := rows.Scan(&actionType, &count); err != nil {
			return nil, err
		}
		out[actionType] = count
	}
	return out, rows.Err()
}
