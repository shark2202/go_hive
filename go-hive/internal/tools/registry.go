package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const VerifiedManifestTool = "__aden_verified_manifest"

type LogRecord struct {
	Level   string
	Message string
	Fields  map[string]any
	Err     error
}

type LogSink func(LogRecord)

var (
	logSinkMu    sync.RWMutex
	globalSink   LogSink = defaultLogSink
	registrySink LogSink
)

func defaultLogSink(rec LogRecord) {
	switch strings.ToLower(rec.Level) {
	case "warning", "warn", "error", "critical":
		if rec.Err != nil {
			log.Printf("%s: %s: %v", strings.ToUpper(rec.Level), rec.Message, rec.Err)
			return
		}
		log.Printf("%s: %s", strings.ToUpper(rec.Level), rec.Message)
	}
}

func SetLogSink(sink LogSink) {
	logSinkMu.Lock()
	if sink == nil {
		globalSink = defaultLogSink
	} else {
		globalSink = sink
	}
	logSinkMu.Unlock()
}

func (r *ToolRegistry) SetLogSink(sink LogSink) { r.logSink = sink }

func (r *ToolRegistry) logInfo(msg string, fields map[string]any) {
	r.emit(LogRecord{Level: "info", Message: msg, Fields: fields})
}

func (r *ToolRegistry) logWarning(msg string) {
	r.emit(LogRecord{Level: "warning", Message: msg})
}

func (r *ToolRegistry) logError(msg string, err error) {
	r.emit(LogRecord{Level: "error", Message: msg, Err: err})
}

func (r *ToolRegistry) emit(rec LogRecord) {
	if r.logSink != nil {
		r.logSink(rec)
		return
	}
	logSinkMu.RLock()
	sink := globalSink
	logSinkMu.RUnlock()
	sink(rec)
}

type mcpClientRecord struct {
	client     MCPClient
	serverName string
	managed    bool
}

type harnessGroupRecord struct {
	names   map[string]struct{}
	catalog []map[string]any
}

type ToolRegistry struct {
	mu        sync.RWMutex
	tools     map[string]RegisteredTool
	toolOrder []string

	sessionContext map[string]any

	mcpClients        []*mcpClientRecord
	mcpClientServers  map[int]string
	mcpManagedClients map[int]struct{}
	nextClientID      int
	mcpConfigPath     string
	hasMCPConfigPath  bool
	mcpToolNames      map[string]struct{}
	mcpCredSnapshot   map[string]struct{}
	mcpAdenKeySnap    *string
	mcpServerTools    map[string]map[string]struct{}
	mcpServerOrder    []string
	mcpFullCatalog    map[string][]map[string]any
	mcpToolClients    map[string]MCPClient
	mcpExtraEnv       map[string]string
	identityEnvFn     func() map[string]string
	mcpRegistryPath   string
	hasMCPRegistry    bool
	mcpGateCredSnap   *mcpGateSnapshot
	harnessGroups     map[string]*harnessGroupRecord
	harnessGroupOrder []string

	providerIndex map[string]map[string]struct{}

	logSink                LogSink
	newMCPClient           MCPClientFactory
	mcpManager             MCPConnectionManager
	snapshotCredentialsFn  func() map[string]struct{}
	sleepFn                func(time.Duration)
	harnessGroupBuilderFn  func(group string, identityEnv func() map[string]string) []HarnessTool
	reloadRegistryServerFn func(r *ToolRegistry)
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:             map[string]RegisteredTool{},
		sessionContext:    map[string]any{},
		mcpClientServers:  map[int]string{},
		mcpManagedClients: map[int]struct{}{},
		mcpToolNames:      map[string]struct{}{},
		mcpCredSnapshot:   map[string]struct{}{},
		mcpServerTools:    map[string]map[string]struct{}{},
		mcpFullCatalog:    map[string][]map[string]any{},
		mcpToolClients:    map[string]MCPClient{},
		mcpExtraEnv:       map[string]string{},
		harnessGroups:     map[string]*harnessGroupRecord{},
		providerIndex:     map[string]map[string]struct{}{},
	}
}

func (r *ToolRegistry) Register(name string, tool Tool, executor ToolExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; !exists {
		r.toolOrder = append(r.toolOrder, name)
	}
	r.tools[name] = RegisteredTool{Tool: tool, Executor: executor}
}

func (r *ToolRegistry) GetTools() map[string]Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Tool, len(r.tools))
	for name, rt := range r.tools {
		out[name] = rt.Tool
	}
	return out
}

func (r *ToolRegistry) GetTool(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	return rt.Tool, ok
}

func (r *ToolRegistry) HasTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

func (r *ToolRegistry) RegisteredNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.toolOrder...)
}

func (r *ToolRegistry) SetSessionContext(values map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range values {
		r.sessionContext[k] = v
	}
}

func (r *ToolRegistry) SetMCPExtraEnv(env map[string]string) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	r.mcpExtraEnv = out
}

func (r *ToolRegistry) SetIdentityEnvProvider(provider func() map[string]string) {
	r.identityEnvFn = provider
}

func (r *ToolRegistry) identityEnv() map[string]string {
	base := make(map[string]string, len(r.mcpExtraEnv))
	for k, v := range r.mcpExtraEnv {
		base[k] = v
	}
	if r.identityEnvFn == nil {
		return base
	}
	for k, v := range r.identityEnvFn() {
		base[k] = v
	}
	return base
}

type FuncParam struct {
	Name       string
	Type       string
	HasDefault bool
}

func jsonSchemaTypeFor(typ string) string {
	switch typ {
	case "int", "integer":
		return "integer"
	case "float", "number":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "dict", "object":
		return "object"
	case "list", "array":
		return "array"
	default:
		return "string"
	}
}

func (r *ToolRegistry) RegisterFunction(name, description string, params []FuncParam, fn ToolExecutor) {
	toolName := name
	toolDesc := description
	if toolDesc == "" {
		toolDesc = "Execute " + toolName
	}
	properties := make(map[string]any, len(params))
	required := make([]string, 0, len(params))
	for _, p := range params {
		properties[p.Name] = map[string]any{"type": jsonSchemaTypeFor(p.Type)}
		if !p.HasDefault {
			required = append(required, p.Name)
		}
	}
	_, safe := ConcurrencySafeTools[toolName]
	tool := Tool{
		Name:            toolName,
		Description:     toolDesc,
		ConcurrencySafe: safe,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
	}
	r.Register(toolName, tool, fn)
}

type Decorate struct {
	Name        string
	Description string
	Params      []FuncParam
	Exec        ToolExecutor
}

type Module struct {
	Tools        map[string]Tool
	ToolExecutor func(ToolUse) ToolResult
	Decorated    []Decorate
}

func (r *ToolRegistry) DiscoverFromModule(module *Module) int {
	if module == nil {
		return 0
	}
	count := 0
	names := make([]string, 0, len(module.Tools))
	for name := range module.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := module.Tools[name]
		if module.ToolExecutor != nil {
			r.Register(name, tool, r.moduleExecutor(name, module.ToolExecutor))
		} else {
			r.Register(name, tool, func(inputs map[string]any) (any, error) {
				return map[string]any{"mock": true, "inputs": inputs}, nil
			})
		}
		count++
	}

	decorated := append([]Decorate(nil), module.Decorated...)
	sort.SliceStable(decorated, func(i, j int) bool { return decorated[i].Name < decorated[j].Name })
	for _, d := range decorated {
		r.RegisterFunction(d.Name, d.Description, d.Params, d.Exec)
		count++
	}
	return count
}

func (r *ToolRegistry) moduleExecutor(toolName string, fn func(ToolUse) ToolResult) ToolExecutor {
	return func(inputs map[string]any) (any, error) {
		result := fn(ToolUse{ID: "call_" + toolName, Name: toolName, Input: inputs})
		if result.Content == "" {
			return map[string]any{}, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(result.Content), &parsed); err != nil {
			r.emit(LogRecord{
				Level:   "warning",
				Message: fmt.Sprintf("Tool '%s' returned invalid JSON: %v", toolName, err),
			})
			return map[string]any{
				"error":       fmt.Sprintf("Invalid JSON response from tool '%s': %v", toolName, err),
				"raw_content": result.Content,
			}, nil
		}
		return parsed, nil
	}
}

func (r *ToolRegistry) DiscoverFromModulePath(path string) int {
	if path == "" {
		return 0
	}
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	r.emit(LogRecord{
		Level:   "warning",
		Message: fmt.Sprintf("Cannot import Python tool module %q; register a tools.Module instead", path),
	})
	return 0
}

type RegistryExecutor struct {
	registry *ToolRegistry
}

func (r *ToolRegistry) GetExecutor() *RegistryExecutor {
	return &RegistryExecutor{registry: r}
}

func (e *RegistryExecutor) Execute(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
	r := e.registry
	r.ResyncMCPServersIfNeeded(false)

	r.mu.RLock()
	registered, ok := r.tools[toolUse.Name]
	names := append([]string(nil), r.toolOrder...)
	r.mu.RUnlock()

	if !ok {
		errorMsg := "Unknown tool: " + toolUse.Name
		if len(names) > 0 {
			if matches := GetCloseMatches(toolUse.Name, names, 1, 0.6); len(matches) > 0 {
				errorMsg += fmt.Sprintf(". Did you mean '%s'?", matches[0])
			}
		}
		return ToolResult{
			ToolUseID: toolUse.ID,
			Content:   PythonJSONDumps(map[string]any{"error": errorMsg}),
			IsError:   true,
		}, nil
	}

	result, err := callToolExecutor(registered.Executor, toolUse.Input)
	if err != nil {
		inputsStr := truncateInputs(toolUse.Input)
		r.emit(LogRecord{
			Level: "error",
			Message: fmt.Sprintf(
				"Tool '%s' execution failed for tool_use_id=%s: %v\nInputs: %s",
				toolUse.Name, toolUse.ID, err, inputsStr),
			Err: err,
		})
		return ToolResult{
			ToolUseID: toolUse.ID,
			Content:   PythonJSONDumps(map[string]any{"error": err.Error()}),
			IsError:   true,
		}, nil
	}
	return wrapToolResult(toolUse.ID, result), nil
}

func callToolExecutor(exec ToolExecutor, inputs map[string]any) (result any, err error) {
	if exec == nil {
		return nil, fmt.Errorf("tool has no executor")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return exec(inputs)
}

func (e *RegistryExecutor) ProbeForTool(toolName string) string {
	return e.registry.ProbeMCPForTool(toolName)
}

func (e *RegistryExecutor) KillForTool(toolName string) error {
	e.registry.KillMCPForTool(toolName)
	return nil
}

func wrapToolResult(toolUseID string, result any) ToolResult {
	switch r := result.(type) {
	case ToolResult:
		if r.ToolUseID == "" {
			r.ToolUseID = toolUseID
		}
		return r
	case *ToolResult:
		if r != nil {
			out := *r
			if out.ToolUseID == "" {
				out.ToolUseID = toolUseID
			}
			return out
		}
	}
	if m, ok := result.(map[string]any); ok {
		if images, found := m["_images"]; found {
			return ToolResult{
				ToolUseID:    toolUseID,
				Content:      textOf(m["_text"]),
				ImageContent: toMapSlice(images),
			}
		}
	}
	if s, ok := result.(string); ok {
		return ToolResult{ToolUseID: toolUseID, Content: s}
	}
	return ToolResult{ToolUseID: toolUseID, Content: PythonJSONDumps(result)}
}

func textOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func toMapSlice(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case map[string]any:
		return []map[string]any{t}
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			switch m := item.(type) {
			case map[string]any:
				out = append(out, m)
			default:
				if raw, err := json.Marshal(m); err == nil {
					var mm map[string]any
					if json.Unmarshal(raw, &mm) == nil {
						out = append(out, mm)
					}
				}
			}
		}
		return out
	}
	return nil
}

func (r *ToolRegistry) BuildProviderIndex() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerIndex = map[string]map[string]struct{}{}
}

func (r *ToolRegistry) GetByProvider(provider string) map[string]Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.providerIndex) == 0 {
		return map[string]Tool{}
	}
	names := r.providerIndex[provider]
	out := map[string]Tool{}
	for name, rt := range r.tools {
		if _, ok := names[name]; ok {
			out[name] = rt.Tool
		}
	}
	return out
}

func (r *ToolRegistry) GetToolNamesByProvider(provider string) []string {
	out := make([]string, 0)
	for name := range r.GetByProvider(provider) {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *ToolRegistry) GetAllProviderToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := map[string]struct{}{}
	for _, names := range r.providerIndex {
		for name := range names {
			all[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(all))
	for name := range r.tools {
		if _, ok := all[name]; ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
