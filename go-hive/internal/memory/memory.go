package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var GlobalMemoryCategories = []string{"profile", "preference", "environment", "feedback"}

const (
	MaxFiles         = 200
	MaxFileSizeBytes = 4096
	HeaderLineLimit  = 30
)

func GlobalMemoryDir(root string) string {
	return filepath.Join(root, "memories", "global")
}

func ColonyMemoryDir(root, colonyID string) string {
	return filepath.Join(root, "memories", "colonies", colonyID)
}

func QueenMemoryDir(root, queenName string) string {
	if queenName == "" {
		queenName = "default"
	}
	return filepath.Join(root, "memories", "agents", "queens", queenName)
}

func AgentMemoryDir(root, agentName string) string {
	return filepath.Join(root, "memories", "agents", agentName)
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n?`)

func ParseFrontmatter(text string) map[string]string {
	m := frontmatterRe.FindStringSubmatch(text)
	if m == nil {
		return map[string]string{}
	}
	result := make(map[string]string)
	for _, raw := range splitLines(m[1]) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 1 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])
		if val != "" {
			result[key] = val
		}
	}
	return result
}

func ParseGlobalMemoryCategory(raw *string) *string {
	if raw == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*raw))
	for _, category := range GlobalMemoryCategories {
		if normalized == category {
			return &normalized
		}
	}
	return nil
}

type MemoryFile struct {
	Filename    string
	Path        string
	Name        *string
	Type        *string
	Description *string
	HeaderLines []string
	MTime       float64
}

func MemoryFileFromPath(path string) MemoryFile {
	filename := filepath.Base(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return MemoryFile{Filename: filename, Path: path}
	}
	text := string(data)
	fm := ParseFrontmatter(text)
	lines := splitLines(text)
	if len(lines) > HeaderLineLimit {
		lines = lines[:HeaderLineLimit]
	}

	mtime := 0.0
	if info, statErr := os.Stat(path); statErr == nil {
		mtime = float64(info.ModTime().UnixNano()) / 1e9
	}

	var name, description *string
	if v, ok := fm["name"]; ok {
		name = &v
	}
	if v, ok := fm["description"]; ok {
		description = &v
	}
	var memType *string
	if raw, ok := fm["type"]; ok {
		memType = ParseGlobalMemoryCategory(&raw)
	}
	return MemoryFile{
		Filename:    filename,
		Path:        path,
		Name:        name,
		Type:        memType,
		Description: description,
		HeaderLines: lines,
		MTime:       mtime,
	}
}

func ScanMemoryFiles(dir string) []MemoryFile {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type candidate struct {
		name  string
		mtime float64
	}
	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		stat, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil || stat.IsDir() {
			continue
		}
		candidates = append(candidates, candidate{
			name:  name,
			mtime: float64(stat.ModTime().UnixNano()) / 1e9,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].mtime != candidates[j].mtime {
			return candidates[i].mtime > candidates[j].mtime
		}
		return candidates[i].name < candidates[j].name
	})
	if len(candidates) > MaxFiles {
		candidates = candidates[:MaxFiles]
	}

	files := make([]MemoryFile, 0, len(candidates))
	for _, c := range candidates {
		files = append(files, MemoryFileFromPath(filepath.Join(dir, c.name)))
	}
	return files
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func SlugifyMemoryName(raw string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "-"), "-")
	if slug == "" {
		return "memory"
	}
	return slug
}

func AllocateMemoryFilename(dir, name, suffix string) string {
	if suffix == "" {
		suffix = ".md"
	}
	base := SlugifyMemoryName(name)
	candidate := base + suffix
	for counter := 2; ; counter++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); err != nil {
			return candidate
		}
		candidate = base + "-" + itoa(counter) + suffix
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func BuildMemoryDocument(name, description, memType, body string) string {
	return "---\nname: " + strings.TrimSpace(name) +
		"\ndescription: " + strings.TrimSpace(description) +
		"\ntype: " + strings.TrimSpace(memType) +
		"\n---\n\n" + strings.TrimSpace(body) + "\n"
}

func FormatMemoryManifest(files []MemoryFile) string {
	lines := make([]string, 0, len(files))
	for _, mf := range files {
		t := "unknown"
		if mf.Type != nil {
			t = *mf.Type
		}
		desc := "(no description)"
		if mf.Description != nil {
			desc = *mf.Description
		}
		lines = append(lines, "["+t+"] "+mf.Filename+": "+desc)
	}
	return strings.Join(lines, "\n")
}

func InitMemoryDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}
