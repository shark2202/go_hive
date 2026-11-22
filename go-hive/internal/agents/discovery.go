package agents

// Parity: framework/agents/discovery.py::discover_agents

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var excludedJSONStems = map[string]struct{}{
	"agent":         {},
	"flowchart":     {},
	"triggers":      {},
	"configuration": {},
	"metadata":      {},
	"tasks":         {},
}

type WorkerEntry struct {
	Name        any
	ConfigPath  string
	Description any
	ToolCount   int
	Task        any
	SpawnedAt   any
	QueenName   string
	ColonyID    string
}

func (w WorkerEntry) ToDict() map[string]any {
	return map[string]any{
		"name":        w.Name,
		"config_path": w.ConfigPath,
		"description": w.Description,
		"tool_count":  w.ToolCount,
		"task":        w.Task,
		"spawned_at":  w.SpawnedAt,
		"queen_name":  w.QueenName,
		"colony_id":   w.ColonyID,
	}
}

type AgentEntry struct {
	Path         string
	Name         string
	Description  any
	Category     string
	SessionCount int
	RunCount     int
	NodeCount    int
	ToolCount    int
	Tags         []string
	LastActive   *string
	CreatedAt    any
	Icon         any
	Workers      []WorkerEntry
}

func DiscoverAgents(coloniesDir string) map[string][]AgentEntry {
	groups := map[string][]AgentEntry{}
	info, err := os.Stat(coloniesDir)
	if err != nil || !info.IsDir() {
		return groups
	}
	entries := []AgentEntry{}
	seen := map[string]struct{}{}
	for _, name := range sortedEntryNames(coloniesDir) {
		path := filepath.Join(coloniesDir, name)
		if !IsColonyDir(path) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		displayName := pythonTitle(strings.ReplaceAll(name, "_", " "))

		colonyQueenName := ""
		var colonyCreatedAt any
		var colonyIcon any
		metadataPath := filepath.Join(path, "metadata.json")
		if data, readErr := os.ReadFile(metadataPath); readErr == nil {
			var mdata map[string]any
			if json.Unmarshal(data, &mdata) == nil && mdata != nil {
				if value, ok := mdata["queen_name"].(string); ok {
					colonyQueenName = value
				}
				if value, present := mdata["created_at"]; present {
					colonyCreatedAt = value
				}
				if value, present := mdata["icon"]; present {
					colonyIcon = value
				}
				if truthy(mdata["deleted"]) {
					continue
				}
			}
		}
		if !truthy(colonyCreatedAt) {
			if created, ok := birthTimeISO(path); ok {
				colonyCreatedAt = created
			} else {
				// Python's `or` chain yields None when neither source has a value.
				colonyCreatedAt = nil
			}
		}

		workerEntries := []WorkerEntry{}
		var description any = ""
		for _, configPath := range FindWorkerConfigs(path) {
			data, readErr := os.ReadFile(configPath)
			if readErr != nil {
				continue
			}
			var config map[string]any
			if json.Unmarshal(data, &config) != nil || config == nil {
				continue
			}
			stem, _ := pyStemSuffix(filepath.Base(configPath))
			var name any = stem
			if rawName, present := config["name"]; present {
				name = rawName
			}
			var workerDescription any = ""
			if rawDescription, present := config["description"]; present {
				workerDescription = rawDescription
			}
			toolCount, ok := toolListLen(config)
			if !ok {
				continue
			}
			var task any = ""
			if rawGoal, present := config["goal"]; present {
				goal, ok := rawGoal.(map[string]any)
				if !ok {
					continue
				}
				if rawTask, present := goal["description"]; present {
					task = rawTask
				}
			}
			var spawnedAt any = ""
			if rawSpawned, present := config["spawned_at"]; present {
				spawnedAt = rawSpawned
			}
			workerEntries = append(workerEntries, WorkerEntry{
				Name:        name,
				ConfigPath:  configPath,
				Description: workerDescription,
				ToolCount:   toolCount,
				Task:        task,
				SpawnedAt:   spawnedAt,
				QueenName:   colonyQueenName,
				ColonyID:    filepath.Base(path),
			})
			if !truthy(description) {
				description = workerDescription
			}
		}

		toolCount := 0
		for _, worker := range workerEntries {
			if worker.ToolCount > toolCount {
				toolCount = worker.ToolCount
			}
		}

		resolvedPath, resolveErr := ResolvePath(path)
		if resolveErr != nil {
			resolvedPath = path
		}
		entries = append(entries, AgentEntry{
			Path:         resolvedPath,
			Name:         displayName,
			Description:  description,
			Category:     "Your Agents",
			SessionCount: CountSessions(path),
			RunCount:     CountRuns(path),
			NodeCount:    len(workerEntries),
			ToolCount:    toolCount,
			Tags:         []string{},
			LastActive:   LastActive(path),
			CreatedAt:    colonyCreatedAt,
			Icon:         colonyIcon,
			Workers:      workerEntries,
		})
	}
	if len(entries) > 0 {
		groups["Your Agents"] = entries
	}
	return groups
}

// Parity: framework/agents/discovery.py::_get_last_active
func LastActive(agentPath string) *string {
	var latest *string

	workersRoot := filepath.Join(agentPath, "workers")
	if info, err := os.Stat(workersRoot); err == nil && info.IsDir() {
		for _, name := range sortedEntryNames(workersRoot) {
			sessionDir := filepath.Join(workersRoot, name)
			if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sessionDir, "state.json"))
			if err != nil {
				continue
			}
			var state map[string]any
			if json.Unmarshal(data, &state) != nil {
				continue
			}
			timestamps, ok := state["timestamps"].(map[string]any)
			if !ok {
				continue
			}
			updatedAt, ok := timestamps["updated_at"].(string)
			if !ok || updatedAt == "" {
				continue
			}
			if latest == nil || updatedAt > *latest {
				value := updatedAt
				latest = &value
			}
		}
	}

	queensRoot := filepath.Join(agentPath, "queens")
	if info, err := os.Stat(queensRoot); err == nil && info.IsDir() {
		for _, queenName := range sortedEntryNames(queensRoot) {
			queenDir := filepath.Join(queensRoot, queenName)
			if info, err := os.Stat(queenDir); err != nil || !info.IsDir() {
				continue
			}
			sessionsRoot := filepath.Join(queenDir, "sessions")
			if info, err := os.Stat(sessionsRoot); err != nil || !info.IsDir() {
				continue
			}
			for _, sessionName := range sortedEntryNames(sessionsRoot) {
				sessionDir := filepath.Join(sessionsRoot, sessionName)
				info, err := os.Stat(sessionDir)
				if err != nil || !info.IsDir() {
					continue
				}
				value := isoformatLocal(info.ModTime())
				if latest == nil || value > *latest {
					latest = &value
				}
			}
		}
	}

	return latest
}

// Parity: framework/agents/discovery.py::_count_sessions
func CountSessions(colonyDir string) int {
	if info, err := os.Stat(colonyDir); err != nil || !info.IsDir() {
		return 0
	}
	count := 0
	workersDir := filepath.Join(colonyDir, "workers")
	if info, err := os.Stat(workersDir); err == nil && info.IsDir() {
		for _, name := range sortedEntryNames(workersDir) {
			if info, err := os.Stat(filepath.Join(workersDir, name)); err == nil && info.IsDir() {
				count++
			}
		}
	}
	queensDir := filepath.Join(colonyDir, "queens")
	if info, err := os.Stat(queensDir); err == nil && info.IsDir() {
		for _, queenName := range sortedEntryNames(queensDir) {
			queenDir := filepath.Join(queensDir, queenName)
			if info, err := os.Stat(queenDir); err != nil || !info.IsDir() {
				continue
			}
			sessionsDir := filepath.Join(queenDir, "sessions")
			if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
				continue
			}
			for _, sessionName := range sortedEntryNames(sessionsDir) {
				if info, err := os.Stat(filepath.Join(sessionsDir, sessionName)); err == nil && info.IsDir() {
					count++
				}
			}
		}
	}
	return count
}

// Parity: framework/agents/discovery.py::_count_runs
func CountRuns(colonyDir string) int {
	sessionsDir := filepath.Join(colonyDir, "workers")
	if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
		return 0
	}
	runIDs := map[string]struct{}{}
	for _, name := range sortedEntryNames(sessionsDir) {
		sessionDir := filepath.Join(sessionsDir, name)
		info, err := os.Stat(sessionDir)
		if err != nil || !info.IsDir() || !strings.HasPrefix(name, "session_") {
			continue
		}
		filepath.WalkDir(sessionDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || entry.Name() != "runs.jsonl" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || !utf8.Valid(data) {
				return nil
			}
			for _, raw := range splitLinesPython(string(data)) {
				line := stripPython(raw)
				if line == "" {
					continue
				}
				decoder := json.NewDecoder(strings.NewReader(line))
				decoder.UseNumber()
				var record map[string]any
				if decoder.Decode(&record) != nil || record == nil {
					return nil
				}
				if _, trailing := decoder.Token(); trailing != io.EOF {
					return nil
				}
				rid := record["run_id"]
				if !truthy(rid) {
					continue
				}
				key, hashable := pySetKey(rid)
				if !hashable {
					return nil
				}
				runIDs[key] = struct{}{}
			}
			return nil
		})
	}
	return len(runIDs)
}

// Parity: framework/agents/discovery.py::_is_colony_dir
func IsColonyDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return len(FindWorkerConfigs(path)) > 0
}

// Parity: framework/agents/discovery.py::_find_worker_configs
func FindWorkerConfigs(colonyDir string) []string {
	info, err := os.Stat(colonyDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	configs := []string{}
	for _, name := range sortedEntryNames(colonyDir) {
		path := filepath.Join(colonyDir, name)
		entryInfo, err := os.Stat(path)
		if err != nil || !entryInfo.Mode().IsRegular() {
			continue
		}
		stem, suffix := pyStemSuffix(name)
		if suffix != ".json" {
			continue
		}
		if _, excluded := excludedJSONStems[stem]; excluded {
			continue
		}
		configs = append(configs, path)
	}
	return configs
}

// Parity: framework/agents/discovery.py::_extract_agent_stats
func ExtractAgentStats(agentPath string) (int, int, []string) {
	tags := []string{}
	workerConfigs := FindWorkerConfigs(agentPath)
	if len(workerConfigs) == 0 {
		return 0, 0, tags
	}
	allTools := map[string]struct{}{}
	for _, configPath := range workerConfigs {
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		var config map[string]any
		if json.Unmarshal(data, &config) != nil || config == nil {
			continue
		}
		tools, ok := config["tools"].([]any)
		if !ok {
			continue
		}
		for _, tool := range tools {
			key, hashable := pySetKey(tool)
			if !hashable {
				break
			}
			allTools[key] = struct{}{}
		}
	}
	return len(workerConfigs), len(allTools), tags
}

func pySetKey(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "none", true
	case string:
		return "s:" + typed, true
	case bool:
		if typed {
			return "n:1", true
		}
		return "n:0", true
	case float64:
		return "n:" + strconv.FormatFloat(typed, 'g', -1, 64), true
	case json.Number:
		value, _ := typed.Float64()
		return "n:" + strconv.FormatFloat(value, 'g', -1, 64), true
	}
	return "", false
}

func sortedEntryNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func toolListLen(config map[string]any) (int, bool) {
	raw, present := config["tools"]
	if !present {
		return 0, true
	}
	return pyLen(raw)
}

func pyLen(value any) (int, bool) {
	switch typed := value.(type) {
	case string:
		return len([]rune(typed)), true
	case []any:
		return len(typed), true
	case map[string]any:
		return len(typed), true
	}
	return 0, false
}

func pythonTitle(text string) string {
	var builder strings.Builder
	previousIsCased := false
	for _, r := range text {
		if unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) {
			if previousIsCased {
				builder.WriteRune(unicode.ToLower(r))
			} else {
				builder.WriteRune(unicode.ToTitle(r))
			}
			previousIsCased = true
			continue
		}
		builder.WriteRune(r)
		previousIsCased = false
	}
	return builder.String()
}

// Python str.splitlines boundaries, without line endings.
func splitLinesPython(text string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch r {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			lines = append(lines, text[start:i])
			i += size
			start = i
		case '\r':
			lines = append(lines, text[start:i])
			i += size
			if i < len(text) && text[i] == '\n' {
				i++
			}
			start = i
		default:
			i += size
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// Python str.strip whitespace, including the control separators 0x1c-0x1f.
func stripPython(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case json.Number:
		value, _ := typed.Float64()
		return value != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func isoformatLocal(value time.Time) string {
	local := value.Local()
	if local.Nanosecond() == 0 {
		return local.Format("2006-01-02T15:04:05")
	}
	return local.Format("2006-01-02T15:04:05.000000")
}

func isoformatUTC(value time.Time) string {
	utc := value.UTC()
	if utc.Nanosecond() == 0 {
		return utc.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return utc.Format("2006-01-02T15:04:05.000000") + "+00:00"
}

func birthTimeISO(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	created, ok := birthTime(info)
	if !ok {
		return "", false
	}
	return isoformatUTC(created), true
}
