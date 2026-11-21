package tools

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type MCPTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	ServerName  string
}

type MCPServerConfig struct {
	Name        string
	Transport   string
	Command     string
	Args        []string
	Env         map[string]string
	Cwd         string
	URL         string
	Headers     map[string]string
	SocketPath  string
	Description string
}

type MCPClient interface {
	Connect() error
	Disconnect() error
	ListTools() []MCPTool
	CallTool(name string, arguments map[string]any) (any, error)
	Config() *MCPServerConfig
}

type MCPLivenessClient interface {
	ProbeLiveness() bool
	LifecycleBusy() bool
	IsConnected() bool
	ConsecutiveProbeFailures() int
	InflightCalls() int
}

type MCPConnectionManager interface {
	Acquire(config *MCPServerConfig) (MCPClient, error)
	Release(serverName string) error
}

type MCPClientFactory func(config *MCPServerConfig) MCPClient

type unsupportedMCPClient struct{ config *MCPServerConfig }

func (c *unsupportedMCPClient) Connect() error {
	return &noMCPTransportError{}
}
func (c *unsupportedMCPClient) Disconnect() error { return nil }
func (c *unsupportedMCPClient) ListTools() []MCPTool {
	return nil
}
func (c *unsupportedMCPClient) CallTool(string, map[string]any) (any, error) {
	return nil, &noMCPTransportError{}
}
func (c *unsupportedMCPClient) Config() *MCPServerConfig { return c.config }

type noMCPTransportError struct{}

func (*noMCPTransportError) Error() string {
	return "no MCP client transport configured (set ToolRegistry.ClientFactory or ConnectionManager)"
}

func DefaultMCPClientFactory(config *MCPServerConfig) MCPClient {
	return NewMCPClient(config)
}

type HarnessTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Params      map[string]struct{}
	Invoke      func(inputs map[string]any) (any, error)
}

func (h HarnessTool) asMCPTool() MCPTool {
	return MCPTool{Name: h.Name, Description: h.Description, InputSchema: h.InputSchema}
}

type HarnessGroupBuilder func(group string, scopeEnv func() map[string]string) []HarnessTool

var HarnessGroups = map[string]string{
	"terminal-tools": "Terminal: command execution, background jobs, PTY sessions, ripgrep/glob search",
	"files-tools":    "File system: read, write, edit and search files",
	"chart-tools":    "BI/financial chart + diagram rendering: ECharts, Mermaid",
	"memory-tools":   "System memory: regex search across a queen/colony's messages",
	"hive_tools":     "Core Hive tools: attachments, PDFs, web scraping, spreadsheets, image generation",
}

var HarnessGroupNames = map[string]struct{}{
	"terminal-tools": {},
	"files-tools":    {},
	"chart-tools":    {},
	"memory-tools":   {},
	"hive_tools":     {},
}

var DefaultLocalServerNames = HarnessGroupNames

var ContextParams = map[string]struct{}{
	"agent_id":             {},
	"data_dir":             {},
	"profile":              {},
	"profile_display_name": {},
	"session_cwd":          {},
	"colony_id":            {},
	"principal":            {},
}

var ConcurrencySafeTools = map[string]struct{}{
	"pdf_read":            {},
	"terminal_rg":         {},
	"terminal_glob":       {},
	"terminal_output_get": {},
	"web_scrape":          {},
	"search_papers":       {},
	"search_wikipedia":    {},
	"download_paper":      {},
}

var imageToolNameRE = regexp.MustCompile(`(?i)(screenshot|screen_capture|capture_image|render_image|get_image|snapshot_image)`)

func ToolProducesImage(name string) bool {
	return imageToolNameRE.MatchString(name)
}

func IsConcurrencySafe(name string) bool {
	_, ok := ConcurrencySafeTools[name]
	return ok
}

const browserImageMarker = "_image"

var browserImagePathRE = regexp.MustCompile(`(/[^"'\\\n\r]*\.jpe?g)`)

func extractBrowserImagePath(text string) (string, bool) {
	env := tryJSONValue(text)
	var objects []any
	if env != nil {
		objects = append(objects, env)
	}
	if envMap, ok := env.(map[string]any); ok {
		if stdout, ok := envMap["stdout"].(string); ok {
			if parsed := tryJSONValue(stdout); parsed != nil {
				objects = append(objects, parsed)
			}
		}
	}
	for _, obj := range objects {
		m, ok := obj.(map[string]any)
		if !ok {
			continue
		}
		if img, ok := m["_image"].(map[string]any); ok {
			if path, ok := img["path"].(string); ok {
				return path, true
			}
		}
		if savedTo, ok := m["saved_to"].(string); ok {
			lower := strings.ToLower(savedTo)
			if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
				return savedTo, true
			}
		}
	}
	if m := browserImagePathRE.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	return "", false
}

func tryJSONValue(raw string) any {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	if dec.More() {
		return nil
	}
	return fromJSON(v)
}

func MaybeInlineBrowserImage(toolName string, result any) any {
	if !strings.HasPrefix(toolName, "terminal") {
		return result
	}
	var text string
	switch v := result.(type) {
	case string:
		text = v
	case map[string]any:
		text, _ = v["_text"].(string)
	default:
		return result
	}
	if !strings.Contains(text, browserImageMarker) {
		return result
	}
	if m, ok := result.(map[string]any); ok {
		if images, ok := m["_images"]; ok {
			if list, ok := images.([]any); ok && len(list) > 0 {
				return result
			}
			if list, ok := images.([]map[string]any); ok && len(list) > 0 {
				return result
			}
		}
	}
	path, ok := extractBrowserImagePath(text)
	if !ok {
		return result
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return result
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	block := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
		},
	}
	baseText := ""
	if s, ok := result.(string); ok {
		baseText = s
	} else if m, ok := result.(map[string]any); ok {
		baseText, _ = m["_text"].(string)
	}
	return map[string]any{"_text": baseText, "_images": []any{block}}
}

func PythonJSONDumps(v any) string {
	var b strings.Builder
	writePythonJSON(&b, v)
	return b.String()
}

func writePythonJSON(b *strings.Builder, v any) {
	switch value := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if value {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(value)
		b.Write(encoded)
	case json.Number:
		b.WriteString(value.String())
	case int:
		b.WriteString(strconv.Itoa(value))
	case int8:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(value), 10))
	case int64:
		b.WriteString(strconv.FormatInt(value, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint8:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint16:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(value, 10))
	case float32:
		b.WriteString(FormatPyFloat(float64(value)))
	case float64:
		b.WriteString(FormatPyFloat(value))
	case []any:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case []map[string]any:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, item)
		}
		b.WriteByte(']')
	case map[string]string:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, k)
			b.WriteString(": ")
			writePythonJSON(b, value[k])
		}
		b.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePythonJSON(b, k)
			b.WriteString(": ")
			writePythonJSON(b, value[k])
		}
		b.WriteByte('}')
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			b.WriteString(strconv.Quote(strconv.FormatInt(0, 10)))
			return
		}
		b.Write(encoded)
	}
}

func truncateInputs(inputs map[string]any) string {
	const maxLen = 500
	s := PythonJSONDumps(inputs)
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "...(truncated)"
	}
	return s
}
