package mcpregistry

import (
	"os"
	"strings"

	"github.com/aden-hive/hive-go/internal/version"
)

var PackageVersion = version.Version

func DefaultProjectTOMLPath() string { return "" }

func HiveVersion() string { return hiveVersionFrom(PackageVersion, DefaultProjectTOMLPath()) }

func hiveVersionFrom(packageVersion, projectTOMLPath string) string {
	if packageVersion != "" {
		return packageVersion
	}
	return ProjectVersion(projectTOMLPath)
}

func ProjectVersion(tomlPath string) string {
	if tomlPath == "" {
		return "unknown"
	}
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		return "unknown"
	}
	value, ok := projectVersionFromTOML(string(raw))
	if !ok {
		return "unknown"
	}
	return value
}

func projectVersionFromTOML(text string) (string, bool) {
	section := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
			section = strings.TrimSpace(trimmed[2 : len(trimmed)-2])
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		if section != "project" {
			continue
		}
		key, value, ok := splitTOMLAssignment(trimmed)
		if !ok || key != "version" {
			continue
		}
		unquoted, ok := unquoteTOML(value)
		if !ok {
			return "", false
		}
		return unquoted, true
	}
	return "", false
}

func splitTOMLAssignment(line string) (string, string, bool) {
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:idx])
	value := strings.TrimSpace(line[idx+1:])
	if i := strings.IndexByte(value, '#'); i >= 0 {
		if !strings.HasPrefix(value, "\"") && !strings.HasPrefix(value, "'") {
			value = strings.TrimSpace(value[:i])
		}
	}
	return key, value, true
}

func unquoteTOML(value string) (string, bool) {
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' {
			inner := value[1 : len(value)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			return inner, true
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1], true
		}
	}
	return "", false
}
