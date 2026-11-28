package nativehttp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CookieConfig is the per-session cookie jar policy.
type CookieConfig struct {
	Enabled        bool
	HostOnly       bool
	AllowedDomains []string
	MaxCookies     int
	MaxValueBytes  int
}

// Config is the merged http_tools.json policy. It is read from $HIVE_HOME only;
// the session directory is agent-writable and is never a config source.
type Config struct {
	Enabled           bool
	AllowPrivateHosts bool
	AllowedHosts      []string
	DeniedHosts       []string

	MaxInlineBytes   int64
	MaxResponseBytes int64
	MaxDownloadBytes int64
	MaxUploadBytes   int64

	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	ConnectTimeout time.Duration

	FollowRedirects  bool
	MaxRedirects     int
	AllowHTTPSToHTTP bool

	UserAgent       string
	Proxy           string
	RedactQueryKeys []string
	Cookies         CookieConfig

	// LoadError is non-nil when a present config file could not be read or
	// parsed. The family is disabled in that case (fail closed).
	LoadError error
}

// DefaultConfig returns the built-in policy.
func DefaultConfig() Config {
	return Config{
		Enabled:           true,
		AllowPrivateHosts: false,
		DeniedHosts: []string{
			"localhost", "*.localhost", "*.internal", "*.local",
			"metadata.google.internal",
		},
		MaxInlineBytes:   64 << 10,
		MaxResponseBytes: 16 << 20,
		MaxDownloadBytes: 1 << 30,
		MaxUploadBytes:   1 << 30,
		DefaultTimeout:   30 * time.Second,
		MaxTimeout:       30 * time.Minute,
		ConnectTimeout:   10 * time.Second,
		FollowRedirects:  true,
		MaxRedirects:     10,
		UserAgent:        "hive-go/1.0 (+https://github.com/shark2202/go_hive)",
		RedactQueryKeys: []string{
			"token", "access_token", "refresh_token", "api_key", "key",
			"secret", "password", "sig", "signature",
		},
		Cookies: CookieConfig{
			Enabled:       true,
			HostOnly:      true,
			MaxCookies:    200,
			MaxValueBytes: 4096,
		},
	}
}

// ConfigPath returns the only on-disk config location.
func ConfigPath(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		home = os.Getenv("HIVE_HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "http_tools.json")
}

type rawCookieConfig struct {
	Enabled        *bool    `json:"enabled"`
	HostOnly       *bool    `json:"host_only"`
	AllowedDomains []string `json:"allowed_domains"`
	MaxCookies     *int     `json:"max_cookies"`
	MaxValueBytes  *int     `json:"max_value_bytes"`
}

type rawConfig struct {
	Enabled           *bool            `json:"enabled"`
	AllowPrivateHosts *bool            `json:"allow_private_hosts"`
	AllowedHosts      []string         `json:"allowed_hosts"`
	DeniedHosts       []string         `json:"denied_hosts"`
	MaxInlineBytes    *int64           `json:"max_inline_bytes"`
	MaxResponseBytes  *int64           `json:"max_response_bytes"`
	MaxDownloadBytes  *int64           `json:"max_download_bytes"`
	MaxUploadBytes    *int64           `json:"max_upload_bytes"`
	DefaultTimeout    *int             `json:"default_timeout_seconds"`
	MaxTimeout        *int             `json:"max_timeout_seconds"`
	ConnectTimeout    *int             `json:"connect_timeout_seconds"`
	FollowRedirects   *bool            `json:"follow_redirects"`
	MaxRedirects      *int             `json:"max_redirects"`
	AllowHTTPSToHTTP  *bool            `json:"allow_https_to_http"`
	UserAgent         *string          `json:"user_agent"`
	Proxy             *string          `json:"proxy"`
	RedactQueryKeys   []string         `json:"redact_query_keys"`
	Cookies           *rawCookieConfig `json:"cookies"`
}

// LoadConfig reads $HIVE_HOME/http_tools.json. A missing file keeps the
// defaults (enabled); a malformed or unreadable file disables the family
// (fail closed). HIVE_HTTP_TOOLS=0 is a hard kill switch.
func LoadConfig(home string) Config {
	cfg := DefaultConfig()
	path := ConfigPath(home)
	if path == "" {
		return applyEnvSwitch(cfg)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed basename under the operator-owned $HIVE_HOME, never an agent-supplied path
	if err != nil {
		if os.IsNotExist(err) {
			return applyEnvSwitch(cfg)
		}
		cfg.Enabled = false
		cfg.LoadError = err
		return applyEnvSwitch(cfg)
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		cfg.Enabled = false
		cfg.LoadError = err
		return applyEnvSwitch(cfg)
	}
	applyRaw(&cfg, raw)
	return applyEnvSwitch(cfg)
}

func applyEnvSwitch(cfg Config) Config {
	switch strings.TrimSpace(os.Getenv("HIVE_HTTP_TOOLS")) {
	case "0":
		cfg.Enabled = false
	case "1":
		// =1 enables only when the config did not disable it.
	}
	return cfg
}

func applyRaw(cfg *Config, raw rawConfig) {
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	if raw.AllowPrivateHosts != nil {
		cfg.AllowPrivateHosts = *raw.AllowPrivateHosts
	}
	if raw.AllowedHosts != nil {
		cfg.AllowedHosts = append([]string(nil), raw.AllowedHosts...)
	}
	if raw.DeniedHosts != nil {
		cfg.DeniedHosts = append([]string(nil), raw.DeniedHosts...)
	}
	if raw.MaxInlineBytes != nil {
		cfg.MaxInlineBytes = *raw.MaxInlineBytes
	}
	if raw.MaxResponseBytes != nil {
		cfg.MaxResponseBytes = *raw.MaxResponseBytes
	}
	if raw.MaxDownloadBytes != nil {
		cfg.MaxDownloadBytes = *raw.MaxDownloadBytes
	}
	if raw.MaxUploadBytes != nil {
		cfg.MaxUploadBytes = *raw.MaxUploadBytes
	}
	if raw.DefaultTimeout != nil {
		cfg.DefaultTimeout = time.Duration(*raw.DefaultTimeout) * time.Second
	}
	if raw.MaxTimeout != nil {
		cfg.MaxTimeout = time.Duration(*raw.MaxTimeout) * time.Second
	}
	if raw.ConnectTimeout != nil {
		cfg.ConnectTimeout = time.Duration(*raw.ConnectTimeout) * time.Second
	}
	if raw.FollowRedirects != nil {
		cfg.FollowRedirects = *raw.FollowRedirects
	}
	if raw.MaxRedirects != nil {
		cfg.MaxRedirects = *raw.MaxRedirects
	}
	if raw.AllowHTTPSToHTTP != nil {
		cfg.AllowHTTPSToHTTP = *raw.AllowHTTPSToHTTP
	}
	if raw.UserAgent != nil {
		cfg.UserAgent = *raw.UserAgent
	}
	if raw.Proxy != nil {
		cfg.Proxy = *raw.Proxy
	}
	if raw.RedactQueryKeys != nil {
		cfg.RedactQueryKeys = append([]string(nil), raw.RedactQueryKeys...)
	}
	if raw.Cookies != nil {
		if raw.Cookies.Enabled != nil {
			cfg.Cookies.Enabled = *raw.Cookies.Enabled
		}
		if raw.Cookies.HostOnly != nil {
			cfg.Cookies.HostOnly = *raw.Cookies.HostOnly
		}
		if raw.Cookies.AllowedDomains != nil {
			cfg.Cookies.AllowedDomains = append([]string(nil), raw.Cookies.AllowedDomains...)
		}
		if raw.Cookies.MaxCookies != nil {
			cfg.Cookies.MaxCookies = *raw.Cookies.MaxCookies
		}
		if raw.Cookies.MaxValueBytes != nil {
			cfg.Cookies.MaxValueBytes = *raw.Cookies.MaxValueBytes
		}
	}
	clamp(cfg)
}

func clamp(cfg *Config) {
	if cfg.MaxInlineBytes < 0 {
		cfg.MaxInlineBytes = 0
	}
	if cfg.MaxResponseBytes < 1 {
		cfg.MaxResponseBytes = 1
	}
	if cfg.MaxInlineBytes > cfg.MaxResponseBytes {
		cfg.MaxInlineBytes = cfg.MaxResponseBytes
	}
	if cfg.MaxDownloadBytes < 1 {
		cfg.MaxDownloadBytes = 1
	}
	if cfg.MaxUploadBytes < 1 {
		cfg.MaxUploadBytes = 1
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = DefaultConfig().MaxTimeout
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultConfig().DefaultTimeout
	}
	if cfg.DefaultTimeout > cfg.MaxTimeout {
		cfg.DefaultTimeout = cfg.MaxTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConfig().ConnectTimeout
	}
	if cfg.ConnectTimeout > 60*time.Second {
		cfg.ConnectTimeout = 60 * time.Second
	}
	if cfg.MaxRedirects < 0 {
		cfg.MaxRedirects = 0
	}
	if cfg.MaxRedirects > 20 {
		cfg.MaxRedirects = 20
	}
	if cfg.Cookies.MaxCookies < 0 {
		cfg.Cookies.MaxCookies = 0
	}
	if cfg.Cookies.MaxValueBytes < 0 {
		cfg.Cookies.MaxValueBytes = 0
	}
}
