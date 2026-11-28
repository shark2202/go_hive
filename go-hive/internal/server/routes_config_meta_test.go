package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

func jsonCanonical(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %#v: %v", value, err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return out
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotCanonical := jsonCanonical(t, got)
	wantCanonical := jsonCanonical(t, want)
	if !reflect.DeepEqual(gotCanonical, wantCanonical) {
		t.Fatalf("body = %#v\nwant   %#v", gotCanonical, wantCanonical)
	}
}

// profileMemoryBody returns the part of a memory file below its frontmatter.
func profileMemoryBody(t *testing.T, text string) string {
	t.Helper()
	parts := strings.SplitN(text, "---\n", 3)
	if len(parts) != 3 {
		t.Fatalf("memory file has no frontmatter: %q", text)
	}
	return strings.TrimPrefix(parts[2], "\n")
}

func configGetRaw(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return resp.StatusCode, resp.Header, data
}

func multipartConfigPayload(t *testing.T, field, contentType string, payload []byte) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, "avatar.bin"))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

func configDoMultipart(t *testing.T, url, contentType string, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("POST %s: decode body: %v", url, err)
	}
	return resp.StatusCode, decoded
}

func clearRateLimitEnv(t *testing.T) {
	t.Helper()
	for _, pair := range [][2]string{
		{"linkedin", "profile_view"},
		{"linkedin", "invite"},
		{"linkedin", "message"},
		{"linkedin", "comment"},
		{"x", "reply"},
		{"x", "dm"},
		{"x", "post"},
		{"instagram", "dm"},
	} {
		for _, window := range []string{"hourly", "daily", "weekly"} {
			t.Setenv(fmt.Sprintf("HIVE_RATE_%s_%s_%s",
				strings.ToUpper(pair[0]), strings.ToUpper(pair[1]), strings.ToUpper(window)), "")
		}
	}
}

func findLimitRow(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	rows, ok := body["limits"].([]any)
	if !ok {
		t.Fatalf("body has no limits list: %#v", body)
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		platform, _ := row["platform"].(string)
		action, _ := row["action_type"].(string)
		if platform+"."+action == key {
			return row
		}
	}
	t.Fatalf("limit row %q not found in %#v", key, rows)
	return nil
}

func writeExternalSkill(t *testing.T, base, name string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: A test skill.\n---\n\nInstructions.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// Parity: framework/server/tests/test_api.py::TestConfigRoutes::test_get_models_uses_shared_model_catalogue
func TestTheModelsRouteUsesTheSharedCatalogue(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	catalogue := llm.GetModelsCatalogue()
	if len(catalogue) == 0 {
		t.Fatal("the shared model catalogue is empty; cannot verify the route")
	}
	wantModels := make(map[string]any, len(catalogue))
	for provider, entries := range catalogue {
		rows := make([]any, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, entry.ToMap())
		}
		wantModels[provider] = rows
	}

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/models", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{"models": wantModels})
}

// Parity-exempt: no upstream test drives the sentinel handlers. This pins
func TestTheSentinelRouteReadsTheGlobalBlock(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
		want   map[string]any
	}{
		{"absent", map[string]any{}, map[string]any{}},
		{"null", map[string]any{"sentinel": nil}, map[string]any{}},
		{"false", map[string]any{"sentinel": false}, map[string]any{}},
		{"zero", map[string]any{"sentinel": float64(0)}, map[string]any{}},
		{"empty string", map[string]any{"sentinel": ""}, map[string]any{}},
		{"empty list", map[string]any{"sentinel": []any{}}, map[string]any{}},
		{"empty object", map[string]any{"sentinel": map[string]any{}}, map[string]any{}},
		{
			name:   "stored block",
			config: map[string]any{"sentinel": map[string]any{"quiet_hours": []any{float64(22), float64(7)}}},
			want:   map[string]any{"quiet_hours": []any{float64(22), float64(7)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := newFeatureHome(t)
			writeFeatureConfig(t, home, tc.config)
			server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

			status, body := configDo(t, http.MethodGet, server.URL+"/api/config/sentinel", nil)
			if status != 200 {
				t.Fatalf("status = %d, want 200 (body %#v)", status, body)
			}
			assertJSONEqual(t, body, map[string]any{"sentinel": tc.want})
		})
	}
}

// Parity-exempt: no upstream test drives the sentinel handlers. This pins the
func TestTheSentinelRouteReplacesTheGlobalBlock(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"other": true})
	refreshed := 0
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home:            home,
		SentinelRefresh: func() { refreshed++ },
	})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/sentinel",
		map[string]any{"sentinel": map[string]any{"max_turns": 9}})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{"sentinel": map[string]any{"max_turns": 9}})
	if refreshed != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshed)
	}
	stored := readFeatureConfig(t, home)
	assertJSONEqual(t, stored["sentinel"], map[string]any{"max_turns": 9})
	if stored["other"] != true {
		t.Fatalf("unrelated key was dropped: %#v", stored)
	}

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/sentinel",
		map[string]any{"interval": 30})
	if status != 200 {
		t.Fatalf("bare body status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{"sentinel": map[string]any{"interval": 30}})
	assertJSONEqual(t, readFeatureConfig(t, home)["sentinel"], map[string]any{"interval": 30})

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/sentinel",
		map[string]any{"sentinel": map[string]any{}})
	if status != 200 {
		t.Fatalf("empty block status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{"sentinel": map[string]any{}})
	if _, present := readFeatureConfig(t, home)["sentinel"]; present {
		t.Fatal("an empty sentinel block must delete the key")
	}
	if refreshed != 3 {
		t.Fatalf("refresh calls = %d, want 3", refreshed)
	}
}

// Parity-exempt: no upstream test drives the sentinel handlers. A non-object
func TestTheSentinelRouteRejectsANonObjectBlockAndSurvivesRefreshFailures(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"sentinel": map[string]any{"kept": true}})
	before, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home:            home,
		SentinelRefresh: func() { panic("listener refresh exploded") },
	})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/sentinel",
		map[string]any{"sentinel": []any{float64(1)}})
	if status != 400 || body["error"] != "sentinel must be an object" {
		t.Fatalf("non-object block: status = %d, body = %#v", status, body)
	}
	after, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("rejected block rewrote the config:\nbefore %s\nafter  %s", before, after)
	}

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/sentinel",
		map[string]any{"sentinel": map[string]any{"kept": false}})
	if status != 200 {
		t.Fatalf("panicking refresh: status = %d, want 200 (body %#v)", status, body)
	}
}

// Parity-exempt: no upstream test drives the rate-limit handlers. This pins
func TestTheRateLimitsRouteReportsTheDeclaredDefaults(t *testing.T) {
	clearRateLimitEnv(t)
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/rate-limits", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	rows, ok := body["limits"].([]any)
	if !ok || len(rows) != 8 {
		t.Fatalf("limits = %#v, want 8 rows", body["limits"])
	}
	wantOrder := []string{
		"linkedin.profile_view", "linkedin.invite", "linkedin.message",
		"linkedin.comment", "x.reply", "x.dm", "x.post", "instagram.dm",
	}
	for i, want := range wantOrder {
		row := rows[i].(map[string]any)
		if got := row["platform"].(string) + "." + row["action_type"].(string); got != want {
			t.Fatalf("row %d = %s, want %s", i, got, want)
		}
	}

	assertJSONEqual(t, findLimitRow(t, body, "linkedin.profile_view"), map[string]any{
		"platform":       "linkedin",
		"action_type":    "profile_view",
		"min_delay_s":    5,
		"hourly":         25,
		"hourly_max":     100,
		"hourly_default": 25,
		"daily":          150,
		"daily_max":      400,
		"daily_default":  150,
	})
	assertJSONEqual(t, findLimitRow(t, body, "linkedin.invite"), map[string]any{
		"platform":       "linkedin",
		"action_type":    "invite",
		"min_delay_s":    30,
		"hourly":         10,
		"hourly_max":     25,
		"hourly_default": 10,
		"daily":          50,
		"daily_max":      125,
		"daily_default":  50,
		"weekly":         200,
		"weekly_max":     500,
		"weekly_default": 200,
	})
	assertJSONEqual(t, findLimitRow(t, body, "instagram.dm"), map[string]any{
		"platform":       "instagram",
		"action_type":    "dm",
		"min_delay_s":    45,
		"hourly":         16,
		"hourly_max":     40,
		"hourly_default": 16,
		"daily":          80,
		"daily_max":      150,
		"daily_default":  80,
		"weekly":         300,
		"weekly_max":     500,
		"weekly_default": 300,
	})
}

// Parity-exempt: no upstream test drives the rate-limit handlers. This pins
func TestTheRateLimitsRouteMergesOverridesAndWarnsInDocumentOrder(t *testing.T) {
	clearRateLimitEnv(t)
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"rate_limits": map[string]any{"linkedin.profile_view.daily": 40},
	})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	raw := `{"limits": {` +
		`"linkedin.invite.daily": 20, ` +
		`"linkedin.invite.weekly": 999, ` +
		`"x.post.hourly": 0, ` +
		`"bogus.key": 5, ` +
		`"linkedin.invite.minutely": 3, ` +
		`"instagram.dm.daily": "12"}}`
	status, body := configDoRaw(t, http.MethodPut, server.URL+"/api/config/rate-limits", raw)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	wantWarnings := []any{
		"linkedin.invite.weekly=999 exceeds recommended max of 500. High values increase the risk of account bans.",
	}
	if !reflect.DeepEqual(body["warnings"], wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", body["warnings"], wantWarnings)
	}

	assertJSONEqual(t, findLimitRow(t, body, "linkedin.invite"), map[string]any{
		"platform":       "linkedin",
		"action_type":    "invite",
		"min_delay_s":    30,
		"hourly":         10,
		"hourly_max":     25,
		"hourly_default": 10,
		"daily":          20,
		"daily_max":      125,
		"daily_default":  50,
		"weekly":         999,
		"weekly_max":     500,
		"weekly_default": 200,
	})
	if got := findLimitRow(t, body, "x.post")["hourly"]; got != float64(1) {
		t.Fatalf("floored x.post.hourly = %#v, want 1", got)
	}
	if got := findLimitRow(t, body, "linkedin.profile_view")["daily"]; got != float64(40) {
		t.Fatalf("existing profile_view.daily = %#v, want 40", got)
	}
	if got := findLimitRow(t, body, "instagram.dm")["daily"]; got != float64(12) {
		t.Fatalf("coerced instagram.dm.daily = %#v, want 12", got)
	}

	assertJSONEqual(t, readFeatureConfig(t, home)["rate_limits"], map[string]any{
		"linkedin.profile_view.daily": 40,
		"linkedin.invite.daily":       20,
		"linkedin.invite.weekly":      999,
		"x.post.hourly":               1,
		"instagram.dm.daily":          12,
	})
}

// Parity-exempt: no upstream test drives the rate-limit handler; this pins the
func TestTheRateLimitsRouteRejectsNonObjectLimits(t *testing.T) {
	clearRateLimitEnv(t)
	for _, raw := range []string{`{}`, `{"limits": []}`, `{"limits": "x"}`, `{"limits": 3}`} {
		home := newFeatureHome(t)
		writeFeatureConfig(t, home, map[string]any{"keep": true})
		before, err := os.ReadFile(home.ConfigurationFile())
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

		status, body := configDoRaw(t, http.MethodPut, server.URL+"/api/config/rate-limits", raw)
		if status != 400 || body["error"] != "limits must be an object" {
			t.Fatalf("%s: status = %d, body = %#v", raw, status, body)
		}
		after, err := os.ReadFile(home.ConfigurationFile())
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: rejected body rewrote the config", raw)
		}
	}
}

// Parity-exempt: no upstream test drives the profile handlers. This pins the
func TestTheProfileRouteReportsDefaultsAndStoredValues(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/profile", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"displayName":         "",
		"about":               "",
		"theme":               "",
		"density":             "",
		"prompt_library_sort": nil,
	})

	writeFeatureConfig(t, home, map[string]any{"user_profile": map[string]any{
		"displayName": "Ada",
		"about":       "Engineer",
		"theme":       "dark",
		"density":     "compact",
		"prompt_library_sort": map[string]any{
			"my":        "name",
			"community": "bogus",
			"extra":     "date",
		},
	}})
	status, body = configDo(t, http.MethodGet, server.URL+"/api/config/profile", nil)
	if status != 200 {
		t.Fatalf("stored profile: status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"displayName":         "Ada",
		"about":               "Engineer",
		"theme":               "dark",
		"density":             "compact",
		"prompt_library_sort": map[string]any{"my": "name"},
	})
}

// Parity-exempt: no upstream test drives the profile handlers. This pins the
func TestTheProfileRoutePersistsAndMirrorsIntoMemory(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/profile", map[string]any{
		"displayName":         "  Ada Lovelace  ",
		"about":               42,
		"theme":               "dark",
		"density":             "weird",
		"prompt_library_sort": map[string]any{"community": "popular", "my": "bogus"},
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"displayName":         "Ada Lovelace",
		"about":               "42",
		"theme":               "dark",
		"density":             "",
		"prompt_library_sort": map[string]any{"community": "popular"},
	})

	stored, _ := readFeatureConfig(t, home)["user_profile"].(map[string]any)
	assertJSONEqual(t, stored, map[string]any{
		"displayName":         "Ada Lovelace",
		"about":               "42",
		"theme":               "dark",
		"prompt_library_sort": map[string]any{"community": "popular"},
	})

	// The mirror is a memory.Write: a profile-type document with stamped
	// provenance and one log.jsonl entry, not a bare file dropped on disk.
	memoryDir := memory.GlobalMemoryDir(home.Root)
	memoryPath := filepath.Join(memoryDir, "user-profile.md")
	data, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	text := string(data)
	fields := memory.ParseFrontmatter(text)
	if fields["name"] != "User Profile" || fields["description"] != "User identity: Ada Lovelace" || fields["type"] != "profile" {
		t.Fatalf("frontmatter = %#v", fields)
	}
	if memory.Day(fields["learned"]) == "" || memory.Day(fields["learned"]) != memory.Day(fields["confirmed"]) {
		t.Fatalf("learned/confirmed = %q/%q", fields["learned"], fields["confirmed"])
	}
	wantBody := "## Identity\n- **Name:** Ada Lovelace\n- **About:** 42\n"
	if body := profileMemoryBody(t, text); body != wantBody {
		t.Fatalf("memory body = %q, want %q", body, wantBody)
	}
	// KeepAuthor (upstream claim=False): a freshly synced profile belongs to
	// nobody yet, so the reflection agent may still add what it learned.
	if author := memory.AuthorOf(text); author != "" {
		t.Fatalf("author = %q, want no author on a new profile", author)
	}
	entries := memory.History(memoryDir, nil)
	if len(entries) != 1 {
		t.Fatalf("log entries = %#v, want one profile-settings write", entries)
	}
	entry := entries[0]
	if entry.Op != "write" || entry.File != "user-profile.md" || entry.Author != "owner" || entry.Reason != "profile settings" {
		t.Fatalf("log entry = %#v", entry)
	}
	if entry.Before != nil || entry.After == nil || *entry.After != text {
		t.Fatalf("log before/after = %#v/%#v", entry.Before, entry.After)
	}
}

// Parity-exempt: no upstream test drives profile memory synchronisation; this
func TestTheProfileRouteReplacesOnlyTheIdentityMemorySection(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	memoryDir := memory.GlobalMemoryDir(home.Root)
	memoryPath := filepath.Join(memoryDir, "user-profile.md")
	existing := memory.BuildMemoryDocument(
		"User Profile", "User profile information", "profile",
		"## Identity\n- **Name:** Old\n- **About:** Old about\n\n## Preferences\n- likes tea")
	// Seed an owner-written profile so the sync must keep the owner author and
	// the day it was learned, while re-stamping when it was confirmed.
	seededText, err := memory.Write(memoryDir, "user-profile.md", existing,
		memory.WriteOptions{Author: memory.AuthorOwner, Reason: "seeded", Now: time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local)})
	if err != nil {
		t.Fatalf("seed memory file: %v", err)
	}

	before := time.Now().Format("2006-01-02")
	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/profile",
		map[string]any{"displayName": "Grace", "about": "hopper"})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	after := time.Now().Format("2006-01-02")

	data, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	text := string(data)
	wantBody := "## Identity\n- **Name:** Grace\n- **About:** hopper\n## Preferences\n- likes tea\n"
	if body := profileMemoryBody(t, text); body != wantBody {
		t.Fatalf("memory body = %q, want %q", body, wantBody)
	}
	fields := memory.ParseFrontmatter(text)
	if fields["author"] != "owner" {
		t.Fatalf("author = %q, want owner kept", fields["author"])
	}
	if learned := memory.Day(fields["learned"]); learned != "2026-01-02" {
		t.Fatalf("learned = %q, want 2026-01-02 kept", fields["learned"])
	}
	if confirmed := memory.Day(fields["confirmed"]); confirmed != before && confirmed != after {
		t.Fatalf("confirmed = %q, want %q", fields["confirmed"], before)
	}
	entries := memory.History(memoryDir, nil)
	if len(entries) != 2 {
		t.Fatalf("log entries = %#v, want the seed plus one profile-settings write", entries)
	}
	if last := entries[len(entries)-1]; last.Reason != "profile settings" || last.Author != "owner" || last.Before == nil || *last.Before != seededText {
		t.Fatalf("last log entry = %#v", last)
	}
}

// Parity: core/framework/server/routes_config.py::_update_user_profile_memory
func TestSyncUserProfileMemoryKeepsTheOwnerAndLogsTheSync(t *testing.T) {
	home := newFeatureHome(t)
	memoryDir := memory.GlobalMemoryDir(home.Root)
	seeded := memory.BuildMemoryDocument(
		"User Profile", "User profile information", "profile",
		"## Identity\n- **Name:** Ada\n\n## Preferences\n- likes tea")
	if _, err := memory.Write(memoryDir, "user-profile.md", seeded,
		memory.WriteOptions{Author: memory.AuthorOwner, Reason: "seeded", Now: time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local)}); err != nil {
		t.Fatalf("seed memory file: %v", err)
	}

	SyncUserProfileMemory(home, "Ada", "Engineer")

	data, err := os.ReadFile(filepath.Join(memoryDir, "user-profile.md"))
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	text := string(data)
	if author := memory.AuthorOf(text); author != "owner" {
		t.Fatalf("author = %q, want owner", author)
	}
	if body := profileMemoryBody(t, text); !strings.Contains(body, "- **About:** Engineer") {
		t.Fatalf("memory body = %q, want the synced identity line", body)
	}
	entries := memory.History(memoryDir, nil)
	if len(entries) != 2 {
		t.Fatalf("log entries = %#v, want the seed plus one sync", entries)
	}
	last := entries[len(entries)-1]
	if last.Op != "write" || last.File != "user-profile.md" || last.Author != "owner" || last.Reason != "profile settings" {
		t.Fatalf("sync log entry = %#v", last)
	}

	// The guard the sync relies on: a profile the owner wrote stays the
	// owner's, and a non-owner write is refused without logging anything.
	if _, err := memory.Write(memoryDir, "user-profile.md", seeded,
		memory.WriteOptions{Author: "reflection", Reason: "learned"}); !memory.IsOwnerWritten(err) {
		t.Fatalf("reflection write = %v, want OwnerWritten", err)
	}
	if entries := memory.History(memoryDir, nil); len(entries) != 2 {
		t.Fatalf("refused write was logged: %#v", entries)
	}
}

// Parity: core/framework/server/routes_config.py::_update_user_profile_memory
// The upstream sync swallows memory-log failures so the profile save still
// succeeds; an oversized mirror must not write a partial file or a log entry.
func TestTheProfileRouteSurvivesAnOversizedMemoryMirror(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	huge := strings.Repeat("x", memory.MaxFileSizeBytes+1)
	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/profile",
		map[string]any{"displayName": "Ada", "about": huge})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if stored, _ := readFeatureConfig(t, home)["user_profile"].(map[string]any); stored["about"] != huge {
		t.Fatalf("stored about was not saved")
	}
	memoryDir := memory.GlobalMemoryDir(home.Root)
	_, statErr := os.Stat(filepath.Join(memoryDir, "user-profile.md"))
	if statErr == nil || !os.IsNotExist(statErr) {
		t.Fatalf("oversized mirror left a memory file: %v", statErr)
	}
	if entries := memory.History(memoryDir, nil); len(entries) != 0 {
		t.Fatalf("oversized mirror was logged: %#v", entries)
	}
}

// Parity-exempt: no upstream test drives the profile handlers. A stored
func TestTheProfileRouteFailsOnANonObjectProfile(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"user_profile": "oops", "keep": true})
	before, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/profile", nil)
	if status != 500 || body["error"] != internalServerError {
		t.Fatalf("GET: status = %d, body = %#v", status, body)
	}

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/profile",
		map[string]any{"displayName": "x"})
	if status != 500 || body["error"] != internalServerError {
		t.Fatalf("mutating PUT: status = %d, body = %#v", status, body)
	}
	after, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("mutating PUT rewrote the config:\nbefore %s\nafter  %s", before, after)
	}

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/profile",
		map[string]any{"unrelated": true})
	if status != 500 || body["error"] != internalServerError {
		t.Fatalf("inert PUT: status = %d, body = %#v", status, body)
	}
	stored := readFeatureConfig(t, home)
	if stored["user_profile"] != "oops" || stored["keep"] != true {
		t.Fatalf("inert PUT did not preserve the config: %#v", stored)
	}
}

// Parity-exempt: aiohttp's multipart parser is not reachable from the Go test
func TestTheAvatarRouteRejectsEveryBadUpload(t *testing.T) {
	payload := []byte("\x89PNG\r\n\x1a\n image bytes")
	oversized := bytes.Repeat([]byte{0xAB}, maxAvatarBytes+1)
	cases := []struct {
		name        string
		field       string
		contentType string
		payload     []byte
		message     string
	}{
		{
			name:        "wrong field",
			field:       "file",
			contentType: "image/png",
			payload:     payload,
			message:     "Expected a file field named 'avatar'",
		},
		{
			name:        "unsupported type",
			field:       "avatar",
			contentType: "text/plain",
			payload:     payload,
			message:     "Unsupported image type: text/plain. Use JPEG, PNG, or WebP.",
		},
		{
			name:        "missing part content type",
			field:       "avatar",
			contentType: "",
			payload:     payload,
			message:     "Unsupported image type: application/octet-stream. Use JPEG, PNG, or WebP.",
		},
		{
			name:        "empty payload",
			field:       "avatar",
			contentType: "image/png",
			payload:     nil,
			message:     "Empty file",
		},
		{
			name:        "oversized",
			field:       "avatar",
			contentType: "image/png",
			payload:     oversized,
			message:     "Image too large. Maximum size is 2 MB.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := newFeatureHome(t)
			server := newConfigRoutes(t, ConfigRouteDeps{Home: home})
			contentType, body := multipartConfigPayload(t, tc.field, tc.contentType, tc.payload)
			status, response := configDoMultipart(t, server.URL+"/api/config/profile/avatar", contentType, body)
			if status != 400 || response["error"] != tc.message {
				t.Fatalf("status = %d, body = %#v, want 400 %q", status, response, tc.message)
			}
			matches, err := filepath.Glob(filepath.Join(home.Root, "avatar.*"))
			if err != nil {
				t.Fatalf("glob avatar files: %v", err)
			}
			if len(matches) != 0 {
				t.Fatalf("a rejected upload left files behind: %#v", matches)
			}
		})
	}
}

// Parity-exempt: aiohttp's multipart parser is not reachable from the Go test
func TestTheAvatarRouteStoresAndServesTheImage(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/profile/avatar", nil)
	if status != 404 || body["error"] != "No avatar found" {
		t.Fatalf("GET with no avatar: status = %d, body = %#v", status, body)
	}

	png := []byte("\x89PNG\r\n\x1a\nfirst image")
	contentType, payload := multipartConfigPayload(t, "avatar", "image/png", png)
	status, response := configDoMultipart(t, server.URL+"/api/config/profile/avatar", contentType, payload)
	if status != 200 {
		t.Fatalf("upload: status = %d, body = %#v", status, response)
	}
	assertJSONEqual(t, response, map[string]any{"avatar_url": "/api/config/profile/avatar"})

	stored, err := os.ReadFile(filepath.Join(home.Root, "avatar.png"))
	if err != nil {
		t.Fatalf("read stored avatar: %v", err)
	}
	if !bytes.Equal(stored, png) {
		t.Fatalf("stored avatar = %q, want %q", stored, png)
	}

	status, headers, served := configGetRaw(t, server.URL+"/api/config/profile/avatar")
	if status != 200 {
		t.Fatalf("serve: status = %d, want 200", status)
	}
	if got := headers.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := headers.Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q, want public, max-age=3600", got)
	}
	if !bytes.Equal(served, png) {
		t.Fatalf("served avatar = %q, want %q", served, png)
	}

	jpeg := []byte("\xff\xd8\xff\xe0second image")
	contentType, payload = multipartConfigPayload(t, "avatar", "image/jpeg", jpeg)
	status, response = configDoMultipart(t, server.URL+"/api/config/profile/avatar", contentType, payload)
	if status != 200 {
		t.Fatalf("re-upload: status = %d, body = %#v", status, response)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "avatar.png")); !os.IsNotExist(err) {
		t.Fatalf("the previous avatar.png survived the JPEG upload: %v", err)
	}
	stored, err = os.ReadFile(filepath.Join(home.Root, "avatar.jpg"))
	if err != nil {
		t.Fatalf("read re-uploaded avatar: %v", err)
	}
	if !bytes.Equal(stored, jpeg) {
		t.Fatalf("re-uploaded avatar = %q, want %q", stored, jpeg)
	}
	status, headers, served = configGetRaw(t, server.URL+"/api/config/profile/avatar")
	if status != 200 || headers.Get("Content-Type") != "image/jpeg" || !bytes.Equal(served, jpeg) {
		t.Fatalf("serve after re-upload: status = %d, type = %q", status, headers.Get("Content-Type"))
	}
}

// Parity-exempt: no upstream test drives the avatar GET handler. This pins
func TestTheAvatarRouteServesTheFirstExtensionInProbeOrder(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	png := []byte("png bytes")
	webp := []byte("webp bytes")
	if err := os.WriteFile(filepath.Join(home.Root, "avatar.png"), png, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.Root, "avatar.webp"), webp, 0o644); err != nil {
		t.Fatalf("write webp: %v", err)
	}

	status, headers, served := configGetRaw(t, server.URL+"/api/config/profile/avatar")
	if status != 200 || headers.Get("Content-Type") != "image/png" || !bytes.Equal(served, png) {
		t.Fatalf("png should win the probe: status = %d, type = %q", status, headers.Get("Content-Type"))
	}

	if err := os.Remove(filepath.Join(home.Root, "avatar.png")); err != nil {
		t.Fatalf("remove png: %v", err)
	}
	status, headers, served = configGetRaw(t, server.URL+"/api/config/profile/avatar")
	if status != 200 || headers.Get("Content-Type") != "image/webp" || !bytes.Equal(served, webp) {
		t.Fatalf("webp should be served second: status = %d, type = %q", status, headers.Get("Content-Type"))
	}
}

// Parity-exempt: no upstream test drives the external-skills handlers. HOME is
func TestTheExternalSkillsRouteReportsNoPathsByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/external-skills", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"paths":       []any{},
		"resolved":    []any{},
		"suggestions": []any{},
	})
}

// Parity-exempt: no upstream test drives the external-skills handlers. This
func TestTheExternalSkillsRouteResolvesConfiguredPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := newFeatureHome(t)

	root := t.TempDir()
	writeExternalSkill(t, root, "alpha")
	writeExternalSkill(t, root, "beta")
	missing := filepath.Join(t.TempDir(), "absent")
	writeFeatureConfig(t, home, map[string]any{
		"external_skills": []any{root, missing, float64(7)},
	})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/external-skills", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"paths": []any{root, missing},
		"resolved": []any{
			map[string]any{"path": root, "resolved": root, "exists": true, "skills": 2},
			map[string]any{"path": missing, "resolved": missing, "exists": false, "skills": 0},
		},
		"suggestions": []any{},
	})
}

// Parity-exempt: no upstream test drives the external-skills PUT handler. This
func TestTheExternalSkillsRouteWritesTheTrimmedList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := newFeatureHome(t)

	root := t.TempDir()
	writeExternalSkill(t, root, "alpha")
	missing := filepath.Join(t.TempDir(), "absent")
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/external-skills", map[string]any{
		"paths": []any{"  " + root + "  ", "   ", missing},
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{
		"paths": []any{root, missing},
		"resolved": []any{
			map[string]any{"path": root, "resolved": root, "exists": true, "skills": 1},
			map[string]any{"path": missing, "resolved": missing, "exists": false, "skills": 0},
		},
	})
	assertJSONEqual(t, readFeatureConfig(t, home)["external_skills"], []any{root, missing})

	status, body = configDo(t, http.MethodPut, server.URL+"/api/config/external-skills",
		map[string]any{"paths": []any{}})
	if status != 200 {
		t.Fatalf("empty list: status = %d, want 200 (body %#v)", status, body)
	}
	assertJSONEqual(t, body, map[string]any{"paths": []any{}, "resolved": []any{}})
	if _, present := readFeatureConfig(t, home)["external_skills"]; present {
		t.Fatal("an empty path list must delete the external_skills key")
	}
}

// Parity-exempt: no upstream test drives the external-skills PUT handler; this
func TestTheExternalSkillsRouteRejectsMalformedLists(t *testing.T) {
	for _, body := range []map[string]any{
		{},
		{"paths": "abc"},
		{"paths": []any{float64(1), float64(2)}},
		{"paths": []any{"ok", float64(3)}},
	} {
		home := newFeatureHome(t)
		writeFeatureConfig(t, home, map[string]any{"keep": true})
		before, err := os.ReadFile(home.ConfigurationFile())
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

		status, response := configDo(t, http.MethodPut, server.URL+"/api/config/external-skills", body)
		if status != 400 || response["error"] != "'paths' must be a list of strings" {
			t.Fatalf("%#v: status = %d, body = %#v", body, status, response)
		}
		after, err := os.ReadFile(home.ConfigurationFile())
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%#v: rejected body rewrote the config", body)
		}
	}
}

// Parity-exempt: no upstream test drives the external-skills handler. A stored
func TestTheExternalSkillsRouteFailsOnANonIterableStoredValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"external_skills": float64(5)})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/external-skills", nil)
	if status != 500 || body["error"] != internalServerError {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
}
