package browserbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// artifactDir resolves where browser_snapshot / browser_html spill their raw
// payloads. Mirrors tools/inspection.py `_resolve_browser_artifact_dir`:
// HIVE_STORAGE_PATH/data wins (the per-agent spillover directory), then
// HIVE_HOME/tool-artifacts, then ~/.hive/tool-artifacts. Config.ArtifactDir
// overrides all of it (used by the binary and by tests).
func (b *Bridge) artifactDir() string {
	if dir := b.cfg.ArtifactDir; dir != "" {
		return dir
	}
	if storage := os.Getenv("HIVE_STORAGE_PATH"); storage != "" {
		return filepath.Join(storage, "data")
	}
	home := hiveHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "tool-artifacts")
}

// writeArtifact persists one raw payload as text and returns its absolute path.
// Filename shape follows upstream: <tool>_<unix_ms>_tab<id><ext>.
func (b *Bridge) writeArtifact(toolName string, tabID int, raw, ext string) (string, error) {
	dir := b.artifactDir()
	if dir == "" {
		return "", fmt.Errorf("no artifact directory is configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%d_tab%d%s", toolName, time.Now().UnixMilli(), tabID, ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
