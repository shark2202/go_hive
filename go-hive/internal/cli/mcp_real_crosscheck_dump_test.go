package cli

// Go half of the *real* third-party MCP server cross-check.
//
// Gated: runs only when MCP_REAL_OUT and MCP_REAL_SERVERS are set, because it
// spawns real npx/uvx MCP servers and talks to a real SSE / streamable-HTTP
// endpoint. go_hive/scripts/mcp_real_crosscheck_run.sh sets both, runs this
// test, and diffs the dump against the Python-side dump written by
// go_hive/scripts/mcp_real_crosscheck_python.py over the same entries.
//
// Unlike the fixture-based dump, every server here is a third-party
// implementation, so the dump also records the server-side ground truth the
// client claims to pass through.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/mcpregistry"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// realCallSpec mirrors the Python probe's CALL_SPECS exactly: label, tool,
// arguments. get-sum-error is a server-side validation failure (isError:true).
type realCallSpec struct {
	Label     string
	Tool      string
	Arguments map[string]any
}

func realCallSpecs() []realCallSpec {
	return []realCallSpec{
		{"echo", "echo", map[string]any{"message": "Hello MCP"}},
		{"get-sum", "get-sum", map[string]any{"a": 19, "b": 23}},
		{"get-sum-error", "get-sum", map[string]any{"a": 1}},
		{"get-tiny-image", "get-tiny-image", map[string]any{}},
		{"get-structured-content", "get-structured-content", map[string]any{"location": "Chicago"}},
		{"get_current_time", "get_current_time", map[string]any{"timezone": "Asia/Shanghai"}},
		{"convert_time", "convert_time", map[string]any{
			"time": "15:30", "source_timezone": "Asia/Shanghai", "target_timezone": "America/New_York",
		}},
		{"echo_text", "echo_text", map[string]any{"text": "Hello MCP", "count": 2, "mode": "upper"}},
		{"get-resource-links", "get-resource-links", map[string]any{"count": 2}},
		{"trigger-log-messages", "trigger-log-messages", map[string]any{}},
		{"trigger-long-running-operation", "trigger-long-running-operation", map[string]any{"duration": 2, "steps": 2}},
	}
}

const realUnknownTool = "no_such_tool_xyz"

// realCrashMarker is the fallback substring identifying the stdio server
// process the crash probe kills; entries normally carry "crash_marker".
const realCrashMarker = "server-everything"

func realSha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// realCanonical renders a value the way the Python probe's canonical() does.
func realCanonical(value any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func realSha256Value(value any) string { return realSha256Text(realCanonical(value)) }

func realErrorRecord(err error) map[string]any {
	return map[string]any{"ok": false, "error_type": fmt.Sprintf("%T", err), "error": err.Error()}
}

// realToolRecord mirrors the Python probe's tool_record over an llm.ToolDef.
func realToolRecord(def llm.ToolDef) map[string]any {
	description := def.Description
	return map[string]any{
		"registered_name":    def.Name,
		"description":        description,
		"description_sha256": realSha256Text(description),
		"parameters":         def.Parameters,
		"parameters_sha256":  realSha256Value(def.Parameters),
	}
}

// realValueRecord describes a raw MCP client result the way the Python probe
// does: text+images dicts, plain strings, or anything else verbatim.
func realValueRecord(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		if images, present := object["_images"]; present {
			kinds := []any{}
			if list, ok := images.([]map[string]any); ok {
				for _, block := range list {
					kinds = append(kinds, block["type"])
				}
			}
			count := 0
			switch typed := images.(type) {
			case []map[string]any:
				count = len(typed)
			case []any:
				count = len(typed)
			}
			return map[string]any{
				"ok":           true,
				"shape":        "text+images",
				"text":         object["_text"],
				"image_blocks": count,
				"image_types":  kinds,
			}
		}
	}
	return map[string]any{"ok": true, "shape": fmt.Sprintf("%T", value), "value": value}
}

// realDescendants returns (pid, command) for processes whose parent chain
// reaches this process, so the crash probe kills only servers it started.
func realDescendants(t *testing.T) [][2]string {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pid=,ppid=,command=", "-ax").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	type row struct {
		pid, ppid int
		command   string
	}
	rows := []row{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		rows = append(rows, row{pid: pid, ppid: ppid, command: strings.Join(fields[2:], " ")})
	}
	childrenOf := map[int][]row{}
	for _, entry := range rows {
		childrenOf[entry.ppid] = append(childrenOf[entry.ppid], entry)
	}
	out := [][2]string{}
	stack := append([]row{}, childrenOf[os.Getpid()]...)
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, [2]string{strconv.Itoa(current.pid), current.command})
		stack = append(stack, childrenOf[current.pid]...)
	}
	return out
}

// realKillDescendants kills every descendant whose command mentions marker and
// returns what it killed (mirrors the Python probe's probe_crash).
func realKillDescendants(t *testing.T, marker string) map[string]any {
	t.Helper()
	killed := []map[string]any{}
	for _, entry := range realDescendants(t) {
		if !strings.Contains(entry[1], marker) {
			continue
		}
		pid, err := strconv.Atoi(entry[0])
		if err != nil {
			continue
		}
		if process, err := os.FindProcess(pid); err == nil {
			if err := process.Kill(); err == nil {
				command := entry[1]
				if len(command) > 200 {
					command = command[:200]
				}
				killed = append(killed, map[string]any{"pid": pid, "command": command})
			}
		}
	}
	time.Sleep(500 * time.Millisecond)
	return map[string]any{"killed": killed}
}

// realRawClientProbe drives tools.NewMCPClient directly (the client the tool
// registry uses) to capture the exact result shape and error bodies.
func realRawClientProbe(t *testing.T, entry map[string]any, calls []realCallSpec) map[string]any {
	t.Helper()
	config := &tools.MCPServerConfig{Name: realString(entry["name"]), Description: "real server cross-check"}
	config.Transport = realString(entry["transport"])
	config.Command = realString(entry["command"])
	config.URL = realString(entry["url"])
	config.Cwd = realString(entry["cwd"])
	config.SocketPath = realString(entry["socket_path"])
	for _, arg := range realStringList(entry["args"]) {
		config.Args = append(config.Args, arg)
	}
	if headers, ok := entry["headers"].(map[string]any); ok {
		config.Headers = map[string]string{}
		for key, value := range headers {
			config.Headers[key] = fmt.Sprint(value)
		}
	}
	if env, ok := entry["env"].(map[string]any); ok {
		config.Env = map[string]string{}
		for key, value := range env {
			config.Env[key] = fmt.Sprint(value)
		}
	}

	out := map[string]any{}
	client := tools.NewMCPClient(config)
	defer func() { _ = client.Disconnect() }()
	if err := client.Connect(); err != nil {
		out["connected"] = false
		out["connect_error"] = realErrorRecord(err)
		return out
	}
	out["connected"] = true
	listed := client.ListTools()
	names := []string{}
	digests := map[string]string{}
	records := []map[string]any{}
	for _, tool := range listed {
		names = append(names, tool.Name)
		digests[tool.Name] = realSha256Value(map[string]any{
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
		records = append(records, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}
	sort.Strings(names)
	out["tool_names"] = names
	out["tools_sha256"] = digests
	out["tools"] = records

	exposed := map[string]struct{}{}
	for _, name := range names {
		exposed[name] = struct{}{}
	}
	rawCalls := map[string]any{}
	for _, spec := range calls {
		if _, ok := exposed[spec.Tool]; !ok {
			continue
		}
		value, err := client.CallTool(spec.Tool, spec.Arguments)
		if err != nil {
			rawCalls[spec.Label] = realErrorRecord(err)
			continue
		}
		rawCalls[spec.Label] = realValueRecord(value)
	}
	out["calls"] = rawCalls

	if _, err := client.CallTool(realUnknownTool, map[string]any{}); err != nil {
		record := realErrorRecord(err)
		record["raised"] = true
		out["unknown_tool"] = record
	} else {
		out["unknown_tool"] = map[string]any{"raised": false}
	}
	return out
}

func realString(value any) string {
	text, _ := value.(string)
	return text
}

func realStringList(value any) []string {
	out := []string{}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			out = append(out, fmt.Sprint(item))
		}
	}
	return out
}

// Parity-exempt: Go half of a two-runtime evidence dump against real
// third-party MCP servers; its Python half is
// go_hive/scripts/mcp_real_crosscheck_python.py and the comparison happens in
// go_hive/scripts/mcp_real_crosscheck_run.sh, so there is no upstream unit test
// to mirror. It needs npx/uvx and a live network, hence the env gate.
func TestMCPRealServerDump(t *testing.T) {
	outPath := os.Getenv("MCP_REAL_OUT")
	serversFile := os.Getenv("MCP_REAL_SERVERS")
	if outPath == "" || serversFile == "" {
		t.Skip("set MCP_REAL_OUT and MCP_REAL_SERVERS to write the real-server MCP cross-check dump")
	}
	only := map[string]struct{}{}
	for _, name := range strings.Split(os.Getenv("MCP_REAL_ONLY"), ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			only[trimmed] = struct{}{}
		}
	}

	raw, err := os.ReadFile(serversFile)
	if err != nil {
		t.Fatalf("read server entries: %v", err)
	}
	var fixture struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse server entries: %v", err)
	}
	if len(fixture.Servers) == 0 {
		t.Fatal("no server entries in fixture")
	}

	results := []map[string]any{}
	for _, entry := range fixture.Servers {
		name := realString(entry["name"])
		if len(only) > 0 {
			if _, wanted := only[name]; !wanted {
				continue
			}
		}
		results = append(results, realProbeServer(t, entry))
	}

	payload := map[string]any{
		"side":         "go",
		"go_version":   runtime.Version(),
		"servers_file": serversFile,
		"servers":      results,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	fmt.Printf("real-server MCP crosscheck dump written: %s (%d servers)\n", outPath, len(results))
}

// realProbeServer runs every dimension for one server entry on the Go side.
func realProbeServer(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	name := realString(entry["name"])
	transport := realString(entry["transport"])
	calls := realCallSpecs()
	result := map[string]any{
		"side":         "go",
		"name":         name,
		"transport":    transport,
		"entry":        entry,
		"entry_sha256": realSha256Value(entry),
	}

	home := hivehome.Home{Root: t.TempDir()}
	registry := mcpregistry.NewAtHome(home)
	if err := registry.Initialize(); err != nil {
		t.Fatalf("initialize registry: %v", err)
	}
	// Same call `hive mcp add` makes, so the entry lands in installed.json
	// through the production writer.
	addErr := registry.AddLocal(name, mcpregistry.AddLocalOptions{
		Transport:   transport,
		Command:     realString(entry["command"]),
		Args:        realStringList(entry["args"]),
		URL:         realString(entry["url"]),
		Cwd:         realString(entry["cwd"]),
		SocketPath:  realString(entry["socket_path"]),
		Description: "real server cross-check",
	})
	if addErr != nil {
		result["registry_added"] = false
		result["registry_error"] = realErrorRecord(addErr)
	} else {
		result["registry_added"] = true
		if installed, err := registry.GetServer(name); err == nil {
			result["installed_entry"] = installed
		}
	}

	// The loader resolves the registry (registryMCPSpecs) and the runtime
	// loads it exactly like `hive serve` does.
	writeConfiguration(t, home, `{"mcp":{"enabled":true}}`)
	runner := newMCPRuntime(home, io.Discard)
	t.Cleanup(func() {
		tools.SetProcessMCP(nil)
		runner.Close()
	})
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	tools.SetProcessMCP(runner)
	statuses := runner.Status()

	resolved := []any{}
	for _, spec := range registryMCPSpecs(home) {
		resolved = append(resolved, map[string]any{
			"name": spec.Name, "transport": spec.Transport, "command": spec.Command,
			"args": spec.Args, "url": spec.URL, "headers": spec.Headers, "cwd": spec.Cwd,
			"env": spec.Env, "socket_path": spec.SocketPath, "description": spec.Description,
		})
	}
	result["resolved_configs"] = resolved

	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	defs := catalogue.Defs()
	prefix := strings.TrimSuffix(
		tools.NamespacedMCPToolName(tools.MCPToolNamespace, name, "PROBE"),
		"PROBE",
	)

	surface := map[string]any{"namespace_prefix": prefix}
	statusRecords := []map[string]any{}
	for _, status := range statuses {
		statusRecords = append(statusRecords, map[string]any{
			"name": status.Name, "transport": status.Transport, "connected": status.Connected,
			"tools": status.Tools, "tool_names": status.ToolNames, "namespace": status.Namespace,
			"error": status.Error,
		})
	}
	surface["statuses"] = statusRecords

	serverToolNames := []string{}
	surfaceTools := []map[string]any{}
	defsByName := map[string]llm.ToolDef{}
	for _, def := range defs {
		defsByName[def.Name] = def
	}
	for _, def := range defs {
		if !strings.HasPrefix(def.Name, prefix) {
			continue
		}
		serverToolNames = append(serverToolNames, def.Name)
		surfaceTools = append(surfaceTools, realToolRecord(def))
	}
	sort.Strings(serverToolNames)
	surface["server_tool_names"] = serverToolNames
	surface["tools"] = surfaceTools

	ctx := context.Background()
	surfaceCalls := map[string]any{}
	for _, spec := range calls {
		target := tools.NamespacedMCPToolName(tools.MCPToolNamespace, name, spec.Tool)
		if _, present := defsByName[target]; !present {
			continue
		}
		content, isError, images, err := catalogue.ExecuteBlocks(ctx, target, spec.Arguments)
		if err != nil {
			surfaceCalls[spec.Label] = realErrorRecord(err)
			continue
		}
		kinds := []any{}
		for _, block := range images {
			kinds = append(kinds, block["type"])
		}
		surfaceCalls[spec.Label] = map[string]any{
			"ok":           !isError,
			"content":      content,
			"is_error":     isError,
			"image_blocks": len(images),
			"image_kinds":  kinds,
		}
	}
	surface["calls"] = surfaceCalls

	unknownTarget := tools.NamespacedMCPToolName(tools.MCPToolNamespace, name, realUnknownTool)
	if content, isError, _, err := catalogue.ExecuteBlocks(ctx, unknownTarget, map[string]any{}); err == nil {
		surface["unknown_tool"] = map[string]any{
			"raised": false, "ok": !isError, "is_error": isError, "content": content,
		}
	} else {
		record := realErrorRecord(err)
		record["raised"] = true
		surface["unknown_tool"] = record
	}
	result["surface"] = surface

	raw := realRawClientProbe(t, entry, calls)
	result["raw"] = raw
	// Remote names as advertised by the server, so the diff can pair Python's
	// bare names with Go's namespaced ones.
	if names, ok := raw["tool_names"].([]string); ok {
		surface["remote_tool_names"] = names
	} else {
		surface["remote_tool_names"] = []string{}
	}

	// ── lifecycle: disable / enable / reload ────────────────────────────
	lifecycle := map[string]any{}
	if err := registry.Disable(name); err != nil {
		lifecycle["disable_error"] = realErrorRecord(err)
	}
	if err := runner.Reload(); err != nil {
		lifecycle["reload_after_disable_error"] = realErrorRecord(err)
	}
	lifecycle["disabled_status_count"] = len(runner.Status())
	disabledCatalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	lifecycle["disabled_tool_count"] = countPrefixed(disabledCatalogue.Defs(), prefix)
	lifecycle["disabled_resolved"] = len(registryMCPSpecs(home))

	if err := registry.Enable(name); err != nil {
		lifecycle["enable_error"] = realErrorRecord(err)
	}
	if err := runner.Reload(); err != nil {
		lifecycle["reload_after_enable_error"] = realErrorRecord(err)
	}
	lifecycle["reenabled_status_count"] = len(runner.Status())
	reenabledCatalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	lifecycle["reenabled_tool_count"] = countPrefixed(reenabledCatalogue.Defs(), prefix)
	lifecycle["reenabled_resolved"] = len(registryMCPSpecs(home))
	result["lifecycle"] = lifecycle

	// ── crash: kill the server process, then call through the live client ─
	crash := map[string]any{}
	if transport == "stdio" {
		cfg := &tools.MCPServerConfig{Name: name, Transport: transport}
		cfg.Command = realString(entry["command"])
		cfg.Args = realStringList(entry["args"])
		crashClient := tools.NewMCPClient(cfg)
		if err := crashClient.Connect(); err != nil {
			crash["error"] = realErrorRecord(err)
		} else {
			exposed := map[string]struct{}{}
			for _, tool := range crashClient.ListTools() {
				exposed[tool.Name] = struct{}{}
			}
			// Any exposed tool works; "echo" first for readability.
			probeTool := ""
			probeArgs := map[string]any{}
			for _, spec := range calls {
				if spec.Label == "get-sum-error" {
					continue
				}
				if _, ok := exposed[spec.Tool]; !ok {
					continue
				}
				probeTool, probeArgs = spec.Tool, spec.Arguments
				break
			}
			crash["probe_tool"] = probeTool
			if probeTool != "" {
				if value, err := crashClient.CallTool(probeTool, probeArgs); err == nil {
					crash["call_before_kill"] = map[string]any{"ok": true, "tool": probeTool, "value": value}
				} else {
					crash["call_before_kill"] = realErrorRecord(err)
				}
			}
			marker := realString(entry["crash_marker"])
			if marker == "" {
				marker = realCrashMarker
			}
			kill := realKillDescendants(t, marker)
			crash["kill"] = kill
			crash["killed_count"] = len(kill["killed"].([]map[string]any))
			if probeTool != "" {
				if value, err := crashClient.CallTool(probeTool, probeArgs); err == nil {
					crash["call_after_kill"] = map[string]any{"ok": true, "tool": probeTool, "value": value}
				} else {
					crash["call_after_kill"] = realErrorRecord(err)
				}
			}
			if liveness, ok := crashClient.(tools.MCPLivenessClient); ok {
				crash["connected_after_kill"] = liveness.IsConnected()
			}
			_ = crashClient.Disconnect()
		}
	}
	result["crash"] = crash
	return result
}

func countPrefixed(defs []llm.ToolDef, prefix string) int {
	count := 0
	for _, def := range defs {
		if strings.HasPrefix(def.Name, prefix) {
			count++
		}
	}
	return count
}
