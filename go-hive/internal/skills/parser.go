package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxSkillNameLength = 64

type ParsedSkill struct {
	Name        string
	Description string
	Location    string
	BaseDir     string
	SourceScope string
	Body        string

	License       *string
	Compatibility []string
	Metadata      map[string]any
	AllowedTools  []string
	Visibility    []string
}

var yamlColonLineRe = regexp.MustCompile(`^(\s*\w[\w-]*:\s*)(.+)$`)

func tryFixYAML(raw string) string {
	lines := strings.Split(raw, "\n")
	fixed := make([]string, 0, len(lines))
	for _, line := range lines {
		m := yamlColonLineRe.FindStringSubmatch(line)
		if m == nil {
			fixed = append(fixed, line)
			continue
		}
		keyPart, valuePart := m[1], m[2]
		if strings.Contains(valuePart, ":") &&
			!strings.HasPrefix(valuePart, `"`) && !strings.HasPrefix(valuePart, "'") {
			valuePart = `"` + valuePart + `"`
		}
		fixed = append(fixed, keyPart+valuePart)
	}
	return strings.Join(fixed, "\n")
}

func ParseSkillMD(path string, sourceScope string) *ParsedSkill {
	content, err := os.ReadFile(path)
	if err != nil {
		LogSkillError("error", SkillActivationFailed,
			fmt.Sprintf("Failed to read '%s'", path), err.Error(),
			"Check the file exists and has read permissions.")
		return nil
	}
	text := string(content)
	if strings.TrimSpace(text) == "" {
		LogSkillError("error", SkillParseError,
			fmt.Sprintf("Invalid SKILL.md at '%s'", path),
			"The file exists but contains no content.",
			"Add valid YAML frontmatter and a markdown body to the SKILL.md.")
		return nil
	}

	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		LogSkillError("error", SkillParseError,
			fmt.Sprintf("Invalid SKILL.md at '%s'", path),
			"Missing YAML frontmatter (---).",
			"Wrap the frontmatter with --- on its own line at the top and bottom.")
		return nil
	}
	rawYAML := strings.TrimSpace(parts[1])
	body := strings.TrimSpace(parts[2])
	if rawYAML == "" {
		LogSkillError("error", SkillParseError,
			fmt.Sprintf("Invalid SKILL.md at '%s'", path),
			"The --- delimiters are present but the YAML block is empty.",
			"Add at least 'name' and 'description' fields to the frontmatter.")
		return nil
	}

	frontmatter, ok := decodeYAML(rawYAML)
	if !ok {
		fixed, fixedOK := decodeYAML(tryFixYAML(rawYAML))
		if !fixedOK {
			LogSkillError("error", SkillParseError,
				fmt.Sprintf("Invalid SKILL.md at '%s'", path),
				"YAML frontmatter could not be parsed.",
				"Validate the YAML frontmatter at https://yaml-online-parser.appspot.com/")
			return nil
		}
		frontmatter = fixed
		LogSkillError("warning", SkillYAMLFixup,
			fmt.Sprintf("Auto-fixed YAML in '%s'", path),
			"Unquoted colon values detected in frontmatter.",
			`Wrap values containing colons in quotes e.g. description: "Use for: research"`)
	}
	if frontmatter == nil {
		LogSkillError("error", SkillParseError,
			fmt.Sprintf("Invalid SKILL.md at '%s'", path),
			"YAML frontmatter is not a key-value mapping.",
			"Ensure the frontmatter is valid YAML with key: value pairs.")
		return nil
	}

	description, ok := frontmatter["description"]
	if !ok || strings.TrimSpace(pyStr(description)) == "" {
		LogSkillError("error", SkillMissingDescription,
			fmt.Sprintf("Missing 'description' in '%s'", path),
			"The 'description' field is required but is absent or empty.",
			"Add a non-empty 'description' field to the YAML frontmatter.")
		return nil
	}

	parentDir := filepath.Base(filepath.Dir(path))
	name := ""
	if rawName, ok := frontmatter["name"]; ok && strings.TrimSpace(pyStr(rawName)) != "" {
		name = strings.TrimSpace(pyStr(rawName))
	} else {
		name = parentDir
		LogSkillError("warning", SkillNameMismatch,
			fmt.Sprintf("Missing 'name' in '%s' — using directory name '%s'", path, name),
			"The 'name' field is absent from the YAML frontmatter.",
			fmt.Sprintf("Add 'name: %s' to the frontmatter to make this explicit.", name))
	}

	if utf8Len(name) > MaxSkillNameLength {
		LogSkillError("warning", SkillNameMismatch,
			fmt.Sprintf("Skill name exceeds %d chars in %s: '%s'", MaxSkillNameLength, path, name),
			"The name is longer than the recommended maximum.",
			"Shorten the skill name.")
	}
	if name != parentDir && !strings.HasSuffix(name, "."+parentDir) {
		LogSkillError("warning", SkillNameMismatch,
			fmt.Sprintf("Name mismatch in '%s'", path),
			fmt.Sprintf("Skill name '%s' doesn't match directory '%s'.", name, parentDir),
			fmt.Sprintf("Rename the directory to '%s' or set name to '%s'.", name, parentDir))
	}

	rawCompat := frontmatter["compatibility"]
	if s, ok := rawCompat.(string); ok {
		rawCompat = []any{s}
	}
	rawTools := frontmatter["allowed-tools"]
	if s, ok := rawTools.(string); ok {
		rawTools = []any{s}
	}

	var visibility []string
	if meta, ok := frontmatter["metadata"].(map[string]any); ok {
		rawVis := meta["visibility"]
		if s, ok := rawVis.(string); ok {
			rawVis = []any{s}
		}
		if list, ok := rawVis.([]any); ok {
			for _, v := range list {
				if s := strings.TrimSpace(pyStr(v)); s != "" {
					visibility = append(visibility, s)
				}
			}
		}
	}

	location, _ := filepath.Abs(path)
	baseDir, _ := filepath.Abs(filepath.Dir(path))

	return &ParsedSkill{
		Name:          name,
		Description:   strings.TrimSpace(pyStr(description)),
		Location:      location,
		BaseDir:       baseDir,
		SourceScope:   sourceScope,
		Body:          body,
		License:       optionalString(frontmatter["license"]),
		Compatibility: stringList(rawCompat),
		Metadata:      asMap(frontmatter["metadata"]),
		AllowedTools:  stringList(rawTools),
		Visibility:    visibility,
	}
}

func decodeYAML(raw string) (map[string]any, bool) {
	var out any
	if err := yaml.Unmarshal([]byte(raw), &out); err != nil {
		return nil, false
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, true
	}
	return m, true
}

func optionalString(v any) *string {
	if v == nil {
		return nil
	}
	s := pyStr(v)
	return &s
}

func stringList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, pyStr(item))
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		return pyFloatRepr(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func pyFloatRepr(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func utf8Len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
