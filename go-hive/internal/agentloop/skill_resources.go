package agentloop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ToolResult struct {
	Content        string
	IsError        bool
	IsSkillContent bool
}

type SkillFileReadInterceptor struct {
	SkillDirs []string
}

func NewSkillFileReadInterceptor(dirs []string) *SkillFileReadInterceptor {
	normalized := make([]string, len(dirs))
	copy(normalized, dirs)
	return &SkillFileReadInterceptor{SkillDirs: normalized}
}

func (i *SkillFileReadInterceptor) Intercept(toolName string, toolInput map[string]any) (ToolResult, bool) {
	return InterceptSkillFileRead(toolName, toolInput, i.SkillDirs)
}

func InterceptSkillFileRead(toolName string, toolInput map[string]any, skillDirs []string) (ToolResult, bool) {
	if toolName != "view_file" || len(skillDirs) == 0 {
		return ToolResult{}, false
	}
	rawPath, _ := toolInput["path"].(string)
	if rawPath == "" {
		return ToolResult{}, false
	}

	resolved := resolveSkillPath(rawPath)
	roots := make([]string, 0, len(skillDirs))
	for _, skillDir := range skillDirs {
		roots = append(roots, resolveSkillPath(skillDir))
	}

	inside := false
	for _, root := range roots {
		if pathIsRelativeTo(resolved, root) {
			inside = true
			break
		}
	}
	if !inside {
		return ToolResult{}, false
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("Could not read skill resource '%s': %v", rawPath, err),
			IsError: true,
		}, true
	}
	return ToolResult{
		Content:        string(content),
		IsSkillContent: filepath.Base(resolved) == "SKILL.md",
	}, true
}

func resolveSkillPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	remainder := ""
	current := abs
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func pathIsRelativeTo(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
