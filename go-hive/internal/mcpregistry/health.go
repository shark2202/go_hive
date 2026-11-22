package mcpregistry

import (
	"fmt"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

type HealthResult struct {
	Name   string
	Status string
	Tools  int
	Error  any
}

func (h *HealthResult) ToDict() map[string]any {
	return map[string]any{
		"name":   h.Name,
		"status": h.Status,
		"tools":  h.Tools,
		"error":  h.Error,
	}
}

type HealthClient interface {
	ListTools() ([]tools.MCPTool, error)
	Close() error
}

type ClientFactory func(config *ServerConfig) (HealthClient, error)

type ConnectionManager interface {
	HasConnection(name string) bool
	HealthCheck(name string) bool
	Acquire(config *ServerConfig) (HealthClient, error)
	Release(name string)
}

func DefaultClientFactory(config *ServerConfig) (HealthClient, error) { return nil, nil }

func (r *Registry) HealthCheckAll() (map[string]map[string]any, error) {
	installed, err := r.ListInstalled()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]any, len(installed))
	for _, entry := range installed {
		name := asString(entry["name"])
		result, err := r.HealthCheck(name)
		if err != nil {
			return nil, err
		}
		out[name] = result
	}
	return out, nil
}

func (r *Registry) RunHealthCheckAll() (map[string]map[string]any, error) { return r.HealthCheckAll() }

func (r *Registry) HealthCheck(name string) (map[string]any, error) {
	data, err := r.ReadInstalled()
	if err != nil {
		return nil, err
	}
	entry := entryOf(data, name)
	if entry == nil {
		return nil, &Error{
			Code: CodeHealthFailed,
			What: "Cannot health-check server '" + name + "'",
			Why:  "Server is not installed.",
			Fix:  "Run: hive mcp list  — to see installed servers.",
		}
	}

	manifest := r.effectiveManifest(name, entry, nil)
	config := manifestToServerConfig(name, manifest, asMap(entry["overrides"]), asString(entry["transport"]))
	now := r.now().Format(time.RFC3339Nano)
	result := map[string]any{
		"name":   name,
		"status": "unknown",
		"tools":  0,
		"error":  nil,
	}

	if config == nil {
		transport := stringOr(entry["transport"], "unknown")
		result["status"] = "unhealthy"
		result["error"] = "Unsupported transport '" + transport + "'"
		entry["last_health_status"] = "unhealthy"
		entry["last_error"] = "Unsupported transport '" + transport + "'"
		entry["last_health_check_at"] = now
		return result, r.writeInstalled(data)
	}

	toolCount, probeErr, err := r.probe(name, config)
	if err != nil {
		return nil, err
	}
	if probeErr != nil {
		result["status"] = "unhealthy"
		result["error"] = probeErr.Error()
		entry["last_health_status"] = "unhealthy"
		entry["last_error"] = probeErr.Error()
	} else {
		result["status"] = "healthy"
		result["tools"] = toolCount
		entry["last_tool_count"] = toolCount
		entry["last_health_status"] = "healthy"
		entry["last_error"] = nil
		entry["last_validated_with_hive_version"] = HiveVersion()
	}
	entry["last_health_check_at"] = now
	if err := r.writeInstalled(data); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Registry) RunHealthCheck(name string) (map[string]any, error) { return r.HealthCheck(name) }

func (r *Registry) probe(name string, config *ServerConfig) (int, error, error) {
	manager := r.Manager
	if manager != nil {
		if manager.HasConnection(name) {
			if !manager.HealthCheck(name) {
				return 0, &Error{
					Code: CodeHealthFailed,
					What: "Health check failed for server '" + name + "'",
					Why:  "Shared MCP connection reported unhealthy.",
					Fix:  "Run: hive mcp doctor " + name + "  — for diagnostics.",
				}, nil
			}
			client, err := manager.Acquire(config)
			if err != nil {
				return 0, err, nil
			}
			var count int
			var probeErr error
			func() {
				defer manager.Release(name)
				defer func() {
					if rec := recover(); rec != nil {
						count = 0
						probeErr = errorf("tool listing panicked: %v", rec)
					}
				}()
				listed, err := client.ListTools()
				count = len(listed)
				probeErr = err
			}()
			return count, probeErr, nil
		}
	}
	factory := r.ClientFactory
	if factory == nil {
		factory = DefaultClientFactory
	}
	client, connectErr := factory(config)
	if connectErr != nil {
		return 0, connectErr, nil
	}
	if client == nil {
		return 0, errorf("no MCP client transport configured"), nil
	}
	defer func() { _ = client.Close() }()
	listed, probeErr := client.ListTools()
	return len(listed), probeErr, nil
}
