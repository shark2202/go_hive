package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type ValidationResult struct {
	Passed   bool
	Errors   []string
	Warnings []string
}

func ValidateStrict(path string) ValidationResult {
	result := ValidationResult{Errors: []string{}, Warnings: []string{}}
	fail := func(msg string) ValidationResult {
		result.Passed = false
		result.Errors = append(result.Errors, msg)
		return result
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fail("File not found: " + path)
		}
		if os.IsPermission(err) {
			return fail("Permission denied reading: " + path)
		}
		return fail("Cannot read file: " + err.Error())
	}

	if strings.TrimSpace(string(content)) == "" {
		return fail("File is empty.")
	}

	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return fail("Missing YAML frontmatter — wrap frontmatter with --- delimiters.")
	}
	rawYAML := strings.TrimSpace(parts[1])
	body := strings.TrimSpace(parts[2])
	if rawYAML == "" {
		return fail("Frontmatter delimiters present but YAML block is empty.")
	}

	var raw any
	if err := yaml.Unmarshal([]byte(rawYAML), &raw); err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf(`YAML parse error: %v. Wrap values containing colons in quotes, e.g. description: "Use for: research".`, err))
		result.Passed = false
		return result
	}
	frontmatter, ok := raw.(map[string]any)
	if !ok {
		return fail("Frontmatter is not a YAML key-value mapping.")
	}

	description := frontmatter["description"]
	if description == nil || strings.TrimSpace(pyStr(description)) == "" {
		result.Errors = append(result.Errors,
			"Missing required field: 'description' must be present and non-empty.")
	}

	nameRaw := frontmatter["name"]
	if nameRaw == nil || strings.TrimSpace(pyStr(nameRaw)) == "" {
		result.Errors = append(result.Errors,
			"Missing required field: 'name' must be present. Add 'name: your-skill-name' to the frontmatter.")
	} else {
		name := strings.TrimSpace(pyStr(nameRaw))
		parentDir := filepath.Base(filepath.Dir(path))

		if n := utf8Len(name); n > MaxSkillNameLength {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Skill name '%s' is %d characters — maximum is %d. Shorten the name.", name, n, MaxSkillNameLength))
		}

		if name != parentDir && !strings.HasSuffix(name, "."+parentDir) {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Name '%s' does not match directory '%s'. Rename the directory to '%s' or set name to '%s'.",
					name, parentDir, name, parentDir))
		}
	}

	if body == "" {
		result.Errors = append(result.Errors,
			"Skill body (instructions) is empty. Add markdown instructions after the closing --- delimiter.")
	}

	if frontmatter["license"] == nil {
		result.Warnings = append(result.Warnings,
			"No 'license' field — consider adding a license (e.g. MIT, Apache-2.0).")
	}

	scriptsDir := filepath.Join(filepath.Dir(path), "scripts")
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(scriptsDir); statErr == nil && info.IsDir() {
			entries, _ := os.ReadDir(scriptsDir)
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			sort.Strings(names)
			for _, entryName := range names {
				scriptPath := filepath.Join(scriptsDir, entryName)
				scriptInfo, statErr := os.Stat(scriptPath)
				if statErr != nil || scriptInfo.IsDir() {
					continue
				}
				if scriptInfo.Mode().Perm()&0o111 == 0 {
					result.Errors = append(result.Errors,
						fmt.Sprintf("Script not executable: %s. Run: chmod +x %s", entryName, scriptPath))
				}
			}
		}
	}

	if allowedTools, present := frontmatter["allowed-tools"]; present && allowedTools != nil {
		if list, isList := allowedTools.([]any); !isList {
			result.Warnings = append(result.Warnings, "'allowed-tools' should be a list of strings.")
		} else {
			for _, tool := range list {
				if s, isStr := tool.(string); !isStr || strings.TrimSpace(s) == "" {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("'allowed-tools' entry %s is not a non-empty string.", pyRepr(tool)))
				}
			}
		}
	}

	if compatibility, present := frontmatter["compatibility"]; present && compatibility != nil {
		if list, isList := compatibility.([]any); !isList {
			result.Errors = append(result.Errors, "'compatibility' must be a list of strings.")
		} else {
			for _, item := range list {
				if _, isStr := item.(string); !isStr {
					result.Errors = append(result.Errors,
						fmt.Sprintf("'compatibility' entry %s is not a string.", pyRepr(item)))
				}
			}
		}
	}

	if metadata, present := frontmatter["metadata"]; present && metadata != nil {
		if _, isMap := metadata.(map[string]any); !isMap {
			result.Errors = append(result.Errors,
				"'metadata' must be a YAML mapping (dict), not a scalar or list.")
		}
	}

	result.Passed = len(result.Errors) == 0
	return result
}

func pyRepr(v any) string {
	switch x := v.(type) {
	case string:
		return "'" + x + "'"
	case nil:
		return "None"
	default:
		return pyStr(v)
	}
}
