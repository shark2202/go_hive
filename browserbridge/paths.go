package browserbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// paths.go resolves the upstream on-disk locations. HIVE_HOME wins, matching
// the rest of Hive; otherwise ~/.hive.
func hiveHomeDir() string {
	if home := os.Getenv("HIVE_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".hive")
}

func defaultLabelPath() string {
	home := hiveHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "browser_default.json")
}

func defaultRateLimitPath() string {
	home := hiveHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "social_rate_limits.json")
}

func defaultConfigPath() string {
	home := hiveHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "configuration.json")
}

func loadStarredDefault(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var payload struct {
		Label string `json:"label"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	return payload.Label
}

// SetStarredDefault persists the side-panel star and updates routing. Errors
// are swallowed: default-routing persistence must never break the bridge.
func (b *Bridge) SetStarredDefault(label, extensionID string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	b.mu.Lock()
	b.starredDefault = label
	b.mu.Unlock()
	if b.cfg.DefaultLabelFile == "" {
		return
	}
	payload := map[string]any{
		"label":       label,
		"extensionId": extensionID,
		"saved_at_ms": time.Now().UnixMilli(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.cfg.DefaultLabelFile), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(b.cfg.DefaultLabelFile, data, 0o600)
}

// StarredDefault returns the persisted starred label (may be disconnected).
func (b *Bridge) StarredDefault() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.starredDefault
}
