package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/mcpregistry"
	"github.com/aden-hive/hive-go/internal/tools"
)

// filesMCPServerName is the in-process files group every agent gets, config
// or not.
//
// Parity: framework/orchestrator/files.py::FILES_MCP_SERVER_NAME
const filesMCPServerName = "files-tools"

// MCPServerResolver is the narrow view of the global MCP registry the stage
// needs.
type MCPServerResolver interface {
	ResolveForAgent(opts mcpregistry.ResolveOptions) ([]*mcpregistry.ServerConfig, error)
}

// MCPRegistryStage resolves MCP server references and loads their tools into
// a tool registry at startup — upstream's only place MCP tools get loaded.
//
// Resolution order mirrors upstream: the configured server_refs first, then a
// legacy mcp_servers.json next to agent_path, then every installed server as
// the fallback. files-tools is injected last when the registry has not
// provided it, so agents keep file I/O regardless of config.
//
// Config keys (config.*): server_refs (`[{"name": ...}]`), agent_path. The
// tool registry is a typed injection (struct field ToolRegistry).
//
// Parity: framework/pipeline/stages/mcp_registry.py::McpRegistryStage
type MCPRegistryStage struct {
	BaseStage
	// ServerRefs is the configured server name list, in order.
	ServerRefs []string
	// AgentPath is the agent directory that may hold a legacy
	// mcp_servers.json.
	AgentPath string
	// ToolRegistry is the destination registry; nil builds an empty one.
	ToolRegistry *tools.ToolRegistry
	// Resolver is the global registry view; nil opens the process home's.
	Resolver MCPServerResolver
	// Home overrides the hive home; nil resolves the process home.
	Home *hivehome.Home
}

// NewMCPRegistryStage builds the stage from its declarative config.
func NewMCPRegistryStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &MCPRegistryStage{
		BaseStage: BaseStage{StageName: "mcp_registry", StageOrder: 50},
	}
	if raw, present := config["agent_path"]; present {
		stage.AgentPath = pyString(raw)
	}
	if raw, present := config["server_refs"]; present {
		refs, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("mcp_registry server_refs must be a list, got %T", raw)
		}
		for _, entry := range refs {
			switch typed := entry.(type) {
			case string:
				if typed != "" {
					stage.ServerRefs = append(stage.ServerRefs, typed)
				}
			case map[string]any:
				if name := pyString(typed["name"]); name != "" {
					stage.ServerRefs = append(stage.ServerRefs, name)
				}
			}
		}
	}
	return stage, nil
}

// Initialize loads every resolved server's tools.
//
// Parity: framework/pipeline/stages/mcp_registry.py::McpRegistryStage.initialize
func (s *MCPRegistryStage) Initialize() error {
	home, err := s.resolveHome()
	if err != nil {
		return err
	}
	resolver := s.Resolver
	if resolver == nil {
		resolver = mcpregistry.NewAtHome(home)
	}
	toolRegistry := s.ToolRegistry
	if toolRegistry == nil {
		toolRegistry = tools.NewToolRegistry()
	}

	mcpLoaded := false
	load := func(configs []*mcpregistry.ServerConfig) {
		toolRegistry.LoadRegistryServers(serverSpecs(configs), tools.LoadRegistryOptions{})
	}

	// 1. From the agent's declared server refs.
	if len(s.ServerRefs) > 0 {
		configs, err := resolver.ResolveForAgent(mcpregistry.ResolveOptions{Include: s.ServerRefs})
		if err != nil {
			return err
		}
		if len(configs) > 0 {
			load(configs)
			mcpLoaded = true
		}
	}

	// 2. Legacy mcp_servers.json next to the agent.
	if !mcpLoaded && s.AgentPath != "" {
		legacy := filepath.Join(s.AgentPath, "mcp_servers.json")
		if _, statErr := os.Stat(legacy); statErr == nil {
			toolRegistry.LoadMCPConfig(legacy)
			mcpLoaded = true
		}
	}

	// 3. Fallback: every installed server.
	if !mcpLoaded {
		configs, err := resolver.ResolveForAgent(mcpregistry.ResolveOptions{Profile: "all"})
		if err != nil {
			return err
		}
		if len(configs) > 0 {
			load(configs)
		}
	}

	// 4. files-tools is always available — agents need file I/O to read
	//    skills, write data, etc. regardless of config.
	if _, loaded := toolRegistry.MCPServerTools()[filesMCPServerName]; !loaded {
		configs, err := resolver.ResolveForAgent(mcpregistry.ResolveOptions{
			Include: []string{filesMCPServerName},
		})
		if err == nil && len(configs) > 0 {
			load(configs)
		}
	}

	s.ToolRegistry = toolRegistry
	return nil
}

// Process is a pass-through: the work happened in Initialize.
func (s *MCPRegistryStage) Process(_ *Context) (Result, error) { return ContinueResult(), nil }

func (s *MCPRegistryStage) resolveHome() (hivehome.Home, error) {
	if s.Home != nil {
		return *s.Home, nil
	}
	return hivehome.Resolve()
}

// serverSpecs converts resolved registry configs into the tool registry's
// load shape (upstream passes dataclass dicts straight through).
func serverSpecs(configs []*mcpregistry.ServerConfig) []tools.MCPServerSpec {
	specs := make([]tools.MCPServerSpec, 0, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}
		deref := func(value *string) string {
			if value == nil {
				return ""
			}
			return *value
		}
		specs = append(specs, tools.MCPServerSpec{
			Name:        config.Name,
			Transport:   config.Transport,
			Command:     deref(config.Command),
			Args:        append([]string(nil), config.Args...),
			Env:         copyStringMap(config.Env),
			Cwd:         deref(config.Cwd),
			URL:         deref(config.URL),
			Headers:     copyStringMap(config.Headers),
			SocketPath:  deref(config.SocketPath),
			Description: config.Description,
		})
	}
	return specs
}

func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
