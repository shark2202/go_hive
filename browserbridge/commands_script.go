package browserbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commands_script.go ports tools/script.py browser_script.
//
// Divergence from upstream (documented in PARITY.md): upstream imports the
// skill's Python module in-process and hands it a BrowserScriptContext with a
// live bridge object. Go cannot host Python in-process, so the script runs as a
// child process of Config.ScriptCommand (default: `uv run python`), receives a
// JSON envelope on stdin, and talks back to this bridge over the local HTTP API
// (HIVE_BRIDGE_HTTP). Resolution, path-escape validation and result shaping
// follow upstream.

const (
	scriptTimeoutDefault = 60.0
	scriptTimeoutMax     = 300.0
)

// cmdBrowserScript runs a skill-bundled script against this profile's tab group.
func cmdBrowserScript(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	skill, _ := paramString(params, "skill")
	if strings.TrimSpace(skill) == "" {
		return nil, badRequest("missing required parameter 'skill'")
	}
	script, _ := paramString(params, "script")
	if strings.TrimSpace(script) == "" {
		return nil, badRequest("missing required parameter 'script'")
	}
	args, _ := params["args"].(map[string]any)
	timeoutS := paramFloat(params, "timeout_s", scriptTimeoutDefault)
	if timeoutS <= 0 {
		timeoutS = scriptTimeoutDefault
	}
	if timeoutS > scriptTimeoutMax {
		timeoutS = scriptTimeoutMax
	}

	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	profileName := strings.TrimSpace(profile)
	if profileName == "" {
		profileName = "default"
	}
	groupID := 0
	if meta, ok := b.contexts.Get(profileName); ok {
		groupID = meta.GroupID
	}

	skillDir, err := b.resolveSkillDir(skill)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error(), "phase": "resolve"}, nil
	}
	scriptPath, err := resolveScriptPath(skillDir, script)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error(), "phase": "resolve"}, nil
	}

	command := b.cfg.ScriptCommand
	if len(command) == 0 {
		command = []string{"uv", "run", "python"}
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS*float64(time.Second)))
	defer cancel()

	payload := map[string]any{
		"args":        args,
		"profile":     profileName,
		"tab_id":      tabID,
		"group_id":    nullIfZeroInt(groupID),
		"skill_dir":   skillDir,
		"script":      filepath.Base(scriptPath),
		"bridge_http": "http://" + b.HTTPAddr(),
	}
	stdin, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"ok": false, "error": "could not encode script args: " + err.Error(), "phase": "run"}, nil
	}
	cmd := exec.CommandContext(runCtx, command[0], append(append([]string{}, command[1:]...), scriptPath)...)
	cmd.Dir = skillDir
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"HIVE_BRIDGE_HTTP=http://"+b.HTTPAddr(),
		"HIVE_BRIDGE_WS=ws://"+b.WSAddr()+"/bridge",
		"HIVE_BRIDGE_PROFILE="+profileName,
		fmt.Sprintf("HIVE_BRIDGE_TAB_ID=%d", tabID),
		fmt.Sprintf("HIVE_BRIDGE_GROUP_ID=%d", groupID),
		"HIVE_BRIDGE_SKILL_DIR="+skillDir,
	)

	started := time.Now()
	runErr := cmd.Run()
	durationMS := time.Since(started).Milliseconds()
	if runCtx.Err() == context.DeadlineExceeded {
		return map[string]any{
			"ok": false, "error": fmt.Sprintf("script exceeded timeout (%.0fs)", timeoutS),
			"timeout": true, "duration_ms": durationMS, "phase": "run",
			"stderr": tailString(stderr.String(), 2000),
		}, nil
	}
	if runErr != nil {
		out := map[string]any{
			"ok": false, "error": runErr.Error(), "duration_ms": durationMS, "phase": "run",
			"stderr": tailString(stderr.String(), 4000),
		}
		if ctx.Err() != nil {
			out["error"] = "script cancelled"
		}
		return out, nil
	}

	var result any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		return map[string]any{
			"ok": false, "error": "script produced non-JSON output",
			"stdout": tailString(stdout.String(), 4000), "stderr": tailString(stderr.String(), 2000),
			"duration_ms": durationMS, "phase": "run",
		}, nil
	}
	if resultMap, ok := result.(map[string]any); ok {
		return map[string]any{"ok": true, "result": resultMap, "duration_ms": durationMS}, nil
	}
	return map[string]any{
		"ok": true, "result": result, "duration_ms": durationMS,
		"warning": fmt.Sprintf("script returned %T, not a JSON object", result),
	}, nil
}

// resolveSkillDir finds a skill by its SKILL.md `name:` frontmatter or by its
// directory name under Config.SkillDirs (defaults: $HIVE_HOME/skills,
// ./skills, ./.hive/skills).
func (b *Bridge) resolveSkillDir(skill string) (string, error) {
	roots := b.cfg.SkillDirs
	if len(roots) == 0 {
		if home := hiveHomeDir(); home != "" {
			roots = append(roots, filepath.Join(home, "skills"))
		}
		roots = append(roots, "skills", filepath.Join(".hive", "skills"))
	}
	var searched []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		searched = append(searched, root)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if entry.Name() == skill || skillFrontmatterName(dir) == skill {
				return dir, nil
			}
		}
	}
	return "", fmt.Errorf("skill '%s' not found in any discoverable scope (searched: %s)", skill, strings.Join(searched, ", "))
}

// skillFrontmatterName reads the `name:` field of <dir>/SKILL.md, if present.
func skillFrontmatterName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), `"'`)
		}
	}
	return ""
}

// resolveScriptPath applies upstream's path-escape rules: no separators, no
// "..", no leading dot, `.py` suffix, and the resolved file must still live
// inside <skill_dir>/scripts/.
func resolveScriptPath(skillDir, scriptName string) (string, error) {
	if strings.TrimSpace(scriptName) == "" {
		return "", fmt.Errorf("script name is empty")
	}
	if !strings.HasSuffix(scriptName, ".py") {
		scriptName += ".py"
	}
	for _, bad := range []string{"/", "\\", ".."} {
		if strings.Contains(scriptName, bad) {
			return "", fmt.Errorf("script '%s' must not contain %q", scriptName, bad)
		}
	}
	if strings.HasPrefix(scriptName, ".") {
		return "", fmt.Errorf("script '%s' must not start with '.'", scriptName)
	}
	scriptsRoot, err := filepath.Abs(filepath.Join(skillDir, "scripts"))
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(scriptsRoot); err != nil || !info.IsDir() {
		return "", fmt.Errorf("skill has no scripts/ directory at %s", scriptsRoot)
	}
	candidate, err := filepath.Abs(filepath.Join(scriptsRoot, scriptName))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(scriptsRoot, candidate)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("script '%s' resolved outside scripts/", scriptName)
	}
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("script '%s' not found at %s", scriptName, candidate)
	}
	return candidate, nil
}

func tailString(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

func nullIfZeroInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
