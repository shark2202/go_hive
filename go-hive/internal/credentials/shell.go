package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type ShellType string

const (
	ShellBash    ShellType = "bash"
	ShellZsh     ShellType = "zsh"
	ShellUnknown ShellType = "unknown"
)

func DetectShell() ShellType {
	shell := os.Getenv("SHELL")
	if strings.Contains(shell, "zsh") {
		return ShellZsh
	}
	if strings.Contains(shell, "bash") {
		return ShellBash
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ShellUnknown
	}
	if fileExists(filepath.Join(home, ".zshrc")) || fileExists(filepath.Join(home, ".zshenv")) {
		return ShellZsh
	}
	if fileExists(filepath.Join(home, ".bashrc")) || fileExists(filepath.Join(home, ".bash_profile")) {
		return ShellBash
	}
	return ShellUnknown
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func shellCandidates(home string, shell ShellType) []string {
	switch shell {
	case ShellZsh:
		return []string{filepath.Join(home, ".zshrc"), filepath.Join(home, ".zshenv")}
	case ShellBash:
		if runtime.GOOS == "windows" {
			return []string{
				filepath.Join(home, ".bash_profile"),
				filepath.Join(home, ".bashrc"),
				filepath.Join(home, ".profile"),
			}
		}
		return []string{
			filepath.Join(home, ".bashrc"),
			filepath.Join(home, ".bash_profile"),
			filepath.Join(home, ".profile"),
		}
	default:
		return []string{filepath.Join(home, ".profile"), filepath.Join(home, ".bashrc")}
	}
}

func resolveShell(override []ShellType) ShellType {
	if len(override) > 0 {
		return override[0]
	}
	return DetectShell()
}

func GetShellConfigPath(shell ...ShellType) string {
	shellType := resolveShell(shell)
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := shellCandidates(home, shellType)
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	return candidates[0]
}

func CheckEnvVarInShellConfig(envVar string, shell ...ShellType) (bool, string) {
	shellType := resolveShell(shell)
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	pattern := regexp.MustCompile(`(?m)^export\s+` + regexp.QuoteMeta(envVar) + `=(.+)$`)

	for _, candidate := range shellCandidates(home, shellType) {
		if !fileExists(candidate) {
			continue
		}
		content, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		match := pattern.FindStringSubmatch(string(content))
		if match == nil {
			continue
		}
		value := strings.TrimSpace(match[1])
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		return true, value
	}
	return false, ""
}

func AddEnvVarToShellConfig(envVar, value string, shell ...ShellType) (bool, string) {
	configPath := GetShellConfigPath(shell...)
	if configPath == "" {
		return false, "could not resolve user home"
	}
	exportLine := fmt.Sprintf("export %s=\"%s\"", envVar, value)

	if fileExists(configPath) {
		content, err := os.ReadFile(configPath)
		if err != nil {
			return false, err.Error()
		}
		pattern := regexp.MustCompile(`(?m)^export\s+` + regexp.QuoteMeta(envVar) + `=.*$`)
		if pattern.Match(content) {
			updated := pattern.ReplaceAllStringFunc(string(content), func(string) string { return exportLine })
			if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
				return false, err.Error()
			}
			return true, configPath
		}
	}

	f, err := os.OpenFile(configPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Sprintf("Permission denied writing to %s", configPath)
	}
	defer f.Close()
	if _, err := f.WriteString("\n# Added by Hive credential setup\n" + exportLine + "\n"); err != nil {
		return false, err.Error()
	}
	return true, configPath
}

func RemoveEnvVarFromShellConfig(envVar string, shell ...ShellType) (bool, string) {
	configPath := GetShellConfigPath(shell...)
	if configPath == "" {
		return true, "Config file does not exist"
	}
	if !fileExists(configPath) {
		return true, "Config file does not exist"
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return false, err.Error()
	}
	lines := strings.Split(string(raw), "\n")
	kept := make([]string, 0, len(lines))
	exportPrefix := "export " + envVar + "="

	for i := 0; i < len(lines); i++ {
		stripped := strings.TrimSpace(lines[i])
		if strings.HasPrefix(stripped, "# Added by Hive") {
			skipComment := false
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if next != "" {
					skipComment = strings.HasPrefix(next, exportPrefix)
					break
				}
			}
			if skipComment {
				continue
			}
		}
		if strings.HasPrefix(stripped, exportPrefix) {
			continue
		}
		kept = append(kept, lines[i])
	}

	if err := os.WriteFile(configPath, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return false, err.Error()
	}
	return true, configPath
}

func GetShellSourceCommand(shell ...ShellType) string {
	return "source " + GetShellConfigPath(shell...)
}
