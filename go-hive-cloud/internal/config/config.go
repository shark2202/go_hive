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

// Defaults are the built-in values.
func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:8788",
		DataDir:          "./data",
		StatementTimeout: 5 * time.Second,
		RowCapDefault:    1000,
	}
}

// Config is the resolved server configuration.
type Config struct {
	Listen           string
	DataDir          string
	JWTSecret        []byte
	StreamSecret     []byte
	StatementTimeout time.Duration
	RowCapDefault    int
}

// File is the on-disk config.json shape.
type File struct {
	Listen             string `json:"listen"`
	DataDir            string `json:"data_dir"`
	JWTSecret          string `json:"jwt_secret"`
	StreamSecret       string `json:"stream_secret"`
	StatementTimeoutMS int    `json:"statement_timeout_ms"`
	RowCapDefault      int    `json:"row_cap_default"`
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
	if f.StatementTimeoutMS > 0 {
		c.StatementTimeout = time.Duration(f.StatementTimeoutMS) * time.Millisecond
	}
	if f.RowCapDefault > 0 {
		c.RowCapDefault = f.RowCapDefault
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
	if v := strings.TrimSpace(os.Getenv("HIVE_CLOUD_STATEMENT_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.StatementTimeout = time.Duration(n) * time.Millisecond
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
	return cfg, nil
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
