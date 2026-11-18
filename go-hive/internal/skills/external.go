package skills

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

func ExpandExternalPath(raw string) (string, error) {
	expanded, err := expandExternalUser(expandVars(raw))
	if err != nil {
		return "", err
	}
	return posixPathString(expanded), nil
}

func expandExternalUser(s string) (string, error) {
	if !strings.HasPrefix(s, "~") {
		return s, nil
	}
	rest := s[1:]
	slash := strings.IndexByte(rest, '/')
	name := rest
	if slash >= 0 {
		name = rest[:slash]
	}
	home := ""
	if name == "" {
		dir, err := os.UserHomeDir()
		if err != nil || dir == "" {
			return "", errors.New("Could not determine home directory.")
		}
		home = dir
	} else {
		account, err := user.Lookup(name)
		if err != nil || account.HomeDir == "" {
			return "", errors.New("Could not determine home directory.")
		}
		home = account.HomeDir
	}
	if slash < 0 {
		return home, nil
	}
	return filepath.Join(home, rest[slash+1:]), nil
}

func posixPathString(s string) string {
	slashes := 0
	for slashes < len(s) && s[slashes] == '/' {
		slashes++
	}
	prefix := ""
	switch {
	case slashes == 1:
		prefix = "/"
	case slashes == 2:
		prefix = "//"
	case slashes > 2:
		prefix = "/"
	}
	parts := make([]string, 0, slashes+4)
	for _, part := range strings.Split(s[slashes:], "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	joined := strings.Join(parts, "/")
	if joined == "" {
		if prefix != "" {
			return prefix
		}
		return "."
	}
	return prefix + joined
}

func NormalizeExternalPath(raw string) string {
	expanded, err := ExpandExternalPath(raw)
	if err != nil {
		return raw
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return raw
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func CountExternalSkills(dir string) int {
	return len(NewSkillDiscovery(DiscoveryConfig{}).scanScope(dir, "user"))
}
