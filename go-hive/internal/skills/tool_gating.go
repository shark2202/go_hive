package skills

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var bundledSkillDirs = []string{"_default_skills", "_preset_skills"}

type toolGatedSkill struct {
	Prefix  string
	DirName string
	Display string
}

var toolGatedSkills = []toolGatedSkill{
	{"browser_", "browser-automation", "hive.browser-automation"},
	{"terminal_", "terminal-tools-foundations", "hive.terminal-tools-foundations"},
	{"chart_", "chart-creation-foundations", "hive.chart-creation-foundations"},
}

var (
	toolGatedCacheMu sync.Mutex
	toolGatedCache   = map[string]string{}
)

func loadToolGatedBody(dirName string) string {
	toolGatedCacheMu.Lock()
	if body, ok := toolGatedCache[dirName]; ok {
		toolGatedCacheMu.Unlock()
		return body
	}
	toolGatedCacheMu.Unlock()

	body := ""
	root, err := bundledSkillsRoot()
	if err != nil {
		log.Printf("Failed to read tool-gated skill '%s': %v", dirName, err)
	} else {
		var path string
		for _, parent := range bundledSkillDirs {
			candidate := filepath.Join(root, parent, dirName, "SKILL.md")
			if _, statErr := os.Stat(candidate); statErr == nil {
				path = candidate
				break
			}
		}
		if path != "" {
			body = readToolGatedBody(dirName, path)
		}
	}

	toolGatedCacheMu.Lock()
	toolGatedCache[dirName] = body
	toolGatedCacheMu.Unlock()
	return body
}

func readToolGatedBody(dirName, path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Failed to read tool-gated skill '%s': %v", dirName, err)
		return ""
	}
	text := string(raw)
	if strings.HasPrefix(text, "---") {
		parts := strings.SplitN(text, "---", 3)
		if len(parts) >= 3 {
			return strings.TrimSpace(parts[2])
		}
	}
	return strings.TrimSpace(text)
}

func resetToolGatedBodyCache() {
	toolGatedCacheMu.Lock()
	toolGatedCache = map[string]string{}
	toolGatedCacheMu.Unlock()
}

func AugmentCatalogForTools(baseCatalogPrompt string, toolNames []string) string {
	names := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return baseCatalogPrompt
	}

	blocks := []string{}
	for _, gated := range toolGatedSkills {
		if !anyToolHasPrefix(names, gated.Prefix) {
			continue
		}
		body := loadToolGatedBody(gated.DirName)
		if body == "" {
			continue
		}
		blocks = append(blocks, "--- Pre-Activated Skill: "+gated.Display+" ---\n"+body)
	}

	if len(blocks) == 0 {
		return baseCatalogPrompt
	}

	suffix := strings.Join(blocks, "\n\n")
	if baseCatalogPrompt == "" {
		return suffix
	}
	return baseCatalogPrompt + "\n\n" + suffix
}

func anyToolHasPrefix(names []string, prefix string) bool {
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
