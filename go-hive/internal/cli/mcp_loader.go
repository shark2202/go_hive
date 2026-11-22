package cli

// MCP loader for `hive serve`.
//
// serve reads the configured servers, connects them through one shared client
// pool, and fetches tools/list before the HTTP listener opens. Every session
// catalogue built afterwards attaches those tools through the process MCP seam
// (tools.ProcessMCP), so a queen chat session and its colony's worker pool see
// the same servers instead of each spawning its own processes.
//
// Degradation is deliberate: an unreachable server is recorded with its error
// and left out of the load set. serve starts, the remaining servers' tools load,
// and GET /api/mcp/servers shows which server failed and why.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// mcpRuntime owns the process-wide MCP pool and the last load's status snapshot.
type mcpRuntime struct {
	home       hivehome.Home
	configPath string
	out        io.Writer
	budget     time.Duration

	pool *tools.MCPClientPool
	gen  atomic.Int64

	mu        sync.Mutex
	namespace string
	specs     []tools.MCPServerSpec
	statuses  []tools.MCPServerStatus
}

// mcpRuntime is the serve-owned loader installed through tools.SetProcessMCP.
var _ tools.MCPAttacher = (*mcpRuntime)(nil)

func newMCPRuntime(home hivehome.Home, out io.Writer) *mcpRuntime {
	if out == nil {
		out = io.Discard
	}
	return &mcpRuntime{
		home:       home,
		configPath: home.ConfigurationFile(),
		out:        out,
		budget:     mcpStartupBudget,
		pool:       tools.NewMCPClientPool(),
		namespace:  tools.MCPToolNamespace,
	}
}

// startMCPRuntime loads MCP for a serve process and installs the loader every
// new session catalogue attaches from. The returned function detaches the loader
// and closes the pool; serve defers it so a graceful shutdown stops the server
// processes too.
func (r *Runner) startMCPRuntime(home hivehome.Home) func() {
	if strings.TrimSpace(home.Root) == "" {
		// Without a resolvable HIVE_HOME there is no configuration to read;
		// the serve path already warned about that.
		return func() {}
	}
	out := r.Stdout
	if out == nil {
		out = io.Discard
	}
	runtime := newMCPRuntime(home, out)
	if err := runtime.Reload(); err != nil {
		fmt.Fprintf(out, "Warning: MCP unavailable: %v\n", err)
		return func() {}
	}
	tools.SetProcessMCP(runtime)
	return func() {
		tools.SetProcessMCP(nil)
		runtime.Close()
	}
}

// Generation implements tools.MCPAttacher: every Reload bumps it, and a registry
// that already attached re-attaches on its next tool call.
func (m *mcpRuntime) Generation() int64 { return m.gen.Load() }

// Status implements tools.MCPAttacher.
func (m *mcpRuntime) Status() []tools.MCPServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]tools.MCPServerStatus(nil), m.statuses...)
}

// Attach implements tools.MCPAttacher. Only servers connected by the last Reload
// are registered: a server known to be unreachable must not cost every session
// creation a retry round against a dead endpoint.
func (m *mcpRuntime) Attach(registry *tools.ToolRegistry) []tools.ServerResolution {
	m.mu.Lock()
	specs := append([]tools.MCPServerSpec(nil), m.specs...)
	namespace := m.namespace
	m.mu.Unlock()
	if len(specs) == 0 {
		return nil
	}
	registry.SetMCPConnectionManager(m.pool)
	logSummary := true
	preserve := true
	return registry.LoadRegistryServers(specs, tools.LoadRegistryOptions{
		LogSummary:            &logSummary,
		PreserveExistingTools: &preserve,
		LogCollisions:         true,
		ToolNamespace:         namespace,
	})
}

// Close releases the pool's connections (and, for stdio servers, the spawned
// processes).
func (m *mcpRuntime) Close() {
	m.pool.CleanupAll()
}

// Reload re-reads the MCP configuration from disk, reconnects the pool and
// rebuilds the status snapshot. It never fails the caller: a configuration that
// cannot be read degrades to "no MCP servers", which is what an unconfigured
// home looks like.
func (m *mcpRuntime) Reload() error {
	settings := readMCPFileSettings(m.configPath)
	if !settings.Enabled {
		m.pool.CleanupAll()
		m.mu.Lock()
		m.specs = nil
		m.statuses = nil
		m.mu.Unlock()
		m.gen.Add(1)
		fmt.Fprintln(m.out, "MCP: disabled in configuration.json")
		return nil
	}

	m.mu.Lock()
	m.namespace = settings.Namespace
	m.mu.Unlock()
	if settings.Timeout != nil {
		// configuration.json is the fallback for HIVE_MCP_CALL_RESULT_TIMEOUT:
		// the environment wins, the file value next, then the built-in 240s
		// ceiling (the general env > file > default rule).
		tools.SetMCPCallResultTimeout(*settings.Timeout)
	}

	specs := configuredMCPServers(m.home, settings)
	// Drop the previous pool entries first: a reload must not keep serving a
	// removed server or a stale command line.
	m.pool.CleanupAll()
	reachable, statuses := m.connectAll(specs)
	m.mu.Lock()
	m.specs = reachable
	m.statuses = statuses
	m.mu.Unlock()
	m.gen.Add(1)
	m.report(statuses)
	return nil
}

// connectAll connects every spec in parallel and fetches its tools/list, bounded
// by the startup budget. A spec that errors or misses the budget is left out of
// the returned specs and described in the statuses.
func (m *mcpRuntime) connectAll(specs []tools.MCPServerSpec) ([]tools.MCPServerSpec, []tools.MCPServerStatus) {
	if len(specs) == 0 {
		return nil, nil
	}
	type outcome struct {
		spec   tools.MCPServerSpec
		status tools.MCPServerStatus
		ok     bool
	}
	results := make(chan outcome, len(specs))
	for _, spec := range specs {
		go func(spec tools.MCPServerSpec) {
			status := tools.MCPServerStatus{Name: spec.Name, Transport: spec.Transport, Enabled: true, Namespace: m.namespace}
			client, err := m.pool.Acquire(mcpServerConfig(spec))
			if err != nil {
				status.Error = err.Error()
				results <- outcome{spec: spec, status: status}
				return
			}
			names := make([]string, 0, 8)
			for _, mcpTool := range client.ListTools() {
				names = append(names, mcpTool.Name)
			}
			sort.Strings(names)
			status.Connected = true
			status.Tools = len(names)
			status.ToolNames = names
			results <- outcome{spec: spec, status: status, ok: true}
		}(spec)
	}

	deadline := time.NewTimer(m.budget)
	defer deadline.Stop()
	reachable := make([]tools.MCPServerSpec, 0, len(specs))
	statuses := make([]tools.MCPServerStatus, 0, len(specs))
	answered := make(map[string]struct{}, len(specs))
	for pending := len(specs); pending > 0; pending-- {
		select {
		case out := <-results:
			answered[out.spec.Name] = struct{}{}
			statuses = append(statuses, out.status)
			if out.ok {
				reachable = append(reachable, out.spec)
			}
		case <-deadline.C:
			for _, spec := range specs {
				if _, done := answered[spec.Name]; done {
					continue
				}
				statuses = append(statuses, tools.MCPServerStatus{
					Name:      spec.Name,
					Transport: spec.Transport,
					Enabled:   true,
					Namespace: m.namespace,
					Error:     fmt.Sprintf("connect timed out after %s", m.budget),
				})
			}
			pending = 0
		}
	}
	tools.SortMCPServerStatuses(statuses)
	sort.Slice(reachable, func(i, j int) bool { return reachable[i].Name < reachable[j].Name })
	return reachable, statuses
}

// report prints the load summary. A failed server is a warning with its reason,
// never a startup error: MCP is an optional capability.
func (m *mcpRuntime) report(statuses []tools.MCPServerStatus) {
	if len(statuses) == 0 {
		return
	}
	connected, toolsLoaded := 0, 0
	for _, status := range statuses {
		if status.Connected {
			connected++
			toolsLoaded += status.Tools
			continue
		}
		fmt.Fprintf(m.out, "Warning: MCP server %q unavailable: %s\n", status.Name, status.Error)
	}
	fmt.Fprintf(m.out, "MCP: %d server(s) connected, %d tool(s), %d unavailable\n",
		connected, toolsLoaded, len(statuses)-connected)
}

// mcpServerConfig converts a resolved spec into the client config. Env is copied
// so a later config edit cannot mutate the pool's running configuration.
func mcpServerConfig(spec tools.MCPServerSpec) *tools.MCPServerConfig {
	return &tools.MCPServerConfig{
		Name:        spec.Name,
		Transport:   spec.Transport,
		Command:     spec.Command,
		Args:        append([]string(nil), spec.Args...),
		Env:         copyStringMap(spec.Env),
		Cwd:         spec.Cwd,
		URL:         spec.URL,
		Headers:     copyStringMap(spec.Headers),
		SocketPath:  spec.SocketPath,
		Description: spec.Description,
	}
}

// withMCPWorkerTools adds the loaded MCP tools to a colony worker's base
// surface. A worker definition's "tools" list selects from the optional pool;
// MCP tools are capability the colony was configured with, so they join the base
// the way upstream's spawn_tools carry them. The pool is what identifies them
// (only names the loader reports as loaded are promoted), so an unrelated
// integration tool is never dragged in.
func withMCPWorkerTools(base, declared []llm.ToolDef) []llm.ToolDef {
	loaded := mcpLoadedToolNames()
	if len(loaded) == 0 {
		return base
	}
	taken := make(map[string]struct{}, len(base))
	for _, def := range base {
		taken[def.Name] = struct{}{}
	}
	for _, def := range declared {
		if _, ok := loaded[def.Name]; !ok {
			continue
		}
		if _, dup := taken[def.Name]; dup {
			continue
		}
		taken[def.Name] = struct{}{}
		base = append(base, def)
	}
	return base
}

// mcpLoadedToolNames is the set of catalogue tool names the process loader
// connected and fetched on its last reload. Status.ToolNames holds the names
// the server advertised (tools/list); the catalogue registers them under the
// loader's namespace, so they are namespaced here before any comparison with
// session/worker tool defs.
func mcpLoadedToolNames() map[string]struct{} {
	attacher := tools.ProcessMCP()
	if attacher == nil {
		return nil
	}
	names := map[string]struct{}{}
	for _, status := range attacher.Status() {
		if !status.Connected {
			continue
		}
		namespace := status.Namespace
		if namespace == "" {
			namespace = tools.MCPToolNamespace
		}
		for _, name := range status.ToolNames {
			names[tools.NamespacedMCPToolName(namespace, status.Name, name)] = struct{}{}
		}
	}
	return names
}
