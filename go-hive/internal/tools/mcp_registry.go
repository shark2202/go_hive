package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

type MCPServerSpec struct {
	Name        string            `json:"name"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Cwd         string            `json:"cwd"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	SocketPath  string            `json:"socket_path"`
	Description string            `json:"description"`
}

type mcpGateSnapshot struct {
	toolProviders map[string]string
	liveProviders map[string]struct{}
}

type ServerResolution struct {
	Server        string
	Status        string
	ToolsLoaded   int
	SkippedReason *string
}

type RegisterMCPServerOptions struct {
	UseConnectionManager  *bool
	PreserveExistingTools *bool
	ToolCap               *int
	LogCollisions         bool
}

type LoadRegistryOptions struct {
	LogSummary            *bool
	PreserveExistingTools *bool
	MaxTools              *int
	LogCollisions         bool
	EssentialServers      map[string]struct{}
}

var gateCredSnapshotFn func() *mcpGateSnapshot

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func strPtr(s string) *string { return &s }

func ResolveMCPStdioConfig(spec MCPServerSpec, baseDir string) MCPServerSpec {
	if spec.Transport != "stdio" {
		return spec
	}
	args := append([]string(nil), spec.Args...)
	if spec.Cwd == "" && len(args) == 0 {
		return spec
	}

	resolvedCwd := ""
	if spec.Cwd != "" {
		if filepath.IsAbs(spec.Cwd) {
			resolvedCwd = filepath.Clean(spec.Cwd)
		} else if abs, err := filepath.Abs(filepath.Join(baseDir, spec.Cwd)); err == nil {
			resolvedCwd = abs
		}
	}

	scriptName := ""
	scriptIdx := -1
	for i, arg := range args {
		if strings.HasSuffix(arg, ".py") {
			scriptName = arg
			scriptIdx = i
			break
		}
	}
	if resolvedCwd == "" {
		return spec
	}

	cwdInfo, cwdErr := os.Stat(resolvedCwd)
	fallbackDir, fallbackErr := filepath.Abs("tools")
	needFallback := cwdErr != nil || !cwdInfo.IsDir()
	if scriptName != "" && !needFallback {
		if _, err := os.Stat(filepath.Join(resolvedCwd, scriptName)); err != nil {
			needFallback = true
		}
	}
	if needFallback {
		fallbackOK := fallbackErr == nil
		if info, err := os.Stat(fallbackDir); err != nil || !info.IsDir() {
			fallbackOK = false
		}
		if scriptName != "" && fallbackOK {
			if _, err := os.Stat(filepath.Join(fallbackDir, scriptName)); err != nil {
				fallbackOK = false
			}
		}
		if fallbackOK {
			resolvedCwd = fallbackDir
		} else {
			spec.Cwd = resolvedCwd
			return spec
		}
	}

	if scriptName == "" {
		spec.Cwd = resolvedCwd
		return spec
	}
	if runtime.GOOS == "windows" {
		spec.Cwd = ""
		args[scriptIdx] = filepath.Join(resolvedCwd, scriptName)
		spec.Args = args
		return spec
	}
	spec.Cwd = resolvedCwd
	return spec
}

func (r *ToolRegistry) LoadMCPConfig(configPath string) {
	r.mcpConfigPath = configPath
	r.hasMCPConfigPath = configPath != ""

	data, err := os.ReadFile(configPath)
	if err != nil {
		r.logWarning(fmt.Sprintf("Failed to load MCP config from %s: %v", configPath, err))
		return
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		r.logWarning(fmt.Sprintf("Failed to load MCP config from %s: %v", configPath, err))
		return
	}

	baseDir := filepath.Dir(configPath)
	var serverList []MCPServerSpec

	if rawServers, hasServers := config["servers"]; hasServers {
		var entries []json.RawMessage
		if err := json.Unmarshal(rawServers, &entries); err == nil {
			for _, entry := range entries {
				var spec MCPServerSpec
				if err := json.Unmarshal(entry, &spec); err == nil {
					serverList = append(serverList, spec)
				}
			}
		}
	} else {
		names := make([]string, 0, len(config))
		for name := range config {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			var spec MCPServerSpec
			if err := json.Unmarshal(config[name], &spec); err != nil {
				continue
			}
			spec.Name = name
			serverList = append(serverList, spec)
		}
	}

	resolved := make([]MCPServerSpec, 0, len(serverList))
	for _, spec := range serverList {
		resolved = append(resolved, ResolveMCPStdioConfig(spec, baseDir))
	}
	logSummary := false
	preserve := true
	r.LoadRegistryServers(resolved, LoadRegistryOptions{
		LogSummary:            &logSummary,
		PreserveExistingTools: &preserve,
	})

	r.mcpCredSnapshot = r.snapshotCredentials()
	r.mcpAdenKeySnap = adenAPIKeySnapshot()
	r.logRegistrySnapshot("after server config")
}

func adenAPIKeySnapshot() *string {
	value, ok := os.LookupEnv("ADEN_API_KEY")
	if !ok {
		return nil
	}
	return &value
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (r *ToolRegistry) LoadRegistryServers(serverList []MCPServerSpec, opts LoadRegistryOptions) []ServerResolution {
	essential := opts.EssentialServers
	if essential == nil {
		essential = DefaultLocalServerNames
	}
	logSummary := boolOr(opts.LogSummary, true)
	preserve := boolOr(opts.PreserveExistingTools, true)

	results := make([]ServerResolution, 0, len(serverList))
	toolsAdded := 0

	record := func(res ServerResolution) {
		results = append(results, res)
		if _, harness := HarnessGroupNames[res.Server]; logSummary && !harness {
			reason := any(nil)
			if res.SkippedReason != nil {
				reason = *res.SkippedReason
			}
			r.logInfo("MCP registry server resolution", map[string]any{
				"event":          "mcp_registry_server_resolution",
				"server":         res.Server,
				"status":         res.Status,
				"tools_loaded":   res.ToolsLoaded,
				"skipped_reason": reason,
			})
		}
	}

	anyNonHarness := false
	for _, spec := range serverList {
		if _, harness := HarnessGroupNames[spec.Name]; !harness {
			anyNonHarness = true
			break
		}
	}
	if anyNonHarness {
		r.mcpGateCredSnap = r.computeMCPGateCredSnapshot()
	}
	defer func() { r.mcpGateCredSnap = nil }()

	for _, spec := range serverList {
		name := spec.Name
		if name == "" {
			name = "unknown"
		}
		_, isEssential := essential[name]

		var remaining *int
		if opts.MaxTools != nil && !isEssential {
			rem := *opts.MaxTools - toolsAdded
			if rem <= 0 {
				record(ServerResolution{
					Server:        name,
					Status:        "skipped",
					ToolsLoaded:   0,
					SkippedReason: strPtr(fmt.Sprintf("max_tools=%d budget exhausted", *opts.MaxTools)),
				})
				continue
			}
			remaining = &rem
		}

		success, loaded, errMsg := r.registerMCPServerWithRetry(spec, preserve, remaining, opts.LogCollisions)
		toolsAdded += loaded
		res := ServerResolution{Server: name, ToolsLoaded: loaded}
		if success {
			res.Status = "loaded"
		} else {
			res.Status = "skipped"
			if errMsg == "" {
				errMsg = "unknown error"
			}
			res.SkippedReason = strPtr(errMsg)
		}
		record(res)
	}
	return results
}

func (r *ToolRegistry) registerMCPServerWithRetry(spec MCPServerSpec, preserve bool, toolCap *int, logCollisions bool) (bool, int, string) {
	name := spec.Name
	if name == "" {
		name = "unknown"
	}
	alreadyLoaded := len(r.mcpServerTools[name]) > 0
	lastError := ""

	for attempt := 0; attempt < 2; attempt++ {
		count := r.RegisterMCPServer(spec, RegisterMCPServerOptions{
			PreserveExistingTools: &preserve,
			ToolCap:               toolCap,
			LogCollisions:         logCollisions,
		})
		if count > 0 {
			return true, count, ""
		}
		if alreadyLoaded && preserve {
			return true, 0, ""
		}
		lastError = "registered 0 tools"

		if attempt == 0 {
			r.logWarning(fmt.Sprintf("MCP server '%s' failed to register, retrying in 2s: %s", name, lastError))
			r.sleep(2 * time.Second)
		} else {
			r.logWarning(fmt.Sprintf("MCP server '%s' failed after retry: %s", name, lastError))
		}
	}
	return false, 0, lastError
}

func (r *ToolRegistry) sleep(d time.Duration) {
	if r.sleepFn != nil {
		r.sleepFn(d)
		return
	}
	time.Sleep(d)
}

func (r *ToolRegistry) RegisterMCPServer(spec MCPServerSpec, opts RegisterMCPServerOptions) int {
	name := spec.Name
	if _, harness := HarnessGroupNames[name]; harness {
		return r.registerHarnessGroup(name, boolOr(opts.PreserveExistingTools, true), opts.LogCollisions)
	}
	if name == "" {
		name = "unknown"
	}

	count, err := r.registerMCPServerInner(spec, opts)
	if err != nil {
		r.emit(LogRecord{
			Level:   "error",
			Message: "MCP Registry Load",
			Fields: map[string]any{
				"server":         name,
				"status":         "failed",
				"tools_loaded":   0,
				"skipped_reason": err.Error(),
			},
			Err: err,
		})
		return 0
	}
	return count
}

func (r *ToolRegistry) registerMCPServerInner(spec MCPServerSpec, opts RegisterMCPServerOptions) (int, error) {
	preserve := boolOr(opts.PreserveExistingTools, true)
	useManager := boolOr(opts.UseConnectionManager, true)

	mergedEnv := r.identityEnv()
	for k, v := range spec.Env {
		mergedEnv[k] = v
	}
	config := &MCPServerConfig{
		Name:        spec.Name,
		Transport:   spec.Transport,
		Command:     spec.Command,
		Args:        append([]string(nil), spec.Args...),
		Env:         mergedEnv,
		Cwd:         spec.Cwd,
		URL:         spec.URL,
		Headers:     spec.Headers,
		SocketPath:  spec.SocketPath,
		Description: spec.Description,
	}

	var client MCPClient
	managed := false
	if useManager && r.mcpManager != nil {
		acquired, err := r.mcpManager.Acquire(config)
		if err != nil {
			return 0, err
		}
		client = acquired
		managed = true
	} else {
		factory := r.newMCPClient
		if factory == nil {
			factory = DefaultMCPClientFactory
		}
		client = factory(config)
		if err := client.Connect(); err != nil {
			return 0, err
		}
	}

	clientID := r.nextClientID
	r.nextClientID++
	r.mcpClients = append(r.mcpClients, &mcpClientRecord{client: client, serverName: config.Name, managed: managed})
	r.mcpClientServers[clientID] = config.Name
	if managed {
		r.mcpManagedClients[clientID] = struct{}{}
	}

	serverName := spec.Name
	r.ensureServerOrder(serverName)
	if _, exists := r.mcpServerTools[serverName]; !exists {
		r.mcpServerTools[serverName] = map[string]struct{}{}
	}

	admit, libraryAdmit, toolProviderMap := r.buildMCPAdmissionGate(client)

	r.mcpFullCatalog[serverName] = []map[string]any{}
	serverCatalogNames := map[string]struct{}{}

	count := 0
	admittedNames := []string{}
	for _, mcpTool := range client.ListTools() {
		originServer := r.findMCPOriginServerForTool(mcpTool.Name)
		catalogShadowed := preserve && originServer != "" && originServer != serverName
		if libraryAdmit(mcpTool.Name) && !hasName(serverCatalogNames, mcpTool.Name) && !catalogShadowed {
			r.mcpFullCatalog[serverName] = append(r.mcpFullCatalog[serverName], map[string]any{
				"name":         mcpTool.Name,
				"description":  mcpTool.Description,
				"input_schema": mcpTool.InputSchema,
				"provider":     providerValue(toolProviderMap, mcpTool.Name),
			})
			serverCatalogNames[mcpTool.Name] = struct{}{}
		}
		if !admit(mcpTool.Name) {
			continue
		}
		if opts.ToolCap != nil && count >= *opts.ToolCap {
			break
		}
		if preserve && r.HasTool(mcpTool.Name) {
			if opts.LogCollisions {
				origin := originServer
				if origin == "" {
					origin = "<existing>"
				}
				if origin != serverName {
					r.logWarning(fmt.Sprintf(
						"MCP tool '%s' from '%s' shadowed by '%s' (loaded first)",
						mcpTool.Name, serverName, origin))
				}
			}
			continue
		}

		tool := r.ConvertMCPToolToFrameworkTool(mcpTool)
		params := schemaParams(mcpTool.InputSchema)
		r.Register(mcpTool.Name, tool, r.makeMCPExecutor(client, mcpTool.Name, params))
		r.mcpToolNames[mcpTool.Name] = struct{}{}
		r.mcpToolClients[mcpTool.Name] = client
		r.mcpServerTools[serverName][mcpTool.Name] = struct{}{}
		admittedNames = append(admittedNames, mcpTool.Name)
		count++
	}

	r.logInfo("MCP Registry Load", map[string]any{
		"server":         config.Name,
		"status":         "success",
		"tools_loaded":   count,
		"skipped_reason": nil,
	})
	r.logInfo(fmt.Sprintf("MCP server '%s' admitted %d tool(s): %v", config.Name, len(admittedNames), sortedStrings(admittedNames)), nil)
	return count, nil
}

func providerValue(m map[string]string, name string) any {
	if p, ok := m[name]; ok && p != "" {
		return p
	}
	return nil
}

func hasName(set map[string]struct{}, name string) bool {
	_, ok := set[name]
	return ok
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func (r *ToolRegistry) ensureServerOrder(name string) {
	for _, existing := range r.mcpServerOrder {
		if existing == name {
			return
		}
	}
	r.mcpServerOrder = append(r.mcpServerOrder, name)
}

func (r *ToolRegistry) findMCPOriginServerForTool(toolName string) string {
	for _, server := range r.mcpServerOrder {
		if _, ok := r.mcpServerTools[server][toolName]; ok {
			return server
		}
	}
	for server, names := range r.mcpServerTools {
		if _, ok := names[toolName]; ok {
			return server
		}
	}
	return ""
}

func (r *ToolRegistry) makeMCPExecutor(client MCPClient, toolName string, toolParams map[string]struct{}) ToolExecutor {
	return func(inputs map[string]any) (any, error) {
		merged := r.prepareCallInputs(inputs, toolParams, nil)
		result, err := client.CallTool(toolName, merged)
		if err != nil {
			inputsStr := truncateInputs(inputs)
			r.emit(LogRecord{
				Level:   "error",
				Message: fmt.Sprintf("MCP tool '%s' execution failed: %v\nInputs: %s", toolName, err, inputsStr),
				Err:     err,
			})
			return map[string]any{"error": err.Error()}, nil
		}
		result = MaybeInlineBrowserImage(toolName, result)
		switch list := result.(type) {
		case []any:
			if len(list) > 0 {
				if first, ok := list[0].(map[string]any); ok {
					if text, ok := first["text"]; ok {
						return text, nil
					}
				}
				return list[0], nil
			}
		case []map[string]any:
			if len(list) > 0 {
				if text, ok := list[0]["text"]; ok {
					return text, nil
				}
				return list[0], nil
			}
		}
		return result, nil
	}
}

func schemaParams(schema map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	if schema == nil {
		return out
	}
	props, _ := schema["properties"].(map[string]any)
	for name := range props {
		out[name] = struct{}{}
	}
	return out
}

func (r *ToolRegistry) prepareCallInputs(inputs map[string]any, toolParams map[string]struct{}, extraEnv map[string]string) map[string]any {
	baseContext := map[string]any{}
	r.mu.RLock()
	for k, v := range r.sessionContext {
		baseContext[k] = v
	}
	r.mu.RUnlock()
	for k, v := range GetExecutionContext() {
		baseContext[k] = v
	}

	cleanInputs := make(map[string]any, len(inputs))
	for k, v := range inputs {
		if _, isContextParam := ContextParams[k]; isContextParam {
			continue
		}
		cleanInputs[k] = v
	}
	merged := cleanInputs
	for k, v := range baseContext {
		if _, declared := toolParams[k]; declared {
			merged[k] = v
		}
	}

	if _, declaresEnv := toolParams["env"]; declaresEnv {
		injectedEnv := map[string]string{}
		for k, v := range extraEnv {
			injectedEnv[k] = v
		}
		if principal, ok := baseContext["principal"]; ok && principal != nil {
			injectedEnv["HIVE_PRINCIPAL"] = fmt.Sprint(principal)
		}
		if profile, ok := baseContext["profile"]; ok && profile != nil {
			injectedEnv["HIVE_BROWSER_SESSION"] = fmt.Sprint(profile)
		}
		if displayName, ok := baseContext["profile_display_name"]; ok && displayName != nil {
			injectedEnv["HIVE_BROWSER_PROFILE_DISPLAY_NAME"] = fmt.Sprint(displayName)
		}
		if sessionCwd, ok := baseContext["session_cwd"]; ok && sessionCwd != nil {
			injectedEnv["HIVE_STORAGE_PATH"] = fmt.Sprint(sessionCwd)
		}
		if len(injectedEnv) > 0 {
			env := map[string]any{}
			switch existing := merged["env"].(type) {
			case map[string]any:
				for k, v := range existing {
					env[k] = v
				}
			case map[string]string:
				for k, v := range existing {
					env[k] = v
				}
			}
			for k, v := range injectedEnv {
				env[k] = v
			}
			merged["env"] = env
		}
	}
	return merged
}

func (r *ToolRegistry) registerHarnessGroup(group string, preserve bool, logCollisions bool) int {
	if _, exists := r.harnessGroups[group]; exists {
		return 0
	}
	if r.harnessGroupBuilderFn == nil {
		r.logWarning(fmt.Sprintf("No harness group builder configured for '%s'", group))
		return 0
	}

	harnessTools := r.harnessGroupBuilderFn(group, r.identityEnv)
	r.ensureServerOrder(group)
	if _, exists := r.mcpServerTools[group]; !exists {
		r.mcpServerTools[group] = map[string]struct{}{}
	}
	catalog := []map[string]any{}
	registered := []string{}
	for _, ht := range harnessTools {
		if preserve && r.HasTool(ht.Name) {
			origin := r.findMCPOriginServerForTool(ht.Name)
			if logCollisions && origin != group {
				shown := origin
				if shown == "" {
					shown = "<existing>"
				}
				r.logWarning(fmt.Sprintf(
					"Harness tool '%s' from '%s' shadowed by '%s' (loaded first)",
					ht.Name, group, shown))
			}
			continue
		}
		tool := r.ConvertMCPToolToFrameworkTool(ht.asMCPTool())
		r.Register(ht.Name, tool, r.makeHarnessExecutor(ht))
		r.mcpServerTools[group][ht.Name] = struct{}{}
		catalog = append(catalog, map[string]any{
			"name":         ht.Name,
			"description":  ht.Description,
			"input_schema": ht.InputSchema,
			"provider":     nil,
		})
		registered = append(registered, ht.Name)
	}

	r.mcpFullCatalog[group] = catalog
	r.saveHarnessGroup(group)
	r.logInfo(fmt.Sprintf("Harness group '%s' registered %d tool(s): %v", group, len(registered), sortedStrings(registered)), nil)
	return len(registered)
}

func (r *ToolRegistry) RecordBuiltinTools(group string, names []string) {
	r.ensureServerOrder(group)
	members, exists := r.mcpServerTools[group]
	if !exists {
		members = map[string]struct{}{}
		r.mcpServerTools[group] = members
	}
	for _, name := range names {
		if r.HasTool(name) {
			members[name] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(members))
	for name := range members {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	catalog := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		tool, _ := r.GetTool(name)
		catalog = append(catalog, map[string]any{
			"name":         name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
			"provider":     nil,
		})
	}
	r.mcpFullCatalog[group] = catalog
	r.saveHarnessGroup(group)
}

func (r *ToolRegistry) saveHarnessGroup(group string) {
	if _, seen := r.harnessGroups[group]; !seen {
		r.harnessGroupOrder = append(r.harnessGroupOrder, group)
	}
	names := make(map[string]struct{}, len(r.mcpServerTools[group]))
	for name := range r.mcpServerTools[group] {
		names[name] = struct{}{}
	}
	catalog := append([]map[string]any(nil), r.mcpFullCatalog[group]...)
	r.harnessGroups[group] = &harnessGroupRecord{names: names, catalog: catalog}
}

func (r *ToolRegistry) makeHarnessExecutor(ht HarnessTool) ToolExecutor {
	return func(inputs map[string]any) (any, error) {
		callInputs := r.prepareCallInputs(inputs, ht.Params, r.identityEnv())
		result, err := ht.Invoke(callInputs)
		if err != nil {
			inputsStr := truncateInputs(inputs)
			r.emit(LogRecord{
				Level:   "error",
				Message: fmt.Sprintf("Harness tool '%s' execution failed: %v\nInputs: %s", ht.Name, err, inputsStr),
				Err:     err,
			})
			return map[string]any{"error": err.Error()}, nil
		}
		return MaybeInlineBrowserImage(ht.Name, result), nil
	}
}

func (r *ToolRegistry) computeMCPGateCredSnapshot() *mcpGateSnapshot {
	if gateCredSnapshotFn != nil {
		return gateCredSnapshotFn()
	}
	return &mcpGateSnapshot{
		toolProviders: map[string]string{},
		liveProviders: map[string]struct{}{},
	}
}

func (r *ToolRegistry) buildMCPAdmissionGate(client MCPClient) (func(string) bool, func(string) bool, map[string]string) {
	verifiedNames := map[string]struct{}{}
	manifestPresent := false

	sentinelAdvertised := false
	for _, tool := range client.ListTools() {
		if tool.Name == VerifiedManifestTool {
			sentinelAdvertised = true
			break
		}
	}
	if sentinelAdvertised {
		raw, err := client.CallTool(VerifiedManifestTool, map[string]any{})
		if err == nil {
			parsed := raw
			if text, ok := raw.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(text), &decoded) == nil {
					parsed = decoded
				} else {
					parsed = nil
				}
			}
			if names, ok := stringList(parsed); ok {
				for _, name := range names {
					verifiedNames[name] = struct{}{}
				}
				manifestPresent = true
			}
		}
	}

	snapshot := r.mcpGateCredSnap
	if snapshot == nil {
		snapshot = r.computeMCPGateCredSnapshot()
	}
	toolProviderMap := snapshot.toolProviders
	if toolProviderMap == nil {
		toolProviderMap = map[string]string{}
	}
	liveProviders := snapshot.liveProviders
	if liveProviders == nil {
		liveProviders = map[string]struct{}{}
	}

	admit := func(toolName string) bool {
		if toolName == VerifiedManifestTool {
			return false
		}
		if provider := toolProviderMap[toolName]; provider != "" {
			_, live := liveProviders[provider]
			return live
		}
		if !manifestPresent {
			return true
		}
		_, verified := verifiedNames[toolName]
		return verified
	}
	libraryAdmit := func(toolName string) bool {
		if toolName == VerifiedManifestTool {
			return false
		}
		if toolProviderMap[toolName] != "" {
			return true
		}
		if !manifestPresent {
			return true
		}
		_, verified := verifiedNames[toolName]
		return verified
	}
	return admit, libraryAdmit, toolProviderMap
}

func stringList(value any) ([]string, bool) {
	switch list := value.(type) {
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	}
	return nil, false
}

func (r *ToolRegistry) ConvertMCPToolToFrameworkTool(mcpTool MCPTool) Tool {
	properties := map[string]any{}
	if mcpTool.InputSchema != nil {
		if rawProps, ok := mcpTool.InputSchema["properties"].(map[string]any); ok {
			for k, v := range rawProps {
				if _, contextParam := ContextParams[k]; contextParam {
					continue
				}
				properties[k] = v
			}
		}
	}
	required := []string{}
	if mcpTool.InputSchema != nil {
		switch raw := mcpTool.InputSchema["required"].(type) {
		case []any:
			for _, item := range raw {
				if name, ok := item.(string); ok {
					if _, contextParam := ContextParams[name]; !contextParam {
						required = append(required, name)
					}
				}
			}
		case []string:
			for _, name := range raw {
				if _, contextParam := ContextParams[name]; !contextParam {
					required = append(required, name)
				}
			}
		}
	}
	return Tool{
		Name:        mcpTool.Name,
		Description: mcpTool.Description,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
		ProducesImage:   ToolProducesImage(mcpTool.Name),
		ConcurrencySafe: IsConcurrencySafe(mcpTool.Name),
	}
}

func (r *ToolRegistry) GetServerToolNames(serverName string) map[string]struct{} {
	out := map[string]struct{}{}
	for name := range r.mcpServerTools[serverName] {
		out[name] = struct{}{}
	}
	return out
}

func (r *ToolRegistry) GetFullMCPCatalog() map[string][]map[string]any {
	out := make(map[string][]map[string]any, len(r.mcpFullCatalog))
	for server, entries := range r.mcpFullCatalog {
		copied := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			entryCopy := make(map[string]any, len(entry))
			for k, v := range entry {
				entryCopy[k] = v
			}
			copied = append(copied, entryCopy)
		}
		out[server] = copied
	}
	return out
}

func (r *ToolRegistry) logRegistrySnapshot(context string) {
	perGroupCounts := make(map[string]int, len(r.mcpServerTools))
	for server, names := range r.mcpServerTools {
		perGroupCounts[server] = len(names)
	}
	harnessCount := 0
	for _, group := range r.harnessGroups {
		harnessCount += len(group.names)
	}
	total := len(r.tools)
	mcpCount := len(r.mcpToolNames)
	r.logInfo(fmt.Sprintf(
		"ToolRegistry snapshot (%s): total=%d, harness=%d, mcp=%d, other=%d, per_group=%v",
		context, total, harnessCount, mcpCount, total-harnessCount-mcpCount, perGroupCounts), nil)
}

func (r *ToolRegistry) snapshotCredentials() map[string]struct{} {
	if r.snapshotCredentialsFn != nil {
		return r.snapshotCredentialsFn()
	}
	out := map[string]struct{}{}
	home, err := hivehome.Resolve()
	if err != nil {
		return out
	}
	dir := filepath.Join(home.CredentialsDir(), "credentials")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		out[entry.Name()] = struct{}{}
	}
	return out
}

func (r *ToolRegistry) SetMCPRegistryAgentPath(agentPath string) {
	r.mcpRegistryPath = agentPath
	r.hasMCPRegistry = agentPath != ""
}

func (r *ToolRegistry) ReloadRegistryMCPServersAfterResync() {
	if !r.hasMCPRegistry {
		return
	}
	if r.reloadRegistryServerFn != nil {
		r.reloadRegistryServerFn(r)
		return
	}
	r.logWarning(fmt.Sprintf(
		"No MCP registry reloader configured for '%s'; skipping post-resync reload",
		filepath.Base(r.mcpRegistryPath)))
}

func (r *ToolRegistry) ResyncMCPServersIfNeeded(force bool) bool {
	if len(r.mcpClients) == 0 || !r.hasMCPConfigPath {
		return false
	}

	current := r.snapshotCredentials()
	currentAdenKey := adenAPIKeySnapshot()
	filesChanged := !sameStringSet(current, r.mcpCredSnapshot)
	adenKeyChanged := !sameOptionalString(currentAdenKey, r.mcpAdenKeySnap)

	if !force && !filesChanged && !adenKeyChanged {
		return false
	}

	var reason string
	switch {
	case force && !filesChanged && !adenKeyChanged:
		reason = "Forced resync (credential save / explicit refresh)"
	case filesChanged && adenKeyChanged:
		reason = "Credential files and ADEN_API_KEY changed"
	case adenKeyChanged:
		reason = "ADEN_API_KEY changed"
	default:
		reason = "Credential files changed"
	}
	r.logInfo(reason+" — resyncing MCP servers", nil)

	r.CleanupMCPClients("during resync")

	for name := range r.mcpToolNames {
		r.removeTool(name)
	}
	r.mcpToolNames = map[string]struct{}{}
	r.mcpServerTools = map[string]map[string]struct{}{}
	r.mcpFullCatalog = map[string][]map[string]any{}
	r.mcpServerOrder = nil
	for _, group := range r.harnessGroupOrder {
		record, ok := r.harnessGroups[group]
		if !ok {
			continue
		}
		r.mcpServerTools[group] = map[string]struct{}{}
		for name := range record.names {
			r.mcpServerTools[group][name] = struct{}{}
		}
		r.mcpFullCatalog[group] = append([]map[string]any(nil), record.catalog...)
		r.mcpServerOrder = append(r.mcpServerOrder, group)
	}

	r.LoadMCPConfig(r.mcpConfigPath)
	if r.hasMCPRegistry {
		r.ReloadRegistryMCPServersAfterResync()
	}

	r.logInfo("MCP server resync complete", nil)
	r.logRegistrySnapshot("after resync_mcp_servers_if_needed")
	return true
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

func (r *ToolRegistry) removeTool(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; !exists {
		return
	}
	delete(r.tools, name)
	for i, existing := range r.toolOrder {
		if existing == name {
			r.toolOrder = append(r.toolOrder[:i], r.toolOrder[i+1:]...)
			break
		}
	}
}

func (r *ToolRegistry) Cleanup() {
	r.CleanupMCPClients("")
}

func (r *ToolRegistry) CleanupMCPClients(context string) {
	contextSuffix := ""
	if context != "" {
		contextSuffix = " " + context
	}
	for _, record := range r.mcpClients {
		serverName := record.serverName
		if serverName == "" && record.client != nil {
			if cfg := record.client.Config(); cfg != nil && cfg.Name != "" {
				serverName = cfg.Name
			}
		}
		var err error
		if record.managed && r.mcpManager != nil {
			err = r.mcpManager.Release(serverName)
		} else if record.client != nil {
			err = record.client.Disconnect()
		}
		if err != nil {
			r.logWarning(fmt.Sprintf("Error disconnecting MCP client%s: %v", contextSuffix, err))
		}
	}
	r.mcpClients = nil
	r.mcpClientServers = map[int]string{}
	r.mcpManagedClients = map[int]struct{}{}
	r.mcpToolClients = map[string]MCPClient{}
}

func (r *ToolRegistry) KillMCPForTool(toolName string) bool {
	client, ok := r.mcpToolClients[toolName]
	if !ok || client == nil {
		return false
	}
	if checker, ok := client.(interface{ IsConnected() bool }); ok && !checker.IsConnected() {
		return true
	}
	serverName := "?"
	if cfg := client.Config(); cfg != nil && cfg.Name != "" {
		serverName = cfg.Name
	}
	r.logWarning(fmt.Sprintf(
		"Force-disconnecting MCP client for hung tool '%s' on server '%s'",
		toolName, serverName))
	if err := client.Disconnect(); err != nil {
		r.logWarning(fmt.Sprintf("Error force-disconnecting MCP client for '%s': %v", toolName, err))
	}
	return true
}

func (r *ToolRegistry) ProbeMCPForTool(toolName string) string {
	client, ok := r.mcpToolClients[toolName]
	if !ok || client == nil {
		return "unknown"
	}
	probe, hasProbe := client.(interface{ ProbeLiveness() bool })
	if !hasProbe {
		return "unknown"
	}
	transport := ""
	if cfg := client.Config(); cfg != nil {
		transport = cfg.Transport
	}
	if transport != "stdio" && transport != "sse" {
		return "unknown"
	}
	busy := false
	if b, ok := client.(interface{ LifecycleBusy() bool }); ok {
		busy = b.LifecycleBusy()
	}
	connected := true
	if c, ok := client.(interface{ IsConnected() bool }); ok {
		connected = c.IsConnected()
	}
	if busy || !connected {
		return "resetting"
	}
	if callProbe(probe) {
		return "alive"
	}
	failures := 1
	if f, ok := client.(interface{ ConsecutiveProbeFailures() int }); ok {
		failures = f.ConsecutiveProbeFailures()
	}
	inflight := 0
	if f, ok := client.(interface{ InflightCalls() int }); ok {
		inflight = f.InflightCalls()
	}
	if failures >= 2 || inflight == 0 {
		return "dead"
	}
	return "resetting"
}

func callProbe(probe interface{ ProbeLiveness() bool }) (alive bool) {
	defer func() {
		if recover() != nil {
			alive = false
		}
	}()
	return probe.ProbeLiveness()
}
