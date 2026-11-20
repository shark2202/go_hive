package cli

// Parity: framework/skills/cli.py

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/skills"

	"gopkg.in/yaml.v3"
)

const skillMDTemplate = `---
name: {name}
description: <One-sentence description of what this skill does and when to use it.>
version: 0.1.0
license: MIT
author: ""
compatibility:
  - claude-code
  - hive
metadata:
  tags: []
# allowed-tools:
#   - tool_name
---

## Instructions

Describe what the agent should do when this skill is activated.

### When to Use This Skill

Describe the conditions under which the agent should activate this skill.

### Step-by-Step Protocol

1. First, ...
2. Then, ...
3. Finally, ...

### Output Format

Describe the expected output format or deliverable.
`

type doctorReport struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Parseable bool     `json:"parseable"`
	Errors    []string `json:"errors"`
	Warnings  []string `json:"warnings"`
}

type structuralReport struct {
	Passed   bool     `json:"passed"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// Parity: framework/skills/cli.py::register_skill_commands
func (r *Runner) skillCommands() []*commandSpec {
	jsonFlag := argSpec{Dest: "json", Long: "json", Kind: kindBool, Help: "Output as JSON"}
	projectDirFlag := argSpec{Dest: "project_dir", Long: "project-dir", Kind: kindString,
		Help: "Project directory to scan (default: current directory)"}

	return []*commandSpec{{
		Prog: "hive skill",
		Help: "Manage skills",
		Dest: "skill_command",
		Subcommands: []*commandSpec{
			{
				Prog: "hive skill list",
				Help: "List discovered skills across all scopes",
				Flags: []argSpec{
					projectDirFlag,
					jsonFlag,
				},
				Run: r.cmdSkillList,
			},
			{
				Prog: "hive skill install",
				Help: "Install a skill from the registry or a git URL",
				Positionals: []positionalSpec{{Dest: "name_or_url", Required: false,
					Help: "Skill name (from registry) or git URL"}},
				Flags: []argSpec{
					{Dest: "version", Long: "version", Kind: kindString,
						Help: "Git ref (branch/tag) to install"},
					{Dest: "from_url", Long: "from", Kind: kindString,
						Help: "Install from this git URL directly"},
					{Dest: "pack", Long: "pack", Kind: kindString,
						Help: "Install a starter pack by name"},
					{Dest: "install_name", Long: "name", Kind: kindString,
						Help: "Override the skill directory name on install"},
					jsonFlag,
				},
				Run: r.cmdSkillInstall,
			},
			{
				Prog:        "hive skill remove",
				Help:        "Uninstall a skill",
				Positionals: []positionalSpec{{Dest: "name", Required: true, Help: "Skill name to remove"}},
				Flags:       []argSpec{jsonFlag},
				Run:         r.cmdSkillRemove,
			},
			{
				Prog:        "hive skill info",
				Help:        "Show skill details",
				Positionals: []positionalSpec{{Dest: "name", Required: true, Help: "Skill name"}},
				Flags:       []argSpec{projectDirFlag, jsonFlag},
				Run:         r.cmdSkillInfo,
			},
			{
				Prog: "hive skill init",
				Help: "Scaffold a new skill directory with a SKILL.md template",
				Flags: []argSpec{
					{Dest: "skill_name", Long: "name", Kind: kindString},
					{Dest: "target_dir", Long: "dir", Kind: kindString,
						Help: "Parent directory for the new skill (default: current directory)"},
				},
				Run: r.cmdSkillInit,
			},
			{
				Prog: "hive skill validate",
				Help: "Strictly validate a SKILL.md against the Agent Skills spec",
				Positionals: []positionalSpec{{Dest: "path", Required: true,
					Help: "Path to SKILL.md or its parent directory"}},
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdSkillValidate,
			},
			{
				Prog: "hive skill doctor",
				Help: "Health-check skills (parseable, scripts executable, tools available)",
				Positionals: []positionalSpec{{Dest: "name", Required: false,
					Help: "Skill name to check (default: all discovered skills)"}},
				Flags: []argSpec{
					{Dest: "defaults", Long: "defaults", Kind: kindBool,
						Help: "Check all 6 framework default skills"},
					projectDirFlag,
					jsonFlag,
				},
				Run: r.cmdSkillDoctor,
			},
			{
				Prog: "hive skill update",
				Help: "Refresh registry cache or re-install a specific skill",
				Positionals: []positionalSpec{{Dest: "name", Required: false,
					Help: "Skill name to update (default: refresh registry cache only)"}},
				Flags: []argSpec{jsonFlag},
				Run:   r.cmdSkillUpdate,
			},
			{
				Prog:        "hive skill search",
				Help:        "Search the skill registry by name, tag, or description",
				Positionals: []positionalSpec{{Dest: "query", Required: true, Help: "Search query string"}},
				Flags:       []argSpec{jsonFlag},
				Run:         r.cmdSkillSearch,
			},
			{
				Prog:        "hive skill fork",
				Help:        "Create a local editable copy of a skill",
				Positionals: []positionalSpec{{Dest: "name", Required: true, Help: "Skill name to fork"}},
				Flags: []argSpec{
					{Dest: "new_name", Long: "name", Kind: kindString,
						Help: "Name for the forked skill (default: <name>-fork)"},
					{Dest: "target_dir", Long: "dir", Kind: kindString,
						Help: "Parent directory for the fork (default: ~/.hive/skills/)"},
					{Dest: "yes", Long: "yes", Kind: kindBool, Help: "Skip confirmation prompt"},
					projectDirFlag,
					jsonFlag,
				},
				Run: r.cmdSkillFork,
			},
			{
				Prog: "hive skill test",
				Help: "Run a skill in isolation or execute its eval suite (CLI-9)",
				Positionals: []positionalSpec{{Dest: "path", Required: true,
					Help: "Path to SKILL.md or its parent directory"}},
				Flags: []argSpec{
					{Dest: "input_json", Long: "input", Kind: kindString,
						Help: `JSON input to pass to the skill, e.g. '{"prompt": "..."}'`},
					{Dest: "model", Long: "model", Kind: kindString,
						Help: "Override the LLM model (default: claude-haiku-4-5-20251001)"},
					jsonFlag,
				},
				Run: r.cmdSkillTest,
			},
			{
				Prog: "hive skill trust",
				Help: "Permanently trust a project repository so its skills load without prompting",
				Positionals: []positionalSpec{{Dest: "project_path", Required: true,
					Help: "Path to the project directory (must contain a .git with a remote origin)"}},
				Run: r.cmdSkillTrust,
			},
		},
	}}
}

func (r *Runner) outJSON(value any) {
	writeJSONOut(r.Stdout, value, false)
}

func (r *Runner) reportSkillFailure(err error, useJSON bool) {
	var skillErr *skills.SkillError
	if errors.As(err, &skillErr) {
		if useJSON {
			r.outJSON(map[string]any{"error": skillErr.What, "why": skillErr.Why, "fix": skillErr.Fix})
			return
		}
		fmt.Fprintf(r.Stderr, "Error: %s\n  Why: %s\n  Fix: %s\n", skillErr.What, skillErr.Why, skillErr.Fix)
		return
	}
	if useJSON {
		r.outJSON(map[string]any{"error": err.Error()})
		return
	}
	fmt.Fprintf(r.Stderr, "Error: %s\n", err.Error())
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func looseEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if af, ok := numericValue(a); ok {
		if bf, ok := numericValue(b); ok {
			return af == bf
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	}
	return 0, false
}

// Parity: framework/skills/cli.py::cmd_skill_list
func (r *Runner) cmdSkillList(args *parsedArgs) int {
	projectDir := r.workDir()
	if raw := args.str("project_dir"); raw != "" {
		projectDir = resolvePath(raw)
	}
	discovered := r.Discover(projectDir)

	if args.boolean("json") {
		entries := make([]map[string]any, 0, len(discovered))
		for _, skill := range discovered {
			if skill == nil {
				continue
			}
			entries = append(entries, map[string]any{
				"name":        skill.Name,
				"description": skill.Description,
				"scope":       skill.SourceScope,
				"location":    skill.Location,
			})
		}
		r.outJSON(map[string]any{"skills": entries})
		return 0
	}

	if len(discovered) == 0 {
		fmt.Fprintln(r.Stdout, "No skills discovered.")
		return 0
	}

	headers := map[string]string{
		"project":   "PROJECT SKILLS",
		"user":      "USER SKILLS",
		"framework": "FRAMEWORK SKILLS",
	}
	for _, scope := range []string{"project", "user", "framework"} {
		printed := false
		for _, skill := range discovered {
			if skill == nil || skill.SourceScope != scope {
				continue
			}
			if !printed {
				fmt.Fprintf(r.Stdout, "\n%s\n", headers[scope])
				fmt.Fprintln(r.Stdout, strings.Repeat("─", 40))
				printed = true
			}
			fmt.Fprintf(r.Stdout, "  • %s\n    %s\n    %s\n", skill.Name, skill.Description, skill.Location)
		}
	}
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_install
func (r *Runner) cmdSkillInstall(args *parsedArgs) int {
	r.ShowInstallNotice()
	targetDir := r.userSkillsDir()
	version := args.str("version")

	if pack := args.str("pack"); pack != "" {
		return r.installPack(pack, targetDir, version)
	}

	useJSON := args.boolean("json")

	if fromURL := args.str("from_url"); fromURL != "" {
		skillName := args.str("install_name")
		if skillName == "" {
			skillName = deriveNameFromURL(fromURL)
		}
		if !useJSON {
			fmt.Fprintf(r.Stdout, "Installing '%s' from %s ...\n", skillName, fromURL)
		}
		dest, err := r.InstallFromGit(fromURL, skillName, "", version, targetDir)
		if err != nil {
			r.reportSkillFailure(err, useJSON)
			return 1
		}
		if useJSON {
			r.outJSON(map[string]any{"name": skillName, "location": dest})
		} else {
			fmt.Fprintf(r.Stdout, "✓ Installed: %s\n  Location: %s\n", skillName, dest)
		}
		return 0
	}

	if nameOrURL := args.str("name_or_url"); nameOrURL != "" {
		name := args.str("install_name")
		if name == "" {
			name = nameOrURL
		}
		entry := r.registry().GetSkillEntry(nameOrURL)
		if entry == nil {
			if useJSON {
				r.outJSON(map[string]any{
					"error": fmt.Sprintf("skill '%s' not found in registry", nameOrURL),
					"why":   "Registry may be unavailable or skill name is incorrect.",
					"fix":   "hive skill install --from <url>",
				})
			} else {
				fmt.Fprintf(r.Stderr, "Error: skill '%s' not found in registry.\n", nameOrURL)
				fmt.Fprintln(r.Stderr, "  The registry may be unavailable, or the skill name is incorrect.")
				fmt.Fprintln(r.Stderr, "  Install from a git URL directly: hive skill install --from <url>")
			}
			return 1
		}
		if !useJSON {
			fmt.Fprintf(r.Stdout, "Installing '%s' from registry ...\n", name)
		}
		dest, err := r.InstallFromRegistry(entry, targetDir, version)
		if err != nil {
			r.reportSkillFailure(err, useJSON)
			return 1
		}
		if useJSON {
			r.outJSON(map[string]any{"name": name, "location": dest})
		} else {
			fmt.Fprintf(r.Stdout, "✓ Installed: %s\n  Location: %s\n", name, dest)
		}
		return 0
	}

	if useJSON {
		r.outJSON(map[string]any{
			"error": "No install target specified",
			"why":   "Provide a skill name, --from <url>, or --pack <name>.",
			"fix":   "hive skill install --help",
		})
	} else {
		fmt.Fprintln(r.Stderr, "Error: specify a skill name, --from <url>, or --pack <name>.")
		fmt.Fprintln(r.Stderr, "  Usage: hive skill install <name>")
		fmt.Fprintln(r.Stderr, "         hive skill install --from <git-url>")
		fmt.Fprintln(r.Stderr, "         hive skill install --pack <pack-name>")
	}
	return 1
}

// Parity: framework/skills/cli.py::_install_pack
func (r *Runner) installPack(packName, targetDir, version string) int {
	client := r.registry()
	names := client.GetPack(packName)
	if names == nil {
		fmt.Fprintf(r.Stderr, "Error: pack '%s' not found in registry.\n", packName)
		fmt.Fprintln(r.Stderr, "  The registry may be unavailable. Check your network connection.")
		return 1
	}
	if len(names) == 0 {
		fmt.Fprintf(r.Stderr, "Warning: pack '%s' contains no skills.\n", packName)
		return 0
	}

	fmt.Fprintf(r.Stdout, "Installing pack '%s' (%d skills) ...\n", packName, len(names))
	errorCount := 0
	for _, name := range names {
		entry := client.GetSkillEntry(name)
		if entry == nil {
			fmt.Fprintf(r.Stderr, "  ✗ %s — not found in registry, skipping\n", name)
			errorCount++
			continue
		}
		dest, err := r.InstallFromRegistry(entry, targetDir, version)
		if err != nil {
			var skillErr *skills.SkillError
			reason := err.Error()
			if errors.As(err, &skillErr) {
				reason = skillErr.Why
			}
			fmt.Fprintf(r.Stderr, "  ✗ %s — %s\n", name, reason)
			errorCount++
			continue
		}
		fmt.Fprintf(r.Stdout, "  ✓ %s → %s\n", name, dest)
	}

	fmt.Fprintln(r.Stdout)
	if errorCount == 0 {
		fmt.Fprintf(r.Stdout, "✓ Pack '%s' installed successfully.\n", packName)
	} else {
		fmt.Fprintf(r.Stdout, "✗ Pack install completed with %d error(s).\n", errorCount)
	}
	if errorCount == 0 {
		return 0
	}
	return 1
}

// Parity: framework/skills/cli.py::cmd_skill_remove
func (r *Runner) cmdSkillRemove(args *parsedArgs) int {
	useJSON := args.boolean("json")
	name := args.str("name")

	removed, err := r.RemoveSkill(name, "")
	if err != nil {
		r.reportSkillFailure(err, useJSON)
		return 1
	}
	if !removed {
		if useJSON {
			r.outJSON(map[string]any{
				"error": fmt.Sprintf("skill '%s' not found", name),
				"why":   "Skill is not installed in ~/.hive/skills/.",
				"fix":   "hive skill list",
			})
		} else {
			fmt.Fprintf(r.Stderr, "Error: skill '%s' not found in ~/.hive/skills/.\n", name)
			fmt.Fprintln(r.Stderr, "  Use 'hive skill list' to see installed skills.")
		}
		return 1
	}

	if useJSON {
		r.outJSON(map[string]any{"name": name, "removed": true})
	} else {
		fmt.Fprintf(r.Stdout, "✓ Removed: %s\n", name)
	}
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_info
func (r *Runner) cmdSkillInfo(args *parsedArgs) int {
	useJSON := args.boolean("json")
	projectDir := r.workDir()
	if raw := args.str("project_dir"); raw != "" {
		projectDir = resolvePath(raw)
	}
	name := args.str("name")

	var match *skills.ParsedSkill
	for _, skill := range r.Discover(projectDir) {
		if skill != nil && skill.Name == name {
			match = skill
			break
		}
	}

	if match != nil {
		subFiles := map[string][]string{}
		for _, sub := range []string{"scripts", "references", "assets"} {
			subDir := filepath.Join(match.BaseDir, sub)
			info, err := os.Stat(subDir)
			if err != nil || !info.IsDir() {
				continue
			}
			entries, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			files := make([]string, 0, len(entries))
			for _, entry := range entries {
				if !entry.IsDir() {
					files = append(files, entry.Name())
				}
			}
			sort.Strings(files)
			if len(files) > 0 {
				subFiles[sub] = files
			}
		}

		tags := any([]any{})
		if match.Metadata != nil {
			if value, present := match.Metadata["tags"]; present {
				tags = value
			}
		}
		compatibility := match.Compatibility
		if compatibility == nil {
			compatibility = []string{}
		}
		allowedTools := match.AllowedTools
		if allowedTools == nil {
			allowedTools = []string{}
		}

		if useJSON {
			payload := map[string]any{
				"name":          match.Name,
				"description":   match.Description,
				"scope":         match.SourceScope,
				"location":      match.Location,
				"installed":     true,
				"license":       match.License,
				"compatibility": compatibility,
				"allowed_tools": allowedTools,
				"tags":          tags,
			}
			for sub, files := range subFiles {
				payload[sub] = files
			}
			r.outJSON(payload)
			return 0
		}

		fmt.Fprintf(r.Stdout, "\n%s\n", match.Name)
		fmt.Fprintln(r.Stdout, strings.Repeat("─", 40))
		fmt.Fprintf(r.Stdout, "  Description:   %s\n", match.Description)
		fmt.Fprintf(r.Stdout, "  Scope:         %s\n", match.SourceScope)
		fmt.Fprintf(r.Stdout, "  Location:      %s\n", match.Location)
		if match.License != nil && *match.License != "" {
			fmt.Fprintf(r.Stdout, "  License:       %s\n", *match.License)
		}
		if len(match.Compatibility) > 0 {
			fmt.Fprintf(r.Stdout, "  Compatibility: %s\n", strings.Join(match.Compatibility, ", "))
		}
		if len(match.AllowedTools) > 0 {
			fmt.Fprintf(r.Stdout, "  Allowed tools: %s\n", strings.Join(match.AllowedTools, ", "))
		}
		if match.Metadata != nil {
			if tags, ok := match.Metadata["tags"].([]any); ok && len(tags) > 0 {
				parts := make([]string, 0, len(tags))
				for _, tag := range tags {
					parts = append(parts, pyStr(tag))
				}
				fmt.Fprintf(r.Stdout, "  Tags:          %s\n", strings.Join(parts, ", "))
			}
		}
		subNames := make([]string, 0, len(subFiles))
		for sub := range subFiles {
			subNames = append(subNames, sub)
		}
		sort.Strings(subNames)
		for _, sub := range subNames {
			fmt.Fprintf(r.Stdout, "  %-13s: %s\n", capitalize(sub), strings.Join(subFiles[sub], ", "))
		}
		return 0
	}

	entry := r.registry().GetSkillEntry(name)
	if entry != nil {
		entryName := mapStringDefault(entry, "name", name)
		description := mapStringDefault(entry, "description", "")
		version := mapValueDefault(entry, "version", "unknown")
		author := mapValueDefault(entry, "author", "unknown")
		trustTier := mapValueDefault(entry, "trust_tier", "community")
		license := entry["license"]
		tags := mapValueDefault(entry, "tags", []any{})

		if useJSON {
			r.outJSON(map[string]any{
				"name":        entryName,
				"description": description,
				"installed":   false,
				"version":     version,
				"author":      author,
				"trust_tier":  trustTier,
				"license":     license,
				"tags":        tags,
			})
			return 0
		}

		fmt.Fprintf(r.Stdout, "\n%s  (not installed)\n", entryName)
		fmt.Fprintln(r.Stdout, strings.Repeat("─", 40))
		fmt.Fprintf(r.Stdout, "  Description:   %s\n", description)
		fmt.Fprintf(r.Stdout, "  Version:       %s\n", pyStr(version))
		fmt.Fprintf(r.Stdout, "  Author:        %s\n", pyStr(author))
		fmt.Fprintf(r.Stdout, "  Trust tier:    %s\n", pyStr(trustTier))
		if value, present := entry["license"]; present && pyTruthy(value) {
			fmt.Fprintf(r.Stdout, "  License:       %s\n", pyStr(value))
		}
		if value, present := entry["tags"]; present && pyTruthy(value) {
			fmt.Fprintf(r.Stdout, "  Tags:          %s\n", joinValues(value, ", "))
		}
		fmt.Fprintf(r.Stdout, "\n  Install with: hive skill install %s\n", name)
		return 0
	}

	if useJSON {
		r.outJSON(map[string]any{"error": fmt.Sprintf("skill '%s' not found locally or in registry", name)})
	} else {
		fmt.Fprintf(r.Stderr, "Error: skill '%s' not found locally or in registry.\n", name)
	}
	return 1
}

// Parity: framework/skills/cli.py::cmd_skill_init
func (r *Runner) cmdSkillInit(args *parsedArgs) int {
	name := args.str("skill_name")
	if name == "" {
		if r.StdinIsTTY {
			data, err := r.readPromptLine("Skill name (e.g. my-research-skill): ")
			if err == nil {
				name = strings.TrimSpace(data)
			}
		}
		if name == "" {
			fmt.Fprintln(r.Stderr, "Error: provide a skill name with --name <name>.")
			return 1
		}
	}

	parent := r.workDir()
	if raw := args.str("target_dir"); raw != "" {
		parent = resolvePath(raw)
	}
	skillDir := filepath.Join(parent, name)

	if _, err := os.Stat(skillDir); err == nil {
		fmt.Fprintf(r.Stderr, "Error: directory already exists: %s\n", skillDir)
		fmt.Fprintln(r.Stderr, "  Choose a different --name or use --dir to place it elsewhere.")
		return 1
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		fmt.Fprintf(r.Stderr, "Error: %s\n", err.Error())
		return 1
	}
	skillMD := filepath.Join(skillDir, "SKILL.md")
	content := strings.ReplaceAll(skillMDTemplate, "{name}", name)
	if err := os.WriteFile(skillMD, []byte(content), 0o644); err != nil {
		fmt.Fprintf(r.Stderr, "Error: %s\n", err.Error())
		return 1
	}

	fmt.Fprintf(r.Stdout, "✓ Created: %s\n", skillMD)
	fmt.Fprintln(r.Stdout, "  Next steps:")
	fmt.Fprintln(r.Stdout, "  1. Edit SKILL.md — fill in description and instructions")
	fmt.Fprintf(r.Stdout, "  2. Run: hive skill validate %s\n", skillMD)
	fmt.Fprintf(r.Stdout, "  3. Move to ~/.hive/skills/%s/ to make it available to all agents\n", name)
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_validate
func (r *Runner) cmdSkillValidate(args *parsedArgs) int {
	path := args.str("path")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}

	result := skills.ValidateStrict(path)
	if result.Errors == nil {
		result.Errors = []string{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}

	if args.boolean("json") {
		r.outJSON(map[string]any{
			"path":     path,
			"passed":   result.Passed,
			"errors":   result.Errors,
			"warnings": result.Warnings,
		})
		if result.Passed {
			return 0
		}
		return 1
	}

	for _, warning := range result.Warnings {
		fmt.Fprintf(r.Stdout, "  [WARN]  %s\n", warning)
	}
	for _, problem := range result.Errors {
		fmt.Fprintf(r.Stdout, "  [ERROR] %s\n", problem)
	}

	if result.Passed {
		if len(result.Warnings) == 0 {
			fmt.Fprintf(r.Stdout, "✓ %s — valid\n", path)
		} else {
			fmt.Fprintf(r.Stdout, "✓ %s — valid (%d warning(s))\n", path, len(result.Warnings))
		}
		return 0
	}
	fmt.Fprintf(r.Stdout, "✗ %s — invalid (%d error(s), %d warning(s))\n",
		path, len(result.Errors), len(result.Warnings))
	return 1
}

// Parity: framework/skills/cli.py::cmd_skill_doctor
func (r *Runner) cmdSkillDoctor(args *parsedArgs) int {
	useJSON := args.boolean("json")
	overallErrors := 0

	if args.boolean("defaults") {
		if !useJSON {
			fmt.Fprintln(r.Stdout, "\nFRAMEWORK DEFAULT SKILLS")
			fmt.Fprintln(r.Stdout, strings.Repeat("─", 40))
		}
		results := make([]doctorReport, 0, len(skills.DefaultSkillRegistry))
		for _, entry := range skills.DefaultSkillRegistry {
			skillMD := filepath.Join(skills.DefaultSkillsDir(), entry.Dir, "SKILL.md")
			report, count := r.doctorSkillFile(entry.Name, skillMD, useJSON, "framework")
			overallErrors += count
			if useJSON {
				results = append(results, report)
			}
		}
		if useJSON {
			r.outJSON(map[string]any{"skills": results, "total_errors": overallErrors})
		}
		if overallErrors == 0 {
			return 0
		}
		return 1
	}

	projectDir := r.workDir()
	if raw := args.str("project_dir"); raw != "" {
		projectDir = resolvePath(raw)
	}
	discovered := r.Discover(projectDir)

	if name := args.str("name"); name != "" {
		filtered := make([]*skills.ParsedSkill, 0, len(discovered))
		for _, skill := range discovered {
			if skill != nil && skill.Name == name {
				filtered = append(filtered, skill)
			}
		}
		discovered = filtered
		if len(discovered) == 0 {
			candidate := filepath.Join(r.userSkillsDir(), name, "SKILL.md")
			if _, err := os.Stat(candidate); err == nil {
				if useJSON {
					report, count := r.doctorSkillFile(name, candidate, true, "user")
					r.outJSON(map[string]any{"skills": []doctorReport{report}, "total_errors": count})
					if len(report.Errors) > 0 {
						return 1
					}
					return 0
				}
				fmt.Fprintf(r.Stdout, "\nChecking skill: %s  [user]\n", name)
				_, count := r.doctorSkillFile(name, candidate, false, "")
				overallErrors += count
				fmt.Fprintln(r.Stdout)
				fmt.Fprintf(r.Stdout, "✗ %d error(s) found.\n", overallErrors)
				return 1
			}
			if useJSON {
				r.outJSON(map[string]any{"error": fmt.Sprintf("skill '%s' not found", name)})
			} else {
				fmt.Fprintf(r.Stderr, "Error: skill '%s' not found.\n", name)
			}
			return 1
		}
	}

	if len(discovered) == 0 {
		if useJSON {
			r.outJSON(map[string]any{"skills": []doctorReport{}, "total_errors": 0})
		} else {
			fmt.Fprintln(r.Stdout, "No skills discovered.")
		}
		return 0
	}

	results := make([]doctorReport, 0, len(discovered))
	for _, skill := range discovered {
		if skill == nil {
			continue
		}
		if useJSON {
			report, count := r.doctorSkillFile(skill.Name, skill.Location, true, skill.SourceScope)
			overallErrors += count
			results = append(results, report)
			continue
		}
		fmt.Fprintf(r.Stdout, "\nChecking skill: %s  [%s]\n", skill.Name, skill.SourceScope)
		_, count := r.doctorSkillFile(skill.Name, skill.Location, false, "")
		overallErrors += count
	}

	if useJSON {
		r.outJSON(map[string]any{"skills": results, "total_errors": overallErrors})
		if overallErrors == 0 {
			return 0
		}
		return 1
	}

	fmt.Fprintln(r.Stdout)
	if overallErrors == 0 {
		fmt.Fprintln(r.Stdout, "✓ All skills healthy.")
	} else {
		fmt.Fprintf(r.Stdout, "✗ %d error(s) found.\n", overallErrors)
	}
	if overallErrors == 0 {
		return 0
	}
	return 1
}

// Parity: framework/skills/cli.py::cmd_skill_update
func (r *Runner) cmdSkillUpdate(args *parsedArgs) int {
	useJSON := args.boolean("json")
	client := r.registry()

	name := args.str("name")
	if name == "" {
		if !useJSON {
			fmt.Fprintln(r.Stdout, "Refreshing registry cache ...")
		}
		index := client.FetchIndex(true)
		if index == nil {
			if useJSON {
				r.outJSON(map[string]any{
					"status":  "unavailable",
					"warning": "registry unavailable — could not refresh cache",
				})
			} else {
				fmt.Fprintln(r.Stderr, "Warning: registry unavailable — could not refresh cache.")
			}
			return 0
		}
		count := 0
		if list, ok := index["skills"].([]any); ok {
			count = len(list)
		}
		if useJSON {
			r.outJSON(map[string]any{"status": "refreshed", "skill_count": count})
		} else {
			fmt.Fprintf(r.Stdout, "✓ Registry cache updated (%d skills).\n", count)
		}
		return 0
	}

	entry := client.GetSkillEntry(name)
	if entry == nil {
		if useJSON {
			r.outJSON(map[string]any{
				"error": fmt.Sprintf("skill '%s' not found in registry", name),
				"why":   "Registry may be unavailable or skill name is incorrect.",
				"fix":   "Check your network connection or verify the skill name.",
			})
		} else {
			fmt.Fprintf(r.Stderr, "Error: skill '%s' not found in registry — cannot update.\n", name)
			fmt.Fprintln(r.Stderr, "  Check your network connection or verify the skill name.")
		}
		return 1
	}

	registryVersion := entry["version"]
	installedSkillMD := filepath.Join(r.userSkillsDir(), name, "SKILL.md")

	if _, err := os.Stat(installedSkillMD); err == nil {
		var installedVersion any
		if content, readErr := os.ReadFile(installedSkillMD); readErr == nil {
			parts := strings.SplitN(string(content), "---", 3)
			if len(parts) >= 3 {
				var decoded any
				if yaml.Unmarshal([]byte(parts[1]), &decoded) == nil {
					if fields, ok := decoded.(map[string]any); ok {
						installedVersion = fields["version"]
					}
				}
			}
		}

		if pyTruthy(installedVersion) && looseEqual(installedVersion, registryVersion) {
			if useJSON {
				r.outJSON(map[string]any{
					"name":    name,
					"status":  "up_to_date",
					"version": registryVersion,
				})
			} else {
				fmt.Fprintf(r.Stdout, "✓ '%s' is already at version %s.\n", name, pyStr(registryVersion))
			}
			return 0
		}
		if !pyTruthy(installedVersion) && !useJSON {
			fmt.Fprintf(r.Stderr,
				"Warning: installed skill '%s' has no version field — cannot compare. Re-installing.\n", name)
		}
	}

	if !useJSON {
		fmt.Fprintf(r.Stdout, "Updating '%s' ...\n", name)
	}
	if _, err := r.RemoveSkill(name, ""); err != nil {
		r.reportSkillFailure(err, useJSON)
		return 1
	}
	dest, err := r.InstallFromRegistry(entry, r.userSkillsDir(), "")
	if err != nil {
		r.reportSkillFailure(err, useJSON)
		return 1
	}

	newVersion := "unknown"
	if pyTruthy(registryVersion) {
		newVersion = pyStr(registryVersion)
	}
	if useJSON {
		r.outJSON(map[string]any{
			"name":     name,
			"status":   "updated",
			"version":  newVersion,
			"location": dest,
		})
	} else {
		fmt.Fprintf(r.Stdout, "✓ Updated '%s' to version %s.\n  Location: %s\n", name, newVersion, dest)
	}
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_search
func (r *Runner) cmdSkillSearch(args *parsedArgs) int {
	useJSON := args.boolean("json")
	query := args.str("query")
	client := r.registry()

	if client.FetchIndex(false) == nil {
		if useJSON {
			r.outJSON(map[string]any{
				"error": "registry unavailable",
				"query": query,
				"fix":   "hive skill install --from <url>",
			})
		} else {
			fmt.Fprintf(r.Stderr, "Error: registry unavailable — cannot search for '%s'.\n", query)
			fmt.Fprintln(r.Stderr, "  Install from a git URL directly: hive skill install --from <url>")
		}
		return 1
	}

	results := client.Search(query)

	if useJSON {
		entries := make([]map[string]any, 0, len(results))
		for _, entry := range results {
			entries = append(entries, map[string]any{
				"name":        mapStringDefault(entry, "name", ""),
				"description": mapStringDefault(entry, "description", ""),
				"trust_tier":  mapValueDefault(entry, "trust_tier", "community"),
				"tags":        mapValueDefault(entry, "tags", []any{}),
			})
		}
		r.outJSON(map[string]any{"query": query, "results": entries})
		return 0
	}

	if len(results) == 0 {
		fmt.Fprintf(r.Stdout, "No skills found matching '%s'.\n", query)
		return 0
	}

	fmt.Fprintf(r.Stdout, "\n%d result(s) for '%s':\n\n", len(results), query)
	for _, entry := range results {
		name := mapStringDefault(entry, "name", "")
		tier := mapValueDefault(entry, "trust_tier", "community")
		description := mapStringDefault(entry, "description", "")
		fmt.Fprintf(r.Stdout, "  • %s  [%s]\n    %s\n\n", name, pyStr(tier), description)
	}
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_fork
func (r *Runner) cmdSkillFork(args *parsedArgs) int {
	useJSON := args.boolean("json")
	projectDir := r.workDir()
	if raw := args.str("project_dir"); raw != "" {
		projectDir = resolvePath(raw)
	}
	name := args.str("name")

	var source *skills.ParsedSkill
	for _, skill := range r.Discover(projectDir) {
		if skill != nil && skill.Name == name {
			source = skill
			break
		}
	}
	if source == nil {
		if useJSON {
			r.outJSON(map[string]any{"error": fmt.Sprintf("skill '%s' not found", name)})
		} else {
			fmt.Fprintf(r.Stderr, "Error: skill '%s' not found.\n", name)
			fmt.Fprintln(r.Stderr, "  Use 'hive skill list' to see available skills.")
		}
		return 1
	}

	newName := args.str("new_name")
	if newName == "" {
		newName = name + "-fork"
	}
	targetDir := r.userSkillsDir()
	if raw := args.str("target_dir"); raw != "" {
		targetDir = resolvePath(raw)
	}
	dest := filepath.Join(targetDir, newName)

	if !args.boolean("yes") && !useJSON {
		if !r.promptYesNo(fmt.Sprintf("Fork '%s' to %s? [y/N] ", name, dest)) {
			fmt.Fprintln(r.Stdout, "Aborted.")
			return 0
		}
	}

	result, err := r.ForkSkill(source, newName, targetDir)
	if err != nil {
		r.reportSkillFailure(err, useJSON)
		return 1
	}

	if useJSON {
		r.outJSON(map[string]any{"source": name, "new_name": newName, "location": result})
	} else {
		fmt.Fprintf(r.Stdout, "✓ Forked '%s' → '%s'\n", name, newName)
		fmt.Fprintf(r.Stdout, "  Location: %s\n", result)
		fmt.Fprintln(r.Stdout, "  Edit SKILL.md to customise, then run: hive skill validate")
	}
	return 0
}

// Parity: framework/skills/cli.py::cmd_skill_test
func (r *Runner) cmdSkillTest(args *parsedArgs) int {
	useJSON := args.boolean("json")

	path := args.str("path")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}

	vresult := skills.ValidateStrict(path)
	if vresult.Errors == nil {
		vresult.Errors = []string{}
	}
	if vresult.Warnings == nil {
		vresult.Warnings = []string{}
	}
	structural := structuralReport{
		Passed:   vresult.Passed,
		Errors:   vresult.Errors,
		Warnings: vresult.Warnings,
	}

	if !useJSON {
		for _, warning := range vresult.Warnings {
			fmt.Fprintf(r.Stdout, "  [WARN]  %s\n", warning)
		}
		for _, problem := range vresult.Errors {
			fmt.Fprintf(r.Stdout, "  [ERROR] %s\n", problem)
		}
	}

	if !vresult.Passed {
		if useJSON {
			r.outJSON(map[string]any{"path": path, "skill": nil, "structural": structural})
		} else {
			fmt.Fprintf(r.Stdout, "✗ %s — structural validation failed. Fix errors before testing.\n", path)
		}
		return 1
	}

	skill := skills.ParseSkillMD(path, "user")
	if skill == nil {
		if useJSON {
			r.outJSON(map[string]any{
				"path":  path,
				"skill": nil,
				"structural": structuralReport{
					Passed:   false,
					Errors:   []string{"parse_skill_md returned None"},
					Warnings: []string{},
				},
			})
		} else {
			fmt.Fprintf(r.Stderr, "✗ %s — skill could not be parsed.\n", path)
		}
		return 1
	}

	evalsDir := filepath.Join(filepath.Dir(path), "evals")
	evalFiles := []string{}
	if info, err := os.Stat(evalsDir); err == nil && info.IsDir() {
		if matches, err := filepath.Glob(filepath.Join(evalsDir, "*.json")); err == nil {
			evalFiles = matches
		}
	}
	hasEvals := len(evalFiles) > 0
	hasInput := args.wasProvided("input_json")

	if !hasInput && !hasEvals {
		report, doctorErrors := r.doctorSkillFile(skill.Name, path, useJSON, "user")
		if useJSON {
			r.outJSON(map[string]any{
				"path":       path,
				"skill":      skill.Name,
				"structural": structural,
				"doctor":     report,
			})
			if structural.Passed && len(report.Errors) == 0 {
				return 0
			}
			return 1
		}
		if doctorErrors == 0 {
			fmt.Fprintf(r.Stdout, "✓ %s — structurally valid and healthy.\n", skill.Name)
			fmt.Fprintln(r.Stdout, "  No evals/ directory found. Use --input <json> for a live invocation test.")
		} else {
			fmt.Fprintf(r.Stdout, "✗ %s — %d doctor error(s) found.\n", skill.Name, doctorErrors)
		}
		if doctorErrors == 0 {
			return 0
		}
		return 1
	}

	var provider completionProvider
	providerError := ""
	model := args.str("model")
	if model == "" {
		model = llm.DefaultAnthropicModel
	}
	if created, err := r.NewProvider(model); err != nil {
		provider = nil
		providerError = err.Error()
	} else {
		provider = created
	}

	if provider == nil && hasInput {
		if useJSON {
			r.outJSON(map[string]any{
				"path":  path,
				"skill": skill.Name,
				"error": "Cannot initialize LLM provider: " + providerError,
				"fix":   "Set ANTHROPIC_API_KEY to enable live invocation.",
			})
		} else {
			fmt.Fprintf(r.Stderr, "Error: Cannot initialize LLM provider: %s\n", providerError)
			fmt.Fprintln(r.Stderr, "  Set ANTHROPIC_API_KEY to enable live invocation.")
		}
		return 1
	}

	result := map[string]any{
		"path":       path,
		"skill":      skill.Name,
		"structural": structural,
	}
	overallFailed := 0
	auxMaxTokens := llm.AuxMaxTokens()

	if hasInput && provider != nil {
		raw := args.str("input_json")
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		} else {
			decoded = raw
		}
		prompt := ""
		if object, ok := decoded.(map[string]any); ok {
			if value, present := object["prompt"]; present {
				prompt = pyStr(value)
			} else {
				prompt = raw
			}
		} else {
			prompt = pyStr(decoded)
		}

		if !useJSON {
			fmt.Fprintf(r.Stdout, "\nRunning '%s' with provided input ...\n", skill.Name)
		}
		response, err := provider.Complete(context.Background(),
			[]map[string]any{{"role": "user", "content": prompt}},
			llm.CompleteOptions{System: skill.Body, MaxTokens: auxMaxTokens})
		if err != nil {
			if !useJSON {
				fmt.Fprintf(r.Stderr, "Error during invocation: %s\n", err.Error())
			}
			result["invocation"] = map[string]any{"prompt": prompt, "error": err.Error()}
			overallFailed++
		} else {
			if !useJSON {
				fmt.Fprintln(r.Stdout, "\n── Response ──────────────────────────────────────────────────")
				fmt.Fprintln(r.Stdout, response.Content)
				fmt.Fprintln(r.Stdout, "──────────────────────────────────────────────────────────────")
			}
			result["invocation"] = map[string]any{
				"prompt":   prompt,
				"response": response.Content,
				"model":    response.Model,
			}
		}
	}

	if hasEvals {
		if provider == nil {
			if !useJSON {
				fmt.Fprintf(r.Stderr,
					"\nWarning: ANTHROPIC_API_KEY not set — skipping %d eval file(s).\n", len(evalFiles))
			}
		} else {
			judge := r.NewJudge(provider)
			evalResults := make([]map[string]any, 0, len(evalFiles))

			for _, evalFile := range evalFiles {
				content, readErr := os.ReadFile(evalFile)
				var evalData map[string]any
				if readErr == nil {
					readErr = json.Unmarshal(content, &evalData)
				}
				if readErr != nil {
					if !useJSON {
						fmt.Fprintf(r.Stderr, "  [ERROR] Cannot parse %s: %s\n",
							filepath.Base(evalFile), readErr.Error())
					}
					overallFailed++
					continue
				}

				cases, _ := evalData["evals"].([]any)
				for _, item := range cases {
					evalCase, _ := item.(map[string]any)
					caseID := "?"
					if value, present := evalCase["id"]; present {
						caseID = pyStr(value)
					}
					evalPrompt := ""
					if value, present := evalCase["prompt"]; present {
						evalPrompt = pyStr(value)
					}

					if !useJSON {
						fmt.Fprintf(r.Stdout, "\nEval #%s: %s\n", caseID, truncatePrompt(evalPrompt, 60))
					}

					response, err := provider.Complete(context.Background(),
						[]map[string]any{{"role": "user", "content": evalPrompt}},
						llm.CompleteOptions{System: skill.Body, MaxTokens: auxMaxTokens})
					if err != nil {
						if !useJSON {
							fmt.Fprintf(r.Stderr, "  [ERROR] Invocation failed: %s\n", err.Error())
						}
						evalResults = append(evalResults, map[string]any{
							"id":     caseID,
							"prompt": evalPrompt,
							"error":  err.Error(),
							"passed": false,
						})
						overallFailed++
						continue
					}

					assertions, _ := evalCase["assertions"].([]any)
					assertionResults := make([]map[string]any, 0, len(assertions))
					caseFailed := false
					for _, rawAssertion := range assertions {
						assertion := pyStr(rawAssertion)
						judged := judge.Evaluate(assertion, evalPrompt, response.Content,
							"Evaluate whether the skill response satisfies the assertion.")
						passes := pyTruthy(judged["passes"])
						var explanation any = ""
						if value, present := judged["explanation"]; present {
							explanation = value
						}

						assertionResults = append(assertionResults, map[string]any{
							"text":        assertion,
							"passes":      passes,
							"explanation": explanation,
						})
						if !passes {
							caseFailed = true
							overallFailed++
						}

						if !useJSON {
							icon := "✗"
							if passes {
								icon = "✓"
							}
							fmt.Fprintf(r.Stdout, "  %s %s\n", icon, assertion)
							if !passes {
								fmt.Fprintf(r.Stdout, "    → %s\n", pyStr(explanation))
							}
						}
					}

					evalResults = append(evalResults, map[string]any{
						"id":         caseID,
						"prompt":     evalPrompt,
						"response":   response.Content,
						"assertions": assertionResults,
						"passed":     !caseFailed,
					})
				}
			}

			passedCount := 0
			for _, evalResult := range evalResults {
				if pyTruthy(evalResult["passed"]) {
					passedCount++
				}
			}
			result["evals"] = evalResults
			result["total_evals"] = len(evalResults)
			result["total_passed"] = passedCount
			result["total_failed"] = len(evalResults) - passedCount

			if !useJSON {
				fmt.Fprintf(r.Stdout, "\n%d/%d eval(s) passed.\n", passedCount, len(evalResults))
			}
		}
	}

	if useJSON {
		r.outJSON(result)
	}

	if !useJSON {
		fmt.Fprintln(r.Stdout)
		if overallFailed == 0 {
			fmt.Fprintf(r.Stdout, "✓ %s — all tests passed.\n", skill.Name)
		} else {
			fmt.Fprintf(r.Stdout, "✗ %s — %d failure(s).\n", skill.Name, overallFailed)
		}
	}

	if overallFailed == 0 {
		return 0
	}
	return 1
}

// Parity: framework/skills/cli.py::cmd_skill_trust
func (r *Runner) cmdSkillTrust(args *parsedArgs) int {
	projectPath := resolvePath(args.str("project_path"))

	if _, err := os.Stat(projectPath); err != nil {
		fmt.Fprintf(r.Stderr, "Error: path does not exist: %s\n", projectPath)
		return 1
	}

	if _, err := os.Stat(filepath.Join(projectPath, ".git")); err != nil {
		fmt.Fprintf(r.Stderr, "Error: %s is not a git repository (no .git directory).\n", projectPath)
		return 1
	}

	remote, err := r.RunGitRemote(projectPath)
	if err != nil {
		if errors.Is(err, errGitRemoteTimeout) {
			fmt.Fprintln(r.Stderr, "Error: git remote lookup timed out.")
			return 1
		}
		var exitErr *commandExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(r.Stderr, "Error: no remote 'origin' configured in this repository.")
			return 1
		}
		fmt.Fprintf(r.Stderr, "Error reading git remote: %s\n", err.Error())
		return 1
	}

	repoKey := skills.NormalizeRemoteURL(strings.TrimSpace(remote))
	r.NewTrustStore().Trust(repoKey, projectPath)

	fmt.Fprintf(r.Stdout, "✓ Trusted: %s\n", repoKey)
	fmt.Fprintln(r.Stdout, "  Stored in ~/.hive/trusted_repos.json")
	fmt.Fprintln(r.Stdout, "  Skills from this repository will load without prompting in future runs.")
	return 0
}

// Parity: framework/skills/cli.py::_derive_name_from_url
func deriveNameFromURL(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, "/")
	parts := strings.Split(trimmed, "/")
	last := parts[len(parts)-1]
	return strings.TrimSuffix(last, ".git")
}

// Parity: framework/skills/cli.py::_doctor_skill_file
func (r *Runner) doctorSkillFile(skillName, skillMD string, jsonMode bool, scope string) (doctorReport, int) {
	report := doctorReport{
		Name:     skillName,
		Scope:    scope,
		Errors:   []string{},
		Warnings: []string{},
	}

	parsed := skills.ParseSkillMD(skillMD, "user")
	if parsed == nil {
		msg := fmt.Sprintf("SKILL.md not parseable: %s", skillMD)
		report.Errors = append(report.Errors, msg)
		report.Parseable = false
		if !jsonMode {
			fmt.Fprintf(r.Stdout, "  ✗ %s\n", msg)
			return report, 1
		}
		return report, len(report.Errors)
	}
	report.Parseable = true
	if !jsonMode {
		fmt.Fprintln(r.Stdout, "  ✓ SKILL.md parseable")
	}

	baseDir := filepath.Dir(skillMD)

	scriptsDir := filepath.Join(baseDir, "scripts")
	if info, err := os.Stat(scriptsDir); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(scriptsDir); err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if !entry.IsDir() {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				scriptPath := filepath.Join(scriptsDir, name)
				if _, err := os.Stat(scriptPath); err != nil {
					msg := fmt.Sprintf("Script missing: %s", name)
					report.Errors = append(report.Errors, msg)
					if !jsonMode {
						fmt.Fprintf(r.Stdout, "  ✗ %s\n", msg)
					}
					continue
				}
				if !r.IsExecutable(scriptPath) {
					msg := fmt.Sprintf("Script not executable: %s  (run: chmod +x %s)", name, scriptPath)
					report.Errors = append(report.Errors, msg)
					if !jsonMode {
						fmt.Fprintf(r.Stdout, "  ✗ %s\n", msg)
					}
					continue
				}
				if !jsonMode {
					fmt.Fprintf(r.Stdout, "  ✓ Script executable: %s\n", name)
				}
			}
		}
	}

	referencesDir := filepath.Join(baseDir, "references")
	if info, err := os.Stat(referencesDir); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(referencesDir); err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if !entry.IsDir() {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				refPath := filepath.Join(referencesDir, name)
				if !r.Readable(refPath) {
					msg := fmt.Sprintf("Reference not readable: %s", name)
					report.Errors = append(report.Errors, msg)
					if !jsonMode {
						fmt.Fprintf(r.Stdout, "  ✗ %s\n", msg)
					}
					continue
				}
				if !jsonMode {
					fmt.Fprintf(r.Stdout, "  ✓ Reference readable: %s\n", name)
				}
			}
		}
	}

	for _, tool := range parsed.AllowedTools {
		toolName := tool
		if index := strings.LastIndex(toolName, "/"); index >= 0 {
			toolName = toolName[index+1:]
		}
		if index := strings.Index(toolName, "("); index >= 0 {
			toolName = toolName[:index]
		}
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		if _, err := r.LookPath(toolName); err != nil {
			msg := fmt.Sprintf("Tool not found in PATH: %s  (may be an MCP tool — OK)", toolName)
			report.Warnings = append(report.Warnings, msg)
			if !jsonMode {
				fmt.Fprintf(r.Stdout, "  ! %s\n", msg)
			}
		}
	}

	return report, len(report.Errors)
}

// Parity: framework/skills/cli.py::_prompt_yes_no
func (r *Runner) promptYesNo(prompt string) bool {
	if !r.StdinIsTTY {
		return false
	}
	fmt.Fprint(r.Stdout, prompt)
	reader := bufio.NewReader(r.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func (r *Runner) readPromptLine(prompt string) (string, error) {
	fmt.Fprint(r.Stdout, prompt)
	reader := bufio.NewReader(r.Stdin)
	return reader.ReadString('\n')
}

func truncatePrompt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func mapStringDefault(entry map[string]any, key, fallback string) string {
	if value, ok := entry[key].(string); ok {
		return value
	}
	return fallback
}

func mapValueDefault(entry map[string]any, key string, fallback any) any {
	if value, present := entry[key]; present {
		return value
	}
	return fallback
}
