package tools

// Process-level MCP attach seam.
//
// `hive serve` owns the concrete loader (which servers are configured, the
// shared connection pool, the connect-at-boot pass). The tools package only
// knows this interface, so every session catalogue built through
// NewToolRegistry — queen chat, colony worker tool pool, preloaded sessions —
// starts with the loaded servers' tools without the assembly files having to
// import MCP code or grow their own wiring.

import (
	"sort"
	"sync"
)

// MCPServerStatus is one configured server's connection snapshot: whether the
// loader reached it, how many tools it advertised, and why it failed when it
// did not.
//
// ToolNames carries the names the SERVER advertises (tools/list). The session
// catalogue registers them under NamespacedMCPToolName(Namespace, Name, tool),
// so consumers that compare against catalogue defs must namespace them first;
// Namespace is the namespace the loader used (empty means the default "mcp").
type MCPServerStatus struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Enabled   bool     `json:"enabled"`
	Connected bool     `json:"connected"`
	Tools     int      `json:"tool_count"`
	ToolNames []string `json:"tool_names,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// MCPAttacher is the serve-owned MCP loader registries attach through.
type MCPAttacher interface {
	// Attach registers the reachable servers' tools into r.
	Attach(r *ToolRegistry) []ServerResolution
	// Reload re-reads the MCP configuration and reconnects the pool.
	Reload() error
	// Status is the snapshot Reload left behind, sorted by server name.
	Status() []MCPServerStatus
	// Generation increments on every Reload so live registries re-attach.
	Generation() int64
}

var (
	processMCPMu       sync.RWMutex
	processMCPAttacher MCPAttacher
)

// SetProcessMCP installs (or, with nil, clears) the process MCP loader. serve
// installs it before any session exists and clears it on shutdown; tests use
// the same pair to isolate the global.
func SetProcessMCP(attacher MCPAttacher) {
	processMCPMu.Lock()
	processMCPAttacher = attacher
	processMCPMu.Unlock()
}

// ProcessMCP returns the installed loader, or nil when MCP was not configured.
func ProcessMCP() MCPAttacher {
	processMCPMu.RLock()
	defer processMCPMu.RUnlock()
	return processMCPAttacher
}

// attachProcessMCP gives r the loaded servers' tools. A nil loader (serve was
// started without MCP, or a plain unit test) is a no-op, so registries keep the
// pre-existing behaviour unless serve installed one.
func attachProcessMCP(r *ToolRegistry) {
	attacher := ProcessMCP()
	if attacher == nil {
		return
	}
	r.attachMCPFrom(attacher)
}

// attachMCPFrom registers the loader's tools unless this registry already saw
// the same loader generation. The generation check is what makes a manual
// reload reach registries that are already serving turns: the next tool call
// resyncs, sees a newer generation, and re-attaches.
func (r *ToolRegistry) attachMCPFrom(attacher MCPAttacher) bool {
	generation := attacher.Generation()
	if r.mcpAttached && r.mcpAttachGen == generation {
		return false
	}
	r.mcpAttached = true
	r.mcpAttachGen = generation
	attacher.Attach(r)
	return true
}

// SortMCPServerStatuses orders a status snapshot by server name so the HTTP
// listing and the startup summary are stable across runs.
func SortMCPServerStatuses(statuses []MCPServerStatus) {
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
}
