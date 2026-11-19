package host

// MCP capability boundary for a colony host.
//
// The colony tools sidecar (<colony>/tools.json) stores the operator's enabled
// MCP tool list. Until now only the HTTP PATCH route pushed it into a live
// runtime, and the runtime's "all MCP names" set was never populated, so
// applyToolAllowlist had nothing to gate. This file resolves both at host
// construction: the process MCP loader names the MCP surface, and the colony's
// own sidecar supplies the allowlist.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools"
)

// discoveredMCPToolNames is the set of MCP tool names the process loader
// connected. It mirrors the CLI's mcpLoadedToolNames: only names a connected
// server reports are treated as MCP, so integration tools are never gated.
func discoveredMCPToolNames() map[string]struct{} {
	attacher := tools.ProcessMCP()
	if attacher == nil {
		return nil
	}
	names := map[string]struct{}{}
	for _, status := range attacher.Status() {
		if !status.Connected {
			continue
		}
		for _, name := range status.ToolNames {
			if strings.TrimSpace(name) != "" {
				names[name] = struct{}{}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// readColonyToolsAllowlist reads enabled_mcp_tools from the colony's tools.json,
// falling back to the legacy metadata.json field when the sidecar has not been
// created yet. present is false when neither file, the field, nor a valid list
// is available; an explicit empty list is a real allowlist (gate every MCP
// tool) and returns present=true. A malformed file fails open, matching the
// HTTP reader.
func readColonyToolsAllowlist(storagePath string) (enabled []string, present bool) {
	if strings.TrimSpace(storagePath) == "" {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(storagePath, "tools.json"))
	if err != nil {
		raw, err = os.ReadFile(filepath.Join(storagePath, "metadata.json"))
		if err != nil {
			return nil, false
		}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	field, ok := root["enabled_mcp_tools"]
	if !ok || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
		return nil, false
	}
	var list []string
	if json.Unmarshal(field, &list) != nil {
		return nil, false
	}
	if list == nil {
		list = []string{}
	}
	return list, true
}
