package nativecli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// defaultCacheDir mirrors mcp2cli: MCP2CLI_CACHE_DIR, else <hive home>/cache/mcp2cli.
// It reads the invocation's environment rather than the process's so tests and
// callers can isolate the cache without mutating global state.
func defaultCacheDir(environ []string) string {
	configured := ""
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if found && key == "MCP2CLI_CACHE_DIR" && value != "" {
			configured = value // last assignment wins, like exec
		}
	}
	if configured != "" {
		return configured
	}
	home, err := hivehome.Resolve()
	if err != nil {
		return filepath.Join(os.TempDir(), "mcp2cli")
	}
	return filepath.Join(home.CacheDir(), "mcp2cli")
}

// cacheKeyFor derives mcp2cli's 16-hex cache key from a source description,
// hashing json.dumps(config, sort_keys=True).
func cacheKeyFor(source string, authHeaders []Field, transport string, envVars []Field, isStdio bool) string {
	headers := make([]any, 0, len(authHeaders))
	for _, header := range authHeaders {
		headers = append(headers, []any{header.Key, header.Value})
	}
	sortPairs(headers)
	env := Ordered{}
	envPairs := append([]Field{}, envVars...)
	sort.SliceStable(envPairs, func(i, j int) bool { return envPairs[i].Key < envPairs[j].Key })
	for _, pair := range envPairs {
		env = append(env, Field{Key: pair.Key, Value: pair.Value})
	}
	config := Ordered{
		{Key: "auth_headers", Value: headers},
		{Key: "env_vars", Value: env},
		{Key: "is_stdio", Value: isStdio},
		{Key: "source", Value: source},
		{Key: "transport", Value: transport},
	}
	digest := sha256.Sum256([]byte(MarshalPy(config)))
	return hex.EncodeToString(digest[:])[:16]
}

func sortPairs(pairs []any) {
	sort.SliceStable(pairs, func(i, j int) bool {
		left, _ := pairs[i].([]any)
		right, _ := pairs[j].([]any)
		leftKey := fmt.Sprint(left[0])
		rightKey := fmt.Sprint(right[0])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return fmt.Sprint(left[1]) < fmt.Sprint(right[1])
	})
}

// sourceHash derives the usage-tracking bucket for a server source.
func sourceHash(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])[:16]
}

// loadCachedTools reads <dir>/<key>_tools.json, treating a missing, stale or
// corrupt entry as a miss.
func loadCachedTools(dir, key string, ttl int, now time.Time) ([]any, bool) {
	path := filepath.Join(dir, key+"_tools.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	age := now.Sub(info.ModTime())
	if age >= time.Duration(ttl)*time.Second {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		return nil, false
	}
	tools, ok := parsed.([]any)
	if !ok {
		return nil, false
	}
	return tools, true
}

// saveCachedTools writes the tool list atomically.
func saveCachedTools(dir, key string, tools []any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, key+"_tools.json")
	payload := MarshalPy(anySlice(tools))
	temporary, err := os.CreateTemp(dir, key+"_tools.json.*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.WriteString(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func anySlice(items []any) any {
	if items == nil {
		return []any{}
	}
	return items
}

// usageEntry is one tool's recorded call count and last-used timestamp.
type usageEntry struct {
	count    int
	lastUsed string
}

// loadUsage reads usage.json, tolerating any failure as "no data".
func loadUsage(dir string) map[string]map[string]usageEntry {
	path := filepath.Join(dir, "usage.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]map[string]usageEntry{}
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		return map[string]map[string]usageEntry{}
	}
	root, ok := parsed.(Ordered)
	if !ok {
		return map[string]map[string]usageEntry{}
	}
	usage := map[string]map[string]usageEntry{}
	for _, bucket := range root {
		tools, ok := bucket.Value.(Ordered)
		if !ok {
			continue
		}
		entries := map[string]usageEntry{}
		for _, tool := range tools {
			fields, ok := tool.Value.(Ordered)
			if !ok {
				continue
			}
			entry := usageEntry{}
			if count, found := fields.Lookup("count"); found {
				if number, ok := count.(json.Number); ok {
					if parsedCount, err := number.Int64(); err == nil {
						entry.count = int(parsedCount)
					}
				}
			}
			if lastUsed, found := fields.Lookup("last_used"); found {
				if text, ok := lastUsed.(string); ok {
					entry.lastUsed = text
				}
			}
			entries[tool.Key] = entry
		}
		usage[bucket.Key] = entries
	}
	return usage
}

// recordUsage bumps the call count for a tool, last-write-wins.
func recordUsage(dir, source, toolName string, now time.Time) {
	usage := loadUsage(dir)
	bucket, ok := usage[source]
	if !ok {
		bucket = map[string]usageEntry{}
		usage[source] = bucket
	}
	entry := bucket[toolName]
	entry.count++
	entry.lastUsed = now.UTC().Format("2006-01-02T15:04:05.000000+00:00")
	bucket[toolName] = entry

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	root := Ordered{}
	for _, bucketKey := range sortedKeys(usage) {
		tools := Ordered{}
		bucketEntries := usage[bucketKey]
		names := make([]string, 0, len(bucketEntries))
		for name := range bucketEntries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := bucketEntries[name]
			tools = append(tools, Field{Key: name, Value: Ordered{
				{Key: "count", Value: entry.count},
				{Key: "last_used", Value: entry.lastUsed},
			}})
		}
		root = append(root, Field{Key: bucketKey, Value: tools})
	}
	_ = os.WriteFile(filepath.Join(dir, "usage.json"), []byte(MarshalPyPretty(root)+"\n"), 0o600)
}

func sortedKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// resolveSortMode picks the effective sort: an explicit --sort wins, else
// "usage" when usage data exists for this source, else "default".
func resolveSortMode(explicit, dir, source string) string {
	if explicit != "" {
		return explicit
	}
	if len(loadUsage(dir)[source]) > 0 {
		return "usage"
	}
	return "default"
}

// sortCommands applies a sort mode to a command list.
func sortCommands(commands []Command, mode, dir, source string) []Command {
	switch mode {
	case "alpha":
		sorted := append([]Command{}, commands...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
		return sorted
	case "usage", "recent":
		usage := loadUsage(dir)[source]
		if len(usage) == 0 {
			return commands
		}
		sorted := append([]Command{}, commands...)
		sort.SliceStable(sorted, func(i, j int) bool {
			left := usage[toolKey(sorted[i])]
			right := usage[toolKey(sorted[j])]
			if mode == "recent" {
				return left.lastUsed > right.lastUsed
			}
			return left.count > right.count
		})
		return sorted
	default:
		return commands
	}
}

func toolKey(command Command) string {
	if command.ToolName != "" {
		return command.ToolName
	}
	return command.Name
}

// trimTrailingNewline mirrors mcp2cli's resolve_secret for file: values.
func trimTrailingNewline(value string) string {
	return strings.TrimSuffix(value, "\n")
}
