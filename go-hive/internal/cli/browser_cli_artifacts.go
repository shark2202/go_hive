package cli

// browser_cli_artifacts.go is the CLI-side spill layer: it turns the bridge's
// base64 image payloads and inline snapshot trees into on-disk artifacts and
// the upstream pointer contract — `saved_to` plus the `_image` marker the
// framework re-inlines into the agent's session.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::_resolve_browser_artifact_dir
// (HIVE_STORAGE_PATH/data → HIVE_HOME/tool-artifacts → ~/.hive/tool-artifacts)
// and ::_write_browser_artifact_bytes / ::_write_browser_artifact.

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// browserScreenshotScaleHint is the metadata note upstream attaches to every
// CLI screenshot result.
const browserScreenshotScaleHint = "Coordinates for click / hover / press are " +
	"fractions 0..1 of the viewport. Read a target's proportional position off this " +
	"image (e.g. '~35 % from the left, ~20 % from the top' → (0.35, 0.20)) and pass " +
	"that as the coordinate to browser interact. shadow-query / focused_element.rect " +
	"return fractions too."

// browserCLIArtifactDir mirrors the Python resolver used by the CLI process:
// HIVE_STORAGE_PATH/data wins, then HIVE_HOME/tool-artifacts, then
// ~/.hive/tool-artifacts.
func browserCLIArtifactDir(env func(string) string) string {
	if storage := strings.TrimSpace(env("HIVE_STORAGE_PATH")); storage != "" {
		return filepath.Join(storage, "data")
	}
	if home := strings.TrimSpace(env("HIVE_HOME")); home != "" {
		return filepath.Join(home, "tool-artifacts")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".hive", "tool-artifacts")
	}
	return ""
}

// writeBrowserCLIArtifact persists one payload and returns its absolute path.
// Filename shape follows upstream: <tool>_<unix_ms>_tab<id><ext>.
func writeBrowserCLIArtifact(env func(string) string, toolName string, tabID int, raw []byte, ext string) (string, error) {
	dir := browserCLIArtifactDir(env)
	if dir == "" {
		return "", fmt.Errorf("no artifact directory is configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%d_tab%d%s", toolName, time.Now().UnixMilli(), tabID, ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func cliRound4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// spillImageResult replaces an inline base64 image with the upstream CLI
// contract: the JPEG written to <artifactDir>/<tool>_<ms>_tab<id>.jpg, plus
// `saved_to`, `size`, `imageType`, `crop_box`, `scaleHint` and an `_image`
// marker. Mirrors render_screenshot(spill=True) / _blocks_to_cli_dict.
func (c *browserCLI) spillImageResult(result map[string]any, toolName string, intent string, hasIntent bool) {
	data := stringOf(result["data"])
	if data == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return
	}
	tabID := intOf(result["tabId"])
	path, err := writeBrowserCLIArtifact(c.env, toolName, tabID, raw, ".jpg")
	if err != nil {
		result["ok"] = false
		result["error"] = "Failed to write screenshot artifact: " + err.Error()
		delete(result, "data")
		return
	}
	// crop_box maps the image back to viewport fractions when the capture was
	// a clip (selector / full page). Computed before `clip` is dropped.
	if _, present := result["crop_box"]; !present {
		result["crop_box"] = cropBoxFromClip(result)
	}
	delete(result, "data")
	delete(result, "mimeType")
	delete(result, "format")
	delete(result, "clip")
	delete(result, "imageHeight")
	result["imageType"] = "jpeg"
	result["size"] = len(raw)
	if result["fullPage"] == nil {
		result["fullPage"] = false
	}
	result["scaleHint"] = browserScreenshotScaleHint
	result["saved_to"] = path
	image := map[string]any{"path": path, "mime": "image/jpeg"}
	if hasIntent {
		image["intent"] = intent
	} else {
		image["intent"] = nil
	}
	result["_image"] = image
}

// cropBoxFromClip renders the viewport-fraction rectangle the image spans, or
// nil when the capture covered the whole viewport.
func cropBoxFromClip(result map[string]any) any {
	clip, ok := result["clip"].(map[string]any)
	if !ok {
		return nil
	}
	cssWidth := floatOf(result["cssWidth"])
	cssHeight := floatOf(result["cssHeight"])
	if cssWidth <= 0 || cssHeight <= 0 {
		return nil
	}
	x, y := floatOf(clip["x"]), floatOf(clip["y"])
	w, h := floatOf(clip["width"]), floatOf(clip["height"])
	// A full-viewport capture gets crop_box=null upstream; the bridge adds a
	// full-viewport clip only as the vehicle for the 800px scale.
	if x == 0 && y == 0 && w == cssWidth && h == cssHeight {
		return nil
	}
	return []any{
		cliRound4(x / cssWidth),
		cliRound4(y / cssHeight),
		cliRound4((x + w) / cssWidth),
		cliRound4((y + h) / cssHeight),
	}
}

// spillSnapshotResult replaces the inline AX tree with the upstream pointer
// contract: the raw tree text on disk, `length` (rune count, like Python's
// len(str)) and `saved_to`. A failed write becomes an ok:false result, which
// the caller re-classifies — same as upstream's OSError branch.
func (c *browserCLI) spillSnapshotResult(result map[string]any) {
	tree, ok := result["tree"].(string)
	if !ok {
		return
	}
	tabID := intOf(result["tabId"])
	path, err := writeBrowserCLIArtifact(c.env, "browser_snapshot", tabID, []byte(tree), ".txt")
	if err != nil {
		delete(result, "tree")
		delete(result, "nodes")
		result["ok"] = false
		result["error"] = "Failed to write snapshot artifact: " + err.Error()
		return
	}
	delete(result, "tree")
	delete(result, "nodes")
	result["length"] = utf8.RuneCountInString(tree)
	result["saved_to"] = path
}

// snapshotResultPost runs the snapshot spill and then the Go-only --head
// echo, in that order (head reads the artifact written by the spill).
func (c *browserCLI) snapshotResultPost(args *parsedArgs, result map[string]any) {
	c.spillSnapshotResult(result)
	c.spillHead(args, result)
}

func floatOf(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}
