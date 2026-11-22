package llm

// Antigravity credential sources and OAuth refresh: the native accounts file,
// the Antigravity IDE SQLite state DB, and the Google OAuth2 refresh-token
// exchange that keeps an access token live.
//
// Parity: core/framework/llm/antigravity.py (_load_from_json_file,
// _load_from_ide_db, _do_token_refresh, AntigravityProvider._ensure_token)

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	_ "modernc.org/sqlite"
)

// antigravityCredentialError is upstream's _ensure_token failure text.
const antigravityCredentialError = "No valid Antigravity credentials. Run: uv run python core/antigravity_auth.py auth account add"

var (
	antigravityTokenCandidateRE = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
	antigravityAccessTokenRE    = regexp.MustCompile(`ya29\.[A-Za-z0-9_\-\.]+`)
	antigravityRefreshTokenRE   = regexp.MustCompile(`1//[A-Za-z0-9_\-\.]+`)
)

// defaultAntigravityAccountsPath resolves _ACCOUNTS_FILE under the hive home
// (HIVE_HOME, else ~/.hive).
func defaultAntigravityAccountsPath() string {
	home, err := hivehome.Resolve()
	if err != nil {
		return ""
	}
	return filepath.Join(home.Root, antigravityAccountsFileName)
}

// defaultAntigravityIDEDBPaths returns the IDE state DB candidates
// (_IDE_STATE_DB_MAC, _IDE_STATE_DB_LINUX). A platform without either file
// simply skips the fallback.
func defaultAntigravityIDEDBPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb"),
	}
}

// initCredentials loads the best available credential source: the accounts
// file first, the IDE state DB as the fallback.
//
// Parity: AntigravityProvider._init_credentials
func (p *AntigravityProvider) initCredentials() {
	access, refresh, projectID, expiresAt := loadAntigravityAccountsFile(p.accountsPath, p.nowTime())
	if refresh != "" {
		p.accessToken = access
		p.refreshToken = refresh
		if projectID != "" {
			p.projectID = projectID
		}
		p.expiresAt = expiresAt
		return
	}

	access, refresh, expiresAt = loadAntigravityIDEDB(p.ideDBPaths)
	if access != "" {
		p.accessToken = access
		p.refreshToken = refresh
		p.expiresAt = expiresAt
	}
}

// loadAntigravityAccountsFile ports _load_from_json_file. It returns
// (access, refresh, project, expires); a missing or malformed file leaves the
// default project and empty credentials.
func loadAntigravityAccountsFile(path string, now time.Time) (string, string, string, time.Time) {
	if path == "" {
		return "", "", AntigravityDefaultProjectID, time.Time{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", AntigravityDefaultProjectID, time.Time{}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", AntigravityDefaultProjectID, time.Time{}
	}
	accounts, ok := doc["accounts"].([]any)
	if !ok || len(accounts) == 0 {
		return "", "", AntigravityDefaultProjectID, time.Time{}
	}

	var account map[string]any
	for _, rawAccount := range accounts {
		candidate, ok := rawAccount.(map[string]any)
		if !ok {
			continue
		}
		// `enabled is not False`: only the literal JSON false disables an
		// account; an absent value (or any other falsy JSON value) keeps it.
		if enabled, ok := candidate["enabled"].(bool); ok && !enabled {
			continue
		}
		account = candidate
		break
	}
	if account == nil {
		return "", "", AntigravityDefaultProjectID, time.Time{}
	}

	schemaVersion := 1.0
	if value, ok := antigravityFloat(doc["schemaVersion"]); ok {
		schemaVersion = value
	}

	if schemaVersion >= 4 {
		// V4 schema: refresh = "refreshToken|projectId[|managedProjectId]"
		refreshStr := antigravityString(account["refresh"])
		parts := []string{}
		if refreshStr != "" {
			parts = strings.Split(refreshStr, "|")
		}
		refresh := ""
		if len(parts) > 0 {
			refresh = parts[0]
		}
		projectID := AntigravityDefaultProjectID
		if len(parts) >= 2 && parts[1] != "" {
			projectID = parts[1]
		}

		access := antigravityString(account["access"])
		expiresAt := time.Time{}
		if expiresMS, ok := antigravityFloat(account["expires"]); ok && expiresMS != 0 {
			expiresAt = time.Unix(0, int64(expiresMS*float64(time.Millisecond)))
		}
		// Treat near-expiry tokens as absent so ensureToken triggers a refresh.
		if access != "" && !expiresAt.IsZero() && !now.Before(expiresAt.Add(-antigravityTokenRefreshBuffer)) {
			access = ""
			expiresAt = time.Time{}
		}
		return access, refresh, projectID, expiresAt
	}

	// V1–V3 schema: plain accessToken / refreshToken fields, expiry estimated
	// from last_refresh + 1h.
	access := antigravityString(account["accessToken"])
	refresh := antigravityString(account["refreshToken"])
	expiresAt := time.Time{}
	if lastRefresh := antigravityString(doc["last_refresh"]); lastRefresh != "" {
		if timestamp, err := parseAntigravityTimestamp(lastRefresh); err == nil {
			expiresAt = timestamp.Add(time.Hour)
			if !now.Before(expiresAt.Add(-antigravityTokenRefreshBuffer)) {
				access = ""
			}
		}
	}
	return access, refresh, AntigravityDefaultProjectID, expiresAt
}

// parseAntigravityTimestamp mirrors datetime.fromisoformat(value) after the
// upstream "Z" → "+00:00" replacement. A zone-less timestamp is local time,
// as in Python.
func parseAntigravityTimestamp(value string) (time.Time, error) {
	normalized := strings.ReplaceAll(value, "Z", "+00:00")
	if timestamp, err := time.Parse(time.RFC3339Nano, normalized); err == nil {
		return timestamp, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if timestamp, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return timestamp, nil
		}
	}
	return time.Time{}, errors.New("unrecognised timestamp")
}

// loadAntigravityIDEDB extracts (access, refresh, expires) from the IDE state
// DB. The IDE refreshes the token while it runs, so the DB's mtime is the
// expiry estimate.
//
// Parity: core/framework/llm/antigravity.py::_load_from_ide_db
func loadAntigravityIDEDB(paths []string) (string, string, time.Time) {
	for _, dbPath := range paths {
		if dbPath == "" {
			continue
		}
		info, err := os.Stat(dbPath)
		if err != nil || info.IsDir() {
			continue
		}
		access, refresh, err := readAntigravityIDEDB(dbPath)
		if err != nil || access == "" {
			continue
		}
		return access, refresh, info.ModTime().Add(time.Hour)
	}
	return "", "", time.Time{}
}

// readAntigravityIDEDB reads the base64-encoded OAuth blob out of VS Code's
// ItemTable and recovers the ya29./1// tokens buried in it.
func readAntigravityIDEDB(path string) (string, string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return "", "", err
	}
	defer db.Close()

	var raw any
	if err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", antigravityIDEStateKey).Scan(&raw); err != nil {
		return "", "", err
	}
	var encoded string
	switch typed := raw.(type) {
	case []byte:
		encoded = string(typed)
	case string:
		encoded = typed
	default:
		return "", "", errors.New("unexpected ItemTable value type")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", err
	}

	accessToken := ""
	refreshToken := ""
	for _, candidate := range antigravityTokenCandidateRE.FindAllString(string(blob), -1) {
		padded := candidate + strings.Repeat("=", (4-len(candidate)%4)%4)
		// Python's urlsafe_b64decode: translate the URL alphabet, then decode.
		translated := strings.NewReplacer("-", "+", "_", "/").Replace(padded)
		inner, err := base64.StdEncoding.DecodeString(translated)
		if err != nil {
			continue
		}
		if accessToken == "" {
			if match := antigravityAccessTokenRE.Find(inner); match != nil {
				accessToken = string(match)
			}
		}
		if refreshToken == "" {
			if match := antigravityRefreshTokenRE.Find(inner); match != nil {
				refreshToken = string(match)
			}
		}
		if accessToken != "" && refreshToken != "" {
			break
		}
	}
	if accessToken == "" {
		return "", "", errors.New("no access token in IDE state DB")
	}
	return accessToken, refreshToken, nil
}

// ensureToken returns a valid access token, refreshing through OAuth when the
// cached one is missing or near expiry. The stale-access fallback mirrors
// upstream: a failed refresh still sends the old token, which the gateway may
// accept until the IDE rewrites the file.
//
// Parity: AntigravityProvider._ensure_token
func (p *AntigravityProvider) ensureToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.accessToken != "" && !p.expiresAt.IsZero() && p.nowTime().Before(p.expiresAt.Add(-antigravityTokenRefreshBuffer)) {
		return p.accessToken, nil
	}
	if p.refreshToken != "" {
		access, expiresAt, err := p.refreshAccessTokenLocked(ctx)
		if err != nil {
			return "", err
		}
		if access != "" {
			p.accessToken = access
			p.expiresAt = expiresAt
			return access, nil
		}
	}
	if p.accessToken != "" {
		return p.accessToken, nil
	}
	return "", errors.New(antigravityCredentialError)
}

// refreshForRetry refreshes after a 401/403. A missing refresh token is a
// no-op; the caller keeps its last error.
func (p *AntigravityProvider) refreshForRetry(ctx context.Context) (string, time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshToken == "" {
		return "", time.Time{}, nil
	}
	access, expiresAt, err := p.refreshAccessTokenLocked(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	if access != "" {
		p.accessToken = access
		p.expiresAt = expiresAt
	}
	return access, expiresAt, nil
}

func (p *AntigravityProvider) currentRefreshToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refreshToken
}

// refreshAccessTokenLocked performs the refresh_token grant. HTTP or payload
// failures are soft (upstream returns None and the caller falls back); a
// missing client id is loud because upstream's get_antigravity_client_id raises
// before its try/except. The caller holds p.mu.
//
// Parity: core/framework/llm/antigravity.py::_do_token_refresh
func (p *AntigravityProvider) refreshAccessTokenLocked(ctx context.Context) (string, time.Time, error) {
	clientID := ""
	if p.clientID != nil {
		clientID = strings.TrimSpace(p.clientID())
	}
	if clientID == "" {
		return "", time.Time{}, errors.New("Could not obtain Antigravity OAuth client ID")
	}
	clientSecret := ""
	if p.clientSecret != nil {
		clientSecret = p.clientSecret()
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.refreshToken)
	form.Set("client_id", clientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, nil
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := p.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", time.Time{}, ctxErr
		}
		return "", time.Time{}, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", time.Time{}, nil
	}
	var payload struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.AccessToken == "" {
		return "", time.Time{}, nil
	}
	expiresIn := 3600
	if len(payload.ExpiresIn) > 0 {
		if seconds, err := strconv.Atoi(strings.TrimSpace(string(payload.ExpiresIn))); err == nil {
			expiresIn = seconds
		}
	}
	return payload.AccessToken, p.nowTime().Add(time.Duration(expiresIn) * time.Second), nil
}

// snapshotThoughtSigs returns a copy of the tool_use id → thoughtSignature map
// so request building never races a streaming parse.
func (p *AntigravityProvider) snapshotThoughtSigs() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.thoughtSigs) == 0 {
		return nil
	}
	copied := make(map[string]string, len(p.thoughtSigs))
	for key, value := range p.thoughtSigs {
		copied[key] = value
	}
	return copied
}

func (p *AntigravityProvider) rememberThoughtSignature(toolUseID, signature string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.thoughtSigs[toolUseID] = signature
}

func antigravityString(value any) string {
	text, _ := value.(string)
	return text
}

func antigravityFloat(value any) (float64, bool) {
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
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}
