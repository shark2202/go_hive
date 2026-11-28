package mcpregistry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	DefaultIndexURL             = "https://raw.githubusercontent.com/aden-hive/hive-mcp-registry/main/registry_index.json"
	DefaultRefreshIntervalHours = 24

	lastFetchedFilename       = "last_fetched"
	legacyLastFetchedFilename = "last_fetched.json"
)

var retiredBundledServers = map[string]string{
	"hive_tools":     "mcp_server.py",
	"hive-tools":     "mcp_server.py",
	"terminal-tools": "terminal_tools_server.py",
	"shell-tools":    "shell_tools_server.py",
	"chart-tools":    "chart_tools_server.py",
	"memory-tools":   "memory_tools_server.py",
	"files-tools":    "files_server.py",
	"gcu-tools":      "gcu.server",
}

func defaultConfig() map[string]any {
	return map[string]any{
		"index_url":              DefaultIndexURL,
		"refresh_interval_hours": float64(DefaultRefreshIntervalHours),
	}
}

type HTTPFetch func(rawURL string, timeout time.Duration) ([]byte, error)

func DefaultHTTPFetch(rawURL string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{URL: rawURL, Status: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

type HTTPStatusError struct {
	URL    string
	Status int
}

func (e *HTTPStatusError) Error() string {
	return "registry index fetch failed: " + e.URL + " -> HTTP " + strconv.Itoa(e.Status)
}

type Registry struct {
	base          string
	Fetch         HTTPFetch
	Home          hivehome.Home
	Now           func() time.Time
	Manager       ConnectionManager
	ClientFactory ClientFactory
}

func NewAtHome(home hivehome.Home) *Registry {
	return &Registry{base: home.MCPRegistryDir(), Home: home}
}

func NewAtBase(basePath string) *Registry {
	return &Registry{base: basePath}
}

func Default() (*Registry, error) {
	home, err := hivehome.Resolve()
	if err != nil {
		return nil, err
	}
	return NewAtHome(home), nil
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Registry) Base() string { return r.base }

func (r *Registry) InstalledPath() string { return filepath.Join(r.base, "installed.json") }

func (r *Registry) ConfigPath() string { return filepath.Join(r.base, "config.json") }

func (r *Registry) CacheDir() string { return filepath.Join(r.base, "cache") }

func (r *Registry) indexPath() string {
	return filepath.Join(r.CacheDir(), "registry_index.json")
}

func (r *Registry) lastFetchedPath() string {
	return filepath.Join(r.CacheDir(), lastFetchedFilename)
}

func (r *Registry) legacyLastFetchedPath() string {
	return filepath.Join(r.CacheDir(), legacyLastFetchedFilename)
}

func (r *Registry) Initialize() error {
	if err := r.bootstrapIO(); err != nil {
		return err
	}
	return r.retireBundledServers()
}

func (r *Registry) EnsureDefaults() []string {
	_ = r.Initialize()
	return nil
}

func (r *Registry) bootstrapIO() error {
	if err := os.MkdirAll(r.base, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(r.CacheDir(), 0o700); err != nil {
		return err
	}
	if !atomicfs.Exists(r.ConfigPath()) {
		if err := atomicfs.WriteJSON(r.ConfigPath(), defaultConfig(), 0o600); err != nil {
			return err
		}
	}
	if !atomicfs.Exists(r.InstalledPath()) {
		if err := atomicfs.WriteJSON(r.InstalledPath(), map[string]any{"servers": map[string]any{}}, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) retireBundledServers() error {
	data, err := r.readInstalledRaw()
	if err != nil {
		return err
	}
	servers := asMap(data["servers"])
	if servers == nil {
		return nil
	}
	removed := false
	for name, script := range retiredBundledServers {
		raw, ok := servers[name]
		if !ok {
			continue
		}
		entry := asMap(raw)
		manifest := asMap(entry["manifest"])
		stdio := asMap(manifest["stdio"])
		args := asStringSlice(stdio["args"])
		if contains(args, script) {
			delete(servers, name)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	return r.writeInstalled(data)
}

func (r *Registry) readInstalledRaw() (map[string]any, error) {
	data, err := readJSONFile(r.InstalledPath())
	if err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{"servers": map[string]any{}}, nil
	}
	if _, ok := data["servers"]; !ok {
		data["servers"] = map[string]any{}
	}
	return data, nil
}

func (r *Registry) ReadInstalled() (map[string]any, error) {
	if !atomicfs.Exists(r.InstalledPath()) {
		if err := r.Initialize(); err != nil {
			return nil, err
		}
	}
	data, err := readJSONFile(r.InstalledPath())
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	servers := asMap(data["servers"])
	if servers == nil {
		servers = map[string]any{}
		data["servers"] = servers
	}
	return data, nil
}

func (r *Registry) WriteInstalled(data map[string]any) error {
	return r.writeInstalled(data)
}

func (r *Registry) writeInstalled(data map[string]any) error {
	return r.writeJSON(r.InstalledPath(), data)
}

func (r *Registry) readConfig() (map[string]any, error) {
	if !atomicfs.Exists(r.ConfigPath()) {
		if err := r.Initialize(); err != nil {
			return nil, err
		}
	}
	data, err := readJSONFile(r.ConfigPath())
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func (r *Registry) readCachedIndex() (map[string]any, error) {
	data, err := readJSONFile(r.indexPath())
	if err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{"servers": map[string]any{}}, nil
	}
	if _, ok := data["servers"]; !ok {
		data["servers"] = map[string]any{}
	}
	return data, nil
}

func (r *Registry) writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfs.WriteJSON(path, v, 0o600)
}

func readJSONFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, &DecodeError{Path: path, Err: err}
	}
	return out, nil
}

type DecodeError struct {
	Path string
	Err  error
}

func (e *DecodeError) Error() string { return "invalid JSON in " + e.Path + ": " + e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

func (r *Registry) effectiveManifest(name string, entry map[string]any, cachedIndex map[string]any) map[string]any {
	manifest := asMap(entry["manifest"])
	if manifest == nil {
		manifest = map[string]any{}
	}
	if asString(entry["source"]) != "registry" {
		return manifest
	}
	if cachedIndex == nil {
		cachedIndex, _ = r.readCachedIndex()
	}
	if servers := asMap(cachedIndex["servers"]); servers != nil {
		if entryFromIndex := asMap(servers[name]); entryFromIndex != nil {
			return entryFromIndex
		}
	}
	if len(manifest) > 0 {
		return manifest
	}
	return map[string]any{}
}

type AddLocalOptions struct {
	Transport   string
	Command     string
	Args        []string
	Env         map[string]string
	Cwd         string
	URL         string
	Headers     map[string]string
	SocketPath  string
	Description string
	Manifest    map[string]any
	Mode        string
}

func (r *Registry) AddLocal(name string, opts AddLocalOptions) error {
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	servers := serversOf(data)
	if _, exists := servers[name]; exists {
		return &Error{
			Code: CodeInstallFailed,
			What: "Server '" + name + "' already exists",
			Why:  "A server with this name is already registered locally.",
			Fix:  "Run: hive mcp remove " + name + "  — then add it again.",
		}
	}

	manifest := opts.Manifest
	if manifest != nil {
		manifest = copyMap(manifest)
		manifest["name"] = name
		transportConfig := asMap(manifest["transport"])
		transport := opts.Transport
		if transport == "" {
			transport = stringOr(transportConfig["default"], "stdio")
		}
		if _, ok := manifest["transport"]; !ok {
			manifest["transport"] = map[string]any{
				"supported": []any{transport},
				"default":   transport,
			}
		}
		servers[name] = makeEntry(entryOptions{
			Source:      "local",
			Manifest:    manifest,
			Transport:   transport,
			InstalledBy: "hive mcp add",
			Mode:        opts.Mode,
		})
		return r.writeInstalled(data)
	}

	transport := opts.Transport
	if transport == "" {
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot register server '" + name + "'",
			Why:  "transport is required when manifest is not provided.",
			Fix:  "Pass --transport stdio|http|unix|sse when using hive mcp add.",
		}
	}

	built := map[string]any{
		"name":        name,
		"description": opts.Description,
		"transport": map[string]any{
			"supported": []any{transport},
			"default":   transport,
		},
	}

	switch transport {
	case "http":
		if opts.URL == "" {
			return &Error{
				Code: CodeInstallFailed,
				What: "Cannot register server '" + name + "' with http transport",
				Why:  "url is required for http transport.",
				Fix:  "Pass --url https://your-server to hive mcp add.",
			}
		}
		headers := opts.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		built["http"] = map[string]any{
			"url":     opts.URL,
			"headers": stringMapToAny(headers),
		}
	case "stdio":
		if opts.Command == "" {
			return &Error{
				Code: CodeInstallFailed,
				What: "Cannot register server '" + name + "' with stdio transport",
				Why:  "command is required for stdio transport.",
				Fix:  "Pass --command <executable> to hive mcp add.",
			}
		}
		args := opts.Args
		if args == nil {
			args = []string{}
		}
		env := opts.Env
		if env == nil {
			env = map[string]string{}
		}
		var cwd any
		if opts.Cwd != "" {
			cwd = opts.Cwd
		}
		built["stdio"] = map[string]any{
			"command": opts.Command,
			"args":    stringSliceToAny(args),
			"env":     stringMapToAny(env),
			"cwd":     cwd,
		}
	case "unix":
		if opts.SocketPath == "" {
			return &Error{
				Code: CodeInstallFailed,
				What: "Cannot register server '" + name + "' with unix transport",
				Why:  "socket_path is required for unix transport.",
				Fix:  "Pass --socket-path /path/to/socket to hive mcp add.",
			}
		}
		built["unix"] = map[string]any{"socket_path": opts.SocketPath}
		httpURL := opts.URL
		if httpURL == "" {
			httpURL = "http://localhost"
		}
		built["http"] = map[string]any{"url": httpURL}
	case "sse":
		if opts.URL == "" {
			return &Error{
				Code: CodeInstallFailed,
				What: "Cannot register server '" + name + "' with sse transport",
				Why:  "url is required for sse transport.",
				Fix:  "Pass --url https://your-server to hive mcp add.",
			}
		}
		built["sse"] = map[string]any{"url": opts.URL}
	default:
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot register server '" + name + "'",
			Why:  "Unsupported transport: '" + transport + "'.",
			Fix:  "Use one of: stdio, http, unix, sse.",
		}
	}

	servers[name] = makeEntry(entryOptions{
		Source:      "local",
		Manifest:    built,
		Transport:   transport,
		InstalledBy: "hive mcp add",
		Mode:        opts.Mode,
	})
	return r.writeInstalled(data)
}

type InstallOptions struct {
	Transport string
	Version   string
}

func (r *Registry) Install(name string, opts InstallOptions) error {
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	servers := serversOf(data)
	if _, exists := servers[name]; exists {
		return installError(name,
			"Server '"+name+"' already exists in the registry.",
			"Run: hive mcp remove "+name+"  — then install again.")
	}

	index, err := r.readCachedIndex()
	if err != nil {
		return err
	}
	indexed := asMap(asMap(index["servers"])[name])
	if indexed == nil {
		return installError(name,
			"Server '"+name+"' not found in registry index.",
			"Run: hive mcp update  — then try again.")
	}

	indexedVersion := asString(indexed["version"])
	if opts.Version != "" {
		if indexedVersion == "" {
			return &Error{
				Code: CodeVersionConflict,
				What: "Cannot pin version for '" + name + "'",
				Why:  "The registry manifest has no version field.",
				Fix:  "Run: hive mcp update  — then omit --version to use latest.",
			}
		}
		if opts.Version != indexedVersion {
			return &Error{
				Code: CodeVersionConflict,
				What: "Version mismatch for '" + name + "'",
				Why:  "Requested " + opts.Version + " but index has " + indexedVersion + ".",
				Fix:  "Run: hive mcp update  — or omit --version to use latest.",
			}
		}
	}

	supported := asStringSlice(asMap(indexed["transport"])["supported"])
	if opts.Transport != "" && len(supported) > 0 && !contains(supported, opts.Transport) {
		return &Error{
			Code: CodeInstallFailed,
			What: "Transport '" + opts.Transport + "' not supported by '" + name + "'",
			Why:  "Server supports: " + pyListRepr(supported) + ".",
			Fix:  "Use one of the supported transports: " + pyListRepr(supported) + ".",
		}
	}

	resolved := opts.Transport
	if resolved == "" {
		resolved = stringOr(asMap(indexed["transport"])["default"], "stdio")
	}

	servers[name] = makeEntry(entryOptions{
		Source:                 "registry",
		Manifest:               registryManifestSnapshot(name, indexed),
		Transport:              resolved,
		InstalledBy:            "hive mcp install",
		Pinned:                 opts.Version != "",
		AutoUpdate:             opts.Version == "",
		ResolvedPackageVersion: asString(indexed["version"]),
	})
	return r.writeInstalled(data)
}

func (r *Registry) Remove(name string) error {
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	servers := asMap(data["servers"])
	if servers == nil {
		servers = map[string]any{}
		data["servers"] = servers
	}
	if _, exists := servers[name]; !exists {
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot remove server '" + name + "'",
			Why:  "Server is not installed.",
			Fix:  "Run: hive mcp list  — to see installed servers.",
		}
	}
	delete(servers, name)
	return r.writeInstalled(data)
}

func (r *Registry) SetMode(name, mode string) error {
	if mode != "tools" && mode != "cli" {
		return &Error{
			Code: CodeInstallFailed,
			What: "Invalid mode '" + mode + "' for server '" + name + "'",
			Why:  "Mode must be 'tools' or 'cli'.",
			Fix:  "Run: hive mcp mode " + name + " cli  — or: hive mcp mode " + name + " tools",
		}
	}
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	entry := entryOf(data, name)
	if entry == nil {
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot set the mode of server '" + name + "'",
			Why:  "Server is not installed.",
			Fix:  "Run: hive mcp list  — to see installed servers.",
		}
	}
	entry["mode"] = mode
	return r.writeInstalled(data)
}

func (r *Registry) Enable(name string) error { return r.setEnabled(name, true) }

func (r *Registry) Disable(name string) error { return r.setEnabled(name, false) }

func (r *Registry) setEnabled(name string, enabled bool) error {
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	entry := entryOf(data, name)
	if entry == nil {
		verb := "disable"
		if enabled {
			verb = "enable"
		}
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot " + verb + " server '" + name + "'",
			Why:  "Server is not installed.",
			Fix:  "Run: hive mcp list  — to see installed servers.",
		}
	}
	entry["enabled"] = enabled
	return r.writeInstalled(data)
}

func entryOf(data map[string]any, name string) map[string]any {
	servers := asMap(data["servers"])
	if servers == nil {
		return nil
	}
	return asMap(servers[name])
}

func (r *Registry) ListInstalled() ([]map[string]any, error) {
	data, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	servers := asMap(data["servers"])
	out := make([]map[string]any, 0, len(servers))
	for _, name := range r.installedServerOrder(servers) {
		entry := asMap(servers[name])
		row := copyMap(entry)
		row["name"] = name
		out = append(out, row)
	}
	return out, nil
}

// installedServerOrder is the file's server order, matching Python's dict
// iteration in list_installed/list_available. A registry rewritten by Go or
// Python without the raw file still yields every server via the sorted tail.
func (r *Registry) installedServerOrder(servers map[string]any) []string {
	raw, err := os.ReadFile(r.InstalledPath())
	if err != nil {
		return sortedKeys(servers)
	}
	return orderedServerList(orderedServerNames(raw), servers)
}

func (r *Registry) indexServerOrder(servers map[string]any) []string {
	raw, err := os.ReadFile(r.indexPath())
	if err != nil {
		return sortedKeys(servers)
	}
	return orderedServerList(orderedServerNames(raw), servers)
}

func (r *Registry) GetServer(name string) (map[string]any, error) {
	data, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	entry := entryOf(data, name)
	if entry == nil {
		return nil, nil
	}
	row := copyMap(entry)
	row["name"] = name
	return row, nil
}

func (r *Registry) CLIServers() ([]map[string]any, error) {
	data, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	servers := asMap(data["servers"])
	index, err := r.readCachedIndex()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for _, name := range r.installedServerOrder(servers) {
		entry := asMap(servers[name])
		if !getTruthy(entry, "enabled", true) {
			continue
		}
		if asString(entry["mode"]) != "cli" {
			continue
		}
		manifest := r.effectiveManifest(name, entry, index)
		var toolCount any
		if tools, ok := manifest["tools"].([]any); ok && len(tools) > 0 {
			toolCount = len(tools)
		} else {
			toolCount = entry["last_tool_count"]
		}
		out = append(out, map[string]any{
			"name":        name,
			"description": stringOr(manifest["description"], ""),
			"tool_count":  toolCount,
		})
	}
	return out, nil
}

func (r *Registry) ServerConfig(name string) (*ServerConfig, error) {
	data, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	entry := entryOf(data, name)
	if entry == nil {
		return nil, nil
	}
	manifest := r.effectiveManifest(name, entry, nil)
	return manifestToServerConfig(name, manifest, asMap(entry["overrides"]), asString(entry["transport"])), nil
}

func (r *Registry) ListAvailable() ([]map[string]any, error) {
	index, err := r.readCachedIndex()
	if err != nil {
		return nil, err
	}
	servers := asMap(index["servers"])
	out := make([]map[string]any, 0, len(servers))
	for _, name := range r.indexServerOrder(servers) {
		manifest := asMap(servers[name])
		row := copyMap(manifest)
		row["name"] = name
		out = append(out, row)
	}
	return out, nil
}

func (r *Registry) SetOverride(name, overrideType, key, value string) error {
	data, err := r.ReadInstalled()
	if err != nil {
		return err
	}
	entry := entryOf(data, name)
	if entry == nil {
		return &Error{
			Code: CodeInstallFailed,
			What: "Cannot set override for server '" + name + "'",
			Why:  "Server is not installed.",
			Fix:  "Run: hive mcp list  — to see installed servers.",
		}
	}
	if overrideType != "env" && overrideType != "headers" {
		return &Error{
			Code: CodeInstallFailed,
			What: "Invalid override type '" + overrideType + "' for server '" + name + "'",
			Why:  "Override type must be 'env' or 'headers'.",
			Fix:  "Use --type env or --type headers.",
		}
	}
	overrides := asMap(entry["overrides"])
	if overrides == nil {
		overrides = map[string]any{}
		entry["overrides"] = overrides
	}
	bucket := asMap(overrides[overrideType])
	if bucket == nil {
		bucket = map[string]any{}
		overrides[overrideType] = bucket
	}
	bucket[key] = value
	return r.writeInstalled(data)
}

func (r *Registry) Search(query string) ([]map[string]any, error) {
	index, err := r.readCachedIndex()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	servers := asMap(index["servers"])
	out := make([]map[string]any, 0)
	for _, name := range sortedKeys(servers) {
		manifest := asMap(servers[name])
		if !matchesQuery(name, manifest, q) {
			continue
		}
		row := copyMap(manifest)
		row["name"] = name
		out = append(out, row)
	}
	return out, nil
}

func matchesQuery(name string, manifest map[string]any, query string) bool {
	if strings.Contains(strings.ToLower(name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(stringOr(manifest["description"], "")), query) {
		return true
	}
	for _, tag := range asStringSlice(manifest["tags"]) {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	for _, raw := range asSlice(manifest["tools"]) {
		toolName := ""
		if m := asMap(raw); m != nil {
			toolName = stringOr(m["name"], "")
		} else {
			toolName = pyStr(raw)
		}
		if strings.Contains(strings.ToLower(toolName), query) {
			return true
		}
	}
	return false
}

func (r *Registry) IsIndexStale() bool {
	config, err := r.readConfig()
	if err != nil {
		return true
	}
	lastFetched := r.lastFetchedPath()
	if !atomicfs.Exists(lastFetched) {
		lastFetched = r.legacyLastFetchedPath()
		if !atomicfs.Exists(lastFetched) {
			return true
		}
	}
	raw, err := os.ReadFile(lastFetched)
	if err != nil {
		return true
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return true
	}
	stamp, ok := envelope["timestamp"].(string)
	if !ok {
		return true
	}
	parsed, naive, err := parseISOTimestamp(stamp)
	if err != nil {
		return true
	}
	if naive {
		// Legacy Go builds wrote an offset-less stamp. Python's
		// datetime.fromisoformat turns that into a naive datetime, and
		// is_index_stale's `datetime.now(UTC) - last_fetched` then raises
		// TypeError (not caught) -- every Python process sharing this
		// HIVE_HOME dies until the index is refreshed. Repair the marker in
		// place with the explicit offset; the instant, and therefore the age
		// computed below, is unchanged. A read-only cache keeps working.
		_ = r.writeJSON(lastFetched, map[string]any{
			"timestamp": parsed.UTC().Format("2006-01-02T15:04:05.000000+00:00"),
		})
	}
	intervalHours, ok := toFloat(config["refresh_interval_hours"])
	if !ok {
		intervalHours = float64(DefaultRefreshIntervalHours)
	}
	ageHours := r.now().Sub(parsed).Hours()
	return ageHours >= intervalHours
}

// parseISOTimestamp parses the last_fetched marker. naive reports that the
// input carried no UTC offset (a legacy Go-written value): Go reads it as UTC
// so staleness stays meaningful, and IsIndexStale rewrites it with an explicit
// offset so Python's datetime.fromisoformat cannot produce a naive datetime.
func parseISOTimestamp(raw string) (parsed time.Time, naive bool, err error) {
	// Python writes datetime.now(UTC).isoformat() — an explicit +00:00
	// offset. RFC3339Nano accepts both that and the "Z" suffix used by
	// time.Time.MarshalJSON, so try it first and only fall back to the
	// offset-less legacy form.
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), false, nil
	}
	cleaned := strings.TrimSuffix(raw, "Z")
	t, err := time.Parse("2006-01-02T15:04:05.999999999", cleaned)
	return t, err == nil, err
}

func (r *Registry) UpdateIndex() (int, error) {
	config, err := r.readConfig()
	if err != nil {
		return 0, err
	}
	indexURL := stringOr(config["index_url"], DefaultIndexURL)
	fetch := r.Fetch
	if fetch == nil {
		fetch = DefaultHTTPFetch
	}
	if _, err := url.Parse(indexURL); err != nil {
		return 0, err
	}
	body, err := fetch(indexURL, 10*time.Second)
	if err != nil {
		return 0, err
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var index map[string]any
	if err := dec.Decode(&index); err != nil {
		return 0, err
	}
	if err := r.writeJSON(r.indexPath(), index); err != nil {
		return 0, err
	}
	// Python's is_index_stale parses this with datetime.fromisoformat and
	// subtracts it from datetime.now(UTC); a naive stamp raises TypeError
	// there, so the timestamp must always carry an explicit +00:00 offset on
	// a shared HIVE_HOME.
	stamp := map[string]any{"timestamp": r.now().UTC().Format("2006-01-02T15:04:05.000000+00:00")}
	if err := r.writeJSON(r.lastFetchedPath(), stamp); err != nil {
		return 0, err
	}
	return len(asMap(index["servers"])), nil
}

type entryOptions struct {
	Source                 string
	Manifest               map[string]any
	Transport              string
	InstalledBy            string
	Pinned                 bool
	AutoUpdate             bool
	ResolvedPackageVersion string
	Mode                   string
}

func makeEntry(opts entryOptions) map[string]any {
	mode := opts.Mode
	if mode == "" {
		mode = "tools"
	}
	var resolved any
	if opts.ResolvedPackageVersion != "" {
		resolved = opts.ResolvedPackageVersion
	}
	return map[string]any{
		"source":                   opts.Source,
		"manifest_version":         stringOr(opts.Manifest["version"], "0.0.0"),
		"manifest":                 opts.Manifest,
		"installed_at":             time.Now().UTC().Format(time.RFC3339Nano),
		"installed_by":             opts.InstalledBy,
		"transport":                opts.Transport,
		"mode":                     mode,
		"enabled":                  true,
		"pinned":                   opts.Pinned,
		"auto_update":              opts.AutoUpdate,
		"resolved_package_version": resolved,
		"overrides": map[string]any{
			"env":     map[string]any{},
			"headers": map[string]any{},
		},
		"last_health_check_at":             nil,
		"last_health_status":               nil,
		"last_error":                       nil,
		"last_used_at":                     nil,
		"last_validated_with_hive_version": nil,
	}
}

func registryManifestSnapshot(name string, manifest map[string]any) map[string]any {
	out := copyMap(manifest)
	out["name"] = name
	return out
}

var ErrServerNotFound = errors.New("mcp server not found")
