package ratelimit

import (
	"database/sql"
	"os"
	"testing"
)

func init() {
	SetConfigOverrides(func() map[string]any { return nil })
}

func newLimiter(t *testing.T) *SocialRateLimiter {
	t.Helper()
	limiter, err := NewSocialRateLimiter(t.TempDir() + "/rate_limits.db")
	if err != nil {
		t.Fatalf("NewSocialRateLimiter: %v", err)
	}
	return limiter
}

func rawDB(t *testing.T, limiter *SocialRateLimiter) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", limiter.DBPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertRaw(t *testing.T, limiter *SocialRateLimiter, platform, account, action string, performedAt float64) {
	t.Helper()
	if _, err := rawDB(t, limiter).Exec(
		"INSERT INTO actions (platform, account_id, action_type, performed_at) VALUES (?, ?, ?, ?)",
		platform, account, action, performedAt,
	); err != nil {
		t.Fatalf("insert raw action: %v", err)
	}
}

func mustCheck(t *testing.T, limiter *SocialRateLimiter, platform, account, action string) CheckResult {
	t.Helper()
	result, err := limiter.Check(platform, account, action)
	if err != nil {
		t.Fatalf("Check(%s, %s, %s): %v", platform, account, action, err)
	}
	return result
}

func count(t *testing.T, ptr *int, want int, field string) {
	t.Helper()
	if ptr == nil {
		t.Fatalf("%s = nil, want %d", field, want)
	}
	if *ptr != want {
		t.Errorf("%s = %d, want %d", field, *ptr, want)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_creates_db_and_table
func TestCreatesDBAndTable(t *testing.T) {
	dbPath := t.TempDir() + "/sub/rate_limits.db"
	if _, err := NewSocialRateLimiter(dbPath); err != nil {
		t.Fatalf("NewSocialRateLimiter: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file missing: %v", err)
	}
	rows, err := rawDB(t, &SocialRateLimiter{DBPath: dbPath}).Query(
		"SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		if name == "actions" {
			found = true
		}
	}
	if !found {
		t.Error("actions table missing")
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_check_allowed_when_empty
func TestCheckAllowedWhenEmpty(t *testing.T) {
	result := mustCheck(t, newLimiter(t), "linkedin", "user1", "invite")
	if !result.Allowed {
		t.Fatal("allowed = false, want true")
	}
	count(t, result.DailyCount, 0, "daily_count")
}

// Parity: core/tests/test_social_rate_limiter.py::test_check_allowed_after_some_records
func TestCheckAllowedAfterSomeRecords(t *testing.T) {
	limiter := newLimiter(t)
	for i := 0; i < 5; i++ {
		target := "target_" + string(rune('0'+i))
		if err := limiter.Record("linkedin", "user1", "invite", &target, nil); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	db := rawDB(t, limiter)
	if _, err := db.Exec("DELETE FROM actions"); err != nil {
		t.Fatalf("delete actions: %v", err)
	}
	now := unixSeconds()
	for i := 0; i < 5; i++ {
		insertRaw(t, limiter, "linkedin", "user1", "invite", now-60*float64(i+1))
	}
	result := mustCheck(t, limiter, "linkedin", "user1", "invite")
	if !result.Allowed {
		t.Fatal("allowed = false, want true")
	}
	count(t, result.DailyCount, 5, "daily_count")
}

// Parity: core/tests/test_social_rate_limiter.py::test_daily_limit_blocks
func TestDailyLimitBlocks(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	for i := 0; i < 50; i++ {
		insertRaw(t, limiter, "linkedin", "acct1", "invite", now-900*float64(i+1))
	}

	result := mustCheck(t, limiter, "linkedin", "acct1", "invite")
	if result.Allowed {
		t.Fatal("allowed = true, want false")
	}
	if result.Reason != "daily_limit_reached" {
		t.Errorf("reason = %q, want daily_limit_reached", result.Reason)
	}
	if !result.HaltCampaign {
		t.Error("halt_campaign = false, want true")
	}
	count(t, result.DailyCount, 50, "daily_count")
	count(t, result.DailyLimit, 50, "daily_limit")
}

// Parity: core/tests/test_social_rate_limiter.py::test_daily_limit_different_accounts_independent
func TestDailyLimitDifferentAccountsIndependent(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	for i := 0; i < 50; i++ {
		insertRaw(t, limiter, "linkedin", "acct_a", "invite", now-900*float64(i+1))
	}

	if blocked := mustCheck(t, limiter, "linkedin", "acct_a", "invite"); blocked.Allowed {
		t.Error("acct_a allowed = true, want false")
	}
	if allowed := mustCheck(t, limiter, "linkedin", "acct_b", "invite"); !allowed.Allowed {
		t.Error("acct_b allowed = false, want true")
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_weekly_limit_blocks
func TestWeeklyLimitBlocks(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	base := now - 2*86400
	for i := 0; i < 200; i++ {
		insertRaw(t, limiter, "linkedin", "user1", "invite", base-1800*float64(i))
	}

	result := mustCheck(t, limiter, "linkedin", "user1", "invite")
	if result.Allowed {
		t.Fatal("allowed = true, want false")
	}
	if result.Reason != "weekly_limit_reached" {
		t.Errorf("reason = %q, want weekly_limit_reached", result.Reason)
	}
	count(t, result.WeeklyCount, 200, "weekly_count")
	count(t, result.WeeklyLimit, 200, "weekly_limit")
}

// Parity: core/tests/test_social_rate_limiter.py::test_no_weekly_limit_for_actions_without_one
func TestNoWeeklyLimitForActionsWithoutOne(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	for i := 0; i < 19; i++ {
		insertRaw(t, limiter, "linkedin", "user1", "message", now-3600*float64(i+1))
	}

	result := mustCheck(t, limiter, "linkedin", "user1", "message")
	if !result.Allowed {
		t.Fatal("allowed = false, want true")
	}
	if result.WeeklyCount != nil {
		t.Errorf("weekly_count = %d, want nil", *result.WeeklyCount)
	}
	if result.WeeklyLimit != nil {
		t.Errorf("weekly_limit = %d, want nil", *result.WeeklyLimit)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_min_delay_blocks_too_fast
func TestMinDelayBlocksTooFast(t *testing.T) {
	limiter := newLimiter(t)
	insertRaw(t, limiter, "linkedin", "user1", "invite", unixSeconds()-5)

	result := mustCheck(t, limiter, "linkedin", "user1", "invite")
	if result.Allowed {
		t.Fatal("allowed = true, want false")
	}
	if result.Reason != "too_fast" {
		t.Errorf("reason = %q, want too_fast", result.Reason)
	}
	if result.HaltCampaign {
		t.Error("halt_campaign = true, want false")
	}
	if result.RetryAfterSeconds == nil || *result.RetryAfterSeconds <= 0 {
		t.Errorf("retry_after_seconds = %v, want > 0", result.RetryAfterSeconds)
	}
	if result.RetryAfterSeconds != nil && *result.RetryAfterSeconds > 25 {
		t.Errorf("retry_after_seconds = %v, want <= 25", *result.RetryAfterSeconds)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_min_delay_allows_after_enough_time
func TestMinDelayAllowsAfterEnoughTime(t *testing.T) {
	limiter := newLimiter(t)
	insertRaw(t, limiter, "linkedin", "user1", "invite", unixSeconds()-31)

	if result := mustCheck(t, limiter, "linkedin", "user1", "invite"); !result.Allowed {
		t.Fatal("allowed = false, want true")
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_record_inserts_action
func TestRecordInsertsAction(t *testing.T) {
	limiter := newLimiter(t)
	target, session := "https://x.com/post/123", "s1"
	if err := limiter.Record("x", "@handle", "reply", &target, &session); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var platform, account, action string
	var gotTarget, gotSession sql.NullString
	if err := rawDB(t, limiter).QueryRow(
		"SELECT platform, account_id, action_type, target_id, session_id FROM actions",
	).Scan(&platform, &account, &action, &gotTarget, &gotSession); err != nil {
		t.Fatalf("select action: %v", err)
	}
	if platform != "x" || account != "@handle" || action != "reply" ||
		gotTarget.String != target || gotSession.String != session {
		t.Errorf("row = (%q,%q,%q,%q,%q)", platform, account, action, gotTarget.String, gotSession.String)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_record_normalizes_platform_and_action
func TestRecordNormalizesPlatformAndAction(t *testing.T) {
	limiter := newLimiter(t)
	if err := limiter.Record("LinkedIn", "@user", "INVITE", nil, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var platform, action string
	if err := rawDB(t, limiter).QueryRow(
		"SELECT platform, action_type FROM actions",
	).Scan(&platform, &action); err != nil {
		t.Fatalf("select action: %v", err)
	}
	if platform != "linkedin" || action != "invite" {
		t.Errorf("row = (%q,%q), want (linkedin,invite)", platform, action)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_env_var_raises_limit
func TestEnvVarRaisesLimit(t *testing.T) {
	t.Setenv("HIVE_RATE_LINKEDIN_INVITE_DAILY", "20")
	if got := resolveLimit("linkedin", "invite", "daily"); got == nil || *got != 20 {
		t.Errorf("resolveLimit = %v, want 20", got)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_env_var_clamped_to_ceiling
func TestEnvVarClampedToCeiling(t *testing.T) {
	t.Setenv("HIVE_RATE_LINKEDIN_INVITE_DAILY", "999")
	if got := resolveLimit("linkedin", "invite", "daily"); got == nil || *got != 125 {
		t.Errorf("resolveLimit = %v, want 125", got)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_env_var_invalid_falls_back_to_default
func TestEnvVarInvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("HIVE_RATE_LINKEDIN_INVITE_DAILY", "not_a_number")
	if got := resolveLimit("linkedin", "invite", "daily"); got == nil || *got != 50 {
		t.Errorf("resolveLimit = %v, want 50", got)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_env_var_weekly_clamped
func TestEnvVarWeeklyClamped(t *testing.T) {
	t.Setenv("HIVE_RATE_LINKEDIN_INVITE_WEEKLY", "999")
	if got := resolveLimit("linkedin", "invite", "weekly"); got == nil || *got != 500 {
		t.Errorf("resolveLimit = %v, want 500", got)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_get_daily_counts
func TestGetDailyCounts(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	insertRaw(t, limiter, "linkedin", "u1", "invite", now-100)
	insertRaw(t, limiter, "linkedin", "u1", "invite", now-200)
	insertRaw(t, limiter, "linkedin", "u1", "message", now-300)
	insertRaw(t, limiter, "linkedin", "u1", "invite", now-90000)

	counts, err := limiter.GetDailyCounts("linkedin", "u1")
	if err != nil {
		t.Fatalf("GetDailyCounts: %v", err)
	}
	if len(counts) != 2 || counts["invite"] != 2 || counts["message"] != 1 {
		t.Errorf("counts = %v, want map[invite:2 message:1]", counts)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_get_weekly_counts
func TestGetWeeklyCounts(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	for i := 0; i < 10; i++ {
		insertRaw(t, limiter, "x", "@h", "reply", now-3600*float64(i+1))
	}
	insertRaw(t, limiter, "x", "@h", "reply", now-700000)

	counts, err := limiter.GetWeeklyCounts("x", "@h")
	if err != nil {
		t.Fatalf("GetWeeklyCounts: %v", err)
	}
	if len(counts) != 1 || counts["reply"] != 10 {
		t.Errorf("counts = %v, want map[reply:10]", counts)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_unknown_action_always_allowed
func TestUnknownActionAlwaysAllowed(t *testing.T) {
	result := mustCheck(t, newLimiter(t), "tiktok", "user1", "dance")
	if !result.Allowed {
		t.Fatal("allowed = false, want true")
	}
	if result.DailyLimit != nil {
		t.Errorf("daily_limit = %d, want nil", *result.DailyLimit)
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_cross_platform_counts_isolated
func TestCrossPlatformCountsIsolated(t *testing.T) {
	limiter := newLimiter(t)
	now := unixSeconds()
	for i := 0; i < 15; i++ {
		insertRaw(t, limiter, "linkedin", "user1", "comment", now-60*float64(i+1))
	}

	if blocked := mustCheck(t, limiter, "linkedin", "user1", "comment"); blocked.Allowed {
		t.Error("linkedin comment allowed = true, want false")
	}
	if allowed := mustCheck(t, limiter, "x", "user1", "reply"); !allowed.Allowed {
		t.Error("x reply allowed = false, want true")
	}
}

// Parity: core/tests/test_social_rate_limiter.py::test_min_delay_values
func TestMinDelayValues(t *testing.T) {
	cases := []struct {
		platform, action string
		want             float64
	}{
		{"linkedin", "invite", 30},
		{"linkedin", "message", 15},
		{"x", "reply", 30},
		{"x", "dm", 20},
		{"x", "post", 60},
		{"unknown", "action", 0},
	}
	for _, tc := range cases {
		if got := minDelay(tc.platform, tc.action); got != tc.want {
			t.Errorf("minDelay(%q, %q) = %v, want %v", tc.platform, tc.action, got, tc.want)
		}
	}
}

// Parity-exempt: upstream has no test for the configuration-file middle
func TestConfigurationOverridesSitBetweenEnvAndDefaults(t *testing.T) {
	defer SetConfigOverrides(func() map[string]any { return nil })

	SetConfigOverrides(func() map[string]any {
		return map[string]any{
			"linkedin.invite.daily":  float64(20),
			"linkedin.invite.weekly": float64(9999),
			"x.reply.hourly":         float64(0),
		}
	})

	if got := resolveLimit("linkedin", "invite", "daily"); got == nil || *got != 20 {
		t.Errorf("daily = %v, want 20", got)
	}
	if got := resolveLimit("linkedin", "invite", "weekly"); got == nil || *got != 9999 {
		t.Errorf("weekly = %v, want 9999 (unclamped)", got)
	}
	if got := resolveLimit("x", "reply", "hourly"); got == nil || *got != 1 {
		t.Errorf("hourly = %v, want 1 (raised to minimum)", got)
	}
	t.Setenv("HIVE_RATE_LINKEDIN_INVITE_DAILY", "30")
	if got := resolveLimit("linkedin", "invite", "daily"); got == nil || *got != 30 {
		t.Errorf("daily with env = %v, want 30", got)
	}
	if got := resolveLimit("linkedin", "message", "daily"); got == nil || *got != 60 {
		t.Errorf("message daily = %v, want 60", got)
	}
}
