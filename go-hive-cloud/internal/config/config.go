// Package config loads go-hive-cloud server configuration from defaults, an
// optional config.json (0600), environment variables and flags.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultLLMUpstreamAuth is the auth style used by api.anthropic.com.
	DefaultLLMUpstreamAuth = "x-api-key"
	// DefaultAnthropicVersion is the Messages API version sent upstream when
	// the client omits anthropic-version.
	DefaultAnthropicVersion = "2023-06-01"
)

// Defaults are the built-in values.
func Defaults() Config {
	return Config{
		Listen:               "127.0.0.1:8788",
		DataDir:              "./data",
		StatementTimeout:     5 * time.Second,
		RowCapDefault:        1000,
		InboxRateWindow:      time.Minute,
		LLMUpstreamAuth:      DefaultLLMUpstreamAuth,
		LLMAnthropicVersion:  DefaultAnthropicVersion,
		LLMRequestTimeout:    10 * time.Minute,
		ImagesRequestTimeout: 225 * time.Second,
	}
}

// Config is the resolved server configuration.
type Config struct {
	Listen       string
	DataDir      string
	JWTSecret    []byte
	StreamSecret []byte
	// StreamSecretPrevious is the optional previous streamToken signing
	// secret. When set, tokens signed with it still verify (rotation window)
	// while new tokens are signed with StreamSecret.
	StreamSecretPrevious []byte
	StatementTimeout     time.Duration
	RowCapDefault        int
	InboxRateLimit       int
	InboxRateWindow      time.Duration

	// LLMUpstreamURL is the explicit Anthropic-compatible upstream base (or
	// full /v1/messages endpoint). Empty disables pass-through: /v1/messages
	// answers 400 with llm_upstream_not_configured and never fakes a 200.
	LLMUpstreamURL string
	// LLMUpstreamKey is the credential sent to the Messages upstream.
	LLMUpstreamKey string
	// LLMUpstreamAuth selects the upstream auth header: "x-api-key"
	// (default, api.anthropic.com) or "bearer" (Anthropic-compatible proxy).
	LLMUpstreamAuth string
	// LLMAnthropicVersion is sent when the client omits anthropic-version.
	LLMAnthropicVersion string
	// LLMRequestTimeout bounds one upstream Messages call, streaming included.
	LLMRequestTimeout time.Duration
	// ImagesUpstreamURL is the explicit full URL of an OpenAI-compatible
	// images endpoint. Empty makes /v1/images/generations answer 503.
	ImagesUpstreamURL string
	// ImagesUpstreamKey is the credential sent to the images upstream.
	ImagesUpstreamKey string
	// ImagesRequestTimeout bounds one upstream images call (default 225s).
	ImagesRequestTimeout time.Duration
}

// File is the on-disk config.json shape.
type File struct {
	Listen               string `json:"listen"`
	DataDir              string `json:"data_dir"`
	JWTSecret            string `json:"jwt_secret"`
	StreamSecret         string `json:"stream_secret"`
	StreamSecretPrevious string `json:"stream_secret_previous"`
	StatementTimeoutMS   int    `json:"statement_timeout_ms"`
	RowCapDefault        int    `json:"row_cap_default"`
	InboxRateLimit       int    `json:"inbox_rate_limit"`
	InboxRateWindowMS    int    `json:"inbox_rate_window_ms"`
	LLMUpstreamURL       string `json:"llm_upstream_url"`
	LLMUpstreamKey       string `json:"llm_upstream_key"`
	LLMUpstreamAuth      string `json:"llm_upstream_auth"`
	LLMAnthropicVersion  string `json:"llm_anthropic_version"`
	LLMRequestTimeoutMS  int    `json:"llm_request_timeout_ms"`
	ImagesUpstreamURL    string `json:"images_upstream_url"`
	ImagesUpstreamKey    string `json:"images_upstream_key"`
	ImagesTimeoutMS      int    `json:"images_request_timeout_ms"`
}

// ReadFile parses a config.json.
func ReadFile(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return f, nil
}

// Apply overlays non-zero fields of f onto c.
func (c Config) Apply(f File) Config {
	if f.Listen != "" {
		c.Listen = f.Listen
	}
	if f.DataDir != "" {
		c.DataDir = f.DataDir
	}
	if f.JWTSecret != "" {
		c.JWTSecret = []byte(f.JWTSecret)
	}
	if f.StreamSecret != "" {
		c.StreamSecret = []byte(f.StreamSecret)
	}
	if f.StreamSecretPrevious != "" {
		c.StreamSecretPrevious = []byte(f.StreamSecretPrevious)
	}
	if f.StatementTimeoutMS > 0 {
		c.StatementTimeout = time.Duration(f.StatementTimeoutMS) * time.Millisecond
	}
	if f.RowCapDefault > 0 {
		c.RowCapDefault = f.RowCapDefault
	}
	if f.InboxRateLimit > 0 {
		c.InboxRateLimit = f.InboxRateLimit
	}
	if f.InboxRateWindowMS > 0 {
		c.InboxRateWindow = time.Duration(f.InboxRateWindowMS) * time.Millisecond
	}
	if f.LLMUpstreamURL != "" {
		c.LLMUpstreamURL = f.LLMUpstreamURL
	}
	if f.LLMUpstreamKey != "" {
		c.LLMUpstreamKey = f.LLMUpstreamKey
	}
	if f.LLMUpstreamAuth != "" {
		c.LLMUpstreamAuth = f.LLMUpstreamAuth
	}
	if f.LLMAnthropicVersion != "" {
		c.LLMAnthropicVersion = f.LLMAnthropicVersion
	}
	if f.LLMRequestTimeoutMS > 0 {
		c.LLMRequestTimeout = time.Duration(f.LLMRequestTimeoutMS) * time.Millisecond
	}
	if f.ImagesUpstreamURL != "" {
		c.ImagesUpstreamURL = f.ImagesUpstreamURL
	}
	if f.ImagesUpstreamKey != "" {
		c.ImagesUpstreamKey = f.ImagesUpstreamKey
	}
	if f.ImagesTimeoutMS > 0 {
		c.ImagesRequestTimeout = time.Duration(f.ImagesTimeoutMS) * time.Millisecond
	}
	return c
}

// ApplyEnv overlays HIVE_CLOUD_* environment variables.
func (c Config) ApplyEnv() Config {
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LISTEN")); v != "" {
		c.Listen = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_DATA_DIR")); v != "" {
		c.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_JWT_SECRET")); v != "" {
		c.JWTSecret = []byte(v)
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_STREAM_SECRET")); v != "" {
		c.StreamSecret = []byte(v)
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_STREAM_SECRET_PREVIOUS")); v != "" {
		c.StreamSecretPrevious = []byte(v)
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_STATEMENT_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.StatementTimeout = time.Duration(n) * time.Millisecond
		}
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_INBOX_RATE_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.InboxRateLimit = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_INBOX_RATE_WINDOW_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.InboxRateWindow = time.Duration(n) * time.Millisecond
		}
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LLM_UPSTREAM_URL")); v != "" {
		c.LLMUpstreamURL = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LLM_UPSTREAM_KEY")); v != "" {
		c.LLMUpstreamKey = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LLM_UPSTREAM_AUTH")); v != "" {
		c.LLMUpstreamAuth = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LLM_ANTHROPIC_VERSION")); v != "" {
		c.LLMAnthropicVersion = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_LLM_REQUEST_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LLMRequestTimeout = time.Duration(n) * time.Millisecond
		}
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_IMAGES_UPSTREAM_URL")); v != "" {
		c.ImagesUpstreamURL = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_IMAGES_UPSTREAM_KEY")); v != "" {
		c.ImagesUpstreamKey = v
	}
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_IMAGES_REQUEST_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ImagesRequestTimeout = time.Duration(n) * time.Millisecond
		}
	}
	return c
}

// ResolveSecrets fills in missing secrets from <data_dir>/jwt_secret and
// <data_dir>/stream_secret, generating 0600 files on first use so `serve` and
// `token` share them without extra operator steps.
func ResolveSecrets(cfg Config) (Config, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return cfg, fmt.Errorf("config: create data dir: %w", err)
	}
	if len(cfg.JWTSecret) == 0 {
		secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, "jwt_secret"))
		if err != nil {
			return cfg, err
		}
		cfg.JWTSecret = secret
	}
	if len(cfg.StreamSecret) == 0 {
		secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, "stream_secret"))
		if err != nil {
			return cfg, err
		}
		cfg.StreamSecret = secret
	}
	// The previous secret is optional and is never generated: its presence is
	// exactly what keeps the rotation window open.
	if len(cfg.StreamSecretPrevious) == 0 {
		secret, err := loadSecretIfExists(filepath.Join(cfg.DataDir, "stream_secret_previous"))
		if err != nil {
			return cfg, err
		}
		cfg.StreamSecretPrevious = secret
	}
	return cfg, nil
}

// loadSecretIfExists reads a secret file, returning nil when the file is
// absent. Unlike loadOrCreateSecret it never creates a new key.
func loadSecretIfExists(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return nil, errors.New("config: empty secret file " + path)
	}
	return []byte(secret), nil
}

func loadOrCreateSecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		secret := strings.TrimSpace(string(raw))
		if secret == "" {
			return nil, errors.New("config: empty secret file " + path)
		}
		return []byte(secret), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("config: write %s: %w", path, err)
	}
	return []byte(secret), nil
}
