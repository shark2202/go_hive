package skills

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	MaxSkillDescriptionLength = 1024

	skillNamePatternText = `^[a-z0-9.-]+$`
)

var skillNamePattern = regexp.MustCompile(skillNamePatternText)

type SkillFile struct {
	RelPath string
	Content string
}

type SkillDraft struct {
	Name        string
	Description string
	Body        string
	Files       []SkillFile
}

func (d *SkillDraft) SkillMDText() string {
	bodyNorm := strings.TrimRightFunc(d.Body, unicode.IsSpace) + "\n"
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", d.Name, d.Description, bodyNorm)
}

func ValidateSkillName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "skill_name is required"
	}
	if !skillNamePattern.MatchString(name) {
		return "", fmt.Sprintf("skill_name '%s' must match [a-z0-9-] pattern", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return "", fmt.Sprintf("skill_name '%s' has leading/trailing/consecutive hyphens", name)
	}
	if utf8Len(name) > MaxSkillNameLength {
		return "", fmt.Sprintf("skill_name '%s' exceeds %d chars", name, MaxSkillNameLength)
	}
	return name, ""
}

func ValidateDescription(raw string) (string, string) {
	desc := strings.TrimSpace(raw)
	if desc == "" {
		return "", "skill_description is required"
	}
	if utf8Len(desc) > MaxSkillDescriptionLength {
		return "", fmt.Sprintf("skill_description must be 1–%d chars", MaxSkillDescriptionLength)
	}
	if strings.ContainsAny(desc, "\n\r") {
		return "", "skill_description must be a single line (no newlines)"
	}
	return desc, ""
}

func ValidateSkillFiles(raw any) ([]SkillFile, string) {
	entries, ok := skillFileEntries(raw)
	if !ok {
		if raw == nil {
			return []SkillFile{}, ""
		}
		return nil, "skill_files must be an array"
	}
	out := make([]SkillFile, 0, len(entries))
	for _, entry := range entries {
		m, isMap := entry.(map[string]any)
		if !isMap {
			return nil, "each skill_files entry must be an object with 'path' and 'content'"
		}
		relRaw, hasPath := m["path"]
		relStr, isStr := relRaw.(string)
		if !hasPath || !isStr || strings.TrimSpace(relStr) == "" {
			return nil, "skill_files entry missing non-empty 'path'"
		}
		content, isContent := m["content"].(string)
		if !isContent {
			return nil, fmt.Sprintf("skill_files entry '%s' missing string 'content'", relStr)
		}
		relStripped := strings.TrimSpace(relStr)
		if strings.HasPrefix(relStripped, "./") {
			relStripped = relStripped[2:]
		}
		if isUnsafeRelativePath(relStripped) {
			return nil, fmt.Sprintf("skill_files path '%s' must be relative and inside the skill folder", relStr)
		}
		if normalizeRelPath(relStripped) == "SKILL.md" {
			return nil, "skill_files must not contain SKILL.md — pass skill_body instead"
		}
		out = append(out, SkillFile{RelPath: relStripped, Content: content})
	}
	return out, ""
}

func skillFileEntries(raw any) ([]any, bool) {
	switch typed := raw.(type) {
	case nil:
		return nil, true
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = entry
		}
		return out, true
	default:
		return nil, false
	}
}

func isUnsafeRelativePath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}
	if filepath.IsAbs(p) {
		return true
	}
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return true
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func normalizeRelPath(p string) string {
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}

func BuildSkillDraft(skillName, skillDescription, skillBody string, skillFiles any) (*SkillDraft, string) {
	name, errMsg := ValidateSkillName(skillName)
	if errMsg != "" {
		return nil, errMsg
	}
	desc, errMsg := ValidateDescription(skillDescription)
	if errMsg != "" {
		return nil, errMsg
	}
	if strings.TrimSpace(skillBody) == "" {
		return nil, "skill_body is required — the operational procedure the colony worker needs to run this job unattended"
	}
	files, errMsg := ValidateSkillFiles(skillFiles)
	if errMsg != "" {
		return nil, errMsg
	}
	return &SkillDraft{Name: name, Description: desc, Body: skillBody, Files: files}, ""
}

func WriteSkill(draft *SkillDraft, targetRoot string, replaceExisting bool) (string, string, bool) {
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return "", fmt.Sprintf("failed to create skills root: %v", err), false
	}

	target := filepath.Join(targetRoot, draft.Name)
	replaced := false
	if _, statErr := os.Stat(target); statErr == nil {
		if !replaceExisting {
			return "", fmt.Sprintf("skill '%s' already exists", draft.Name), false
		}
		replaced = true
		if err := os.RemoveAll(target); err != nil {
			return "", fmt.Sprintf("failed to write skill folder %s: %v", target, err), replaced
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Sprintf("failed to write skill folder %s: %v", target, statErr), replaced
	}

	if err := os.Mkdir(target, 0o755); err != nil {
		return "", fmt.Sprintf("failed to write skill folder %s: %v", target, err), replaced
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(draft.SkillMDText()), 0o644); err != nil {
		return "", fmt.Sprintf("failed to write skill folder %s: %v", target, err), replaced
	}
	for _, sf := range draft.Files {
		fullPath := filepath.Join(target, filepath.FromSlash(sf.RelPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return "", fmt.Sprintf("failed to write skill folder %s: %v", target, err), replaced
		}
		if err := os.WriteFile(fullPath, []byte(sf.Content), 0o644); err != nil {
			return "", fmt.Sprintf("failed to write skill folder %s: %v", target, err), replaced
		}
	}
	return target, "", replaced
}

func RemoveSkill(targetRoot, skillName string) (bool, string) {
	name, errMsg := ValidateSkillName(skillName)
	if errMsg != "" {
		return false, errMsg
	}
	target := filepath.Join(targetRoot, name)
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		return false, ""
	}
	if err := os.RemoveAll(target); err != nil {
		return false, fmt.Sprintf("failed to remove skill folder %s: %v", target, err)
	}
	return true, ""
}
