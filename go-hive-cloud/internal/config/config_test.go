package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStreamSecretPreviousEnv pins the M3 rotation-window configuration:
// the previous secret is accepted from env/config and is never generated.
func TestStreamSecretPreviousEnv(t *testing.T) {
	t.Setenv("HIVE_CLOUD_STREAM_SECRET", "current-secret")
	t.Setenv("HIVE_CLOUD_STREAM_SECRET_PREVIOUS", "previous-secret")
	cfg := Defaults().ApplyEnv()
	if string(cfg.StreamSecret) != "current-secret" {
		t.Fatalf("StreamSecret = %q", cfg.StreamSecret)
	}
	if string(cfg.StreamSecretPrevious) != "previous-secret" {
		t.Fatalf("StreamSecretPrevious = %q", cfg.StreamSecretPrevious)
	}

	file := File{StreamSecretPrevious: "file-previous"}
	if got := string((Config{}).Apply(file).StreamSecretPrevious); got != "file-previous" {
		t.Fatalf("Apply previous = %q", got)
	}
}

func TestResolveSecretsPreviousFileOptional(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ResolveSecrets(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StreamSecret) == 0 {
		t.Fatal("current stream secret was not generated")
	}
	if len(cfg.StreamSecretPrevious) != 0 {
		t.Fatalf("previous secret must stay empty when no file exists: %q", cfg.StreamSecretPrevious)
	}

	if err := os.WriteFile(filepath.Join(dir, "stream_secret_previous"), []byte("old-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = ResolveSecrets(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.StreamSecretPrevious) != "old-key" {
		t.Fatalf("previous secret = %q", cfg.StreamSecretPrevious)
	}
}

// TestLLMUpstreamConfig pins the M5 explicit-upstream configuration: defaults
// are conservative, env/config overlay every field, and no upstream key is
// generated or implied.
func TestLLMUpstreamConfig(t *testing.T) {
	defaults := Defaults()
	if defaults.LLMUpstreamURL != "" || defaults.ImagesUpstreamURL != "" {
		t.Fatalf("upstream defaults must be empty: %+v", defaults)
	}
	if defaults.LLMUpstreamAuth != DefaultLLMUpstreamAuth || defaults.LLMAnthropicVersion != DefaultAnthropicVersion {
		t.Fatalf("llm defaults = %+v", defaults)
	}
	if defaults.LLMRequestTimeout != 10*time.Minute || defaults.ImagesRequestTimeout != 225*time.Second {
		t.Fatalf("timeout defaults = %v / %v", defaults.LLMRequestTimeout, defaults.ImagesRequestTimeout)
	}

	t.Setenv("HIVE_CLOUD_LLM_UPSTREAM_URL", "http://127.0.0.1:9000")
	t.Setenv("HIVE_CLOUD_LLM_UPSTREAM_KEY", "env-messages-key")
	t.Setenv("HIVE_CLOUD_LLM_UPSTREAM_AUTH", "bearer")
	t.Setenv("HIVE_CLOUD_LLM_ANTHROPIC_VERSION", "2023-06-01")
	t.Setenv("HIVE_CLOUD_LLM_REQUEST_TIMEOUT_MS", "1234")
	t.Setenv("HIVE_CLOUD_IMAGES_UPSTREAM_URL", "http://127.0.0.1:9001/v1/images/generations")
	t.Setenv("HIVE_CLOUD_IMAGES_UPSTREAM_KEY", "env-images-key")
	t.Setenv("HIVE_CLOUD_IMAGES_REQUEST_TIMEOUT_MS", "5678")
	cfg := Defaults().ApplyEnv()
	if cfg.LLMUpstreamURL != "http://127.0.0.1:9000" || cfg.LLMUpstreamKey != "env-messages-key" ||
		cfg.LLMUpstreamAuth != "bearer" || cfg.LLMAnthropicVersion != "2023-06-01" ||
		cfg.LLMRequestTimeout != 1234*time.Millisecond {
		t.Fatalf("messages env overlay = %+v", cfg)
	}
	if cfg.ImagesUpstreamURL != "http://127.0.0.1:9001/v1/images/generations" ||
		cfg.ImagesUpstreamKey != "env-images-key" || cfg.ImagesRequestTimeout != 5678*time.Millisecond {
		t.Fatalf("images env overlay = %+v", cfg)
	}

	file := File{
		LLMUpstreamURL:      "https://api.anthropic.com",
		LLMUpstreamKey:      "file-key",
		LLMUpstreamAuth:     "x-api-key",
		LLMAnthropicVersion: "2023-06-01",
		LLMRequestTimeoutMS: 9000,
		ImagesUpstreamURL:   "https://images.example/v1/images/generations",
		ImagesUpstreamKey:   "file-images-key",
		ImagesTimeoutMS:     1000,
	}
	applied := (Config{}).Apply(file)
	if applied.LLMUpstreamURL != file.LLMUpstreamURL || applied.LLMUpstreamKey != file.LLMUpstreamKey ||
		applied.LLMUpstreamAuth != file.LLMUpstreamAuth || applied.LLMRequestTimeout != 9*time.Second {
		t.Fatalf("messages file overlay = %+v", applied)
	}
	if applied.ImagesUpstreamURL != file.ImagesUpstreamURL || applied.ImagesUpstreamKey != file.ImagesUpstreamKey ||
		applied.ImagesRequestTimeout != time.Second {
		t.Fatalf("images file overlay = %+v", applied)
	}
}
